package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sguldemond/goblin/agent/internal/messenger"
)

// fakeMessenger replays scripted answers to Ask and records what was sent.
type fakeMessenger struct {
	answers []string
	asked   []string
	sent    []string
}

func (f *fakeMessenger) Send(text string) error { f.sent = append(f.sent, text); return nil }

func (f *fakeMessenger) Ask(_ context.Context, q string, _ [][]messenger.Button) (string, error) {
	f.asked = append(f.asked, q)
	if len(f.answers) == 0 {
		return "", nil
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return a, nil
}

func (f *fakeMessenger) StartThinking() func() { return func() {} }

func stagedChange(applied *bool, verify func(context.Context, messenger.Messenger) (string, bool)) *StagedChange {
	return &StagedChange{
		Target: "default/demo",
		Diff:   "- old\n+ new",
		Apply:  func(context.Context) error { *applied = true; return nil },
		Verify: verify,
	}
}

func TestStageRefusesSecondChange(t *testing.T) {
	var g ApprovalGate
	var applied bool

	if err := g.Stage(stagedChange(&applied, nil)); err != nil {
		t.Fatalf("first Stage: %v", err)
	}
	err := g.Stage(&StagedChange{Target: "default/other"})
	if err == nil {
		t.Fatal("second Stage succeeded; want refusal so the first proposal is not silently dropped")
	}
	if g.pending.Target != "default/demo" {
		t.Fatalf("pending was overwritten: got %q, want default/demo", g.pending.Target)
	}
}

func TestApproveAppliesAndReportsOutcome(t *testing.T) {
	var g ApprovalGate
	var applied bool
	verify := func(context.Context, messenger.Messenger) (string, bool) { return "rolled out", true }
	if err := g.Stage(stagedChange(&applied, verify)); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	m := &fakeMessenger{answers: []string{"y"}}
	if _, _, err := g.AfterTurn(context.Background(), m); err != nil {
		t.Fatalf("AfterTurn: %v", err)
	}
	if !applied {
		t.Error("Apply was not called after approval")
	}
	o, ok := g.Outcome()
	if !ok || o.Phase != "Applied" || o.Message != "rolled out" {
		t.Errorf("Outcome = %+v (ok=%v); want Applied/rolled out", o, ok)
	}
	if _, ok := g.Outcome(); ok {
		t.Error("Outcome reported twice; want report-once")
	}
	if g.Active() {
		t.Error("gate still active after approval")
	}
}

func TestUnsettledVerifyReportsNoOutcome(t *testing.T) {
	var g ApprovalGate
	var applied bool
	verify := func(context.Context, messenger.Messenger) (string, bool) { return "timed out", false }
	if err := g.Stage(stagedChange(&applied, verify)); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	m := &fakeMessenger{answers: []string{"y"}}
	if _, _, err := g.AfterTurn(context.Background(), m); err != nil {
		t.Fatalf("AfterTurn: %v", err)
	}
	if !applied {
		t.Error("Apply was not called")
	}
	if _, ok := g.Outcome(); ok {
		t.Error("reported Applied for a change that never settled")
	}
}

func TestRejectDoesNotApply(t *testing.T) {
	var g ApprovalGate
	var applied bool
	if err := g.Stage(stagedChange(&applied, nil)); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	m := &fakeMessenger{answers: []string{"n", "wrong namespace"}}
	msgs, _, err := g.AfterTurn(context.Background(), m)
	if err != nil {
		t.Fatalf("AfterTurn: %v", err)
	}
	if applied {
		t.Fatal("Apply was called despite rejection")
	}
	if len(msgs) != 1 || msgs[0] != "Human rejected the change. Reason: wrong namespace" {
		t.Errorf("msgs = %v; want rejection carrying the reason", msgs)
	}
	if _, ok := g.Outcome(); ok {
		t.Error("rejection reported an outcome; today rejection leaves the CR untouched")
	}
	if g.Active() {
		t.Error("gate still active after rejection")
	}
}

func TestApplyErrorClearsPending(t *testing.T) {
	var g ApprovalGate
	if err := g.Stage(&StagedChange{
		Target: "default/demo",
		Apply:  func(context.Context) error { return errors.New("conflict") },
	}); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	m := &fakeMessenger{answers: []string{"y"}}
	msgs, stop, err := g.AfterTurn(context.Background(), m)

	// A failed apply must not be fatal. The scout is long-lived and still owes
	// work to every other incident it holds, so the failure goes back to the
	// model as a message rather than killing the process.
	if err != nil {
		t.Fatalf("failed apply returned a fatal error: %v", err)
	}
	if stop {
		t.Error("failed apply asked the loop to stop")
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0], "conflict") {
		t.Errorf("msgs = %v; want the apply error reported back to the model", msgs)
	}
	if g.Active() {
		t.Error("gate still active after a failed apply; a later turn would re-prompt")
	}
	if _, ok := g.Outcome(); ok {
		t.Error("reported an outcome for a change that never landed")
	}
}

