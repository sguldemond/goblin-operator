package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// NewAnthropic returns a SendFunc that calls the Anthropic Messages API.
// The internal types map directly to Anthropic's wire format.
func NewAnthropic(apiKey string) SendFunc {
	return func(ctx context.Context, req Request) (Response, error) {
		body, err := json.Marshal(req)
		if err != nil {
			return Response{}, fmt.Errorf("marshaling request: %w", err)
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"https://api.anthropic.com/v1/messages", bytes.NewReader(body))
		if err != nil {
			return Response{}, err
		}
		httpReq.Header.Set("x-api-key", apiKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
		httpReq.Header.Set("content-type", "application/json")

		resp, err := httpClient.Do(httpReq)
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

		var result Response
		if err := json.Unmarshal(respBody, &result); err != nil {
			return Response{}, fmt.Errorf("decoding response: %w", err)
		}
		return result, nil
	}
}
