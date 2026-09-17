package job

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/pyed/CordBrief/internal/brief"
	"github.com/pyed/CordBrief/internal/llm"
)

// RetryCompleter wraps a brief.Completer with a bounded single-retry policy for transient failures.
// It executes at most 2 total attempts with a 2-second sleep between attempts.
// It retries only genuinely transient failures:
// - HTTP 429
// - HTTP 5xx
// - genuine network/transport errors
// - CordBrief's own default HTTP-client timeout while caller context is still alive
// It never retries if caller context was canceled or deadline expired, on permanent HTTP errors,
// or on local/parsing/validation errors.
type RetryCompleter struct {
	base  brief.Completer
	sleep time.Duration
}

// NewRetryCompleter creates a RetryCompleter wrapping the given base completer.
func NewRetryCompleter(base brief.Completer) *RetryCompleter {
	return &RetryCompleter{
		base:  base,
		sleep: 2 * time.Second,
	}
}

// isTransient reports whether an error is genuinely transient and eligible for retry.
func isTransient(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}

	// 1. Caller context canceled or caller deadline expired -> no retry
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}

	// 2. HTTP status error classification:
	// Retry HTTP 429 (rate limit) and HTTP 5xx (server error).
	// Never retry permanent client errors (HTTP 4xx).
	var statusErr *llm.StatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode == http.StatusTooManyRequests || (statusErr.StatusCode >= 500 && statusErr.StatusCode <= 599)
	}

	// 3. Raw context.DeadlineExceeded without transport wrapping indicates a caller deadline -> no retry
	var urlErr *url.Error
	if errors.Is(err, context.DeadlineExceeded) && !errors.As(err, &urlErr) {
		return false
	}

	// 4. Genuine network/transport errors, including HTTP client timeout while caller context is alive
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	// 5. Arbitrary/local errors, malformed responses, JSON/response parsing errors, validation errors -> no retry
	return false
}

// Complete implements brief.Completer with bounded retry behavior.
func (r *RetryCompleter) Complete(ctx context.Context, messages []llm.Message) (string, error) {
	if r.base == nil {
		return "", errors.New("base completer is nil")
	}

	res, err := r.base.Complete(ctx, messages)
	if err == nil {
		return res, nil
	}

	if !isTransient(ctx, err) {
		return "", err
	}

	// Bounded wait before attempt 2
	sleepDur := r.sleep
	if sleepDur <= 0 {
		sleepDur = 2 * time.Second
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(sleepDur):
	}

	return r.base.Complete(ctx, messages)
}
