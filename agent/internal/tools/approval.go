package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/sguldemond/goblin/agent/internal/messenger"
)

// StagedChange is one mutation computed but not yet applied, waiting on a human.
// Apply performs the real write; Verify optionally confirms it landed, returning
// a summary for the human and whether the change actually settled.
type StagedChange struct {
	Target string // "namespace/name", for display only
	Diff   string
	// Fingerprint identifies the change itself, so a re-proposal can be
	// recognised as identical to one the human already approved.
	Fingerprint string
	// Resolves names the incidents this change fixes. One patch commonly
	// resolves several — sibling replicas of one Deployment — and without this
	// only one of them could be closed.
	Resolves []string
	Apply    func(ctx context.Context) error
	Verify   func(ctx context.Context, m messenger.Messenger) (summary string, ok bool)

	// Snapshot is opaque to the gate: whatever the caller needs in order to
	// judge, at approval time, whether the world has moved since staging.
	Snapshot any
}

// ApprovalGate is the human gate shared by every mutating tool: stage a change,
// show the diff, ask, apply on yes, capture a reason on no. Embed it in a tool
// and call Stage from Execute — the tool supplies what to apply, the gate owns
// the conversation.
type ApprovalGate struct {
	pending *StagedChange
	outcome *Outcome

	// OnStage lets the caller attach state to a change as it is staged — the
	// snapshot it should later be judged against.
	OnStage func(c *StagedChange)

	// OnApprove runs after the human approves but before anything is applied.
	// Returning proceed=false abandons the change and sends note back to the
	// model instead, which is how an approval given on stale information stops
	// being acted on. The gate stays ignorant of what "stale" means.
	OnApprove func(ctx context.Context, c *StagedChange) (proceed bool, note string, err error)

	// PreApproved reports that this exact change was already approved and only
	// re-proposed because the gate asked for a re-think. Without it, every
	// unrelated incident that happens to arrive would cost the human a second
	// identical button press.
	PreApproved func(c *StagedChange) bool
}

// Stage arms the gate. It refuses a second change while one is already pending:
// the model can emit several tool_use blocks in a single turn, and silently
// overwriting the first would drop a proposal the human never saw. The error
// goes back to the model as a tool result, telling it to slow down.
func (g *ApprovalGate) Stage(c *StagedChange) error {
	if g.pending != nil {
		return fmt.Errorf(
			"a change to %s is already staged and awaiting human approval; propose one change at a time",
			g.pending.Target)
	}
	if g.OnStage != nil {
		g.OnStage(c)
	}
	g.pending = c
	return nil
}

func (g *ApprovalGate) Active() bool { return g.pending != nil }

func (g *ApprovalGate) AfterTurn(ctx context.Context, m messenger.Messenger) ([]string, bool, error) {
	c := g.pending

	// Already approved and merely re-proposed unchanged: do not ask twice.
	if g.PreApproved != nil && g.PreApproved(c) {
		m.Send(fmt.Sprintf("Proposal for %s is unchanged from the one you approved — applying.", c.Target)) //nolint:errcheck
		return g.apply(ctx, c, m)
	}

	m.Send(fmt.Sprintf("Diff: %s\n%s", c.Target, c.Diff)) //nolint:errcheck

	answer, err := m.Ask(ctx, "Apply this change?", [][]messenger.Button{
		{{Text: "✅ Apply", Data: "y"}, {Text: "❌ Reject", Data: "n"}},
	})
	if err != nil {
		g.pending = nil
		return nil, false, err
	}

	if strings.ToLower(strings.TrimSpace(answer)) == "y" {
		// The human decided using what was true when they were asked. Check
		// that it still holds before mutating anything.
		if g.OnApprove != nil {
			proceed, note, err := g.OnApprove(ctx, c)
			if err != nil {
				g.pending = nil
				return nil, false, err
			}
			if !proceed {
				g.pending = nil
				m.Send("Holding off — the situation changed since I proposed that. Re-evaluating.") //nolint:errcheck
				return []string{note}, false, nil
			}
		}
		return g.apply(ctx, c, m)
	}

	reason, _ := m.Ask(ctx, "Rejection reason (optional, or leave empty):", nil)
	g.pending = nil

	msg := "Human rejected the change."
	if reason != "" {
		msg += " Reason: " + reason
	}
	return []string{msg}, false, nil
}

// apply performs the change and clears the gate. Split out because approval
// now has two entry points: the human answering, and a re-proposal that the
// human had already approved unchanged.
func (g *ApprovalGate) apply(ctx context.Context, c *StagedChange, m messenger.Messenger) ([]string, bool, error) {
	if err := c.Apply(ctx); err != nil {
		g.pending = nil
		// A failed apply is a fact about this change, not a fatal condition:
		// the scout is long-lived and still owes work to every other incident
		// it holds. Report it and let the model decide what to do — most often
		// escalate, since a rejected write usually means missing permission or
		// an object that moved underneath.
		m.Send(fmt.Sprintf("❌ Could not apply the change to %s: %v", c.Target, err)) //nolint:errcheck
		return []string{fmt.Sprintf(
			"Applying your approved change to %s failed: %v\n"+
				"Nothing was changed. Do not simply retry the same patch — work out why it was refused "+
				"(a missing permission, or the object changed since you looked) and escalate if you cannot.",
			c.Target, err)}, false, nil
	}
	g.pending = nil
	m.Send("Change applied.") //nolint:errcheck

	summary, settled := "", true
	if c.Verify != nil {
		summary, settled = c.Verify(ctx, m)
	}
	// Only claim Applied once the change actually settled — a timed-out
	// rollout is not a successful remediation.
	if settled {
		g.outcome = &Outcome{Incidents: c.Resolves, Phase: "Applied", Message: summary}
	}
	return []string{strings.TrimSpace("Change applied. " + summary)}, false, nil
}

// Outcome implements OutcomeReporter. It reports once and clears, so a single
// applied change does not re-write the Incident status on every later turn.
func (g *ApprovalGate) Outcome() (Outcome, bool) {
	if g.outcome == nil {
		return Outcome{}, false
	}
	o := *g.outcome
	g.outcome = nil
	return o, true
}
