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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	opsv1alpha1 "github.com/sguldemond/goblin/api/v1alpha1"
	"github.com/sguldemond/goblin/internal/detection"
)

const oomCELExpr = `has(object.status.containerStatuses) && object.status.containerStatuses.exists(c, has(c.lastState) && has(c.lastState.terminated) && c.lastState.terminated.reason == 'OOMKilled')`

var _ = Describe("IncidentDetector", func() {
	const namespace = "default"

	var registry *detection.Registry
	var detector *IncidentDetector

	BeforeEach(func() {
		registry = detection.NewRegistry()
		matcher, err := detection.Compile(oomCELExpr)
		Expect(err).NotTo(HaveOccurred())
		registry.Set("oom-policy", schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, detection.CompiledPolicy{
			Name:    "oom-policy",
			Trigger: "OOMKilled",
			Matcher: matcher,
		})
		detector = &IncidentDetector{
			Client:            k8sClient,
			GVK:               schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
			Registry:          registry,
			IncidentNamespace: namespace,
		}
	})

	makePod := func(name string) *corev1.Pod {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  "app",
					Image: "busybox",
				}},
			},
		}
		Expect(k8sClient.Create(context.Background(), pod)).To(Succeed())
		return pod
	}

	cleanupIncidents := func() {
		var list opsv1alpha1.IncidentList
		Expect(k8sClient.List(context.Background(), &list, client.InNamespace(namespace))).To(Succeed())
		for i := range list.Items {
			Expect(k8sClient.Delete(context.Background(), &list.Items[i])).To(Succeed())
		}
	}

	AfterEach(func() {
		cleanupIncidents()
	})

	It("creates an Incident for an OOMKilled pod", func() {
		ctx := context.Background()
		pod := makePod(fmt.Sprintf("oom-pod-%d", GinkgoRandomSeed()))
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, pod)
		})

		pod.Status = corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app",
				LastTerminationState: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"},
				},
			}},
		}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		_, err := detector.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace},
		})
		Expect(err).NotTo(HaveOccurred())

		var list opsv1alpha1.IncidentList
		Expect(k8sClient.List(ctx, &list,
			client.InNamespace(namespace),
			client.MatchingLabels{targetUIDLabel: string(pod.UID)},
		)).To(Succeed())
		Expect(list.Items).To(HaveLen(1))
		Expect(list.Items[0].Spec.Trigger).To(Equal("OOMKilled"))
		Expect(list.Items[0].Spec.PolicyRef).To(Equal("oom-policy"))
		Expect(list.Items[0].Labels[targetUIDLabel]).To(Equal(string(pod.UID)))
	})

	It("creates exactly one Incident when the same pod is reconciled twice", func() {
		ctx := context.Background()
		pod := makePod(fmt.Sprintf("oom-pod-dup-%d", GinkgoRandomSeed()))
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, pod)
		})

		pod.Status = corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app",
				LastTerminationState: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"},
				},
			}},
		}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}}
		_, err := detector.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = detector.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var list opsv1alpha1.IncidentList
		Expect(k8sClient.List(ctx, &list,
			client.InNamespace(namespace),
			client.MatchingLabels{targetUIDLabel: string(pod.UID)},
		)).To(Succeed())
		Expect(list.Items).To(HaveLen(1))
	})

	It("creates no Incident for a pod annotated no-auto-remediate", func() {
		ctx := context.Background()
		pod := makePod(fmt.Sprintf("oom-pod-optout-%d", GinkgoRandomSeed()))
		pod.Annotations = map[string]string{noAutoRemediateAnnotation: "true"}
		Expect(k8sClient.Update(ctx, pod)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, pod)
		})

		pod.Status = corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app",
				LastTerminationState: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"},
				},
			}},
		}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		_, err := detector.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace},
		})
		Expect(err).NotTo(HaveOccurred())

		var list opsv1alpha1.IncidentList
		Expect(k8sClient.List(ctx, &list,
			client.InNamespace(namespace),
			client.MatchingLabels{targetUIDLabel: string(pod.UID)},
		)).To(Succeed())
		Expect(list.Items).To(BeEmpty())
	})

	It("creates no Incident for a healthy pod", func() {
		ctx := context.Background()
		pod := makePod(fmt.Sprintf("healthy-pod-%d", GinkgoRandomSeed()))
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, pod)
		})

		pod.Status = corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "app",
				Ready: true,
			}},
		}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		_, err := detector.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace},
		})
		Expect(err).NotTo(HaveOccurred())

		var list opsv1alpha1.IncidentList
		Expect(k8sClient.List(ctx, &list,
			client.InNamespace(namespace),
			client.MatchingLabels{targetUIDLabel: string(pod.UID)},
		)).To(Succeed())
		Expect(list.Items).To(BeEmpty())
	})
})
