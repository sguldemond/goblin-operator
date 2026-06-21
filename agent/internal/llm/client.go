package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const defaultBaseURL = "https://api.anthropic.com"

type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: defaultBaseURL,
		HTTP:    http.DefaultClient,
	}
}

func (c *Client) Send(ctx context.Context, req Request) (Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("x-api-key", c.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("content-type", "application/json")

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, fmt.Errorf("reading response body: %w", err)
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

type APIError struct {
	StatusCode int
	Body       string
}

func (e APIError) Error() string {
	return fmt.Sprintf("anthropic API error %d: %s", e.StatusCode, e.Body)
}
