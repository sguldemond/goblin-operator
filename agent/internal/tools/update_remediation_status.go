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

// RemediationGVR is the GroupVersionResource for the Remediation CRD.
var RemediationGVR = schema.GroupVersionResource{
	Group:    "ops.goblinoperator.io",
	Version:  "v1alpha1",
	Resource: "remediations",
}

type UpdateRemediationStatus struct {
	dynCli    dynamic.Interface
	name      string
	namespace string
}

func NewUpdateRemediationStatus(dynCli dynamic.Interface, name, namespace string) *UpdateRemediationStatus {
	return &UpdateRemediationStatus{dynCli: dynCli, name: name, namespace: namespace}
}

func (t *UpdateRemediationStatus) Name() string { return "updateRemediationStatus" }

func (t *UpdateRemediationStatus) Description() string {
	return "Update the status of the Remediation CR for this incident. " +
		"Use this to record the outcome after a fix is applied or the issue is resolved. " +
		"Valid phases: Applied, Escalated, HandedOff, Rejected."
}

func (t *UpdateRemediationStatus) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"phase":   {"type": "string", "description": "New phase: Applied, Escalated, HandedOff, or Rejected"},
			"message": {"type": "string", "description": "Short human-readable summary of the outcome"}
		},
		"required": ["phase"]
	}`)
}

type updateRemediationStatusParams struct {
	Phase   string `json:"phase"`
	Message string `json:"message"`
}

func (t *UpdateRemediationStatus) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var p updateRemediationStatusParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}

	status := map[string]any{"phase": p.Phase}
	if p.Message != "" {
		status["message"] = p.Message
	}
	patch, _ := json.Marshal(map[string]any{"status": status})

	_, err := t.dynCli.Resource(RemediationGVR).
		Namespace(t.namespace).
		Patch(ctx, t.name, types.MergePatchType, patch, metav1.PatchOptions{}, "status")
	if err != nil {
		return "", fmt.Errorf("patching Remediation: %w", err)
	}
	return fmt.Sprintf("Remediation %s/%s phase set to %q.", t.namespace, t.name, p.Phase), nil
}

var _ Tool = (*UpdateRemediationStatus)(nil)
