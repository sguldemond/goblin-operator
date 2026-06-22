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

// +kubebuilder:validation:Enum=Detected;Assessing;AwaitingApproval;Validating;Applied;Rejected;Escalated;HandedOff
type RemediationPhase string

const (
	PhaseDetected         RemediationPhase = "Detected"
	PhaseAssessing        RemediationPhase = "Assessing"
	PhaseAwaitingApproval RemediationPhase = "AwaitingApproval"
	PhaseValidating       RemediationPhase = "Validating"
	PhaseApplied          RemediationPhase = "Applied"
	PhaseRejected         RemediationPhase = "Rejected"
	PhaseEscalated        RemediationPhase = "Escalated"
	PhaseHandedOff        RemediationPhase = "HandedOff"
)

// +kubebuilder:validation:Enum=patchMemoryLimit;restartPod;scaleReplicas;flagForHuman
type ActionType string

const (
	ActionPatchMemoryLimit ActionType = "patchMemoryLimit"
	ActionRestartPod       ActionType = "restartPod"
	ActionScaleReplicas    ActionType = "scaleReplicas"
	ActionFlagForHuman     ActionType = "flagForHuman"
)

type RemediationSpec struct {
	PodRef corev1.ObjectReference `json:"podRef"`

	// +kubebuilder:validation:Enum=OOMKilled;Unschedulable
	Trigger string `json:"trigger"`

	// +kubebuilder:default=2
	MaxAttempts int `json:"maxAttempts,omitempty"`
}

type ProposedAction struct {
	Action    ActionType        `json:"action"`
	Params    map[string]string `json:"params,omitempty"`
	Reasoning string            `json:"reasoning,omitempty"`
}

type AttemptRecord struct {
	Action    ProposedAction `json:"action"`
	AppliedAt metav1.Time    `json:"appliedAt"`
	// "stable" | "recurred" — written by the deterministic validate step only
	Outcome string `json:"outcome"`
}

type ConversationTurn struct {
	From      string      `json:"from"` // "agent" | "human"
	Text      string      `json:"text"`
	Timestamp metav1.Time `json:"timestamp"`
}

type RemediationStatus struct {
	Phase          RemediationPhase   `json:"phase,omitempty"`
	ProposedAction *ProposedAction    `json:"proposedAction,omitempty"`
	Attempts       int                `json:"attempts,omitempty"`
	History        []AttemptRecord    `json:"history,omitempty"`
	Conversation   []ConversationTurn `json:"conversation,omitempty"`
	Message        string             `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.spec.trigger`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Attempts",type=integer,JSONPath=`.status.attempts`
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
