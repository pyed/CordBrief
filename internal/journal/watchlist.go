package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"cordbrief/internal/durable"
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

// Validate strictly enforces fail-closed invariant: Watchlist must never be empty.
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
	return SaveWatchlist(path, w)
}

// SaveWatchlist writes watchlist.json using crash-resistant safe replacement
// via durable.AtomicWriteJSON (write temp, fsync, close, rename, parent dir fsync).
func SaveWatchlist(path string, w *Watchlist) error {
	if w == nil {
		return errors.New("cannot write nil watchlist")
	}
	w.ChannelIDs = NormalizeChannelIDs(w.ChannelIDs)

	if err := w.Validate(); err != nil {
		return fmt.Errorf("cannot write invalid watchlist: %w", err)
	}

	return durable.AtomicWriteJSON(path, w, 0644)
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
