package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Applied is load-bearing: the operator treats it as terminal and revokes the
// scout's grants. A model that reported Applied when it had merely *proposed* a
// patch destroyed the permission it then needed to apply that patch, and the
// incident was left looking resolved when nothing had changed.
func TestModelCannotSetApplied(t *testing.T) {
	tool := NewUpdateIncidentStatus(nil)

	raw, _ := json.Marshal(map[string]string{
		"incidentName":      "oom-1",
		"incidentNamespace": "goblin",
		"phase":             "Applied",
	})

	out, err := tool.Execute(context.Background(), raw)
	if err == nil {
		t.Fatalf("Applied was accepted (returned %q); it must be derived from a verified apply", out)
	}
	if !strings.Contains(err.Error(), "Applied is recorded automatically") {
		t.Errorf("error = %q; want it to explain why", err)
	}
}

func TestModelMaySetTerminalNonFixPhases(t *testing.T) {
	for _, phase := range []string{"Escalated", "HandedOff", "Rejected"} {
		if !modelSettablePhase(phase) {
			t.Errorf("%s should be settable: it means the scout is done and did not fix anything", phase)
		}
	}
}

// Phases the operator and gate own must not be reachable from the model, or a
// mid-flight incident could be knocked out of the lifecycle.
func TestModelCannotSetLifecyclePhases(t *testing.T) {
	for _, phase := range []string{"Applied", "Detected", "Queued", "Assessing", "AwaitingApproval", ""} {
		if modelSettablePhase(phase) {
			t.Errorf("%s should not be settable by the model", phase)
		}
	}
}

func TestUpdateIncidentStatusRequiresIncident(t *testing.T) {
	tool := NewUpdateIncidentStatus(nil)

	raw, _ := json.Marshal(map[string]string{"phase": "Escalated"})
	if _, err := tool.Execute(context.Background(), raw); err == nil {
		t.Error("accepted an update with no incident named; several may be open at once")
	}
}
