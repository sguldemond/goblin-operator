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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	opsv1alpha1 "github.com/sguldemond/goblin/api/v1alpha1"
)

// NOTE: these tests cover the operator's own logic — which bindings it creates
// and removes. They do NOT prove the security boundary: the resourceNames
// allowlist and the RBAC escalation check are API-server authorization
// behaviour, and envtest runs permissively, so a test asserting "binding an
// unvetted role is refused" would pass here for the wrong reason. That
// verification belongs on a real cluster.
var _ = Describe("GrantManager", func() {
	const targetNS = "default"
	var (
		grants   *GrantManager
		recorder *events.FakeRecorder
		counter  int
	)

	BeforeEach(func() {
		recorder = events.NewFakeRecorder(20)
		grants = &GrantManager{Client: k8sClient, Recorder: recorder}
		counter++
	})

	// newIncident returns a persisted Incident targeting a Deployment in
	// targetNS. The Incident itself lives in the goblin namespace, which is why
	// grants cannot use ownerReferences.
	newIncident := func(phase opsv1alpha1.IncidentPhase) *opsv1alpha1.Incident {
		inc := &opsv1alpha1.Incident{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("grant-test-%d", counter),
				Namespace: "default",
			},
			Spec: opsv1alpha1.IncidentSpec{
				TargetRef: corev1.ObjectReference{
					APIVersion: "apps/v1", Kind: "Deployment",
					Name: "some-deploy", Namespace: targetNS,
				},
				Trigger:   "Test",
				PolicyRef: "test-policy",
			},
		}
		Expect(k8sClient.Create(ctx, inc)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, inc))).To(Succeed())
		})
		if phase != "" {
			inc.Status.Phase = phase
			Expect(k8sClient.Status().Update(ctx, inc)).To(Succeed())
		}
		return inc
	}

	policyAllowing := func(roles ...string) *opsv1alpha1.IncidentPolicy {
		allow := make([]opsv1alpha1.AllowedRole, 0, len(roles))
		for _, r := range roles {
			allow = append(allow, opsv1alpha1.AllowedRole{ClusterRole: r})
		}
		return &opsv1alpha1.IncidentPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
			Spec: opsv1alpha1.IncidentPolicySpec{
				Permissions: &opsv1alpha1.PermissionsSpec{Allow: allow},
			},
		}
	}

	bindingsFor := func(inc *opsv1alpha1.Incident) []rbacv1.RoleBinding {
		var list rbacv1.RoleBindingList
		Expect(k8sClient.List(ctx, &list, client.MatchingLabels{
			grantManagedLabel:  "true",
			grantIncidentLabel: inc.Name,
		})).To(Succeed())
		return list.Items
	}

	It("binds in the target namespace, not the incident's", func() {
		inc := newIncident(opsv1alpha1.PhaseDetected)
		Expect(grants.Grant(ctx, inc, policyAllowing("goblin-scout-patch-deployments"))).To(Succeed())

		bindings := bindingsFor(inc)
		Expect(bindings).To(HaveLen(1))
		rb := bindings[0]
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &rb))).To(Succeed())
		})

		Expect(rb.Namespace).To(Equal(targetNS))
		Expect(rb.RoleRef.Kind).To(Equal("ClusterRole"))
		Expect(rb.RoleRef.Name).To(Equal("goblin-scout-patch-deployments"))

		// The subject must always be the scout, never anything else.
		Expect(rb.Subjects).To(HaveLen(1))
		Expect(rb.Subjects[0].Name).To(Equal(scoutServiceAccount))
		Expect(rb.Subjects[0].Kind).To(Equal(rbacv1.ServiceAccountKind))
	})

	It("grants nothing when the policy declares no permissions", func() {
		inc := newIncident(opsv1alpha1.PhaseDetected)
		pol := &opsv1alpha1.IncidentPolicy{ObjectMeta: metav1.ObjectMeta{Name: "test-policy"}}

		Expect(grants.Grant(ctx, inc, pol)).To(Succeed())
		Expect(bindingsFor(inc)).To(BeEmpty())
	})

	It("tolerates a nil policy", func() {
		inc := newIncident(opsv1alpha1.PhaseDetected)
		Expect(grants.Grant(ctx, inc, nil)).To(Succeed())
		Expect(bindingsFor(inc)).To(BeEmpty())
	})

	It("is idempotent, so a retried grant does not duplicate bindings", func() {
		inc := newIncident(opsv1alpha1.PhaseDetected)
		pol := policyAllowing("goblin-scout-patch-deployments")

		Expect(grants.Grant(ctx, inc, pol)).To(Succeed())
		Expect(grants.Grant(ctx, inc, pol)).To(Succeed())

		bindings := bindingsFor(inc)
		Expect(bindings).To(HaveLen(1))
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &bindings[0]))).To(Succeed())
		})
	})

	It("revokes every binding it made", func() {
		inc := newIncident(opsv1alpha1.PhaseDetected)
		Expect(grants.Grant(ctx, inc, policyAllowing("goblin-scout-patch-deployments"))).To(Succeed())
		Expect(bindingsFor(inc)).To(HaveLen(1))

		Expect(grants.Revoke(ctx, inc)).To(Succeed())
		Expect(bindingsFor(inc)).To(BeEmpty())
	})

	It("revokes safely when there is nothing to revoke", func() {
		inc := newIncident(opsv1alpha1.PhaseDetected)
		Expect(grants.Revoke(ctx, inc)).To(Succeed())
	})

	It("sweeps grants whose incident is gone", func() {
		inc := newIncident(opsv1alpha1.PhaseDetected)
		Expect(grants.Grant(ctx, inc, policyAllowing("goblin-scout-patch-deployments"))).To(Succeed())
		Expect(bindingsFor(inc)).To(HaveLen(1))

		// Simulate a force-delete: the incident vanishes without the finalizer
		// running.
		Expect(k8sClient.Delete(ctx, inc)).To(Succeed())
		Eventually(func() bool {
			var got opsv1alpha1.Incident
			err := k8sClient.Get(ctx, types.NamespacedName{Name: inc.Name, Namespace: inc.Namespace}, &got)
			return err != nil
		}, "10s", "200ms").Should(BeTrue())

		Expect(grants.Sweep(ctx)).To(Succeed())
		Expect(bindingsFor(inc)).To(BeEmpty())
	})

	It("sweeps grants whose incident has finished", func() {
		inc := newIncident(opsv1alpha1.PhaseDetected)
		Expect(grants.Grant(ctx, inc, policyAllowing("goblin-scout-patch-deployments"))).To(Succeed())

		inc.Status.Phase = opsv1alpha1.PhaseApplied
		Expect(k8sClient.Status().Update(ctx, inc)).To(Succeed())

		Expect(grants.Sweep(ctx)).To(Succeed())
		Expect(bindingsFor(inc)).To(BeEmpty())
	})

	// A scout awaiting approval still has to apply the patch afterwards.
	It("leaves grants alone while the incident is still live", func() {
		inc := newIncident(opsv1alpha1.PhaseAwaitingApproval)
		Expect(grants.Grant(ctx, inc, policyAllowing("goblin-scout-patch-deployments"))).To(Succeed())

		Expect(grants.Sweep(ctx)).To(Succeed())

		bindings := bindingsFor(inc)
		Expect(bindings).To(HaveLen(1))
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &bindings[0]))).To(Succeed())
		})
	})

	It("records an Event for each grant and revoke", func() {
		inc := newIncident(opsv1alpha1.PhaseDetected)
		Expect(grants.Grant(ctx, inc, policyAllowing("goblin-scout-patch-deployments"))).To(Succeed())
		Expect(grants.Revoke(ctx, inc)).To(Succeed())

		Expect(recorder.Events).To(Receive(ContainSubstring("Granted")))
		Expect(recorder.Events).To(Receive(ContainSubstring("Revoked")))
	})
})
