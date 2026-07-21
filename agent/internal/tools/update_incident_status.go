package tools

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// IncidentGVR is the GroupVersionResource for the Incident CRD.
var IncidentGVR = schema.GroupVersionResource{
	Group:    "ops.goblinoperator.io",
	Version:  "v1alpha1",
	Resource: "incidents",
}

// UpdateIncidentStatus records the outcome of an investigation.
//
// The incident is a parameter rather than fixed at construction: the scout is
// long-lived and may have several incidents open at once, so "the incident" is
// ambiguous and has to be named explicitly.
type UpdateIncidentStatus struct {
	dynCli dynamic.Interface
}

func NewUpdateIncidentStatus(dynCli dynamic.Interface) *UpdateIncidentStatus {
	return &UpdateIncidentStatus{dynCli: dynCli}
}

func (t *UpdateIncidentStatus) Name() string { return "updateIncidentStatus" }

func (t *UpdateIncidentStatus) Description() string {
	return "Close an incident you are NOT fixing. " +
		"Name the incident explicitly — several may be open at once. " +
		"Valid phases: Escalated (you cannot find a safe fix), HandedOff (a human is taking it), " +
		"Rejected (the proposed fix was refused). " +
		"You cannot set Applied: that is recorded automatically once a change has actually been " +
		"applied and verified. Do not call this after proposing a patch — the patch has not landed yet."
}

func (t *UpdateIncidentStatus) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"incidentName":      {"type": "string", "description": "Name of the Incident CR to update"},
			"incidentNamespace": {"type": "string", "description": "Namespace of the Incident CR"},
			"phase":             {"type": "string", "enum": ["Escalated", "HandedOff", "Rejected"], "description": "Escalated, HandedOff, or Rejected. Applied is set automatically after a verified apply and cannot be set here."},
			"message":           {"type": "string", "description": "Short human-readable summary of the outcome"}
		},
		"required": ["incidentName", "incidentNamespace", "phase"]
	}`)
}

type updateIncidentStatusParams struct {
	IncidentName      string `json:"incidentName"`
	IncidentNamespace string `json:"incidentNamespace"`
	Phase             string `json:"phase"`
	Message           string `json:"message"`
}

func (t *UpdateIncidentStatus) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var p updateIncidentStatusParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	if p.IncidentName == "" || p.IncidentNamespace == "" {
		return "", fmt.Errorf("incidentName and incidentNamespace are required")
	}
	// Applied is a fact about the cluster, not a claim the model gets to make.
	// It is also load-bearing: the operator treats it as terminal and revokes
	// the scout's permissions, so setting it early destroys the very grant
	// needed to apply the change. The gate records it after a verified apply.
	if !modelSettablePhase(p.Phase) {
		return "", fmt.Errorf(
			"cannot set phase %q: only Escalated, HandedOff and Rejected may be set here. "+
				"Applied is recorded automatically once a change has been applied and verified — "+
				"proposing a patch is not the same as applying one", p.Phase)
	}
	return t.Set(ctx, p.IncidentNamespace, p.IncidentName, p.Phase, p.Message)
}

// Set is the same write, callable from the scout loop rather than the model.
func (t *UpdateIncidentStatus) Set(ctx context.Context, namespace, name, phase, message string) (string, error) {
	status := map[string]any{"phase": phase}
	if message != "" {
		status["message"] = message
	}
	patch, _ := json.Marshal(map[string]any{"status": status})

	_, err := t.dynCli.Resource(IncidentGVR).
		Namespace(namespace).
		Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}, "status")
	if err != nil {
		return "", fmt.Errorf("patching Incident: %w", err)
	}
	return fmt.Sprintf("Incident %s/%s phase set to %q.", namespace, name, phase), nil
}

// modelSettablePhase lists the phases the model may set: the ones meaning "I am
// done and did not fix it". Everything else is derived from what actually
// happened to the cluster.
func modelSettablePhase(phase string) bool {
	switch phase {
	case "Escalated", "HandedOff", "Rejected":
		return true
	}
	return false
}

var _ Tool = (*UpdateIncidentStatus)(nil)
