package scout

import (
	"context"
	"testing"
	"time"
)

// Shrink the windows: the behaviour under test is the debounce shape, not the
// wall-clock values.
func init() {
	settleWindow = 60 * time.Millisecond
	settleCeiling = 500 * time.Millisecond
}

func inc(name string) *Incident {
	return &Incident{IncidentName: name, Namespace: "goblin"}
}

func names(batch []*Incident) []string {
	out := make([]string, 0, len(batch))
	for _, i := range batch {
		out = append(out, i.IncidentName)
	}
	return out
}

// The case that motivated this: a Deployment with two replicas OOMKills, and
// the incidents arrive a moment apart. Investigating the first alone and the
// second separately does duplicate work and hides that they are one fault.
func TestCollectBatchesSiblings(t *testing.T) {
	ch := make(chan *Incident, 4)
	go func() {
		time.Sleep(20 * time.Millisecond)
		ch <- inc("b")
		time.Sleep(20 * time.Millisecond)
		ch <- inc("c")
	}()

	start := time.Now()
	batch := collect(context.Background(), inc("a"), ch)
	elapsed := time.Since(start)

	if got := names(batch); len(got) != 3 {
		t.Fatalf("batch = %v; want all three siblings collected", got)
	}
	// Debounce, not a fixed window: it returns once things go quiet, so the
	// total is roughly the arrivals plus one settle period.
	if elapsed > settleWindow+200*time.Millisecond {
		t.Errorf("took %s; a debounce should return shortly after the last arrival", elapsed)
	}
}

// A lone incident must not wait for the ceiling.
func TestCollectReturnsAfterQuiet(t *testing.T) {
	ch := make(chan *Incident)

	start := time.Now()
	batch := collect(context.Background(), inc("solo"), ch)
	elapsed := time.Since(start)

	if len(batch) != 1 {
		t.Fatalf("batch = %v; want just the one", names(batch))
	}
	if elapsed < settleWindow {
		t.Errorf("returned after %s; should wait a settle window for siblings", elapsed)
	}
	if elapsed > settleWindow+200*time.Millisecond {
		t.Errorf("took %s; a lone incident should not wait for the ceiling", elapsed)
	}
}

// Already-queued incidents are free to collect: they arrived while the scout
// was busy and belong in the same batch.
func TestCollectDrainsWhatIsAlreadyQueued(t *testing.T) {
	ch := make(chan *Incident, 4)
	ch <- inc("b")
	ch <- inc("c")

	batch := collect(context.Background(), inc("a"), ch)

	if got := names(batch); len(got) != 3 {
		t.Fatalf("batch = %v; want queued incidents drained immediately", got)
	}
}

// A cancelled context must not leave the scout waiting out the window.
func TestCollectStopsOnContextCancel(t *testing.T) {
	ch := make(chan *Incident)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	batch := collect(ctx, inc("a"), ch)

	if len(batch) != 1 {
		t.Errorf("batch = %v; want the incident already in hand", names(batch))
	}
	if elapsed := time.Since(start); elapsed >= settleWindow {
		t.Errorf("waited %s after cancellation", elapsed)
	}
}

func TestDrainReturnsNothingWhenEmpty(t *testing.T) {
	if got := drain(make(chan *Incident)); len(got) != 0 {
		t.Errorf("drain of an empty channel returned %v", names(got))
	}
}
