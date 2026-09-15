package job

import (
	"context"
	"errors"
	"time"

	"github.com/pyed/CordBrief/internal/brief"
	"github.com/pyed/CordBrief/internal/llm"
)

// RetryCompleter wraps a brief.Completer with a bounded single-retry policy for transient failures.
// It executes at most 2 total attempts with a 2-second sleep between attempts.
// It never retries if the context is canceled or deadline exceeded.
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

// Complete implements brief.Completer with bounded retry behavior.
func (r *RetryCompleter) Complete(ctx context.Context, messages []llm.Message) (string, error) {
	if r.base == nil {
		return "", errors.New("base completer is nil")
	}

	res, err := r.base.Complete(ctx, messages)
	if err == nil {
		return res, nil
	}

	// Do not retry on context cancellation or deadline expiration
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
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
