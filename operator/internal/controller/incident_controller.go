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

const incidentJobLabel = "goblinoperator.io/incident"
const noAutoRemediateAnnotation = "goblinoperator.io/no-auto-remediate"

// preStartFailureReasons are container waiting reasons that mean the scout pod
// can never start without intervention — no point waiting for the Job to finish.
var preStartFailureReasons = map[string]bool{
	"CreateContainerConfigError": true,
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"InvalidImageName":           true,
}

// IncidentReconciler reconciles an Incident object
type IncidentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=incidents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=incidents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ops.goblinoperator.io,resources=incidents/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete

func (r *IncidentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var inc opsv1alpha1.Incident
	if err := r.Get(ctx, req.NamespacedName, &inc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch inc.Status.Phase {
	case "", opsv1alpha1.PhaseDetected:
		return r.handleDetected(ctx, &inc)
	case opsv1alpha1.PhaseAssessing:
		return r.handleAssessing(ctx, &inc)
	default:
		log.Info("No action for phase", "phase", inc.Status.Phase)
		return ctrl.Result{}, nil
	}
}

func (r *IncidentReconciler) handleDetected(ctx context.Context, inc *opsv1alpha1.Incident) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Idempotency: check for an existing Job.
	existing, err := r.findJob(ctx, inc)
	if err != nil {
		return ctrl.Result{}, err
	}
	if existing == nil {
		if err := r.createScoutJob(ctx, inc); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Created scout Job", "incident", inc.Name)
	}

	inc.Status.Phase = opsv1alpha1.PhaseAssessing
	return ctrl.Result{}, r.statusUpdate(ctx, inc)
}

func (r *IncidentReconciler) handleAssessing(ctx context.Context, inc *opsv1alpha1.Incident) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	job, err := r.findJob(ctx, inc)
	if err != nil {
		return ctrl.Result{}, err
	}
	if job == nil {
		// Job was deleted externally; re-create on next reconcile by dropping back to Detected.
		inc.Status.Phase = opsv1alpha1.PhaseDetected
		return ctrl.Result{}, r.statusUpdate(ctx, inc)
	}

	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
			log.Info("Scout Job failed, escalating", "incident", inc.Name)
			inc.Status.Phase = opsv1alpha1.PhaseEscalated
			inc.Status.Message = fmt.Sprintf("scout job failed: %s", cond.Message)
			return ctrl.Result{}, r.statusUpdate(ctx, inc)
		}
		if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
			log.Info("Scout Job completed", "incident", inc.Name)
			inc.Status.Phase = opsv1alpha1.PhaseAwaitingApproval
			return ctrl.Result{}, r.statusUpdate(ctx, inc)
		}
	}

	// Check for pod-level pre-start failures (e.g. missing secret key).
	if msg := r.scoutPodFailureMessage(ctx, inc.Namespace, inc.Name); msg != "" {
		log.Info("Scout pod stuck in pre-start failure, escalating", "incident", inc.Name, "reason", msg)
		inc.Status.Phase = opsv1alpha1.PhaseEscalated
		inc.Status.Message = fmt.Sprintf("scout pod cannot start: %s", msg)
		return ctrl.Result{}, r.statusUpdate(ctx, inc)
	}

	// Job still running.
	return ctrl.Result{}, nil
}

// scoutPodFailureMessage returns the waiting message if the scout pod is stuck
// in a pre-start failure state (CreateContainerConfigError, ImagePullBackOff, etc.).
func (r *IncidentReconciler) scoutPodFailureMessage(ctx context.Context, namespace, incName string) string {
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(namespace),
		client.MatchingLabels{incidentJobLabel: incName},
	); err != nil {
		return ""
	}
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil && preStartFailureReasons[cs.State.Waiting.Reason] {
				return cs.State.Waiting.Message
			}
		}
	}
	return ""
}

func (r *IncidentReconciler) findJob(ctx context.Context, inc *opsv1alpha1.Incident) (*batchv1.Job, error) {
	var jobs batchv1.JobList
	if err := r.List(ctx, &jobs,
		client.InNamespace(inc.Namespace),
		client.MatchingLabels{incidentJobLabel: inc.Name},
	); err != nil {
		return nil, err
	}
	if len(jobs.Items) == 0 {
		return nil, nil
	}
	return &jobs.Items[0], nil
}

func (r *IncidentReconciler) createScoutJob(ctx context.Context, inc *opsv1alpha1.Incident) error {
	ttl := int32(300)
	optional := true

	scoutEnv := []corev1.EnvVar{
		{Name: "INCIDENT_NAME", Value: inc.Name},
		{Name: "INCIDENT_NAMESPACE", Value: inc.Namespace},
		{
			Name: "LLM_API_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "goblin-scout-secrets"},
					Key:                  "LLM_API_KEY",
				},
			},
		},
	}
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
			GenerateName: "goblin-scout-" + inc.Name + "-",
			Namespace:    inc.Namespace,
			Labels:       map[string]string{incidentJobLabel: inc.Name},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{incidentJobLabel: inc.Name},
					Annotations: map[string]string{noAutoRemediateAnnotation: "true"},
				},
				Spec: podSpec,
			},
		},
	}

	if err := ctrl.SetControllerReference(inc, job, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, job)
}

// statusUpdate writes status and silently requeues on conflict rather than
// logging an error. Conflicts happen when the agent patches status concurrently.
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
		Owns(&batchv1.Job{}).
		Named("incident").
		Complete(r)
}
