package scheduler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"cordbrief/internal/durable"
)

// LoadState reads scheduler-state.json from dataDir. Returns default state if file does not exist.
func LoadState(dataDir string) (*State, error) {
	statePath := filepath.Join(dataDir, DefaultStateFilename)
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{Version: 1}, nil
		}
		return nil, fmt.Errorf("reading scheduler state %s: %w", statePath, err)
	}

	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parsing scheduler state %s: %w", statePath, err)
	}

	if st.Version != 1 {
		return nil, fmt.Errorf("unsupported scheduler state version %d (expected 1)", st.Version)
	}

	return &st, nil
}

// SaveState writes scheduler-state.json atomically using durable.AtomicWriteJSON.
func SaveState(dataDir string, s *State) error {
	if s == nil {
		return fmt.Errorf("scheduler state cannot be nil")
	}
	s.Version = 1

	statePath := filepath.Join(dataDir, DefaultStateFilename)
	return durable.AtomicWriteJSON(statePath, s, 0644)
}

