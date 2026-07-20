package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sguldemond/goblin/agent/internal/messenger"
)

type Escalate struct {
	triggered bool
	reason    string
	reported  bool
}

func NewEscalate() *Escalate {
	return &Escalate{}
}

func (t *Escalate) Name() string { return "escalate" }

func (t *Escalate) Description() string {
	return "Signal that you cannot determine a safe fix. Ends the investigation and surfaces the reason to the human. Use this instead of guessing."
}

func (t *Escalate) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"reason": {"type": "string", "description": "Why you cannot determine a safe fix"}
		},
		"required": ["reason"]
	}`)
}

type escalateParams struct {
	Reason string `json:"reason"`
}

func (t *Escalate) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var p escalateParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	t.triggered = true
	t.reason = p.Reason
	return fmt.Sprintf("Escalated: %s", p.Reason), nil
}

func (t *Escalate) AfterTool(_ context.Context, m messenger.Messenger) (bool, error) {
	if !t.triggered {
		return false, nil
	}
	m.Send(fmt.Sprintf("🚨 Goblin is escalating and stepping away.\n\n<b>Reason:</b> %s\n\nNo changes were made. Goodbye.", t.reason)) //nolint:errcheck
	return true, nil
}

func (t *Escalate) Outcome() (string, string, bool) {
	if !t.triggered || t.reported {
		return "", "", false
	}
	t.reported = true
	return "Escalated", t.reason, true
}

var _ Tool = (*Escalate)(nil)
var _ AfterToolHook = (*Escalate)(nil)
var _ OutcomeReporter = (*Escalate)(nil)
