package tools

import (
	"context"
	"errors"
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
	phase, msg, ok := g.Outcome()
	if !ok || phase != "Applied" || msg != "rolled out" {
		t.Errorf("Outcome = (%q, %q, %v); want (Applied, rolled out, true)", phase, msg, ok)
	}
	if _, _, ok := g.Outcome(); ok {
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
	if _, _, ok := g.Outcome(); ok {
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
	if _, _, ok := g.Outcome(); ok {
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
	if _, _, err := g.AfterTurn(context.Background(), m); err == nil {
		t.Fatal("AfterTurn returned nil error when Apply failed")
	}
	if g.Active() {
		t.Error("gate still active after a failed apply; a later turn would re-prompt")
	}
}
