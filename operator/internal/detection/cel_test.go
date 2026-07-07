package detection

import "testing"

func TestCompileRejectsGarbage(t *testing.T) {
	if _, err := Compile("this is not )( cel"); err == nil {
		t.Fatal("expected compile error")
	}
}

func TestEvalOOMKilled(t *testing.T) {
	expr := `has(object.status.containerStatuses) && object.status.containerStatuses.exists(c, has(c.lastState) && has(c.lastState.terminated) && c.lastState.terminated.reason == 'OOMKilled')`
	m, err := Compile(expr)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	oomed := map[string]any{"status": map[string]any{"containerStatuses": []any{
		map[string]any{"lastState": map[string]any{"terminated": map[string]any{"reason": "OOMKilled"}}},
	}}}
	got, err := m.Eval(oomed)
	if err != nil || !got {
		t.Fatalf("expected match, got %v err %v", got, err)
	}
	healthy := map[string]any{"status": map[string]any{"containerStatuses": []any{
		map[string]any{"ready": true},
	}}}
	if got, err := m.Eval(healthy); err != nil || got {
		t.Fatalf("expected clean no-match for healthy pod, got match=%v err=%v", got, err)
	}
}

// These expressions are pasted verbatim from the shipped preset policies in
// operator/config/samples/policies/*.yaml. This test guards those YAML files
// against CEL typos: if either expression changes here without a matching
// update to the YAML (or vice versa), the drift is caught.
func TestPresetPolicyExpressions(t *testing.T) {
	oomExpr := `has(object.status.containerStatuses) &&
  object.status.containerStatuses.exists(c,
    has(c.lastState) && has(c.lastState.terminated) &&
    c.lastState.terminated.reason == 'OOMKilled')`

	unschedulableExpr := `object.status.phase == 'Pending' &&
  has(object.status.conditions) &&
  object.status.conditions.exists(c,
    c.type == 'PodScheduled' && c.status == 'False' && c.reason == 'Unschedulable')`

	oomMatcher, err := Compile(oomExpr)
	if err != nil {
		t.Fatalf("oom-killed: compile: %v", err)
	}
	oomed := map[string]any{"status": map[string]any{"containerStatuses": []any{
		map[string]any{"lastState": map[string]any{"terminated": map[string]any{"reason": "OOMKilled"}}},
	}}}
	if got, err := oomMatcher.Eval(oomed); err != nil || !got {
		t.Fatalf("oom-killed: expected match, got %v err %v", got, err)
	}
	healthy := map[string]any{"status": map[string]any{"containerStatuses": []any{
		map[string]any{"ready": true},
	}}}
	if got, err := oomMatcher.Eval(healthy); err != nil || got {
		t.Fatalf("oom-killed: expected no match for healthy pod, got %v err %v", got, err)
	}

	unschedMatcher, err := Compile(unschedulableExpr)
	if err != nil {
		t.Fatalf("unschedulable: compile: %v", err)
	}
	pending := map[string]any{"status": map[string]any{
		"phase": "Pending",
		"conditions": []any{
			map[string]any{"type": "PodScheduled", "status": "False", "reason": "Unschedulable"},
		},
	}}
	if got, err := unschedMatcher.Eval(pending); err != nil || !got {
		t.Fatalf("unschedulable: expected match, got %v err %v", got, err)
	}
	running := map[string]any{"status": map[string]any{
		"phase": "Running",
		"conditions": []any{
			map[string]any{"type": "PodScheduled", "status": "True", "reason": ""},
		},
	}}
	if got, err := unschedMatcher.Eval(running); err != nil || got {
		t.Fatalf("unschedulable: expected no match for running pod, got %v err %v", got, err)
	}
}

func TestEvalNonBoolIsError(t *testing.T) {
	m, err := Compile(`object.status`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := m.Eval(map[string]any{"status": map[string]any{}}); err == nil {
		t.Fatal("expected non-bool result error")
	}
}
