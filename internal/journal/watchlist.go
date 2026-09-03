package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultWatchlistFilename is the default filename for the channel allowlist.
const DefaultWatchlistFilename = "watchlist.json"

// NormalizeChannelIDs deduplicates, strips whitespace, removes empty entries, and sorts IDs deterministically.
func NormalizeChannelIDs(raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))

	for _, id := range raw {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; !exists {
			seen[trimmed] = struct{}{}
			out = append(out, trimmed)
		}
	}

	sort.Strings(out)
	return out
}

// Validate checks that the watchlist meets strict schema v1 constraints.
func (w *Watchlist) Validate() error {
	if w == nil {
		return errors.New("watchlist cannot be nil")
	}
	if w.Version != CurrentSchemaVersion {
		return fmt.Errorf("unsupported watchlist version: %d (expected %d)", w.Version, CurrentSchemaVersion)
	}
	if w.Generation < 0 {
		return fmt.Errorf("invalid generation: %d (must be >= 0)", w.Generation)
	}
	if len(w.ChannelIDs) == 0 {
		return errors.New("channel_ids cannot be empty (fail-closed)")
	}
	for i, id := range w.ChannelIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("channel_ids[%d] cannot be empty string", i)
		}
	}
	return nil
}

// WriteWatchlist writes and validates watchlist.json using safe replacement semantics.
func WriteWatchlist(path string, w *Watchlist) error {
	if w == nil {
		return errors.New("watchlist cannot be nil")
	}

	// Normalize IDs before validation and persistence
	w.ChannelIDs = NormalizeChannelIDs(w.ChannelIDs)

	if err := w.Validate(); err != nil {
		return fmt.Errorf("cannot write invalid watchlist: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create watchlist directory: %w", err)
	}

	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal watchlist: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".watchlist-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp watchlist file: %w", err)
	}
	tmpName := tmp.Name()

	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write watchlist temp file: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync watchlist temp file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close watchlist temp file: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("safe replacement rename watchlist file: %w", err)
	}

	cleanup = false
	return nil
}

// ReadWatchlist reads and strictly validates watchlist.json.
// Enforces fail-closed rules: any read/parse/validation failure returns an error.
func ReadWatchlist(path string) (*Watchlist, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open watchlist file: %w", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()

	var w Watchlist
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("strict decode watchlist json: %w", err)
	}

	w.ChannelIDs = NormalizeChannelIDs(w.ChannelIDs)
	if err := w.Validate(); err != nil {
		return nil, fmt.Errorf("validate watchlist: %w", err)
	}

	return &w, nil
}
