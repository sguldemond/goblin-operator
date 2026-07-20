package tools

import (
	"context"
	"encoding/json"

	"github.com/sguldemond/goblin/agent/internal/messenger"
)

// Tool maps directly to the Claude tool_use API format.
// The interface is the safety boundary: only tools in this list can be called.
type Tool interface {
	Name() string
	Description() string
	InputSchema() json.RawMessage
	Execute(ctx context.Context, params json.RawMessage) (string, error)
}

// AfterToolHook is an optional interface for tools that need to act immediately
// after Execute() within the tool_use phase. Return stop=true to exit the loop.
type AfterToolHook interface {
	AfterTool(ctx context.Context, m messenger.Messenger) (stop bool, err error)
}

// AfterTurnHook is an optional interface for tools that replace the normal stdin
// prompt after end_turn. Active() gates whether the hook fires this turn.
// Returns messages to append and stop=true to exit the loop.
type AfterTurnHook interface {
	Active() bool
	AfterTurn(ctx context.Context, m messenger.Messenger) (msgs []string, stop bool, err error)
}

// OutcomeReporter is an optional interface for tools that conclude something
// about the incident. The scout loop — not the tool — writes the Incident CR,
// so tools stay ignorant of the CR entirely. Returns ok=false when there is
// nothing to report; implementations report once and then clear.
type OutcomeReporter interface {
	Outcome() (phase, message string, ok bool)
}

// ToolResult holds the output of a single tool execution for context assembly.
type ToolResult struct {
	ToolName string
	Output   string
	Err      error
}
