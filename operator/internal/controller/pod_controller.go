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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	opsv1alpha1 "github.com/sguldemond/goblin/api/v1alpha1"
)

const podUIDLabel = "goblinoperator.io/pod-uid"

// PodReconciler watches Pods and creates Remediation CRs for failed pods.
type PodReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	RemediationNamespace string
}

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=remediations,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=remediations/status,verbs=update

func (r *PodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	trigger := detectTrigger(pod)
	if trigger == "" {
		return ctrl.Result{}, nil
	}

	// One Remediation per pod UID — idempotency across trigger types.
	remNS := r.RemediationNamespace
	if remNS == "" {
		remNS = req.Namespace
	}
	var existing opsv1alpha1.RemediationList
	if err := r.List(ctx, &existing,
		client.InNamespace(remNS),
		client.MatchingLabels{podUIDLabel: string(pod.UID)},
	); err != nil {
		return ctrl.Result{}, err
	}
	if len(existing.Items) > 0 {
		return ctrl.Result{}, nil
	}

	namePrefix := strings.ToLower(trigger) + "-" + pod.Name + "-"
	rem := &opsv1alpha1.Remediation{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: namePrefix,
			Namespace:    remNS,
			Labels:       map[string]string{podUIDLabel: string(pod.UID)},
		},
		Spec: opsv1alpha1.RemediationSpec{
			PodRef: corev1.ObjectReference{
				APIVersion: "v1",
				Kind:       "Pod",
				Name:       pod.Name,
				Namespace:  pod.Namespace,
				UID:        pod.UID,
			},
			Trigger:     trigger,
			MaxAttempts: 2,
		},
	}

	if err := r.Create(ctx, rem); err != nil {
		if errors.IsAlreadyExists(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	rem.Status.Phase = opsv1alpha1.PhaseDetected
	if err := r.Status().Update(ctx, rem); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Created Remediation", "trigger", trigger, "pod", req.NamespacedName, "remediation", rem.Name)
	return ctrl.Result{}, nil
}

// detectTrigger returns the trigger type for a pod that needs remediation, or "".
// Pods annotated with noAutoRemediateAnnotation are excluded to prevent spawn loops.
func detectTrigger(pod corev1.Pod) string {
	if pod.Annotations[noAutoRemediateAnnotation] == "true" {
		return ""
	}
	if hasOOMKilled(pod) {
		return "OOMKilled"
	}
	if hasUnschedulable(pod) {
		return "Unschedulable"
	}
	return ""
}

func hasOOMKilled(pod corev1.Pod) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.LastTerminationState.Terminated != nil &&
			cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
			return true
		}
	}
	return false
}

func hasUnschedulable(pod corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodPending {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodScheduled &&
			cond.Status == corev1.ConditionFalse &&
			cond.Reason == "Unschedulable" {
			return true
		}
	}
	return false
}

// podFailurePredicate fires when a pod has an actionable failure condition.
var podFailurePredicate = predicate.Funcs{
	CreateFunc: func(e event.CreateEvent) bool {
		pod, ok := e.Object.(*corev1.Pod)
		if !ok {
			return false
		}
		return detectTrigger(*pod) != ""
	},
	UpdateFunc: func(e event.UpdateEvent) bool {
		pod, ok := e.ObjectNew.(*corev1.Pod)
		if !ok {
			return false
		}
		return detectTrigger(*pod) != ""
	},
	DeleteFunc:  func(event.DeleteEvent) bool { return false },
	GenericFunc: func(event.GenericEvent) bool { return false },
}

func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}, builder.WithPredicates(podFailurePredicate)).
		Named("pod-failure").
		Complete(r)
}
