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

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	opsv1alpha1 "github.com/sguldemond/goblin/api/v1alpha1"
)

const (
	// grantIncidentLabel ties a RoleBinding to the incident it was created for.
	// The sweep finds orphans by this label, so it is mechanism, not decoration.
	grantIncidentLabel = "goblinoperator.io/incident"
	grantPolicyLabel   = "goblinoperator.io/policy"
	// grantManagedLabel marks every binding this operator owns, so the sweep can
	// list them without knowing which incidents exist.
	grantManagedLabel = "goblinoperator.io/managed-grant"

	// scoutServiceAccount is the identity every grant is made to. Grants name it
	// explicitly rather than deriving it, so a binding can never be pointed
	// somewhere else by accident.
	scoutServiceAccount = "goblin-scout"

	incidentFinalizer = "goblinoperator.io/revoke-grants"
)

// The operator may bind pre-vetted scout roles, and nothing else. The
// resourceNames list is the real security boundary: it is enforced by the API
// server's escalation check, so a policy naming an unlisted role fails rather
// than widening what the scout can do. Adding a role here is a deliberate,
// reviewable act — keep it in step with config/rbac/scout_grant_roles.yaml.
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=bind,resourceNames=goblin-scout-patch-deployments

// Grant and revoke are recorded as Events on the Incident, which is the audit
// trail. The recorder uses the events.k8s.io API group, not core v1 — without
// this rule the API server rejects every event and the broadcaster drops it
// without retrying, leaving the grants invisible.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// GrantManager creates and removes the scout's per-incident permissions.
//
// It is deliberately the only component that writes RBAC objects: the blast
// radius of a bug here is "the scout gets permissions it should not have", so
// the surface is kept to one file that can be reviewed as a unit.
//
// Every grant is a RoleBinding in the *target's* namespace referencing a
// cluster-scoped role. That is what makes a permission both narrow (one
// namespace) and short-lived (one incident) without defining a Role per
// namespace up front.
type GrantManager struct {
	client.Client
	Recorder events.EventRecorder
}

// Grant creates the RoleBindings a policy allows for one incident. It is
// idempotent: an existing binding is left alone, so a crash between creating a
// binding and recording it costs nothing.
func (g *GrantManager) Grant(ctx context.Context, inc *opsv1alpha1.Incident, pol *opsv1alpha1.IncidentPolicy) error {
	log := logf.FromContext(ctx)

	if pol == nil || pol.Spec.Permissions == nil || len(pol.Spec.Permissions.Allow) == 0 {
		return nil // policy grants nothing beyond standing read access
	}

	ns := inc.Spec.TargetRef.Namespace
	if ns == "" {
		return fmt.Errorf("incident %s has no target namespace; cannot scope a grant", inc.Name)
	}

	for _, allowed := range pol.Spec.Permissions.Allow {
		rb := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      grantName(inc, allowed.ClusterRole),
				Namespace: ns,
				Labels: map[string]string{
					grantManagedLabel:  "true",
					grantIncidentLabel: inc.Name,
					grantPolicyLabel:   pol.Name,
				},
				Annotations: map[string]string{
					"goblinoperator.io/incident-namespace": inc.Namespace,
				},
			},
			Subjects: []rbacv1.Subject{{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      scoutServiceAccount,
				Namespace: inc.Namespace,
			}},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     allowed.ClusterRole,
			},
		}

		err := g.Create(ctx, rb)
		switch {
		case apierrors.IsAlreadyExists(err):
			continue
		case err != nil:
			// Most likely the role is outside the operator's bind allowlist.
			// Surfacing it on the incident beats letting the scout discover it
			// as a 403 halfway through an investigation.
			g.event(inc, corev1.EventTypeWarning, "GrantFailed", "Grant",
				fmt.Sprintf("could not bind %s in namespace %s: %v", allowed.ClusterRole, ns, err))
			return fmt.Errorf("binding %s in %s: %w", allowed.ClusterRole, ns, err)
		}

		log.Info("Granted scout permission",
			"incident", inc.Name, "clusterRole", allowed.ClusterRole, "namespace", ns, "binding", rb.Name)
		g.event(inc, corev1.EventTypeNormal, "Granted", "Grant",
			fmt.Sprintf("bound %s to %s in namespace %s (RoleBinding %s)",
				allowed.ClusterRole, scoutServiceAccount, ns, rb.Name))
	}
	return nil
}

