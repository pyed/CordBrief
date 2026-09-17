package job

import (
	"context"
	"errors"

	"github.com/pyed/CordBrief/internal/brief"
	"github.com/pyed/CordBrief/internal/llm"
)

// One instance belongs to one sequential brief job, including all reduction passes.
// Each provider retains the existing bounded retry policy.
type fallbackCompleter struct {
	primary        brief.Completer
	fallback       brief.Completer
	createFallback func() (brief.Completer, error)
	active         bool
}

func (f *fallbackCompleter) Complete(ctx context.Context, messages []llm.Message) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if f.active {
		return f.fallback.Complete(ctx, messages)
	}
	text, err := f.primary.Complete(ctx, messages)
	if err == nil {
		return text, nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if f.createFallback == nil {
		return "", err
	}
	if f.fallback == nil {
		f.fallback, err = f.createFallback()
		if err != nil {
			return "", errors.New("failed to initialize fallback LLM")
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	text, err = f.fallback.Complete(ctx, messages)
	if err == nil && ctx.Err() == nil {
		f.active = true
	}
	return text, err
}
