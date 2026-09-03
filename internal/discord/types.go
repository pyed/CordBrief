package discord

import "time"

// Discord channel types supported by CordBrief v0.1
const (
	ChannelTypeGuildText         = 0
	ChannelTypeGuildAnnouncement = 5
)

// Channel represents Discord channel metadata needed for configuration and routing.
type Channel struct {
	ID       string `json:"id"`
	GuildID  string `json:"guild_id,omitempty"`
	Name     string `json:"name"`
	Type     int    `json:"type"`
	Position int    `json:"position"`
}

// IsSupported returns true if the channel is GUILD_TEXT or GUILD_ANNOUNCEMENT.
func (c Channel) IsSupported() bool {
	return c.Type == ChannelTypeGuildText || c.Type == ChannelTypeGuildAnnouncement
}

// TypeName returns a human-readable name for supported channel types.
func (c Channel) TypeName() string {
	switch c.Type {
	case ChannelTypeGuildText:
		return "GUILD_TEXT"
	case ChannelTypeGuildAnnouncement:
		return "GUILD_ANNOUNCEMENT"
	default:
		return "UNSUPPORTED"
	}
}

// BotIdentity holds minimal information about the authenticated Discord bot.
type BotIdentity struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	IsBot    bool   `json:"bot"`
}

// HistoryProbeResult captures diagnostic results from probing a channel history endpoint (limit=1).
// Per SPEC.md, probe results never store or expose message text.
type HistoryProbeResult struct {
	Accessible   bool
	MessageCount int
	HasContent   bool
	StatusCode   int
	Error        error
}

// NormalizedMessage represents a Discord message reduced to the exact fields
// needed for digestion, prompt building, and source attribution.
// Per SPEC.md, raw Discord messages are kept only in memory and never persisted.
type NormalizedMessage struct {
	LocalSourceID   string    `json:"local_source_id,omitempty"` // Left unassigned until global merge
	ID              string    `json:"id"`                        // Discord snowflake message ID
	ChannelID       string    `json:"channel_id"`                // Discord snowflake channel ID
	ChannelName     string    `json:"channel_name,omitempty"`    // Channel name if available
	AuthorName      string    `json:"author_name"`               // Display name or username
	AuthorIsBot     bool      `json:"author_is_bot"`             // True if message author is a bot
	Timestamp       time.Time `json:"timestamp"`                 // Message creation time
	Content         string    `json:"content"`                   // Text content
	ReplyToID       string    `json:"reply_to_id,omitempty"`     // Referenced message ID if reply
	AttachmentNames []string  `json:"attachment_names,omitempty"`// File names only; no binary downloading
}
