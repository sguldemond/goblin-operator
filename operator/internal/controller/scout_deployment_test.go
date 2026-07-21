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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("ScoutReconciler", func() {
	const ns = "default"
	var r *ScoutReconciler

	key := types.NamespacedName{Name: scoutName, Namespace: ns}

	BeforeEach(func() {
		r = &ScoutReconciler{Client: k8sClient, Namespace: ns, Image: "example/scout:test"}
		DeferCleanup(func() {
			var d appsv1.Deployment
			if err := k8sClient.Get(ctx, key, &d); err == nil {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &d))).To(Succeed())
			}
		})
	})

	It("creates the scout Deployment when absent", func() {
		Expect(r.Ensure(ctx)).To(Succeed())

		var d appsv1.Deployment
		Expect(k8sClient.Get(ctx, key, &d)).To(Succeed())
		Expect(d.Spec.Template.Spec.Containers).To(HaveLen(1))
		Expect(d.Spec.Template.Spec.Containers[0].Image).To(Equal("example/scout:test"))
		Expect(d.Spec.Template.Spec.ServiceAccountName).To(Equal(scoutName))
	})

	// Two scouts would share one Telegram chat and one set of grants, so a
	// rolling update — which deliberately runs two pods — is not safe.
	It("pins one replica and the Recreate strategy", func() {
		Expect(r.Ensure(ctx)).To(Succeed())

		var d appsv1.Deployment
		Expect(k8sClient.Get(ctx, key, &d)).To(Succeed())
		Expect(*d.Spec.Replicas).To(BeNumerically("==", 1))
		Expect(d.Spec.Strategy.Type).To(Equal(appsv1.RecreateDeploymentStrategyType))
	})

	// The scout runs as a Deployment, so without this the detector would raise
	// incidents about the agent investigating incidents.
	It("marks the scout pod no-auto-remediate", func() {
		Expect(r.Ensure(ctx)).To(Succeed())

		var d appsv1.Deployment
		Expect(k8sClient.Get(ctx, key, &d)).To(Succeed())
		Expect(d.Spec.Template.Annotations).To(HaveKeyWithValue(noAutoRemediateAnnotation, "true"))
	})

	It("is idempotent", func() {
		Expect(r.Ensure(ctx)).To(Succeed())
		Expect(r.Ensure(ctx)).To(Succeed())

		var list appsv1.DeploymentList
		Expect(k8sClient.List(ctx, &list, client.InNamespace(ns))).To(Succeed())
		var count int
		for _, d := range list.Items {
			if d.Name == scoutName {
				count++
			}
		}
		Expect(count).To(Equal(1))
	})

	It("corrects a scaled-up scout", func() {
		Expect(r.Ensure(ctx)).To(Succeed())

		var d appsv1.Deployment
		Expect(k8sClient.Get(ctx, key, &d)).To(Succeed())
		two := int32(2)
		d.Spec.Replicas = &two
		Expect(k8sClient.Update(ctx, &d)).To(Succeed())

		Expect(r.Ensure(ctx)).To(Succeed())

		Expect(k8sClient.Get(ctx, key, &d)).To(Succeed())
		Expect(*d.Spec.Replicas).To(BeNumerically("==", 1),
			"a second scout would double-claim incidents and confuse the human")
	})

	It("rolls out a new image", func() {
		Expect(r.Ensure(ctx)).To(Succeed())

		r.Image = "example/scout:v2"
		Expect(r.Ensure(ctx)).To(Succeed())

		var d appsv1.Deployment
		Expect(k8sClient.Get(ctx, key, &d)).To(Succeed())
		Expect(d.Spec.Template.Spec.Containers[0].Image).To(Equal("example/scout:v2"))
	})
})
