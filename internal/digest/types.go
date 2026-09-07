package digest

import (
	"fmt"
	"strings"
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

// TriggerInfo records whether the digest was initiated manually or by the daily scheduler.
type TriggerInfo struct {
	Type   string `json:"type"`              // "scheduled" or "manual"
	SlotID string `json:"slot_id,omitempty"` // e.g. "Asia/Riyadh/2026-09-04/08:00"
}

// IsValidSnowflake validates that s is a canonical Discord decimal snowflake ID.
// Rejects empty strings, strings over 32 characters, whitespace, non-digits, and path traversal characters.
func IsValidSnowflake(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// SourceRef represents the minimal identity metadata required to construct a Discord jump link.
// It contains strictly NO message content, author text, tokens, attachments, or journal records.
type SourceRef struct {
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
	MessageID string `json:"message_id"`
}

// JumpLink returns the canonical Discord web jump link for the source reference.
// Returns an empty string if any of GuildID, ChannelID, or MessageID are not valid decimal snowflakes.
func (r SourceRef) JumpLink() string {
	g := strings.TrimSpace(r.GuildID)
	c := strings.TrimSpace(r.ChannelID)
	msg := strings.TrimSpace(r.MessageID)
	if !IsValidSnowflake(g) || !IsValidSnowflake(c) || !IsValidSnowflake(msg) {
		return ""
	}
	return fmt.Sprintf("https://discord.com/channels/%s/%s/%s", g, c, msg)
}

// DeliveryRequest snapshots the exact intended delivery target for a transaction.
// Contains destination identity only; contains strictly NO credentials or tokens.
type DeliveryRequest struct {
	Provider  string `json:"provider"`             // e.g. "telegram"
	ChatID    string `json:"chat_id"`              // destination chat ID
	ChatLabel string `json:"chat_label,omitempty"` // optional non-sensitive destination label
}

// Artifact represents the durable on-disk record of a completed digest.
type Artifact struct {
	Version              int                  `json:"version"`
	BatchID              string               `json:"batch_id"`
	CreatedAt            time.Time            `json:"created_at"`
	CursorStart          journal.Cursor       `json:"cursor_start"`
	CursorEnd            journal.Cursor       `json:"cursor_end"`
	InputMessageCount    int                  `json:"input_message_count"`
	IncludedMessageCount int                  `json:"included_message_count"`
	Provider             string               `json:"provider"`
	Model                string               `json:"model"`
	Trigger              *TriggerInfo         `json:"trigger,omitempty"`
	DeliveryRequest      *DeliveryRequest     `json:"delivery_request,omitempty"`
	SourceRefs           map[string]SourceRef `json:"source_refs,omitempty"`
	Digest               *Digest              `json:"digest"`
}
