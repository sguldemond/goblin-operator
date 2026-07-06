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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	opsv1alpha1 "github.com/sguldemond/goblin/api/v1alpha1"
	"github.com/sguldemond/goblin/internal/detection"
)

const targetUIDLabel = "goblinoperator.io/target-uid"

var podGVK = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=incidents,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=incidents/status,verbs=update

// IncidentDetector watches Pods and evaluates every registered Pod-targeting
// CEL policy against each Pod, creating an Incident on the first match.
type IncidentDetector struct {
	client.Client
	Registry          *detection.Registry
	IncidentNamespace string
}

func (r *IncidentDetector) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if pod.Annotations[noAutoRemediateAnnotation] == "true" {
		return ctrl.Result{}, nil
	}

	policies := r.Registry.ForGVK(podGVK)
	if len(policies) == 0 {
		return ctrl.Result{}, nil
	}

	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pod)
	if err != nil {
		return ctrl.Result{}, err
	}

	for _, p := range policies {
		match, err := p.Matcher.Eval(obj)
		if err != nil {
			l.V(1).Info("policy eval error", "policy", p.Name, "pod", req.NamespacedName, "error", err)
			continue
		}
		if !match {
			continue
		}
		if err := r.createIncident(ctx, &pod, p); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil // one incident per pod
	}
	return ctrl.Result{}, nil
}

func (r *IncidentDetector) createIncident(ctx context.Context, pod *corev1.Pod, p detection.CompiledPolicy) error {
	l := log.FromContext(ctx)
	ns := r.IncidentNamespace
	if ns == "" {
		ns = pod.Namespace
	}

	var existing opsv1alpha1.IncidentList
	if err := r.List(ctx, &existing,
		client.InNamespace(ns),
		client.MatchingLabels{targetUIDLabel: string(pod.UID)},
	); err != nil {
		return err
	}
	if len(existing.Items) > 0 {
		return nil
	}

	inc := &opsv1alpha1.Incident{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: strings.ToLower(p.Trigger) + "-" + pod.Name + "-",
			Namespace:    ns,
			Labels:       map[string]string{targetUIDLabel: string(pod.UID)},
		},
		Spec: opsv1alpha1.IncidentSpec{
			TargetRef: corev1.ObjectReference{
				APIVersion: "v1", Kind: "Pod",
				Name: pod.Name, Namespace: pod.Namespace, UID: pod.UID,
			},
			Trigger:   p.Trigger,
			PolicyRef: p.Name,
		},
	}
	if err := r.Create(ctx, inc); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	inc.Status.Phase = opsv1alpha1.PhaseDetected
	if err := r.Status().Update(ctx, inc); err != nil {
		return err
	}
	l.Info("Created Incident", "trigger", p.Trigger, "pod", pod.Name, "incident", inc.Name)
	return nil
}

func (r *IncidentDetector) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Named("incident-detector").
		Complete(r)
}
