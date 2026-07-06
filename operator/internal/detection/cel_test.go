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

func TestEvalNonBoolIsError(t *testing.T) {
	m, err := Compile(`object.status`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := m.Eval(map[string]any{"status": map[string]any{}}); err == nil {
		t.Fatal("expected non-bool result error")
	}
}
