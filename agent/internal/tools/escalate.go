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
	incidents []string
	reported  bool
}

func NewEscalate() *Escalate {
	return &Escalate{}
}

func (t *Escalate) Name() string { return "escalate" }

func (t *Escalate) Description() string {
	return "Signal that you cannot determine a safe fix. Ends the investigation and surfaces the reason to the human. " +
		"Name every incident you are giving up on — several may be open. Use this instead of guessing."
}

func (t *Escalate) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"reason":    {"type": "string", "description": "Why you cannot determine a safe fix"},
			"incidents": {"type": "array", "items": {"type": "string"}, "description": "Names of the incidents you are escalating"}
		},
		"required": ["reason"]
	}`)
}

type escalateParams struct {
	Reason    string   `json:"reason"`
	Incidents []string `json:"incidents"`
}

func (t *Escalate) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var p escalateParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	t.triggered = true
	t.reason = p.Reason
	t.incidents = p.Incidents
	return fmt.Sprintf("Escalated: %s", p.Reason), nil
}

func (t *Escalate) AfterTool(_ context.Context, m messenger.Messenger) (bool, error) {
	if !t.triggered {
		return false, nil
	}
	m.Send(fmt.Sprintf("🚨 Goblin is escalating and stepping away.\n\n<b>Reason:</b> %s\n\nNo changes were made. Goodbye.", t.reason)) //nolint:errcheck
	return true, nil
}

func (t *Escalate) Outcome() (Outcome, bool) {
	if !t.triggered || t.reported {
		return Outcome{}, false
	}
	t.reported = true
	return Outcome{Incidents: t.incidents, Phase: "Escalated", Message: t.reason}, true
}

var _ Tool = (*Escalate)(nil)
var _ AfterToolHook = (*Escalate)(nil)
var _ OutcomeReporter = (*Escalate)(nil)
