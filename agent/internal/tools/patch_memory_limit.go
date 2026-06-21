package tools

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

type PatchMemoryLimit struct{ client kubernetes.Interface }

func NewPatchMemoryLimit(client kubernetes.Interface) *PatchMemoryLimit {
	return &PatchMemoryLimit{client}
}

func (t *PatchMemoryLimit) Name() string { return "patchMemoryLimit" }

func (t *PatchMemoryLimit) Description() string {
	return "Increase the memory limit of a container by patching the owning Deployment. Walks pod→ReplicaSet→Deployment. Only call after explicit human approval."
}

func (t *PatchMemoryLimit) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"podName":    {"type": "string",  "description": "Name of the OOMKilled pod"},
			"namespace":  {"type": "string",  "description": "Namespace of the pod"},
			"container":  {"type": "string",  "description": "Container name to patch (defaults to first container)"},
			"newLimitMb": {"type": "integer", "description": "New memory limit in MiB (64–32768)"}
		},
		"required": ["podName", "namespace", "newLimitMb"]
	}`)
}

type patchMemoryLimitParams struct {
	PodName    string `json:"podName"`
	Namespace  string `json:"namespace"`
	Container  string `json:"container"`
	NewLimitMb int    `json:"newLimitMb"`
}

func (t *PatchMemoryLimit) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var p patchMemoryLimitParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	if p.NewLimitMb < 64 || p.NewLimitMb > 32768 {
		return "", fmt.Errorf("newLimitMb must be 64–32768, got %d", p.NewLimitMb)
	}

	pod, err := t.client.CoreV1().Pods(p.Namespace).Get(ctx, p.PodName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting pod: %w", err)
	}

	container := p.Container
	if container == "" {
		if len(pod.Spec.Containers) == 0 {
			return "", fmt.Errorf("pod has no containers")
		}
		container = pod.Spec.Containers[0].Name
	}

	// Walk pod → ReplicaSet → Deployment.
	deployName, err := t.owningDeployment(ctx, pod.Namespace, pod.OwnerReferences)
	if err != nil {
		return "", err
	}

	newLimit := fmt.Sprintf("%dMi", p.NewLimitMb)
	patch, _ := json.Marshal(map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []map[string]any{
						{
							"name": container,
							"resources": map[string]any{
								"limits": map[string]any{"memory": newLimit},
							},
						},
					},
				},
			},
		},
	})

	_, err = t.client.AppsV1().Deployments(p.Namespace).Patch(
		ctx, deployName, types.StrategicMergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		return "", fmt.Errorf("patching deployment: %w", err)
	}

	return fmt.Sprintf("Patched Deployment %s/%s: container %q memory limit → %s", p.Namespace, deployName, container, newLimit), nil
}

func (t *PatchMemoryLimit) owningDeployment(ctx context.Context, namespace string, owners []metav1.OwnerReference) (string, error) {
	for _, o := range owners {
		if o.Kind != "ReplicaSet" {
			continue
		}
		rs, err := t.client.AppsV1().ReplicaSets(namespace).Get(ctx, o.Name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("getting replicaset %s: %w", o.Name, err)
		}
		for _, ro := range rs.OwnerReferences {
			if ro.Kind == "Deployment" {
				return ro.Name, nil
			}
		}
		return "", fmt.Errorf("replicaset %s has no Deployment owner", o.Name)
	}
	return "", fmt.Errorf("pod has no ReplicaSet owner — not managed by a Deployment")
}

var _ Tool = (*PatchMemoryLimit)(nil)
