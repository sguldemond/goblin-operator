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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	opsv1alpha1 "github.com/sguldemond/goblin/api/v1alpha1"
	"github.com/sguldemond/goblin/internal/detection"
)

// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=incidentpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=incidentpolicies/status,verbs=get;update;patch

// PolicyReconciler compiles IncidentPolicy CEL expressions and publishes valid
// ones into the shared in-memory Registry for the detection controller to use.
type PolicyReconciler struct {
	client.Client
	Registry *detection.Registry
}

func (r *PolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var pol opsv1alpha1.IncidentPolicy
	if err := r.Get(ctx, req.NamespacedName, &pol); err != nil {
		if client.IgnoreNotFound(err) == nil {
			r.Registry.Delete(req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	gvk, gvkErr := parseGVK(pol.Spec.Target.APIVersion, pol.Spec.Target.Kind)
	var matcher *detection.Matcher
	var err error
	if gvkErr == nil {
		matcher, err = detection.Compile(pol.Spec.Detect.Expression)
	} else {
		err = gvkErr
	}

	if err != nil {
		r.Registry.Delete(pol.Name)
		l.Info("Policy invalid", "policy", pol.Name, "error", err)
		setValid(&pol, metav1.ConditionFalse, "InvalidExpression", err.Error())
		return ctrl.Result{}, r.Status().Update(ctx, &pol)
	}

	// Only envelope kinds have a detector watching them, so a policy targeting
	// anything else would compile and then never fire. Reject it visibly.
	if !detection.InEnvelope(gvk) {
		r.Registry.Delete(pol.Name)
		l.Info("Policy targets unsupported kind", "policy", pol.Name, "gvk", gvk)
		setValid(&pol, metav1.ConditionFalse, "UnsupportedKind",
			fmt.Sprintf("no detector watches %s; see detection.Envelope", gvk))
		return ctrl.Result{}, r.Status().Update(ctx, &pol)
	}

	r.Registry.Delete(pol.Name)
	r.Registry.Set(pol.Name, gvk, detection.CompiledPolicy{
		Name: pol.Name, Trigger: pol.Spec.Detect.Trigger, Matcher: matcher,
	})
	setValid(&pol, metav1.ConditionTrue, "Compiled", "expression compiled")
	return ctrl.Result{}, r.Status().Update(ctx, &pol)
}

func parseGVK(apiVersion, kind string) (schema.GroupVersionKind, error) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return schema.GroupVersionKind{}, fmt.Errorf("bad target apiVersion %q: %w", apiVersion, err)
	}
	return gv.WithKind(kind), nil
}

func setValid(pol *opsv1alpha1.IncidentPolicy, status metav1.ConditionStatus, reason, msg string) {
	cond := metav1.Condition{
		Type: "Valid", Status: status, Reason: reason, Message: msg,
		LastTransitionTime: metav1.Now(), ObservedGeneration: pol.Generation,
	}
	for i, c := range pol.Status.Conditions {
		if c.Type == "Valid" {
			if c.Status == status && c.Reason == reason {
				cond.LastTransitionTime = c.LastTransitionTime
			}
			pol.Status.Conditions[i] = cond
			return
		}
	}
	pol.Status.Conditions = append(pol.Status.Conditions, cond)
}

func (r *PolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&opsv1alpha1.IncidentPolicy{}).
		Named("incidentpolicy").
		Complete(r)
}
