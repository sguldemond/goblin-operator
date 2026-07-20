package scout

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// recordingTool captures the params it is called with, standing in for any tool.
type recordingTool struct {
	name  string
	calls []map[string]string
}

func (r *recordingTool) Name() string                 { return r.name }
func (r *recordingTool) Description() string          { return "" }
func (r *recordingTool) InputSchema() json.RawMessage { return nil }

func (r *recordingTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var p map[string]string
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", err
	}
	r.calls = append(r.calls, p)
	return "ok", nil
}

// A non-Pod target proves gatherContext carries no Pod assumptions.
func deploymentIncident() *Incident {
	return &Incident{
		IncidentName:     "stalled-1",
		Namespace:        "goblin",
		TargetAPIVersion: "apps/v1",
		TargetKind:       "Deployment",
		TargetName:       "stalled-rollout-test",
		TargetNamespace:  "default",
		Trigger:          "ProgressDeadlineExceeded",
	}
}

func TestGatherContextSeedsTargetAndEvents(t *testing.T) {
	getResource := &recordingTool{name: "getResource"}

	results := gatherContext(context.Background(), deploymentIncident(), getResource)

	if len(results) != 2 {
		t.Fatalf("got %d seed results, want 2 (target + events)", len(results))
	}
	if len(getResource.calls) != 2 {
		t.Fatalf("got %d getResource calls, want 2", len(getResource.calls))
	}

	target := getResource.calls[0]
	if target["apiVersion"] != "apps/v1" || target["kind"] != "Deployment" {
		t.Errorf("target fetched as %s/%s; want apps/v1/Deployment — a Pod literal has leaked back in",
			target["apiVersion"], target["kind"])
	}
	if target["name"] != "stalled-rollout-test" || target["namespace"] != "default" {
		t.Errorf("target fetched as %s/%s; want default/stalled-rollout-test",
			target["namespace"], target["name"])
	}
}

func TestGatherContextEventsMatchKindAndName(t *testing.T) {
	getResource := &recordingTool{name: "getResource"}

	gatherContext(context.Background(), deploymentIncident(), getResource)

	events := getResource.calls[1]
	if events["kind"] != "Event" || events["apiVersion"] != "v1" {
		t.Fatalf("second call is %s/%s; want v1/Event", events["apiVersion"], events["kind"])
	}
	sel := events["fieldSelector"]
	if !strings.Contains(sel, "involvedObject.name=stalled-rollout-test") {
		t.Errorf("fieldSelector %q does not select the target by name", sel)
	}
	// Without the kind clause a same-named Pod's events would be picked up.
	if !strings.Contains(sel, "involvedObject.kind=Deployment") {
		t.Errorf("fieldSelector %q does not constrain kind", sel)
	}
}

func TestGatherContextDoesNotFetchLogs(t *testing.T) {
	getResource := &recordingTool{name: "getResource"}

	results := gatherContext(context.Background(), deploymentIncident(), getResource)

	for _, r := range results {
		if r.ToolName != "getResource" {
			t.Errorf("seed called %q; the seed must stay kind-agnostic and logs are Pod-only",
				r.ToolName)
		}
	}
}
