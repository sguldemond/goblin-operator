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
type IncidentPhase string

const (
	PhaseDetected         IncidentPhase = "Detected"
	PhaseAssessing        IncidentPhase = "Assessing"
	PhaseAwaitingApproval IncidentPhase = "AwaitingApproval"
	PhaseApplied          IncidentPhase = "Applied"
	PhaseRejected         IncidentPhase = "Rejected"
	PhaseEscalated        IncidentPhase = "Escalated"
	PhaseHandedOff        IncidentPhase = "HandedOff"
)

type IncidentSpec struct {
	// TargetRef points at the object the incident is about. Carries its own
	// apiVersion/kind, so it is generic across resource kinds.
	TargetRef corev1.ObjectReference `json:"targetRef"`

	// Trigger is the policy-defined name of what was detected (e.g. "OOMKilled").
	// +kubebuilder:validation:MinLength=1
	Trigger string `json:"trigger"`

	// PolicyRef is the name of the IncidentPolicy that fired, if any.
	PolicyRef string `json:"policyRef,omitempty"`
}

type IncidentStatus struct {
	Phase   IncidentPhase `json:"phase,omitempty"`
	Message string        `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.spec.trigger`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetRef.kind`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
type Incident struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IncidentSpec   `json:"spec"`
	Status IncidentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type IncidentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Incident `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Incident{}, &IncidentList{})
		return nil
	})
}
