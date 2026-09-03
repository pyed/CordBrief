package llm

import (
	"context"
	"cordbrief/internal/digest"
)

// Provider abstracts the language model summarizer.
type Provider interface {
	Summarize(ctx context.Context, systemPrompt, userPrompt string) (*digest.Digest, error)
}

// OpenAICompatibleProvider communicates with OpenAI-compatible chat completion endpoints.
type OpenAICompatibleProvider struct {
	BaseURL         string
	Model           string
	APIKey          string
	MaxOutputTokens int
}
