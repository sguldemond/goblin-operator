package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

type GetPodLogs struct{ client kubernetes.Interface }

func NewGetPodLogs(client kubernetes.Interface) *GetPodLogs { return &GetPodLogs{client} }

func (t *GetPodLogs) Name() string { return "getPodLogs" }

func (t *GetPodLogs) Description() string {
	return "Fetch recent log lines from a container in a pod. Also fetches the previous terminated container's logs when available (useful for OOMKilled pods)."
}

func (t *GetPodLogs) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"podName":   {"type": "string", "description": "Name of the pod"},
			"namespace": {"type": "string", "description": "Namespace of the pod"},
			"container": {"type": "string", "description": "Container name (optional, defaults to first container)"},
			"tailLines": {"type": "integer", "description": "Number of log lines to tail (default 100)"}
		},
		"required": ["podName", "namespace"]
	}`)
}

type getPodLogsParams struct {
	PodName   string `json:"podName"`
	Namespace string `json:"namespace"`
	Container string `json:"container"`
	TailLines *int64 `json:"tailLines"`
}

func (t *GetPodLogs) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var p getPodLogsParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}

	tail := int64(100)
	if p.TailLines != nil {
		tail = *p.TailLines
	}

	opts := &corev1.PodLogOptions{
		Container: p.Container,
		TailLines: &tail,
	}

	current, err := t.fetchLogs(ctx, p.PodName, p.Namespace, opts)
	if err != nil {
		return "", err
	}

	// Also grab the previous terminated container's logs (the actual OOM run).
	prevOpts := &corev1.PodLogOptions{
		Container: p.Container,
		Previous:  true,
		TailLines: &tail,
	}
	prev, _ := t.fetchLogs(ctx, p.PodName, p.Namespace, prevOpts) // best-effort

	out := "=== current logs ===\n" + current
	if prev != "" {
		out += "\n=== previous (terminated) logs ===\n" + prev
	}
	return out, nil
}

func (t *GetPodLogs) fetchLogs(ctx context.Context, pod, ns string, opts *corev1.PodLogOptions) (string, error) {
	req := t.client.CoreV1().Pods(ns).GetLogs(pod, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("log stream: %w", err)
	}
	defer stream.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, stream); err != nil {
		return "", fmt.Errorf("reading logs: %w", err)
	}
	return buf.String(), nil
}

var _ Tool = (*GetPodLogs)(nil)
