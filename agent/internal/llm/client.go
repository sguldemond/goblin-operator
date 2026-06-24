package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultBaseURL = "https://api.anthropic.com"

type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
	OnRetry func(attempt int, err error, delay time.Duration)
}

func NewClient(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: defaultBaseURL,
		HTTP:    http.DefaultClient,
	}
}

// retryableStatus returns true for transient server-side errors worth retrying.
func retryableStatus(code int) bool {
	return code == 529 || code == 503 || code == 502 || code == 500
}

func (c *Client) Send(ctx context.Context, req Request) (Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("marshaling request: %w", err)
	}

	const maxAttempts = 5
	delay := 5 * time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := c.doRequest(ctx, body)
		if err == nil {
			return resp, nil
		}

		apiErr, ok := err.(APIError)
		if !ok || !retryableStatus(apiErr.StatusCode) {
			return Response{}, err
		}

		if attempt == maxAttempts {
			return Response{}, err
		}

		if c.OnRetry != nil {
			c.OnRetry(attempt, err, delay)
		}

		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
	panic("unreachable")
}

func (c *Client) doRequest(ctx context.Context, body []byte) (Response, error) {
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
