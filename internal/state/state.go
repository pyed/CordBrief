package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultPath is the predictable v0.1 state file location.
const DefaultPath = "cordbrief-state.json"

// State holds operational metadata persisted between digest runs.
// Per SPEC.md, LastWindowEnd records the end boundary of the last successfully delivered
// digest window (NOT the wall-clock time when delivery completed), preventing message loss across runs.
// Raw messages, prompts, model inputs, and summaries must NEVER be persisted.
type State struct {
	LastWindowEnd time.Time `json:"last_window_end"`
}

// Load reads and strictly parses the state file from disk.
// If the file does not exist, an empty State is returned with nil error to signify first run.
func Load(path string) (*State, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Clean missing-file handling: first run has no prior state
			return &State{}, nil
		}
		return nil, fmt.Errorf("open state file: %w", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()

	var s State
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("strict decode state json: %w", err)
	}

	return &s, nil
}

// Save persists state using crash-resistant / safe replacement semantics:
// it writes to a temporary file in the same directory, flushes (Sync), closes,
// and renames over the destination file.
// Note: While POSIX renames within the same filesystem are atomic, Go's os.Rename
// does not guarantee universal atomicity on non-Unix platforms (e.g. Windows NTFS).
func Save(path string, s *State) error {
	if s == nil {
		return errors.New("state cannot be nil")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	data = append(data, '\n')

	// Create temp file in the same directory to guarantee same filesystem/volume
	tmp, err := os.CreateTemp(dir, "cordbrief-state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
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
		return fmt.Errorf("write state to temp file: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync state temp file: %w", err)
	}

	// Must close file before renaming (especially required on Windows)
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp state file: %w", err)
	}

	// ponytail: os.Rename provides crash-resistant replacement; on Windows NTFS rename replaces existing file
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("safe replacement rename state file: %w", err)
	}

	cleanup = false
	return nil
}
