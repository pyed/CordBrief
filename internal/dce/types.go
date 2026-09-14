package dce

import (
	"time"

	"github.com/pyed/CordBrief/internal/state"
)

// Author represents normalized Discord message author information.
type Author struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Nickname string `json:"nickname,omitempty"`
	IsBot    bool   `json:"is_bot"`
}

// DisplayName returns Nickname if present, otherwise Name.
func (a Author) DisplayName() string {
	if a.Nickname != "" {
		return a.Nickname
	}
	return a.Name
}

// Attachment represents a file or image attached to a message.
type Attachment struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	FileName string `json:"file_name"`
	Bytes    int64  `json:"bytes"`
}

// Embed represents linked or embedded card content.
type Embed struct {
	Title       string `json:"title,omitempty"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
}

// ReplyRef points to the message being replied to, if any.
type ReplyRef struct {
	MessageID string `json:"message_id"`
	ChannelID string `json:"channel_id,omitempty"`
	GuildID   string `json:"guild_id,omitempty"`
}

// Message represents a normalized Discord message in CordBrief.
type Message struct {
	ID          string       `json:"id"`
	Timestamp   time.Time    `json:"timestamp"`
	Content     string       `json:"content"`
	Author      Author       `json:"author"`
	ReplyTo     *ReplyRef    `json:"reply_to,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Embeds      []Embed      `json:"embeds,omitempty"`
}

// ChannelInfo holds identity metadata for an exported Discord channel.
type ChannelInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Category string `json:"category,omitempty"`
}

// GuildInfo holds identity metadata for the server containing an exported channel.
type GuildInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ExportRequest specifies collection boundaries for a single Discord channel.
type ExportRequest struct {
	ChannelID string
	After     state.Cursor
	Before    time.Time
	OutputDir string
}

// ExportResult contains parsed messages and collection metadata.
type ExportResult struct {
	Guild        GuildInfo
	Channel      ChannelInfo
	Messages     []Message
	MaxMessageID string
}
