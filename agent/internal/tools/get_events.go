package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type GetEvents struct{ client kubernetes.Interface }

func NewGetEvents(client kubernetes.Interface) *GetEvents { return &GetEvents{client} }

func (t *GetEvents) Name() string { return "getEvents" }

func (t *GetEvents) Description() string {
	return "List Kubernetes Warning events for a pod, sorted by time. Useful for understanding OOM history and scheduler decisions."
}

func (t *GetEvents) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"podName":   {"type": "string", "description": "Name of the pod"},
			"namespace": {"type": "string", "description": "Namespace of the pod"}
		},
		"required": ["podName", "namespace"]
	}`)
}

type getEventsParams struct {
	PodName   string `json:"podName"`
	Namespace string `json:"namespace"`
}

func (t *GetEvents) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var p getEventsParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}

	list, err := t.client.CoreV1().Events(p.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=Pod", p.PodName),
	})
	if err != nil {
		return "", fmt.Errorf("listing events: %w", err)
	}

	// Sort by lastTimestamp ascending.
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].LastTimestamp.Before(&list.Items[j].LastTimestamp)
	})

	if len(list.Items) == 0 {
		return "no events found for pod " + p.PodName, nil
	}

	var sb strings.Builder
	for _, ev := range list.Items {
		sb.WriteString(fmt.Sprintf("[%s] %s/%s (x%d): %s\n",
			ev.LastTimestamp.Format("2006-01-02T15:04:05Z"),
			ev.Type,
			ev.Reason,
			ev.Count,
			ev.Message,
		))
	}
	return sb.String(), nil
}

var _ Tool = (*GetEvents)(nil)
