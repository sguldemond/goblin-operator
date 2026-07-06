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

type UpdateIncidentStatus struct {
	dynCli    dynamic.Interface
	name      string
	namespace string
}

func NewUpdateIncidentStatus(dynCli dynamic.Interface, name, namespace string) *UpdateIncidentStatus {
	return &UpdateIncidentStatus{dynCli: dynCli, name: name, namespace: namespace}
}

func (t *UpdateIncidentStatus) Name() string { return "updateIncidentStatus" }

func (t *UpdateIncidentStatus) Description() string {
	return "Update the status of the Incident CR for this incident. " +
		"Use this to record the outcome after a fix is applied or the issue is resolved. " +
		"Valid phases: Applied, Escalated, HandedOff, Rejected."
}

func (t *UpdateIncidentStatus) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"phase":   {"type": "string", "description": "New phase: Applied, Escalated, HandedOff, or Rejected"},
			"message": {"type": "string", "description": "Short human-readable summary of the outcome"}
		},
		"required": ["phase"]
	}`)
}

type updateIncidentStatusParams struct {
	Phase   string `json:"phase"`
	Message string `json:"message"`
}

func (t *UpdateIncidentStatus) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var p updateIncidentStatusParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}

	status := map[string]any{"phase": p.Phase}
	if p.Message != "" {
		status["message"] = p.Message
	}
	patch, _ := json.Marshal(map[string]any{"status": status})

	_, err := t.dynCli.Resource(IncidentGVR).
		Namespace(t.namespace).
		Patch(ctx, t.name, types.MergePatchType, patch, metav1.PatchOptions{}, "status")
	if err != nil {
		return "", fmt.Errorf("patching Incident: %w", err)
	}
	return fmt.Sprintf("Incident %s/%s phase set to %q.", t.namespace, t.name, p.Phase), nil
}

var _ Tool = (*UpdateIncidentStatus)(nil)