// A change approved on stale information must not be applied. The gate asks
// the caller whether the world still matches, and abandons the change if not.
func TestApprovalRevalidationBlocksStaleChange(t *testing.T) {
	var g ApprovalGate
	var applied bool
	g.OnApprove = func(context.Context, *StagedChange) (bool, string, error) {
		return false, "two incidents arrived; re-evaluate", nil
	}
	if err := g.Stage(stagedChange(&applied, nil)); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	m := &fakeMessenger{answers: []string{"y"}}
	msgs, _, err := g.AfterTurn(context.Background(), m)
	if err != nil {
		t.Fatalf("AfterTurn: %v", err)
	}
	if applied {
		t.Fatal("applied a change the caller said was stale")
	}
	if len(msgs) != 1 || msgs[0] != "two incidents arrived; re-evaluate" {
		t.Errorf("msgs = %v; want the re-evaluation note passed back to the model", msgs)
	}
	if g.Active() {
		t.Error("gate still holds the abandoned change")
	}
}

func TestApprovalProceedsWhenNothingChanged(t *testing.T) {
	var g ApprovalGate
	var applied bool
	g.OnApprove = func(context.Context, *StagedChange) (bool, string, error) {
		return true, "", nil
	}
	if err := g.Stage(stagedChange(&applied, nil)); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	m := &fakeMessenger{answers: []string{"y"}}
	if _, _, err := g.AfterTurn(context.Background(), m); err != nil {
		t.Fatalf("AfterTurn: %v", err)
	}
	if !applied {
		t.Error("change was not applied even though revalidation passed")
	}
}

// Re-proposing exactly what the human already approved must not ask again,
// or every unrelated incident costs them a second identical button press.
func TestPreApprovedSkipsTheSecondPrompt(t *testing.T) {
	var g ApprovalGate
	var applied bool
	g.PreApproved = func(*StagedChange) bool { return true }
	if err := g.Stage(stagedChange(&applied, nil)); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	m := &fakeMessenger{} // no scripted answers: an Ask here would be a bug
	if _, _, err := g.AfterTurn(context.Background(), m); err != nil {
		t.Fatalf("AfterTurn: %v", err)
	}
	if !applied {
		t.Error("pre-approved change was not applied")
	}
	if len(m.asked) != 0 {
		t.Errorf("asked the human again: %v", m.asked)
	}
}

// OnStage is what attaches the snapshot a change is later judged against.
func TestOnStageAttachesSnapshot(t *testing.T) {
	var g ApprovalGate
	var applied bool
	g.OnStage = func(c *StagedChange) { c.Snapshot = []string{"goblin/a"} }

	if err := g.Stage(stagedChange(&applied, nil)); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	snap, ok := g.pending.Snapshot.([]string)
	if !ok || len(snap) != 1 || snap[0] != "goblin/a" {
		t.Errorf("snapshot = %v; want it captured at stage time", g.pending.Snapshot)
	}
}
