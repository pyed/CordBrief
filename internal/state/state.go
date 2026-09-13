package state

import (
	"errors"
	"fmt"
	"time"
)

// Allowed cursor kinds.
const (
	CursorKindTimestamp = "timestamp"
	CursorKindMessageID = "message_id"
)

// Cursor represents an independent channel position boundary.
type Cursor struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// ChannelState tracks the operational state of a single followed channel.
type ChannelState struct {
	Cursor        Cursor `json:"cursor"`
	LastSuccessAt string `json:"last_success_at"`
	LastError     string `json:"last_error"`
}

// State represents durable operational state stored in state.json.
type State struct {
	Version  int                     `json:"version"`
	Channels map[string]ChannelState `json:"channels"`
}

// NewEmptyState returns a clean version 1 operational state.
func NewEmptyState() *State {
	return &State{
		Version:  1,
		Channels: make(map[string]ChannelState),
	}
}

// Validate ensures all state fields satisfy required invariants.
func (s *State) Validate() error {
	if s.Version != 1 {
		return fmt.Errorf("unsupported state version: %d (expected 1)", s.Version)
	}

	for channelID, ch := range s.Channels {
		if channelID == "" {
			return errors.New("channel id cannot be empty")
		}
		if !isDecimalString(channelID) {
			return fmt.Errorf("channel id %q must contain only decimal digits", channelID)
		}

		if ch.Cursor.Kind != CursorKindTimestamp && ch.Cursor.Kind != CursorKindMessageID {
			return fmt.Errorf("channel %s: unknown cursor kind %q (must be %q or %q)",
				channelID, ch.Cursor.Kind, CursorKindTimestamp, CursorKindMessageID)
		}
		if ch.Cursor.Value == "" {
			return fmt.Errorf("channel %s: cursor value cannot be empty", channelID)
		}

		switch ch.Cursor.Kind {
		case CursorKindTimestamp:
			if _, err := parseRFC3339(ch.Cursor.Value); err != nil {
				return fmt.Errorf("channel %s: invalid timestamp cursor %q: %w", channelID, ch.Cursor.Value, err)
			}
		case CursorKindMessageID:
			if !isDecimalString(ch.Cursor.Value) {
				return fmt.Errorf("channel %s: invalid message_id cursor %q: must contain only decimal digits", channelID, ch.Cursor.Value)
			}
		}

		if ch.LastSuccessAt != "" {
			if _, err := parseRFC3339(ch.LastSuccessAt); err != nil {
				return fmt.Errorf("channel %s: invalid last_success_at %q: %w", channelID, ch.LastSuccessAt, err)
			}
		}
	}

	return nil
}

// isDecimalString returns true if s is non-empty and consists only of digits '0'-'9'.
// Discord snowflakes and channel IDs are treated as decimal strings without integer
// parsing to prevent overflow or truncation of signed 64-bit integers.
func isDecimalString(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseRFC3339 parses timestamps formatted in either standard RFC3339 or RFC3339Nano.
func parseRFC3339(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}
