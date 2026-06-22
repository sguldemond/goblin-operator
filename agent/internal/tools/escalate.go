package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

type Escalate struct {
	triggered  bool
	reason     string
	onEscalate func(ctx context.Context, reason string) error
}

func NewEscalate(onEscalate func(ctx context.Context, reason string) error) *Escalate {
	return &Escalate{onEscalate: onEscalate}
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

func (t *Escalate) AfterTool(ctx context.Context, out io.Writer) (bool, error) {
	if !t.triggered {
		return false, nil
	}
	fmt.Fprintf(out, "\n[escalated] %s\n", t.reason)
	return true, t.onEscalate(ctx, t.reason)
}

var _ Tool = (*Escalate)(nil)
var _ AfterToolHook = (*Escalate)(nil)
