# Scout LLM Loop — Design Spec
_2026-06-21_

## What this is

Wire the goblin-scout agent binary to the Anthropic Messages API so that after gathering incident context it starts an interactive conversation with Claude — tool calls handled automatically, then stdin open for the human to chat directly.

## Scope

Phase A only: human talks to Claude via stdin (`kubectl attach -it`). Phase C (Claude runs autonomously first, then opens the floor) is explicitly out of scope here.

## Package structure

```
agent/
├── internal/
│   ├── config/config.go        — add APIKey from API_KEY env var
│   ├── llm/
│   │   ├── client.go           — raw HTTP POST to Anthropic Messages API
│   │   └── types.go            — typed request/response structs
│   ├── scout/
│   │   ├── context.go          — unchanged: builds initial user message string
│   │   └── scout.go            — Run() drives the conversation loop
│   └── tools/                  — unchanged
└── main.go                     — unchanged
```

One operator change: `createScoutJob` in `operator/internal/controller/remediation_controller.go` adds `stdin: true` + `tty: true` to the container spec so `kubectl attach -it` works.

## Conversation flow

```
Scout.Run()
  1. loadIncident + runTools        (already exists)
  2. BuildContext                   → initial user message string
  3. Send to Anthropic: system prompt + [user: context] + tools defined
  4. ── inner tool loop ──────────────────────────────────────────────
     │  Claude returns tool_use blocks?
     │    → execute each tool (existing Tool.Execute)
     │    → append assistant msg + tool_result user msg to history
     │    → call API again, repeat
     └── until stop_reason == "end_turn"
  5. Print Claude's text response to stdout
  6. ── stdin REPL ────────────────────────────────────────────────────
     │  Print "> " prompt, read line from stdin
     │  Append as user message, go to step 4
     └── until EOF (Ctrl+D) → clean exit
```

## API client (`internal/llm/client.go`)

Single exported function:
```go
func Send(ctx context.Context, apiKey string, req Request) (Response, error)
```

- Raw `net/http` POST to `https://api.anthropic.com/v1/messages`
- Headers: `x-api-key`, `anthropic-version: 2023-06-01`, `content-type: application/json`
- Non-2xx responses returned as a typed error with status code + body

## Types (`internal/llm/types.go`)

```go
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

// type: "text" | "tool_use" | "tool_result"
type Content struct {
    Type      string          `json:"type"`
    Text      string          `json:"text,omitempty"`
    ID        string          `json:"id,omitempty"`          // tool_use
    Name      string          `json:"name,omitempty"`        // tool_use
    Input     json.RawMessage `json:"input,omitempty"`       // tool_use
    ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result
    IsError   bool            `json:"is_error,omitempty"`    // tool_result
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
```

`StopReason == "tool_use"` drives the inner tool loop. `StopReason == "end_turn"` drops to stdin.

## Tool error handling

Tool execution errors (`Tool.Execute` returns non-nil error) are returned to Claude as a `tool_result` block with `is_error: true` and the error string as content. The binary does not crash — Claude reasons about the failure.

## Config

`API_KEY` env var added to `config.Config.APIKey`. Loaded from the environment; the operator already injects env vars into the Job pod spec (caller's responsibility to set `API_KEY` for now).

## Model

`claude-sonnet-4-6`

## Out of scope

- Streaming responses
- Writing proposed action back to the Remediation CR (future phase)
- Phase C autonomous-first loop
- Approval gate / stdin gating on proposed actions
