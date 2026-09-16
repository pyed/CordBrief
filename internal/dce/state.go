package dce

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const UpdaterStateVersion = 1

// UpdaterState tracks installed, candidate, and rejected DCE versions.
type UpdaterState struct {
	Version          int       `json:"version"`
	ActiveVersion    string    `json:"active_version"`
	ActivePath       string    `json:"active_path"`
	CandidateVersion string    `json:"candidate_version,omitempty"`
	CandidatePath    string    `json:"candidate_path,omitempty"`
	RejectedVersion  string    `json:"rejected_version,omitempty"`
	LastCheck        time.Time `json:"last_check"`
	LastExport       time.Time `json:"last_export,omitempty"`
	PinnedVersion    string    `json:"pinned_version,omitempty"`
}

// LoadUpdaterState reads the updater state file. If missing, it initializes a state pointing
// to the provided bootstrap binary. If corrupt, it fails closed returning the bootstrap state.
func LoadUpdaterState(path, bootstrapPath, bootstrapVersion string) (*UpdaterState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &UpdaterState{
				Version:       UpdaterStateVersion,
				ActiveVersion: strings.TrimSpace(bootstrapVersion),
				ActivePath:    strings.TrimSpace(bootstrapPath),
			}, nil
		}
		return nil, fmt.Errorf("read updater state: %w", err)
	}

	var st UpdaterState
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		// Corrupted state: fail closed to bootstrap path
		return &UpdaterState{
			Version:       UpdaterStateVersion,
			ActiveVersion: strings.TrimSpace(bootstrapVersion),
			ActivePath:    strings.TrimSpace(bootstrapPath),
		}, fmt.Errorf("corrupt updater state (fallback to bootstrap): %w", err)
	}

	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return &UpdaterState{
			Version:       UpdaterStateVersion,
			ActiveVersion: strings.TrimSpace(bootstrapVersion),
			ActivePath:    strings.TrimSpace(bootstrapPath),
		}, fmt.Errorf("unexpected trailing content in updater state")
	}

	if st.ActivePath == "" && bootstrapPath != "" {
		st.ActivePath = bootstrapPath
		st.ActiveVersion = bootstrapVersion
	}

	return &st, nil
}

// SaveUpdaterState atomically writes the updater state to disk.
func SaveUpdaterState(path string, st *UpdaterState) error {
	if st == nil {
		return errors.New("cannot save nil updater state")
	}
	st.Version = UpdaterStateVersion

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal updater state: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create dir for updater state: %w", err)
	}

	TmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp updater state file: %w", err)
	}
	tmpPath := TmpFile.Name()

	cleanedUp := false
	defer func() {
		if !cleanedUp {
			_ = TmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := TmpFile.Write(data); err != nil {
		return fmt.Errorf("write temp updater state: %w", err)
	}
	if err := TmpFile.Sync(); err != nil {
		return fmt.Errorf("sync temp updater state: %w", err)
	}
	if err := TmpFile.Close(); err != nil {
		return fmt.Errorf("close temp updater state: %w", err)
	}

	if err := os.Chmod(tmpPath, 0600); err != nil && runtime.GOOS != "windows" {
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp updater state: %w", err)
	}
	cleanedUp = true

	return nil
}
