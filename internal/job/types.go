package job

import (
	"context"

	"github.com/pyed/CordBrief/internal/brief"
	"github.com/pyed/CordBrief/internal/dce"
)

// DCEExporter abstracts DiscordChatExporter operations.
type DCEExporter interface {
	Export(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error)
	IsConfigured() bool
}

// Deliverer abstracts delivering plain-text messages to the Telegram owner.
type Deliverer interface {
	Deliver(ctx context.Context, text string) error
}

// CompleterFactory produces a brief.Completer given the LLM connection parameters.
type CompleterFactory func(baseURL, model, apiKey string) (brief.Completer, error)
