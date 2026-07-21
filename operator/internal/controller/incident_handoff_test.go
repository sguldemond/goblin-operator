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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	opsv1alpha1 "github.com/sguldemond/goblin/api/v1alpha1"
)

// The operator hands work to a persistent scout by phase, not by creating a
// Job. Queued is set only after grants succeed, which is what stops a scout
// from starting without the permissions its policy promised.
var _ = Describe("Incident handoff to the persistent scout", func() {
	var (
		reconciler *IncidentReconciler
		counter    int
	)

	BeforeEach(func() {
		counter++
		reconciler = &IncidentReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			Grants: &GrantManager{Client: k8sClient, Recorder: events.NewFakeRecorder(10)},
		}
	})

	newIncident := func() *opsv1alpha1.Incident {
		inc := &opsv1alpha1.Incident{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("handoff-%d", counter),
				Namespace: "default",
			},
			Spec: opsv1alpha1.IncidentSpec{
				TargetRef: corev1.ObjectReference{
					APIVersion: "apps/v1", Kind: "Deployment",
					Name: "some-deploy", Namespace: "default",
				},
				Trigger: "Test",
			},
		}
		Expect(k8sClient.Create(ctx, inc)).To(Succeed())
		DeferCleanup(func() {
			var got opsv1alpha1.Incident
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(inc), &got); err == nil {
				got.Finalizers = nil
				Expect(client.IgnoreNotFound(k8sClient.Update(ctx, &got))).To(Succeed())
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &got))).To(Succeed())
			}
		})
		return inc
	}

	It("moves a detected incident to Queued", func() {
		inc := newIncident()

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inc)})
		Expect(err).NotTo(HaveOccurred())

		var got opsv1alpha1.Incident
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inc), &got)).To(Succeed())
		Expect(got.Status.Phase).To(Equal(opsv1alpha1.PhaseQueued),
			"the scout watches for Queued; anything else leaves the incident unclaimed")
	})

	It("creates no Job — dispatch is the scout's job now", func() {
		inc := newIncident()

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inc)})
		Expect(err).NotTo(HaveOccurred())

		var jobs batchv1.JobList
		Expect(k8sClient.List(ctx, &jobs, client.InNamespace("default"))).To(Succeed())
		Expect(jobs.Items).To(BeEmpty())
	})

	It("adds the revoke finalizer before handing over", func() {
		inc := newIncident()

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inc)})
		Expect(err).NotTo(HaveOccurred())

		var got opsv1alpha1.Incident
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inc), &got)).To(Succeed())
		Expect(got.Finalizers).To(ContainElement(incidentFinalizer))
	})

	// Phases the scout owns are none of the operator's business, other than
	// cleaning up permissions once it is finished.
	It("leaves scout-owned phases alone", func() {
		inc := newIncident()
		inc.Status.Phase = opsv1alpha1.PhaseAssessing
		Expect(k8sClient.Status().Update(ctx, inc)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inc)})
		Expect(err).NotTo(HaveOccurred())

		var got opsv1alpha1.Incident
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inc), &got)).To(Succeed())
		Expect(got.Status.Phase).To(Equal(opsv1alpha1.PhaseAssessing))
	})
})
