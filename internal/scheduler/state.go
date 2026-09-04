package scheduler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
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

// SaveState writes scheduler-state.json atomically using temp file write + sync + rename.
func SaveState(dataDir string, s *State) error {
	if s == nil {
		return fmt.Errorf("scheduler state cannot be nil")
	}
	s.Version = 1

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating data directory for scheduler state: %w", err)
	}

	statePath := filepath.Join(dataDir, DefaultStateFilename)
	tmpPath := fmt.Sprintf("%s.tmp.%d", statePath, time.Now().UnixNano())

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling scheduler state: %w", err)
	}
	data = append(data, '\n')

	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("creating tmp scheduler state: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("writing tmp scheduler state: %w", err)
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("syncing tmp scheduler state: %w", err)
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing tmp scheduler state: %w", err)
	}

	if err := os.Rename(tmpPath, statePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming scheduler state to destination: %w", err)
	}

	return nil
}
