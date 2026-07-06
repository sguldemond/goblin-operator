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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +kubebuilder:validation:Enum=Detected;Assessing;AwaitingApproval;Applied;Rejected;Escalated;HandedOff
type RemediationPhase string

const (
	PhaseDetected         RemediationPhase = "Detected"
	PhaseAssessing        RemediationPhase = "Assessing"
	PhaseAwaitingApproval RemediationPhase = "AwaitingApproval"
	PhaseApplied          RemediationPhase = "Applied"
	PhaseRejected         RemediationPhase = "Rejected"
	PhaseEscalated        RemediationPhase = "Escalated"
	PhaseHandedOff        RemediationPhase = "HandedOff"
)

type RemediationSpec struct {
	PodRef corev1.ObjectReference `json:"podRef"`

	// +kubebuilder:validation:Enum=OOMKilled;Unschedulable
	Trigger string `json:"trigger"`
}

type RemediationStatus struct {
	Phase   RemediationPhase `json:"phase,omitempty"`
	Message string           `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.spec.trigger`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
type Remediation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RemediationSpec   `json:"spec"`
	Status RemediationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type RemediationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Remediation `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Remediation{}, &RemediationList{})
		return nil
	})
}
