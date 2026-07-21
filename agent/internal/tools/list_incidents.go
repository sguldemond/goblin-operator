package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

// ListIncidents is the scout's memory.
//
// There is no separate store: past investigations are recorded as Incident CRs,
// with status.message describing what was concluded and done. Rather than
// pre-loading that history into every conversation, the scout fetches it when
// the model judges it relevant — the same reason context gathering seeds only
// the target and its events.
type ListIncidents struct {
	dynCli dynamic.Interface
}

func NewListIncidents(dynCli dynamic.Interface) *ListIncidents {
	return &ListIncidents{dynCli: dynCli}
}

func (t *ListIncidents) Name() string { return "listIncidents" }

func (t *ListIncidents) Description() string {
	return "List past and present Incidents, newest first, with their phase and outcome message. " +
		"Use this to check whether a target has failed before, what was done about it, and whether it worked — " +
		"for example when a Deployment you already patched has failed again, which suggests the first fix " +
		"treated a symptom. Filter by targetName to focus on one object."
}

func (t *ListIncidents) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"targetName": {"type": "string", "description": "Only incidents whose target has this name. Omit for all."},
			"namespace":  {"type": "string", "description": "Incident namespace. Omit to search all namespaces."},
			"limit":      {"type": "integer", "description": "Maximum incidents to return (default 20)."}
		}
	}`)
}

type listIncidentsParams struct {
	TargetName string `json:"targetName"`
	Namespace  string `json:"namespace"`
	Limit      int    `json:"limit"`
}

func (t *ListIncidents) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var p listIncidentsParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return "", fmt.Errorf("invalid params: %w", err)
		}
	}
	if p.Limit <= 0 {
		p.Limit = 20
	}

	ri := t.dynCli.Resource(IncidentGVR)
	var list, err = func() (*unstructuredList, error) {
		if p.Namespace != "" {
			l, err := ri.Namespace(p.Namespace).List(ctx, metav1.ListOptions{})
			return wrap(l), err
		}
		l, err := ri.List(ctx, metav1.ListOptions{})
		return wrap(l), err
	}()
	if err != nil {
		return "", fmt.Errorf("listing incidents: %w", err)
	}

	type row struct {
		created, name, ns, trigger, kind, target, phase, message string
	}
	var rows []row
	for _, item := range list.items {
		meta, _ := item["metadata"].(map[string]any)
		spec, _ := item["spec"].(map[string]any)
		status, _ := item["status"].(map[string]any)
		targetRef, _ := spec["targetRef"].(map[string]any)

		target, _ := targetRef["name"].(string)
		if p.TargetName != "" && target != p.TargetName {
			continue
		}
		name, _ := meta["name"].(string)
		ns, _ := meta["namespace"].(string)
		created, _ := meta["creationTimestamp"].(string)
		trigger, _ := spec["trigger"].(string)
		kind, _ := targetRef["kind"].(string)
		phase, _ := status["phase"].(string)
		message, _ := status["message"].(string)

		rows = append(rows, row{created, name, ns, trigger, kind, target, phase, message})
	}

	// Newest first: recent history is what bears on the incident at hand.
	sort.Slice(rows, func(i, j int) bool { return rows[i].created > rows[j].created })
	if len(rows) > p.Limit {
		rows = rows[:p.Limit]
	}
	if len(rows) == 0 {
		return "No matching incidents.", nil
	}

	var sb strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&sb, "%s  %s/%s  %s on %s %s  phase=%s\n",
			r.created, r.ns, r.name, r.trigger, r.kind, r.target, r.phase)
		if r.message != "" {
			fmt.Fprintf(&sb, "    outcome: %s\n", r.message)
		}
	}
	return sb.String(), nil
}

// unstructuredList keeps the two List call sites returning one shape.
type unstructuredList struct{ items []map[string]any }

func wrap(l interface {
	UnstructuredContent() map[string]any
}) *unstructuredList {
	if l == nil {
		return &unstructuredList{}
	}
	content := l.UnstructuredContent()
	raw, _ := content["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return &unstructuredList{items: out}
}

var _ Tool = (*ListIncidents)(nil)
