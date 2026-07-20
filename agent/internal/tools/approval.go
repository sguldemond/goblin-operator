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
	Apply  func(ctx context.Context) error
	Verify func(ctx context.Context, m messenger.Messenger) (summary string, ok bool)
}

// ApprovalGate is the human gate shared by every mutating tool: stage a change,
// show the diff, ask, apply on yes, capture a reason on no. Embed it in a tool
// and call Stage from Execute — the tool supplies what to apply, the gate owns
// the conversation.
type ApprovalGate struct {
	pending *StagedChange
	outcome *toolOutcome
}

type toolOutcome struct{ phase, message string }

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
	g.pending = c
	return nil
}

func (g *ApprovalGate) Active() bool { return g.pending != nil }

func (g *ApprovalGate) AfterTurn(ctx context.Context, m messenger.Messenger) ([]string, bool, error) {
	c := g.pending

	m.Send(fmt.Sprintf("Diff: %s\n%s", c.Target, c.Diff)) //nolint:errcheck

	answer, err := m.Ask(ctx, "Apply this change?", [][]messenger.Button{
		{{Text: "✅ Apply", Data: "y"}, {Text: "❌ Reject", Data: "n"}},
	})
	if err != nil {
		g.pending = nil
		return nil, false, err
	}

	if strings.ToLower(strings.TrimSpace(answer)) == "y" {
		if err := c.Apply(ctx); err != nil {
			g.pending = nil
			return nil, false, fmt.Errorf("applying change: %w", err)
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
			g.outcome = &toolOutcome{phase: "Applied", message: summary}
		}
		return []string{strings.TrimSpace("Change applied. " + summary)}, false, nil
	}

	reason, _ := m.Ask(ctx, "Rejection reason (optional, or leave empty):", nil)
	g.pending = nil

	msg := "Human rejected the change."
	if reason != "" {
		msg += " Reason: " + reason
	}
	return []string{msg}, false, nil
}

// Outcome implements OutcomeReporter. It reports once and clears, so a single
// applied change does not re-write the Incident status on every later turn.
func (g *ApprovalGate) Outcome() (string, string, bool) {
	if g.outcome == nil {
		return "", "", false
	}
	o := g.outcome
	g.outcome = nil
	return o.phase, o.message, true
}
