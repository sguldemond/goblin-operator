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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	opsv1alpha1 "github.com/sguldemond/goblin/api/v1alpha1"
	"github.com/sguldemond/goblin/internal/detection"
)

const targetUIDLabel = "goblinoperator.io/target-uid"

// Read access to the kinds this detector watches is generated from
// detection.Envelope — see internal/detection/zz_generated_rbac.go.
// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=incidents,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=incidents/status,verbs=update

// IncidentDetector watches one kind and evaluates every registered policy
// targeting that kind against each object, creating an Incident on the first
// match. One instance exists per watched GVK; DetectorManager creates them.
type IncidentDetector struct {
	client.Client
	GVK               schema.GroupVersionKind
	Registry          *detection.Registry
	IncidentNamespace string
}

func (r *IncidentDetector) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(r.GVK)
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if obj.GetAnnotations()[noAutoRemediateAnnotation] == "true" {
		return ctrl.Result{}, nil
	}

	policies := r.Registry.ForGVK(r.GVK)
	if len(policies) == 0 {
		return ctrl.Result{}, nil
	}

	for _, p := range policies {
		// obj.Object is already the map CEL wants — no conversion needed.
		match, err := p.Matcher.Eval(obj.Object)
		if err != nil {
			l.V(1).Info("policy eval error", "policy", p.Name, "target", req.NamespacedName, "error", err)
			continue
		}
		if !match {
			continue
		}
		if err := r.createIncident(ctx, obj, p); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil // one incident per object
	}
	return ctrl.Result{}, nil
}

func (r *IncidentDetector) createIncident(ctx context.Context, obj *unstructured.Unstructured, p detection.CompiledPolicy) error {
	l := log.FromContext(ctx)
	ns := r.IncidentNamespace
	if ns == "" {
		ns = obj.GetNamespace()
	}
	uid := string(obj.GetUID())

	var existing opsv1alpha1.IncidentList
	if err := r.List(ctx, &existing,
		client.InNamespace(ns),
		client.MatchingLabels{targetUIDLabel: uid},
	); err != nil {
		return err
	}
	if len(existing.Items) > 0 {
		return nil
	}

	inc := &opsv1alpha1.Incident{
		ObjectMeta: metav1.ObjectMeta{
			Name:      strings.ToLower(p.Trigger) + "-" + uid,
			Namespace: ns,
			Labels:    map[string]string{targetUIDLabel: uid},
		},
		Spec: opsv1alpha1.IncidentSpec{
			// Taken from the live object, so the Incident records what was
			// actually matched rather than what the detector assumed.
			TargetRef: corev1.ObjectReference{
				APIVersion: obj.GetAPIVersion(),
				Kind:       obj.GetKind(),
				Name:       obj.GetName(),
				Namespace:  obj.GetNamespace(),
				UID:        obj.GetUID(),
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
	l.Info("Created Incident", "trigger", p.Trigger, "kind", obj.GetKind(), "target", obj.GetName(), "incident", inc.Name)
	return nil
}

// SetupWithManager registers a watch for this detector's GVK. One detector is
// registered per kind in detection.Envelope at startup, so an informer runs for
// every targetable kind whether or not a policy currently targets it. That
// wastes a cache on unused kinds; the envelope is small enough that the
// simplicity is worth more than the memory.
func (r *IncidentDetector) SetupWithManager(mgr ctrl.Manager) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(r.GVK)

	return ctrl.NewControllerManagedBy(mgr).
		For(obj, builder.WithPredicates(
			predicate.And(
				predicate.ResourceVersionChangedPredicate{},
				predicate.Funcs{DeleteFunc: func(event.DeleteEvent) bool { return false }},
			),
		)).
		Named(detectorName(r.GVK)).
		Complete(r)
}

// detectorName is a unique, metrics-safe controller name per GVK.
func detectorName(gvk schema.GroupVersionKind) string {
	group := gvk.Group
	if group == "" {
		group = "core"
	}
	return "incident-detector-" + strings.ToLower(group+"-"+gvk.Version+"-"+gvk.Kind)
}
