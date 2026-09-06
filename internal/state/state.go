package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"cordbrief/internal/durable"
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

// Save persists state using crash-resistant / safe replacement semantics
// via durable.AtomicWriteJSON (write temp, sync, close, rename, parent dir sync).
func Save(path string, s *State) error {
	if s == nil {
		return errors.New("state cannot be nil")
	}

	return durable.AtomicWriteJSON(path, s, 0640)
}

