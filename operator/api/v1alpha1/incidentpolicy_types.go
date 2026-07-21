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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// TargetGVK selects which kind of object a policy watches.
type TargetGVK struct {
	// +kubebuilder:validation:MinLength=1
	APIVersion string `json:"apiVersion"`
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`
}

// DetectSpec is the CEL rung: an expression over `object` deciding whether
// the target is an incident of type Trigger.
type DetectSpec struct {
	// +kubebuilder:validation:MinLength=1
	Trigger string `json:"trigger"`
	// Expression is a CEL boolean over the variable `object` (the full target).
	// +kubebuilder:validation:MinLength=1
	Expression string `json:"expression"`
}

// AllowedRole names one pre-vetted ClusterRole the scout may be granted while
// an incident from this policy is open.
type AllowedRole struct {
	// ClusterRole must be one of the roles the operator is permitted to bind.
	// The operator's own RBAC restricts `bind` to a fixed set by resourceNames,
	// so naming anything else fails at the API server rather than escalating.
	// +kubebuilder:validation:MinLength=1
	ClusterRole string `json:"clusterRole"`
}

// PermissionsSpec declares what the scout may do about incidents this policy
// raises. Omitting it grants nothing beyond the scout's standing read access.
type PermissionsSpec struct {
	// +kubebuilder:validation:MinItems=1
	Allow []AllowedRole `json:"allow,omitempty"`
}

type IncidentPolicySpec struct {
	Target TargetGVK  `json:"target"`
	Detect DetectSpec `json:"detect"`
	// Permissions is optional: a policy without it only ever raises incidents.
	Permissions *PermissionsSpec `json:"permissions,omitempty"`
}

type IncidentPolicyStatus struct {
	// Conditions carries a Valid condition; reason InvalidExpression when CEL fails to compile.
	Conditions       []metav1.Condition `json:"conditions,omitempty"`
	IncidentsCreated int64              `json:"incidentsCreated,omitempty"`
	LastTriggeredAt  *metav1.Time       `json:"lastTriggeredAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target.kind`
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.spec.detect.trigger`
// +kubebuilder:printcolumn:name="Valid",type=string,JSONPath=`.status.conditions[?(@.type=="Valid")].status`
type IncidentPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IncidentPolicySpec   `json:"spec"`
	Status IncidentPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type IncidentPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IncidentPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &IncidentPolicy{}, &IncidentPolicyList{})
		return nil
	})
}