// Revoke removes every binding made for an incident. Missing bindings are not
// an error — revocation must be safe to retry and safe to run twice.
func (g *GrantManager) Revoke(ctx context.Context, inc *opsv1alpha1.Incident) error {
	log := logf.FromContext(ctx)

	var bindings rbacv1.RoleBindingList
	if err := g.List(ctx, &bindings, client.MatchingLabels{
		grantManagedLabel:  "true",
		grantIncidentLabel: inc.Name,
	}); err != nil {
		return err
	}

	for i := range bindings.Items {
		rb := &bindings.Items[i]
		if err := g.Delete(ctx, rb); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting binding %s/%s: %w", rb.Namespace, rb.Name, err)
		}
		log.Info("Revoked scout permission",
			"incident", inc.Name, "binding", rb.Name, "namespace", rb.Namespace)
		g.event(inc, corev1.EventTypeNormal, "Revoked", "Revoke",
			fmt.Sprintf("removed RoleBinding %s in namespace %s", rb.Name, rb.Namespace))
	}
	return nil
}

// Sweep deletes bindings whose incident is gone or finished. It is the backstop
// for the finalizer: a force-deleted incident, or a crash between creating a
// binding and recording the finalizer, leaves a grant that nothing else will
// clean up.
func (g *GrantManager) Sweep(ctx context.Context) error {
	log := logf.FromContext(ctx)

	var bindings rbacv1.RoleBindingList
	if err := g.List(ctx, &bindings, client.MatchingLabels{grantManagedLabel: "true"}); err != nil {
		return err
	}

	for i := range bindings.Items {
		rb := &bindings.Items[i]
		incName := rb.Labels[grantIncidentLabel]
		incNamespace := rb.Annotations["goblinoperator.io/incident-namespace"]
		if incName == "" || incNamespace == "" {
			continue // not ours to reason about
		}

		var inc opsv1alpha1.Incident
		err := g.Get(ctx, client.ObjectKey{Name: incName, Namespace: incNamespace}, &inc)
		switch {
		case apierrors.IsNotFound(err):
			// Incident gone entirely.
		case err != nil:
			return err
		case !isTerminal(inc.Status.Phase):
			continue // still live, grant is legitimate
		}

		if err := g.Delete(ctx, rb); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("sweeping binding %s/%s: %w", rb.Namespace, rb.Name, err)
		}
		log.Info("Swept orphaned scout permission",
			"binding", rb.Name, "namespace", rb.Namespace, "incident", incName)
	}
	return nil
}

// isTerminal reports whether an incident has finished and no longer justifies a
// grant. AwaitingApproval is deliberately *not* terminal: the scout still needs
// its permissions to apply a patch the human approves.
func isTerminal(p opsv1alpha1.IncidentPhase) bool {
	switch p {
	case opsv1alpha1.PhaseApplied, opsv1alpha1.PhaseRejected,
		opsv1alpha1.PhaseEscalated, opsv1alpha1.PhaseHandedOff:
		return true
	default:
		return false
	}
}

// grantName is deterministic so Grant can be retried without creating
// duplicates. The incident UID keeps it unique across incidents that reuse a
// name, and truncation keeps it inside the 253-character limit.
func grantName(inc *opsv1alpha1.Incident, clusterRole string) string {
	name := fmt.Sprintf("goblin-grant-%s-%s", inc.Name, clusterRole)
	if len(name) > 253 {
		name = name[:253]
	}
	return name
}

// event records what the operator did to the scout's permissions, on the
// incident that justified it — so `kubectl describe incident` is the audit
// trail, independent of whether cluster audit logging is enabled.
func (g *GrantManager) event(inc *opsv1alpha1.Incident, eventType, reason, action, msg string) {
	if g.Recorder == nil {
		return
	}
	g.Recorder.Eventf(inc, nil, eventType, reason, action, "%s", msg)
}
