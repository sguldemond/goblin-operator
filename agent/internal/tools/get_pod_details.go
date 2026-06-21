package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type GetPodDetails struct{ client kubernetes.Interface }

func NewGetPodDetails(client kubernetes.Interface) *GetPodDetails { return &GetPodDetails{client} }

func (t *GetPodDetails) Name() string { return "getPodDetails" }

func (t *GetPodDetails) Description() string {
	return "Get pod spec details: containers with images and resource limits/requests, node assignment, owner references, current phase, and container statuses including restart count and last termination reason."
}

func (t *GetPodDetails) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"podName":   {"type": "string", "description": "Name of the pod"},
			"namespace": {"type": "string", "description": "Namespace of the pod"}
		},
		"required": ["podName", "namespace"]
	}`)
}

type getPodDetailsParams struct {
	PodName   string `json:"podName"`
	Namespace string `json:"namespace"`
}

func (t *GetPodDetails) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var p getPodDetailsParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}

	pod, err := t.client.CoreV1().Pods(p.Namespace).Get(ctx, p.PodName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting pod: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Pod: %s/%s\n", pod.Namespace, pod.Name))
	sb.WriteString(fmt.Sprintf("Phase: %s\n", pod.Status.Phase))
	sb.WriteString(fmt.Sprintf("Node: %s\n", pod.Spec.NodeName))

	if len(pod.OwnerReferences) > 0 {
		sb.WriteString("Owners:\n")
		for _, ref := range pod.OwnerReferences {
			sb.WriteString(fmt.Sprintf("  %s/%s (%s)\n", ref.Kind, ref.Name, ref.APIVersion))
		}
	}

	sb.WriteString("\nContainers:\n")
	for _, c := range pod.Spec.Containers {
		sb.WriteString(fmt.Sprintf("  %s (image: %s)\n", c.Name, c.Image))
		if r := c.Resources.Requests; len(r) > 0 {
			sb.WriteString(fmt.Sprintf("    requests: cpu=%s memory=%s\n",
				r.Cpu().String(), r.Memory().String()))
		}
		if l := c.Resources.Limits; len(l) > 0 {
			sb.WriteString(fmt.Sprintf("    limits:   cpu=%s memory=%s\n",
				l.Cpu().String(), l.Memory().String()))
		}
	}

	if len(pod.Status.ContainerStatuses) > 0 {
		sb.WriteString("\nContainer statuses:\n")
		for _, cs := range pod.Status.ContainerStatuses {
			sb.WriteString(fmt.Sprintf("  %s: restarts=%d ready=%v\n", cs.Name, cs.RestartCount, cs.Ready))
			if t := cs.LastTerminationState.Terminated; t != nil {
				sb.WriteString(fmt.Sprintf("    last termination: reason=%s exitCode=%d at=%s\n",
					t.Reason, t.ExitCode, t.FinishedAt.Format("2006-01-02T15:04:05Z")))
			}
		}
	}

	return sb.String(), nil
}

var _ Tool = (*GetPodDetails)(nil)
