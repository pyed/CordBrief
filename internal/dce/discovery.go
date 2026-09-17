package dce

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pyed/CordBrief/internal/state"
)

const maxMetadataBytes = 1024 * 1024

// Discover lists servers (guildID empty) or one server's channels. It never
// executes a candidate, reserves an export slot, or writes updater state.
func (m *Manager) Discover(ctx context.Context, guildID string) ([]state.ChannelConfig, error) {
	if !m.opMu.TryLock() {
		return nil, errors.New("DCE is busy; try discovery later")
	}
	defer m.opMu.Unlock()
	snapshot := m.State()
	if snapshot.ActivePath == "" {
		return nil, errors.New("discovery needs an active DCE version; follow by ID and complete a normal brief first")
	}
	client := NewMockClient(snapshot.ActivePath, m.token, m.runner)
	return client.discover(ctx, guildID)
}

func (c *Client) discover(ctx context.Context, guildID string) ([]state.ChannelConfig, error) {
	if !c.IsConfigured() {
		return nil, errors.New("DCE is not configured")
	}
	args := []string{"guilds"}
	if guildID != "" {
		if !state.IsDecimalString(guildID) || len(guildID) > 20 || guildID == "0" {
			return nil, errors.New("invalid server ID")
		}
		// No thread enumeration: DCE can otherwise probe individual channels.
		args = []string{"channels", "-g", guildID, "--include-vc", "false", "--include-threads", "None"}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stdout := cappedBuffer{limit: maxMetadataBytes + 1}
	env := append(commandEnv(), "DISCORD_TOKEN="+c.token)
	if err := c.runner(ctx, c.dcePath, args, env, &stdout, io.Discard); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("DCE metadata command failed; cached data was kept")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if stdout.buf.Len() > maxMetadataBytes {
		return nil, errors.New("DCE metadata exceeds 1 MiB; cached data was kept")
	}
	items, err := parseMetadata(stdout.String())
	if err != nil {
		return nil, err
	}
	if guildID == "" {
		// DCE adds synthetic server 0 for direct messages; this browser is for servers.
		filtered := items[:0]
		for _, item := range items {
			if item.ID != "0" {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	return items, nil
}

// DCE 2.48 (905489b) GetGuildsCommand/GetChannelsCommand emit "ID | name".
// This contains no permission evidence. Names may themselves contain pipes.
func parseMetadata(output string) ([]state.ChannelConfig, error) {
	items := []state.ChannelConfig{}
	seen := make(map[string]bool)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		id, name, ok := strings.Cut(line, " | ")
		id, name = strings.TrimSpace(id), strings.TrimSpace(name)
		if !ok || !state.IsDecimalString(id) || len(id) > 20 || name == "" || len(name) > 1024 || !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) || seen[id] {
			return nil, errors.New("unrecognized DCE metadata; cached data was kept")
		}
		seen[id] = true
		items = append(items, state.ChannelConfig{ID: id, Name: name})
	}
	return items, nil
}
