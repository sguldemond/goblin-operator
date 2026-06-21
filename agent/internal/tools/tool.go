package tools

import (
	"context"
	"encoding/json"
)

// Tool maps directly to the Claude tool_use API format.
// The interface is the safety boundary: only tools in this list can be called.
type Tool interface {
	Name() string
	Description() string
	// InputSchema returns a JSON Schema object describing the tool's parameters.
	InputSchema() json.RawMessage
	Execute(ctx context.Context, params json.RawMessage) (string, error)
}

// ToolResult holds the output of a single tool execution for context assembly.
type ToolResult struct {
	ToolName string
	Output   string
	Err      error
}
