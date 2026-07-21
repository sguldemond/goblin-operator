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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	opsv1alpha1 "github.com/sguldemond/goblin/api/v1alpha1"
)

const noAutoRemediateAnnotation = "goblinoperator.io/no-auto-remediate"

// IncidentReconciler reconciles an Incident object
type IncidentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Grants *GrantManager
}

// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=incidents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=incidents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=incidents/finalizers,verbs=update

func (r *IncidentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var inc opsv1alpha1.Incident
	if err := r.Get(ctx, req.NamespacedName, &inc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deleting: revoke before letting the object go, so a grant can never
	// outlive the incident that justified it.
	if !inc.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &inc)
	}

	switch inc.Status.Phase {
	case "", opsv1alpha1.PhaseDetected:
		return r.handleDetected(ctx, &inc)
	default:
		// Terminal phases: the scout is finished, so its permissions go away
		// even though the Incident is kept for the record.
		if isTerminal(inc.Status.Phase) {
			if err := r.Grants.Revoke(ctx, &inc); err != nil {
				return ctrl.Result{}, err
			}
		}
		log.Info("No action for phase", "phase", inc.Status.Phase)
		return ctrl.Result{}, nil
	}
}

func (r *IncidentReconciler) handleDeletion(ctx context.Context, inc *opsv1alpha1.Incident) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(inc, incidentFinalizer) {
		return ctrl.Result{}, nil
	}
	if err := r.Grants.Revoke(ctx, inc); err != nil {
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(inc, incidentFinalizer)
	return ctrl.Result{}, r.Update(ctx, inc)
}

func (r *IncidentReconciler) handleDetected(ctx context.Context, inc *opsv1alpha1.Incident) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// The finalizer must exist before any grant, or a crash in between leaves a
	// binding with nothing to trigger its removal.
	if !controllerutil.ContainsFinalizer(inc, incidentFinalizer) {
		controllerutil.AddFinalizer(inc, incidentFinalizer)
		if err := r.Update(ctx, inc); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Grant before dispatching: a scout that starts without the permissions its
	// policy promised fails confusingly, mid-investigation.
	pol, err := r.policyFor(ctx, inc)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Grants.Grant(ctx, inc, pol); err != nil {
		inc.Status.Phase = opsv1alpha1.PhaseEscalated
		inc.Status.Message = fmt.Sprintf("could not grant scout permissions: %v", err)
		return ctrl.Result{}, r.statusUpdate(ctx, inc)
	}

	// Queued is the handoff: permissions are in place, so a scout may now claim
	// this. The operator does not dispatch anything — the scout watches for
	// Queued and takes what it wants. Setting this only after a successful
	// grant is what guarantees a scout never starts without its permissions.
	log.Info("Incident queued for a scout", "incident", inc.Name)
	inc.Status.Phase = opsv1alpha1.PhaseQueued
	return ctrl.Result{}, r.statusUpdate(ctx, inc)
}

// policyFor loads the policy that raised an incident. A missing policy is not
// fatal: the incident still deserves a scout, it just gets no extra permissions.
func (r *IncidentReconciler) policyFor(ctx context.Context, inc *opsv1alpha1.Incident) (*opsv1alpha1.IncidentPolicy, error) {
	if inc.Spec.PolicyRef == "" {
		return nil, nil
	}
	var pol opsv1alpha1.IncidentPolicy
	err := r.Get(ctx, client.ObjectKey{Name: inc.Spec.PolicyRef}, &pol)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pol, nil
}

func (r *IncidentReconciler) statusUpdate(ctx context.Context, inc *opsv1alpha1.Incident) error {
	if err := r.Status().Update(ctx, inc); apierrors.IsConflict(err) {
		return nil // controller-runtime will requeue on next watch event
	} else {
		return err
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *IncidentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&opsv1alpha1.Incident{}).
		Named("incident").
		Complete(r)
}
