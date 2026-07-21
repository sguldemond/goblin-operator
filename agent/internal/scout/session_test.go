package scout

import (
	"testing"
)

func sessionWith(keys ...string) *session {
	s := &session{open: map[string]*Incident{}}
	for _, k := range keys {
		s.open[k] = &Incident{IncidentName: k, Namespace: "goblin"}
	}
	return s
}

func TestArrivedSinceReportsOnlyNewIncidents(t *testing.T) {
	s := sessionWith("a", "b", "c")

	// Snapshot taken when only a and b were open.
	arrived := s.arrivedSince([]string{"a", "b"})

	if len(arrived) != 1 {
		t.Fatalf("got %d arrivals, want 1", len(arrived))
	}
	if arrived[0].IncidentName != "c" {
		t.Errorf("arrival = %q, want c", arrived[0].IncidentName)
	}
}

// An approval must be acted on when nothing has changed — the re-check exists
// to catch a moved world, not to add a round trip to every approval.
func TestArrivedSinceEmptyWhenNothingChanged(t *testing.T) {
	s := sessionWith("a", "b")

	if arrived := s.arrivedSince([]string{"a", "b"}); len(arrived) != 0 {
		t.Errorf("got %d arrivals for an unchanged set, want 0", len(arrived))
	}
}

// An incident closing between staging and approval must not look like an
// arrival, or the scout would abandon a good change for no reason.
func TestArrivedSinceIgnoresClosedIncidents(t *testing.T) {
	s := sessionWith("a")

	if arrived := s.arrivedSince([]string{"a", "b"}); len(arrived) != 0 {
		t.Errorf("got %d arrivals after an incident closed, want 0", len(arrived))
	}
}

func TestOpenKeysIsStable(t *testing.T) {
	s := sessionWith("c", "a", "b")

	keys := s.openKeys()
	want := []string{"a", "b", "c"}
	if len(keys) != len(want) {
		t.Fatalf("got %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("got %v, want %v — order must be stable so snapshots compare cleanly", keys, want)
		}
	}
}

func TestIncidentKeyDistinguishesNamespaces(t *testing.T) {
	a := Incident{IncidentName: "oom-1", Namespace: "goblin"}
	b := Incident{IncidentName: "oom-1", Namespace: "other"}

	if a.Key() == b.Key() {
		t.Errorf("same key %q for incidents in different namespaces", a.Key())
	}
}

func TestIsTerminalPhase(t *testing.T) {
	terminal := []string{"Applied", "Rejected", "Escalated", "HandedOff"}
	for _, p := range terminal {
		if !isTerminalPhase(p) {
			t.Errorf("%s should be terminal", p)
		}
	}
	// AwaitingApproval is deliberately live: the scout still has to apply the
	// change once the human answers, which needs its grants intact.
	live := []string{"Detected", "Queued", "Assessing", "AwaitingApproval", ""}
	for _, p := range live {
		if isTerminalPhase(p) {
			t.Errorf("%s should not be terminal", p)
		}
	}
}

func TestIncidentFromUnstructured(t *testing.T) {
	obj := map[string]any{
		"metadata": map[string]any{"name": "oom-1", "namespace": "goblin"},
		"spec": map[string]any{
			"trigger":   "OOMKilled",
			"policyRef": "oom-killed",
			"targetRef": map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"name":       "web",
				"namespace":  "prod",
			},
		},
	}

	inc, err := incidentFromUnstructured(obj)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if inc.TargetKind != "Deployment" || inc.TargetNamespace != "prod" {
		t.Errorf("target = %s in %s; want Deployment in prod", inc.TargetKind, inc.TargetNamespace)
	}
	if inc.Key() != "goblin/oom-1" {
		t.Errorf("key = %q, want goblin/oom-1", inc.Key())
	}
}

// A target in no namespace falls back to the incident's own, which is only
// correct because incidents are namespaced alongside or centrally to targets.
func TestIncidentFromUnstructuredDefaultsNamespace(t *testing.T) {
	obj := map[string]any{
		"metadata": map[string]any{"name": "oom-1", "namespace": "goblin"},
		"spec": map[string]any{
			"targetRef": map[string]any{"kind": "Pod", "name": "web-abc"},
		},
	}

	inc, err := incidentFromUnstructured(obj)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if inc.TargetNamespace != "goblin" {
		t.Errorf("target namespace = %q, want goblin", inc.TargetNamespace)
	}
	if inc.TargetAPIVersion != "v1" {
		t.Errorf("apiVersion = %q, want v1 for the core group", inc.TargetAPIVersion)
	}
}

func TestIncidentFromUnstructuredRejectsMissingKind(t *testing.T) {
	obj := map[string]any{
		"metadata": map[string]any{"name": "oom-1", "namespace": "goblin"},
		"spec": map[string]any{
			"targetRef": map[string]any{"name": "web-abc"},
		},
	}

	if _, err := incidentFromUnstructured(obj); err == nil {
		t.Error("missing kind accepted; it decides what context gets gathered, so guessing is worse than refusing")
	}
}

// One patch commonly fixes several incidents — sibling replicas of a broken
// Deployment. Before this, only one could be closed, so the model reached for
// HandedOff to report a fix it had genuinely applied.
func TestResolveTargetsClosesEveryNamedIncident(t *testing.T) {
	s := sessionWith("a", "b", "c")

	got := s.resolveTargets([]string{"a", "c"})

	if len(got) != 2 {
		t.Fatalf("resolved %d incidents, want 2", len(got))
	}
	names := map[string]bool{got[0].IncidentName: true, got[1].IncidentName: true}
	if !names["a"] || !names["c"] {
		t.Errorf("resolved %v; want a and c", names)
	}
}

// With one candidate an unnamed outcome is unambiguous.
func TestResolveTargetsFallsBackToTheOnlyIncident(t *testing.T) {
	s := sessionWith("solo")

	got := s.resolveTargets(nil)

	if len(got) != 1 || got[0].IncidentName != "solo" {
		t.Errorf("got %v; want the single open incident", got)
	}
}

// With several open, an unnamed outcome must not be guessed at: closing the
// wrong incident is worse than closing none and saying so.
func TestResolveTargetsRefusesToGuess(t *testing.T) {
	s := sessionWith("a", "b")

	if got := s.resolveTargets(nil); len(got) != 0 {
		t.Errorf("guessed %v with several incidents open", got)
	}
}

// The model may name an incident that closed while it was working.
func TestResolveTargetsIgnoresUnknownNames(t *testing.T) {
	s := sessionWith("a")

	got := s.resolveTargets([]string{"a", "already-closed"})

	if len(got) != 1 || got[0].IncidentName != "a" {
		t.Errorf("got %v; want only the open one", got)
	}
}
