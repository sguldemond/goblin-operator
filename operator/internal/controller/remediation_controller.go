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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	opsv1alpha1 "github.com/sguldemond/goblin/api/v1alpha1"
)

const remediationJobLabel = "goblinoperator.io/remediation"
const noAutoRemediateAnnotation = "goblinoperator.io/no-auto-remediate"

// RemediationReconciler reconciles a Remediation object
type RemediationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=remediations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=remediations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=remediations/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete

func (r *RemediationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var rem opsv1alpha1.Remediation
	if err := r.Get(ctx, req.NamespacedName, &rem); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch rem.Status.Phase {
	case "", opsv1alpha1.PhaseDetected:
		return r.handleDetected(ctx, &rem)
	case opsv1alpha1.PhaseAssessing:
		return r.handleAssessing(ctx, &rem)
	default:
		log.Info("No action for phase", "phase", rem.Status.Phase)
		return ctrl.Result{}, nil
	}
}

func (r *RemediationReconciler) handleDetected(ctx context.Context, rem *opsv1alpha1.Remediation) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// If the pod carries the no-auto-remediate annotation, skip job creation.
	// The Remediation CR is still created so a local scout can handle it manually.
	var pod corev1.Pod
	podKey := client.ObjectKey{Name: rem.Spec.PodRef.Name, Namespace: rem.Spec.PodRef.Namespace}
	if err := r.Get(ctx, podKey, &pod); err == nil {
		if pod.Annotations[noAutoRemediateAnnotation] == "true" {
			log.Info("Pod opted out of auto-remediation, skipping job", "pod", podKey, "remediation", rem.Name)
			rem.Status.Phase = opsv1alpha1.PhaseAssessing
			return ctrl.Result{}, r.statusUpdate(ctx, rem)
		}
	}

	// Idempotency: check for an existing Job.
	existing, err := r.findJob(ctx, rem)
	if err != nil {
		return ctrl.Result{}, err
	}
	if existing == nil {
		if err := r.createScoutJob(ctx, rem); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Created scout Job", "remediation", rem.Name)
	}

	rem.Status.Phase = opsv1alpha1.PhaseAssessing
	return ctrl.Result{}, r.statusUpdate(ctx, rem)
}

func (r *RemediationReconciler) handleAssessing(ctx context.Context, rem *opsv1alpha1.Remediation) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	job, err := r.findJob(ctx, rem)
	if err != nil {
		return ctrl.Result{}, err
	}
	if job == nil {
		// Job was deleted externally; re-create on next reconcile by dropping back to Detected.
		rem.Status.Phase = opsv1alpha1.PhaseDetected
		return ctrl.Result{}, r.statusUpdate(ctx, rem)
	}

	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
			log.Info("Scout Job failed, escalating", "remediation", rem.Name)
			rem.Status.Phase = opsv1alpha1.PhaseEscalated
			rem.Status.Message = fmt.Sprintf("scout job failed: %s", cond.Message)
			return ctrl.Result{}, r.statusUpdate(ctx, rem)
		}
		if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
			log.Info("Scout Job completed", "remediation", rem.Name)
			rem.Status.Phase = opsv1alpha1.PhaseAwaitingApproval
			return ctrl.Result{}, r.statusUpdate(ctx, rem)
		}
	}

	// Job still running.
	return ctrl.Result{}, nil
}

func (r *RemediationReconciler) findJob(ctx context.Context, rem *opsv1alpha1.Remediation) (*batchv1.Job, error) {
	var jobs batchv1.JobList
	if err := r.List(ctx, &jobs,
		client.InNamespace(rem.Namespace),
		client.MatchingLabels{remediationJobLabel: rem.Name},
	); err != nil {
		return nil, err
	}
	if len(jobs.Items) == 0 {
		return nil, nil
	}
	return &jobs.Items[0], nil
}

func (r *RemediationReconciler) createScoutJob(ctx context.Context, rem *opsv1alpha1.Remediation) error {
	ttl := int32(300)

	scoutEnv := []corev1.EnvVar{
		{Name: "REMEDIATION_NAME", Value: rem.Name},
		{Name: "REMEDIATION_NAMESPACE", Value: rem.Namespace},
		{
			Name: "API_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "goblin-scout-secrets"},
					Key:                  "API_KEY",
				},
			},
		},
	}
	optional := true
	scoutEnv = append(scoutEnv,
		corev1.EnvVar{
			Name: "TELEGRAM_BOT_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "goblin-horn-secrets"},
					Key:                  "TELEGRAM_BOT_TOKEN",
					Optional:             &optional,
				},
			},
		},
		corev1.EnvVar{
			Name: "TELEGRAM_CHAT_ID",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "goblin-horn-secrets"},
					Key:                  "TELEGRAM_CHAT_ID",
					Optional:             &optional,
				},
			},
		},
	)

	podSpec := corev1.PodSpec{
		ServiceAccountName: "goblin-scout",
		RestartPolicy:      corev1.RestartPolicyNever,
		Containers: []corev1.Container{{
			Name:            "scout",
			Image:           "sguldemond/goblin-scout:latest",
			ImagePullPolicy: corev1.PullAlways,
			Stdin:           true,
			TTY:             true,
			Env:             scoutEnv,
		}},
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "goblin-scout-" + rem.Name + "-",
			Namespace:    rem.Namespace,
			Labels:       map[string]string{remediationJobLabel: rem.Name},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						remediationJobLabel: rem.Name,
					},
				},
				Spec: podSpec,
			},
		},
	}

	if err := ctrl.SetControllerReference(rem, job, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, job)
}

// statusUpdate writes status and silently requeues on conflict rather than
// logging an error. Conflicts happen when the agent patches status concurrently.
func (r *RemediationReconciler) statusUpdate(ctx context.Context, rem *opsv1alpha1.Remediation) error {
	if err := r.Status().Update(ctx, rem); apierrors.IsConflict(err) {
		return nil // controller-runtime will requeue on next watch event
	} else {
		return err
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *RemediationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&opsv1alpha1.Remediation{}).
		Owns(&batchv1.Job{}).
		Named("remediation").
		Complete(r)
}
