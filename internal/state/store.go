package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// Store handles durable reads and atomic writes for CordBrief configuration and state.
type Store struct {
	dataDir string
}

// NewStore creates a Store operating within the given data directory.
func NewStore(dataDir string) *Store {
	return &Store{dataDir: dataDir}
}

// DataDir returns the root directory for this store.
func (s *Store) DataDir() string {
	return s.dataDir
}

// ConfigPath returns the full path to config.json.
func (s *Store) ConfigPath() string {
	return filepath.Join(s.dataDir, "config.json")
}

// StatePath returns the full path to state.json.
func (s *Store) StatePath() string {
	return filepath.Join(s.dataDir, "state.json")
}

// LoadConfig reads config.json. If the file does not exist, DefaultConfig is returned.
// Corrupted or invalid files return an error fail-closed.
func (s *Store) LoadConfig() (*Config, error) {
	data, err := os.ReadFile(s.ConfigPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := decodeStrictJSON(data, &cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	if cfg.Channels == nil {
		cfg.Channels = []ChannelConfig{}
	}

	return &cfg, nil
}

// SaveConfig atomically writes config.json after validating in memory.
// If validation fails, existing files are preserved unchanged.
func (s *Store) SaveConfig(cfg *Config) error {
	if cfg == nil {
		return errors.New("cannot save nil config")
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')

	if err := s.atomicWrite(s.ConfigPath(), data); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// LoadState reads state.json. If the file does not exist, an empty v1 State is returned.
// Corrupted or invalid files return an error fail-closed.
func (s *Store) LoadState() (*State, error) {
	data, err := os.ReadFile(s.StatePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return NewEmptyState(), nil
		}
		return nil, fmt.Errorf("read state: %w", err)
	}

	var st State
	if err := decodeStrictJSON(data, &st); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}

	if err := st.Validate(); err != nil {
		return nil, fmt.Errorf("validate state: %w", err)
	}

	if st.Channels == nil {
		st.Channels = make(map[string]ChannelState)
	}

	return &st, nil
}

// SaveState atomically writes state.json after validating in memory.
// If validation fails, existing files are preserved unchanged.
func (s *Store) SaveState(st *State) error {
	if st == nil {
		return errors.New("cannot save nil state")
	}
	if err := st.Validate(); err != nil {
		return fmt.Errorf("validate state: %w", err)
	}

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	data = append(data, '\n')

	if err := s.atomicWrite(s.StatePath(), data); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// decodeStrictJSON unmarshals data into target and rejects any unknown JSON fields.
func decodeStrictJSON(data []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}

	// Verify no extraneous non-whitespace content remains after the JSON document.
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing content after JSON")
	}
	return nil
}

// atomicWrite writes data to a temporary file in the same directory as dst,
// flushes with fsync, closes, and atomically renames over dst.
func (s *Store) atomicWrite(dst string, data []byte) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, filepath.Base(dst)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	cleanedUp := false
	defer func() {
		if !cleanedUp {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("fsync temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Chmod(tmpPath, 0600); err != nil && runtime.GOOS != "windows" {
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	cleanedUp = true

	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}

	return nil
}

// syncDir flushes parent directory entries to disk on platforms that support it.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
