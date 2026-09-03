package catalog

import (
	"errors"
	"time"
)

var (
	ErrCatalogNotFound    = errors.New("catalog.json not found")
	ErrInvalidCatalog     = errors.New("invalid catalog schema")
	ErrUnsupportedVersion = errors.New("unsupported catalog version (expected version 1)")
)

// Channel represents text-capable channel selection metadata.
type Channel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type int    `json:"type"`
}

// Guild represents a Discord server containing channels.
type Guild struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Channels []Channel `json:"channels"`
}

// Catalog represents the complete published Discord server and channel catalog.
type Catalog struct {
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
	Guilds    []Guild   `json:"guilds"`
}

// FindChannel searches all guilds for a channel by ID.
func (c *Catalog) FindChannel(channelID string) (Channel, Guild, bool) {
	if c == nil {
		return Channel{}, Guild{}, false
	}
	for _, g := range c.Guilds {
		for _, ch := range g.Channels {
			if ch.ID == channelID {
				return ch, g, true
			}
		}
	}
	return Channel{}, Guild{}, false
}

// AllChannelIDs returns a set of all channel IDs present in the catalog.
func (c *Catalog) AllChannelIDs() map[string]bool {
	set := make(map[string]bool)
	if c == nil {
		return set
	}
	for _, g := range c.Guilds {
		for _, ch := range g.Channels {
			set[ch.ID] = true
		}
	}
	return set
}

// ValidateChannelIDs divides an input slice into valid channel IDs (present in catalog)
// and invalid channel IDs (not found in catalog).
func (c *Catalog) ValidateChannelIDs(ids []string) (valid []string, invalid []string) {
	set := c.AllChannelIDs()
	for _, id := range ids {
		if set[id] {
			valid = append(valid, id)
		} else {
			invalid = append(invalid, id)
		}
	}
	return valid, invalid
}
