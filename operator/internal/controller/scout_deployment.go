/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const scoutName = "goblin-scout"

// The operator owns the scout's Deployment, so it needs write access to
// Deployments — but only in its own namespace. A namespaced Role keeps that
// from becoming cluster-wide write on every workload in the cluster, which is
// what a ClusterRole entry here would mean.
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=create;update;patch,namespace=goblin

// ScoutReconciler keeps the persistent scout's Deployment matching what the
// operator expects.
//
// The scout is part of the operator's lifecycle, not a separate manifest
// someone has to remember to apply: upgrading the operator upgrades the scout,
// and deleting the Deployment by hand gets it recreated. The Deployment
// informer already exists for incident detection, so watching costs nothing.
type ScoutReconciler struct {
	client.Client
	Namespace string
	Image     string
}

func (r *ScoutReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	return ctrl.Result{}, r.Ensure(ctx)
}

// Ensure creates or corrects the scout Deployment.
func (r *ScoutReconciler) Ensure(ctx context.Context) error {
	log := logf.FromContext(ctx)
	desired := r.desired()

	var current appsv1.Deployment
	err := r.Get(ctx, types.NamespacedName{Name: scoutName, Namespace: r.Namespace}, &current)
	switch {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, desired); err != nil {
			return err
		}
		log.Info("Created scout Deployment", "namespace", r.Namespace, "image", r.Image)
		return nil
	case err != nil:
		return err
	}

	// Only correct what the operator owns. Comparing the whole spec would fight
	// with defaulting the API server applies.
	if reflect.DeepEqual(current.Spec.Template.Spec, desired.Spec.Template.Spec) &&
		current.Spec.Replicas != nil && *current.Spec.Replicas == *desired.Spec.Replicas &&
		current.Spec.Strategy.Type == desired.Spec.Strategy.Type {
		return nil
	}

	current.Spec.Replicas = desired.Spec.Replicas
	current.Spec.Strategy = desired.Spec.Strategy
	current.Spec.Template = desired.Spec.Template
	if err := r.Update(ctx, &current); err != nil {
		return err
	}
	log.Info("Corrected scout Deployment", "namespace", r.Namespace)
	return nil
}

func resourceQty(s string) resource.Quantity { return resource.MustParse(s) }

func (r *ScoutReconciler) desired() *appsv1.Deployment {
	replicas := int32(1)
	optional := true
	labels := map[string]string{"app": scoutName}

	secretEnv := func(name, secret, key string, opt bool) corev1.EnvVar {
		ref := &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secret},
			Key:                  key,
		}
		if opt {
			ref.Optional = &optional
		}
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: ref}}
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      scoutName,
			Namespace: r.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			// Recreate, not RollingUpdate. Two scouts would share one Telegram
			// chat, one ServiceAccount and one set of grants: both would claim
			// incidents and propose fixes, and the human could not tell which
			// agent was asking. A rolling update deliberately runs two.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					// The scout must never raise incidents about itself.
					Annotations: map[string]string{noAutoRemediateAnnotation: "true"},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: scoutName,
					Containers: []corev1.Container{{
						Name:            "scout",
						Image:           r.Image,
						ImagePullPolicy: corev1.PullAlways,
						Env: []corev1.EnvVar{
							// Empty means all namespaces. Incidents are
							// centralised today, but watching everything costs
							// nothing and survives decentralising later.
							{Name: "WATCH_NAMESPACE", Value: ""},
							secretEnv("LLM_API_KEY", "goblin-scout-secrets", "LLM_API_KEY", false),
							secretEnv("TELEGRAM_BOT_TOKEN", "goblin-horn-secrets", "TELEGRAM_BOT_TOKEN", true),
							secretEnv("TELEGRAM_CHAT_ID", "goblin-horn-secrets", "TELEGRAM_CHAT_ID", true),
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceMemory: resourceQty("64Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceMemory: resourceQty("256Mi")},
						},
					}},
				},
			},
		},
	}
}

// SetupWithManager watches only the scout's own Deployment, so an accidental
// delete or a hand-edit is corrected rather than silently persisting.
func (r *ScoutReconciler) SetupWithManager(mgr ctrl.Manager) error {
	isScout := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetName() == scoutName && obj.GetNamespace() == r.Namespace
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.Deployment{}, builder.WithPredicates(isScout)).
		Named("scout-deployment").
		Complete(r)
}

// ScoutBootstrap creates the scout Deployment at startup. A controller only
// reconciles on events, and a Deployment that has never existed produces none —
// so first install needs an explicit nudge.
type ScoutBootstrap struct{ Scout *ScoutReconciler }

func (b *ScoutBootstrap) Start(ctx context.Context) error {
	if err := b.Scout.Ensure(ctx); err != nil {
		logf.FromContext(ctx).Error(err, "Could not ensure scout Deployment at startup")
	}
	<-ctx.Done()
	return nil
}
