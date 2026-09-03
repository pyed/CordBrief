package digest

import (
	"time"

	"cordbrief/internal/journal"
)

// Allowed kinds for structured digest items.
const (
	KindImportant    = "important"
	KindFinding      = "finding"
	KindExperiment   = "experiment"
	KindDisagreement = "disagreement"
	KindQuestion     = "question"
	KindResource     = "resource"
)

// ValidKinds is the set of allowed item kinds.
var ValidKinds = map[string]struct{}{
	KindImportant:    {},
	KindFinding:      {},
	KindExperiment:   {},
	KindDisagreement: {},
	KindQuestion:     {},
	KindResource:     {},
}

// Digest represents the structured digest produced by the LLM.
type Digest struct {
	Title    string `json:"title"`
	Overview string `json:"overview"`
	Items    []Item `json:"items"`
}

// Item represents a single summarized point with local source attributions.
type Item struct {
	Kind           string   `json:"kind"`
	Text           string   `json:"text"`
	SourceIDs      []string `json:"source_ids"`
	ChannelContext string   `json:"channel_context,omitempty"`
}

// SourceMessage represents a normalized message ready for the LLM pipeline with a stable local ID.
type SourceMessage struct {
	SourceID        string               `json:"source_id"` // S000001, S000002...
	GuildID         string               `json:"guild_id"`
	ChannelID       string               `json:"channel_id"`
	MessageID       string               `json:"message_id"`
	Timestamp       time.Time            `json:"timestamp"`
	AuthorName      string               `json:"author_name"`
	AuthorDisplay   string               `json:"author_display"`
	AuthorBot       bool                 `json:"author_bot"`
	Content         string               `json:"content"`
	ReplyToSourceID string               `json:"reply_to_source_id,omitempty"`
	ExternalReplyID string               `json:"external_reply_id,omitempty"`
	Attachments     []journal.Attachment `json:"attachments,omitempty"`
}

// Batch represents a deterministic collection of messages to be summarized.
type Batch struct {
	Version             int                      `json:"version"`
	BatchID             string                   `json:"batch_id"`
	StartCursor         journal.Cursor           `json:"start_cursor"`
	EndCursor           journal.Cursor           `json:"end_cursor"`
	Watermark           journal.Watermark        `json:"watermark"`
	TotalJournalRecords int                      `json:"total_journal_records"`
	IncludedMessages    []SourceMessage          `json:"included_messages"`
	SourceMap           map[string]SourceMessage `json:"source_map"`
}

// Artifact represents the durable on-disk record of a completed digest.
type Artifact struct {
	Version              int            `json:"version"`
	BatchID              string         `json:"batch_id"`
	CreatedAt            time.Time      `json:"created_at"`
	CursorStart          journal.Cursor `json:"cursor_start"`
	CursorEnd            journal.Cursor `json:"cursor_end"`
	InputMessageCount    int            `json:"input_message_count"`
	IncludedMessageCount int            `json:"included_message_count"`
	Provider             string         `json:"provider"`
	Model                string         `json:"model"`
	Digest               *Digest        `json:"digest"`
}
