package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// NewOpenAI returns a SendFunc that calls the OpenAI Chat Completions API.
// Translates between the internal canonical types and OpenAI's wire format.
func NewOpenAI(apiKey string) SendFunc {
	return func(ctx context.Context, req Request) (Response, error) {
		oaiReq := toOpenAIRequest(req)

		body, err := json.Marshal(oaiReq)
		if err != nil {
			return Response{}, fmt.Errorf("marshaling request: %w", err)
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			return Response{}, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		httpReq.Header.Set("content-type", "application/json")

		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			return Response{}, err
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return Response{}, fmt.Errorf("reading response: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return Response{}, APIError{StatusCode: resp.StatusCode, Body: string(respBody)}
		}

		var oaiResp openAIResponse
		if err := json.Unmarshal(respBody, &oaiResp); err != nil {
			return Response{}, fmt.Errorf("decoding response: %w", err)
		}
		return fromOpenAIResponse(oaiResp), nil
	}
}

// --- OpenAI wire types ---

type openAIRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []openAIMessage `json:"messages"`
	Tools     []openAITool    `json:"tools,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"` // string or []openAIContentPart
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAITool struct {
	Type     string          `json:"type"` // "function"
	Function openAIFunction  `json:"function"`
}

type openAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   string           `json:"content"`
			ToolCalls []openAIToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

// --- translation ---

func toOpenAIRequest(req Request) openAIRequest {
	msgs := make([]openAIMessage, 0, len(req.Messages)+1)

	// System prompt becomes first message.
	if req.System != "" {
		msgs = append(msgs, openAIMessage{Role: "system", Content: req.System})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			// tool_result blocks become separate role:"tool" messages.
			// Other content blocks are batched as a single user message.
			var userParts []openAIContentPart
			for _, c := range m.Content {
				if c.Type == "tool_result" {
					if len(userParts) > 0 {
						msgs = append(msgs, openAIMessage{Role: "user", Content: userParts})
						userParts = nil
					}
					msgs = append(msgs, openAIMessage{
						Role:       "tool",
						Content:    c.Content,
						ToolCallID: c.ToolUseID,
					})
				} else if c.Type == "text" {
					userParts = append(userParts, openAIContentPart{Type: "text", Text: c.Text})
				}
			}
			if len(userParts) > 0 {
				msgs = append(msgs, openAIMessage{Role: "user", Content: userParts})
			}

		case "assistant":
			var text string
			var toolCalls []openAIToolCall
			for _, c := range m.Content {
				if c.Type == "text" {
					text = c.Text
				} else if c.Type == "tool_use" {
					toolCalls = append(toolCalls, openAIToolCall{
						ID:   c.ID,
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{Name: c.Name, Arguments: string(c.Input)},
					})
				}
			}
			msgs = append(msgs, openAIMessage{Role: "assistant", Content: text, ToolCalls: toolCalls})
		}
	}

	tools := make([]openAITool, len(req.Tools))
	for i, t := range req.Tools {
		tools[i] = openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		}
	}

	return openAIRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		Messages:  msgs,
		Tools:     tools,
	}
}

func fromOpenAIResponse(resp openAIResponse) Response {
	if len(resp.Choices) == 0 {
		return Response{}
	}
	choice := resp.Choices[0]

	var content []Content
	if choice.Message.Content != "" {
		content = append(content, Content{Type: "text", Text: choice.Message.Content})
	}
	for _, tc := range choice.Message.ToolCalls {
		content = append(content, Content{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: json.RawMessage(tc.Function.Arguments),
		})
	}

	stopReason := "end_turn"
	if choice.FinishReason == "tool_calls" {
		stopReason = "tool_use"
	}

	return Response{Content: content, StopReason: stopReason}
}
