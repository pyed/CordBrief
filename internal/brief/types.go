package brief

import (
	"context"
	"time"

	"github.com/pyed/CordBrief/internal/llm"
)

// Attachment represents a file or media attached to a Discord message.
type Attachment struct {
	FileName string `json:"file_name"`
	URL      string `json:"url"`
}

// Embed represents rich metadata card links inside a message.
type Embed struct {
	Title       string `json:"title,omitempty"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
}

// Message represents a normalized Discord message ready for brief compilation.
// Intentionally minimal: contains only fields needed for transcript rendering and LLM briefing.
type Message struct {
	ID          string       `json:"id"`
	Timestamp   time.Time    `json:"timestamp"`
	Author      string       `json:"author"`
	Content     string       `json:"content"`
	ReplyToID   string       `json:"reply_to_id,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Embeds      []Embed      `json:"embeds,omitempty"`
}

// Channel represents channel identity metadata for the briefing prompt.
type Channel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Completer abstracts the LLM Chat Completions boundary for the briefing engine.
type Completer interface {
	Complete(ctx context.Context, messages []llm.Message) (string, error)
}
