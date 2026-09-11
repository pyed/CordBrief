package journal

import (
	"time"
)

// CurrentSchemaVersion defines the supported exchange contract version.
const CurrentSchemaVersion = 1

// Event represents a normalized Discord Gateway event stored in segmented NDJSON.
type Event struct {
	Version          int          `json:"version"`
	Event            string       `json:"event"`
	MessageID        string       `json:"message_id"`
	GuildID          string       `json:"guild_id"`
	ChannelID        string       `json:"channel_id"`
	Timestamp        time.Time    `json:"timestamp"`
	CapturedAt       time.Time    `json:"captured_at"`
	Author           Author       `json:"author"`
	Content          string       `json:"content"`
	ReplyToMessageID *string      `json:"reply_to_message_id,omitempty"`
	Attachments      []Attachment `json:"attachments,omitempty"`
}

// Author contains identity metadata of the message author.
type Author struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Bot         bool   `json:"bot"`
}

// Attachment contains non-downloaded metadata for message file attachments.
type Attachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

// Cursor defines the exact committed ingestion checkpoint in core-ack.json.
type Cursor struct {
	Version int    `json:"version"`
	Segment uint64 `json:"segment"`
	Offset  int64  `json:"offset"`
}

// Watchlist defines the versioned channel allowlist in watchlist.json.
type Watchlist struct {
	Version    int      `json:"version"`
	Generation int64    `json:"generation"`
	ChannelIDs []string `json:"channel_ids"`
}

// Catalog represents discovered guild and channel metadata in catalog.json.
type Catalog struct {
	Version   int            `json:"version"`
	UpdatedAt time.Time      `json:"updated_at"`
	Guilds    []GuildCatalog `json:"guilds"`
}

// GuildCatalog holds channels visible within a single guild.
type GuildCatalog struct {
	GuildID   string           `json:"guild_id"`
	GuildName string           `json:"guild_name"`
	Channels  []ChannelCatalog `json:"channels"`
}

// ChannelCatalog holds minimal channel metadata for channel selection.
type ChannelCatalog struct {
	ChannelID   string `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	ChannelType string `json:"channel_type"`
}

// CollectorStatus represents the operational telemetry in collector-status.json.
type CollectorStatus struct {
	Version                 int        `json:"version"`
	UpdatedAt               time.Time  `json:"updated_at"`
	Mode                    string     `json:"mode,omitempty"`
	CollectorState          string     `json:"collector_state"`
	DiscordAuthenticated    *bool      `json:"discord_authenticated"`
	CatalogState            string     `json:"catalog_state,omitempty"`
	CatalogUpdatedAt        *time.Time `json:"catalog_updated_at,omitempty"`
	WatchedGeneration       int64      `json:"watched_generation"`
	WatchedChannelCount     int        `json:"watched_channel_count"`
	ActiveSegment           uint64     `json:"active_segment"`
	LastEventAt             *time.Time `json:"last_event_at,omitempty"`
	LastError               *string    `json:"last_error,omitempty"`
	PromptState             *string    `json:"prompt_state,omitempty"`
	ActionRequired          *string    `json:"action_required,omitempty"`
	RecoveryState           string     `json:"recovery_state,omitempty"`
	RecoveryLastAt          *time.Time `json:"recovery_last_at,omitempty"`
	RecoveryPendingChannels int        `json:"recovery_pending_channels,omitempty"`
	RecoveryLastError       *string    `json:"recovery_last_error,omitempty"`
}

// CollectorCommand represents an atomic control command sent from Core to Collector via exchange.
type CollectorCommand struct {
	Version     int       `json:"version"`
	Command     string    `json:"command"`
	RequestID   string    `json:"request_id"`
	RequestedAt time.Time `json:"requested_at"`
}

// CollectorCommandAck represents the acknowledgement written by Collector upon processing a command.
type CollectorCommandAck struct {
	Version   int       `json:"version"`
	RequestID string    `json:"request_id"`
	Command   string    `json:"command"`
	Status    string    `json:"status"`
	AppliedAt time.Time `json:"applied_at"`
	Error     *string   `json:"error"`
}
