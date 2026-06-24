package llm

import (
	"context"
	"encoding/json"
)

type Request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system"`
	Messages  []Message `json:"messages"`
	Tools     []ToolDef `json:"tools,omitempty"`
}

type Message struct {
	Role    string    `json:"role"`
	Content []Content `json:"content"`
}

// Content covers text, tool_use, and tool_result blocks.
type Content struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`          // text blocks
	ID        string          `json:"id,omitempty"`            // tool_use
	Name      string          `json:"name,omitempty"`          // tool_use
	Input     json.RawMessage `json:"input,omitempty"`         // tool_use
	ToolUseID string          `json:"tool_use_id,omitempty"`   // tool_result
	IsError   bool            `json:"is_error,omitempty"`      // tool_result
	Content   string          `json:"content,omitempty"`       // tool_result payload
}

type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type Response struct {
	Content    []Content `json:"content"`
	StopReason string    `json:"stop_reason"` // "end_turn" | "tool_use"
}

// SendFunc is the provider-agnostic function signature used throughout the agent.
type SendFunc = func(ctx context.Context, req Request) (Response, error)
