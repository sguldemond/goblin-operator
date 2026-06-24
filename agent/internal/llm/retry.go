package llm

import (
	"context"
	"fmt"
	"time"
)

// APIError is returned by providers when the HTTP response is not 200.
type APIError struct {
	StatusCode int
	Body       string
}

func (e APIError) Error() string {
	return fmt.Sprintf("API error %d: %s", e.StatusCode, e.Body)
}

func retryableStatus(code int) bool {
	return code == 529 || code == 503 || code == 502 || code == 500
}

// WithRetry wraps a SendFunc with exponential backoff retry on transient errors.
func WithRetry(fn SendFunc, maxAttempts int, onRetry func(attempt int, err error, delay time.Duration)) SendFunc {
	return func(ctx context.Context, req Request) (Response, error) {
		delay := 5 * time.Second
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			resp, err := fn(ctx, req)
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
			if onRetry != nil {
				onRetry(attempt, err, delay)
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
}
