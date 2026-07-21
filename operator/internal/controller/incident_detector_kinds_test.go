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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	opsv1alpha1 "github.com/sguldemond/goblin/api/v1alpha1"
	"github.com/sguldemond/goblin/internal/detection"
)

var (
	deploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	secretGVK     = schema.GroupVersionKind{Version: "v1", Kind: "Secret"}
)

// The detector must work for kinds other than Pod. Reconcile is driven directly
// rather than through a manager: the watch wiring is controller-runtime's job,
// what matters here is that a non-Pod object is evaluated and recorded as
// itself.
var _ = Describe("IncidentDetector with a non-Pod target", func() {
	const namespace = "default"
	var (
		registry *detection.Registry
		detector *IncidentDetector
	)

	BeforeEach(func() {
		registry = detection.NewRegistry()
		detector = &IncidentDetector{
			Client:            k8sClient,
			GVK:               deploymentGVK,
			Registry:          registry,
			IncidentNamespace: namespace,
		}
	})

	It("creates an Incident whose TargetRef describes the Deployment", func() {
		matcher, err := detection.Compile("has(object.spec.replicas) && object.spec.replicas == 3")
		Expect(err).NotTo(HaveOccurred())
		registry.Set("replicas-three", deploymentGVK, detection.CompiledPolicy{
			Name: "replicas-three", Trigger: "ReplicasThree", Matcher: matcher,
		})

		replicas := int32(3)
		deploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "kinds-test", Namespace: namespace},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "kinds-test"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "kinds-test"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "app", Image: "busybox"}},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, deploy)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, deploy))).To(Succeed())
		})

		_, err = detector.Reconcile(ctx, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(deploy),
		})
		Expect(err).NotTo(HaveOccurred())

		var incidents opsv1alpha1.IncidentList
		Expect(k8sClient.List(ctx, &incidents,
			client.InNamespace(namespace),
			client.MatchingLabels{targetUIDLabel: string(deploy.UID)},
		)).To(Succeed())
		Expect(incidents.Items).To(HaveLen(1))

		inc := incidents.Items[0]
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &inc))).To(Succeed())
		})

		// A Pod literal creeping back into createIncident fails here.
		Expect(inc.Spec.TargetRef.Kind).To(Equal("Deployment"))
		Expect(inc.Spec.TargetRef.APIVersion).To(Equal("apps/v1"))
		Expect(inc.Spec.TargetRef.Name).To(Equal("kinds-test"))
		Expect(inc.Spec.TargetRef.UID).To(Equal(deploy.UID))
		Expect(inc.Spec.Trigger).To(Equal("ReplicasThree"))
	})
})

var _ = Describe("PolicyReconciler envelope gating", func() {
	It("marks a policy targeting an unwatched kind as invalid", func() {
		registry := detection.NewRegistry()
		r := &PolicyReconciler{Client: k8sClient, Registry: registry}

		pol := &opsv1alpha1.IncidentPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "targets-secret"},
			Spec: opsv1alpha1.IncidentPolicySpec{
				Target: opsv1alpha1.TargetGVK{APIVersion: "v1", Kind: "Secret"},
				Detect: opsv1alpha1.DetectSpec{Trigger: "Whatever", Expression: "true"},
			},
		}
		Expect(k8sClient.Create(ctx, pol)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, pol))).To(Succeed())
		})

		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pol)})
		Expect(err).NotTo(HaveOccurred())

		var got opsv1alpha1.IncidentPolicy
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pol), &got)).To(Succeed())

		var valid *metav1.Condition
		for i := range got.Status.Conditions {
			if got.Status.Conditions[i].Type == "Valid" {
				valid = &got.Status.Conditions[i]
			}
		}
		Expect(valid).NotTo(BeNil())
		Expect(valid.Status).To(Equal(metav1.ConditionFalse))
		Expect(valid.Reason).To(Equal("UnsupportedKind"))

		// It must not reach the registry either, or it would look active while
		// nothing evaluates it.
		Expect(registry.ForGVK(secretGVK)).To(BeEmpty())
	})
})
