package state

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// 1. Missing config loads defaults
func TestLoadConfig_MissingFileReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("expected no error loading missing config, got: %v", err)
	}

	def := DefaultConfig()
	if cfg.Version != def.Version {
		t.Errorf("version mismatch: got %d, want %d", cfg.Version, def.Version)
	}
	if cfg.Schedule.Enabled != def.Schedule.Enabled || cfg.Schedule.Time != def.Schedule.Time {
		t.Errorf("schedule mismatch: got %+v, want %+v", cfg.Schedule, def.Schedule)
	}
	if cfg.Timezone != def.Timezone {
		t.Errorf("timezone mismatch: got %s, want %s", cfg.Timezone, def.Timezone)
	}
	if cfg.LLM.BaseURL != def.LLM.BaseURL || cfg.LLM.Model != def.LLM.Model {
		t.Errorf("llm mismatch: got %+v, want %+v", cfg.LLM, def.LLM)
	}
	if len(cfg.Channels) != 0 {
		t.Errorf("expected 0 channels, got %d", len(cfg.Channels))
	}
}

// 2. Missing state loads empty v1 state
func TestLoadState_MissingFileReturnsEmptyV1State(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	st, err := store.LoadState()
	if err != nil {
		t.Fatalf("expected no error loading missing state, got: %v", err)
	}

	if st.Version != 1 {
		t.Errorf("expected version 1, got %d", st.Version)
	}
	if st.Channels == nil {
		t.Fatal("expected non-nil channels map")
	}
	if len(st.Channels) != 0 {
		t.Errorf("expected 0 channels in empty state, got %d", len(st.Channels))
	}
}

// 3. Config save -> load round trip
func TestSaveAndLoadConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	orig := &Config{
		Version: 1,
		Channels: []ChannelConfig{
			{ID: "178281233233608705", Name: "general"},
			{ID: "987654321098765432", Name: "announcements"},
		},
		Schedule: ScheduleConfig{
			Enabled: true,
			Time:    "09:30",
		},
		Timezone: "Asia/Riyadh",
		LLM: LLMConfig{
			BaseURL: "https://api.openai.com/v1",
			Model:   "gpt-4o-mini",
		},
	}

	if err := store.SaveConfig(orig); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	loaded, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if loaded.Version != orig.Version {
		t.Errorf("version mismatch: got %d, want %d", loaded.Version, orig.Version)
	}
	if loaded.Timezone != orig.Timezone {
		t.Errorf("timezone mismatch: got %s, want %s", loaded.Timezone, orig.Timezone)
	}
	if loaded.Schedule != orig.Schedule {
		t.Errorf("schedule mismatch: got %+v, want %+v", loaded.Schedule, orig.Schedule)
	}
	if loaded.LLM != orig.LLM {
		t.Errorf("llm mismatch: got %+v, want %+v", loaded.LLM, orig.LLM)
	}
	if len(loaded.Channels) != len(orig.Channels) {
		t.Fatalf("channel count mismatch: got %d, want %d", len(loaded.Channels), len(orig.Channels))
	}
	for i := range orig.Channels {
		if loaded.Channels[i] != orig.Channels[i] {
			t.Errorf("channel [%d] mismatch: got %+v, want %+v", i, loaded.Channels[i], orig.Channels[i])
		}
	}
}

// 4. State save -> load round trip
func TestSaveAndLoadState_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	orig := &State{
		Version: 1,
		Channels: map[string]ChannelState{
			"178281233233608705": {
				Cursor: Cursor{
					Kind:  CursorKindTimestamp,
					Value: "2026-09-13T00:00:00Z",
				},
				LastSuccessAt: "2026-09-13T05:00:00Z",
				LastError:     "",
			},
			"987654321098765432": {
				Cursor: Cursor{
					Kind:  CursorKindMessageID,
					Value: "1548424596989149195",
				},
				LastSuccessAt: "",
				LastError:     "network timeout connecting to provider",
			},
		},
	}

	if err := store.SaveState(orig); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	loaded, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	if loaded.Version != orig.Version {
		t.Errorf("version mismatch: got %d, want %d", loaded.Version, orig.Version)
	}
	if len(loaded.Channels) != len(orig.Channels) {
		t.Fatalf("channels count mismatch: got %d, want %d", len(loaded.Channels), len(orig.Channels))
	}
	for id, wantCh := range orig.Channels {
		gotCh, exists := loaded.Channels[id]
		if !exists {
			t.Errorf("missing channel %s in loaded state", id)
			continue
		}
		if gotCh != wantCh {
			t.Errorf("channel %s mismatch: got %+v, want %+v", id, gotCh, wantCh)
		}
	}
}

// 5. Timestamp cursor survives round trip
func TestTimestampCursor_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	st := &State{
		Version: 1,
		Channels: map[string]ChannelState{
			"100": {
				Cursor: Cursor{
					Kind:  CursorKindTimestamp,
					Value: "2026-09-13T08:00:00Z",
				},
			},
		},
	}

	if err := store.SaveState(st); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	loaded, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	ch := loaded.Channels["100"]
	if ch.Cursor.Kind != CursorKindTimestamp {
		t.Errorf("expected cursor kind %q, got %q", CursorKindTimestamp, ch.Cursor.Kind)
	}
	if ch.Cursor.Value != "2026-09-13T08:00:00Z" {
		t.Errorf("expected cursor value %q, got %q", "2026-09-13T08:00:00Z", ch.Cursor.Value)
	}
}

// 6. Message ID cursor survives round trip
func TestMessageIDCursor_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	st := &State{
		Version: 1,
		Channels: map[string]ChannelState{
			"100": {
				Cursor: Cursor{
					Kind:  CursorKindMessageID,
					Value: "1548424596989149195",
				},
			},
		},
	}

	if err := store.SaveState(st); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	loaded, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	ch := loaded.Channels["100"]
	if ch.Cursor.Kind != CursorKindMessageID {
		t.Errorf("expected cursor kind %q, got %q", CursorKindMessageID, ch.Cursor.Kind)
	}
	if ch.Cursor.Value != "1548424596989149195" {
		t.Errorf("expected cursor value %q, got %q", "1548424596989149195", ch.Cursor.Value)
	}
}

// 7. Duplicate channel config rejected
func TestValidateConfig_DuplicateChannelRejected(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Channels = []ChannelConfig{
		{ID: "178281233233608705", Name: "general"},
		{ID: "178281233233608705", Name: "general-duplicate"},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error on duplicate channel ID, got nil")
	}
}

// 8. Malformed schedule rejected
func TestValidateConfig_MalformedScheduleRejected(t *testing.T) {
	invalidTimes := []string{
		"25:00",
		"12:60",
		"8:00",
		"08:0",
		"abc",
		"08-00",
		"",
		"24:00",
	}

	for _, badTime := range invalidTimes {
		cfg := DefaultConfig()
		cfg.Schedule.Time = badTime
		if err := cfg.Validate(); err == nil {
			t.Errorf("expected error for schedule time %q, got nil", badTime)
		}
	}

	validTimes := []string{
		"00:00",
		"08:00",
		"12:30",
		"23:59",
	}
	for _, goodTime := range validTimes {
		cfg := DefaultConfig()
		cfg.Schedule.Time = goodTime
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected valid schedule time for %q, got error: %v", goodTime, err)
		}
	}
}

// 9. Malformed timezone rejected
func TestValidateConfig_MalformedTimezoneRejected(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Timezone = "Mars/Phobos"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid timezone, got nil")
	}

	cfg.Timezone = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty timezone, got nil")
	}

	cfg.Timezone = "America/New_York"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid timezone for America/New_York, got: %v", err)
	}
}

// 10. Unsupported config schema rejected
func TestValidateConfig_UnsupportedVersionRejected(t *testing.T) {
	for _, v := range []int{0, 2, -1, 99} {
		cfg := DefaultConfig()
		cfg.Version = v
		if err := cfg.Validate(); err == nil {
			t.Errorf("expected error for config version %d, got nil", v)
		}
	}
}

// 11. Unsupported state schema rejected
func TestValidateState_UnsupportedVersionRejected(t *testing.T) {
	for _, v := range []int{0, 2, -1, 99} {
		st := NewEmptyState()
		st.Version = v
		if err := st.Validate(); err == nil {
			t.Errorf("expected error for state version %d, got nil", v)
		}
	}
}

// 12. Unknown cursor kind rejected
func TestValidateState_UnknownCursorKindRejected(t *testing.T) {
	st := &State{
		Version: 1,
		Channels: map[string]ChannelState{
			"100": {
				Cursor: Cursor{
					Kind:  "snowflake",
					Value: "12345",
				},
			},
		},
	}

	if err := st.Validate(); err == nil {
		t.Fatal("expected error for unknown cursor kind, got nil")
	}
}

// 13. Malformed timestamp cursor rejected
func TestValidateState_MalformedTimestampCursorRejected(t *testing.T) {
	badTimestamps := []string{
		"not-a-date",
		"2026/09/13",
		"2026-09-13",
		"13-09-2026T00:00:00Z",
		"",
	}

	for _, badTS := range badTimestamps {
		st := &State{
			Version: 1,
			Channels: map[string]ChannelState{
				"100": {
					Cursor: Cursor{
						Kind:  CursorKindTimestamp,
						Value: badTS,
					},
				},
			},
		}
		if err := st.Validate(); err == nil {
			t.Errorf("expected error for invalid timestamp cursor %q, got nil", badTS)
		}
	}
}

// 14. Corrupted JSON returns error rather than silently resetting
func TestLoadConfigAndState_CorruptedJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Corrupted config.json
	if err := os.WriteFile(store.ConfigPath(), []byte(`{ invalid config JSON`), 0600); err != nil {
		t.Fatalf("failed to write corrupted config: %v", err)
	}
	if _, err := store.LoadConfig(); err == nil {
		t.Fatal("expected error loading corrupted config.json, got nil")
	}

	// Corrupted state.json
	if err := os.WriteFile(store.StatePath(), []byte(`{ "version": 1, channels: bad }`), 0600); err != nil {
		t.Fatalf("failed to write corrupted state: %v", err)
	}
	if _, err := store.LoadState(); err == nil {
		t.Fatal("expected error loading corrupted state.json, got nil")
	}
}

// 15. Unknown JSON fields behavior is explicitly tested according to chosen policy (disallow)
func TestLoadConfigAndState_UnknownJSONFieldsRejected(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Unknown field in config
	cfgJSON := `{
  "version": 1,
  "channels": [],
  "schedule": {"enabled": true, "time": "08:00"},
  "timezone": "UTC",
  "llm": {"base_url": "https://example.com", "model": "test"},
  "extra_bogus_field": "disallowed"
}`
	if err := os.WriteFile(store.ConfigPath(), []byte(cfgJSON), 0600); err != nil {
		t.Fatalf("failed to write config fixture: %v", err)
	}
	if _, err := store.LoadConfig(); err == nil {
		t.Fatal("expected error loading config with unknown field, got nil")
	}

	// Unknown field in state
	stJSON := `{
  "version": 1,
  "channels": {},
  "unexpected_key": 42
}`
	if err := os.WriteFile(store.StatePath(), []byte(stJSON), 0600); err != nil {
		t.Fatalf("failed to write state fixture: %v", err)
	}
	if _, err := store.LoadState(); err == nil {
		t.Fatal("expected error loading state with unknown field, got nil")
	}
}

// 16. Saved JSON ends with newline
func TestSave_FilesEndWithNewline(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if err := store.SaveConfig(DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}
	cfgBytes, err := os.ReadFile(store.ConfigPath())
	if err != nil {
		t.Fatalf("failed to read config.json: %v", err)
	}
	if !bytes.HasSuffix(cfgBytes, []byte("\n")) {
		t.Error("config.json does not end with a newline")
	}

	if err := store.SaveState(NewEmptyState()); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}
	stBytes, err := os.ReadFile(store.StatePath())
	if err != nil {
		t.Fatalf("failed to read state.json: %v", err)
	}
	if !bytes.HasSuffix(stBytes, []byte("\n")) {
		t.Error("state.json does not end with a newline")
	}
}

// 17. Saving creates parent data directory when absent
func TestSave_CreatesParentDataDirWhenAbsent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data", "dir")
	store := NewStore(dir)

	if err := store.SaveConfig(DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig failed on non-existent directory: %v", err)
	}
	if err := store.SaveState(NewEmptyState()); err != nil {
		t.Fatalf("SaveState failed on non-existent directory: %v", err)
	}

	if _, err := os.Stat(store.ConfigPath()); err != nil {
		t.Errorf("expected config.json to exist: %v", err)
	}
	if _, err := os.Stat(store.StatePath()); err != nil {
		t.Errorf("expected state.json to exist: %v", err)
	}
}

// 18. Repeated save cleanly replaces previous contents
func TestSave_RepeatedSaveCleanlyReplacesPreviousContents(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	cfg1 := DefaultConfig()
	cfg1.Timezone = "America/Chicago"
	if err := store.SaveConfig(cfg1); err != nil {
		t.Fatalf("first SaveConfig failed: %v", err)
	}

	cfg2 := DefaultConfig()
	cfg2.Timezone = "Europe/London"
	cfg2.Channels = []ChannelConfig{{ID: "111", Name: "first"}}
	if err := store.SaveConfig(cfg2); err != nil {
		t.Fatalf("second SaveConfig failed: %v", err)
	}

	loadedCfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if loadedCfg.Timezone != "Europe/London" {
		t.Errorf("expected timezone Europe/London, got %s", loadedCfg.Timezone)
	}
	if len(loadedCfg.Channels) != 1 || loadedCfg.Channels[0].ID != "111" {
		t.Errorf("expected 1 channel '111', got %+v", loadedCfg.Channels)
	}

	// Verify no temporary files were left behind
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".tmp" {
			t.Errorf("stray temporary file found: %s", entry.Name())
		}
	}
}

// 19. State/channel IDs larger than signed 64-bit remain intact as strings
func TestSnowflakeIDs_LargerThanSigned64BitRemainIntact(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// 18446744073709551615 = 2^64 - 1 (overflows signed int64 9223372036854775807)
	largeChannelID := "18446744073709551615"
	largeMessageID := "18446744073709551614"

	// Config round-trip
	cfg := DefaultConfig()
	cfg.Channels = []ChannelConfig{
		{ID: largeChannelID, Name: "large-id-channel"},
	}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig with large ID failed: %v", err)
	}

	loadedCfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if loadedCfg.Channels[0].ID != largeChannelID {
		t.Errorf("config channel ID truncated or modified: got %s, want %s", loadedCfg.Channels[0].ID, largeChannelID)
	}

	// State round-trip
	st := &State{
		Version: 1,
		Channels: map[string]ChannelState{
			largeChannelID: {
				Cursor: Cursor{
					Kind:  CursorKindMessageID,
					Value: largeMessageID,
				},
				LastSuccessAt: "2026-09-13T08:00:00Z",
			},
		},
	}
	if err := store.SaveState(st); err != nil {
		t.Fatalf("SaveState with large ID failed: %v", err)
	}

	loadedSt, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	ch, exists := loadedSt.Channels[largeChannelID]
	if !exists {
		t.Fatalf("channel %s missing from loaded state", largeChannelID)
	}
	if ch.Cursor.Value != largeMessageID {
		t.Errorf("cursor value truncated or modified: got %s, want %s", ch.Cursor.Value, largeMessageID)
	}
}

// 20. Failed validation does not overwrite an existing valid destination file
func TestFailedValidation_PreservesExistingFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// 20a: Config
	validCfg := DefaultConfig()
	validCfg.Timezone = "Europe/Paris"
	if err := store.SaveConfig(validCfg); err != nil {
		t.Fatalf("initial valid SaveConfig failed: %v", err)
	}

	// Attempt saving invalid config
	invalidCfg := DefaultConfig()
	invalidCfg.Timezone = "Invalid/Timezone"
	if err := store.SaveConfig(invalidCfg); err == nil {
		t.Fatal("expected error saving invalid config, got nil")
	}

	// Verify previous file unchanged
	currentCfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("failed to load config after rejected save: %v", err)
	}
	if currentCfg.Timezone != "Europe/Paris" {
		t.Errorf("config was overwritten! expected timezone Europe/Paris, got %s", currentCfg.Timezone)
	}

	// 20b: State
	validSt := &State{
		Version: 1,
		Channels: map[string]ChannelState{
			"12345": {
				Cursor: Cursor{
					Kind:  CursorKindMessageID,
					Value: "99999",
				},
			},
		},
	}
	if err := store.SaveState(validSt); err != nil {
		t.Fatalf("initial valid SaveState failed: %v", err)
	}

	// Attempt saving invalid state
	invalidSt := &State{
		Version: 1,
		Channels: map[string]ChannelState{
			"12345": {
				Cursor: Cursor{
					Kind:  "unknown_kind",
					Value: "99999",
				},
			},
		},
	}
	if err := store.SaveState(invalidSt); err == nil {
		t.Fatal("expected error saving invalid state, got nil")
	}

	// Verify previous state unchanged
	currentSt, err := store.LoadState()
	if err != nil {
		t.Fatalf("failed to load state after rejected save: %v", err)
	}
	ch, exists := currentSt.Channels["12345"]
	if !exists {
		t.Fatal("channel 12345 missing after rejected save")
	}
	if ch.Cursor.Kind != CursorKindMessageID || ch.Cursor.Value != "99999" {
		t.Errorf("state was overwritten! got %+v", ch)
	}
}

// Additional validation checks
func TestValidation_EdgeCases(t *testing.T) {
	t.Run("nil config save returns error", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := store.SaveConfig(nil); err == nil {
			t.Error("expected error saving nil config")
		}
	})

	t.Run("nil state save returns error", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := store.SaveState(nil); err == nil {
			t.Error("expected error saving nil state")
		}
	})

	t.Run("config channel ID empty", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Channels = []ChannelConfig{{ID: "", Name: "empty"}}
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for empty channel id")
		}
	})

	t.Run("config channel ID non-decimal", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Channels = []ChannelConfig{{ID: "abc123", Name: "letters"}}
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for non-decimal channel id")
		}
	})

	t.Run("config empty LLM fields", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.LLM.BaseURL = ""
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for empty LLM base_url")
		}

		cfg = DefaultConfig()
		cfg.LLM.Model = "  "
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for whitespace LLM model")
		}
	})

	t.Run("state non-decimal channel ID", func(t *testing.T) {
		st := NewEmptyState()
		st.Channels["ch-123"] = ChannelState{
			Cursor: Cursor{Kind: CursorKindMessageID, Value: "123"},
		}
		if err := st.Validate(); err == nil {
			t.Error("expected error for non-decimal channel key in state")
		}
	})

	t.Run("state empty cursor value", func(t *testing.T) {
		st := NewEmptyState()
		st.Channels["123"] = ChannelState{
			Cursor: Cursor{Kind: CursorKindMessageID, Value: ""},
		}
		if err := st.Validate(); err == nil {
			t.Error("expected error for empty cursor value")
		}
	})

	t.Run("state non-decimal message_id cursor value", func(t *testing.T) {
		st := NewEmptyState()
		st.Channels["123"] = ChannelState{
			Cursor: Cursor{Kind: CursorKindMessageID, Value: "msg-123"},
		}
		if err := st.Validate(); err == nil {
			t.Error("expected error for non-decimal message_id cursor value")
		}
	})

	t.Run("state malformed last_success_at", func(t *testing.T) {
		st := NewEmptyState()
		st.Channels["123"] = ChannelState{
			Cursor:        Cursor{Kind: CursorKindMessageID, Value: "123"},
			LastSuccessAt: "not-a-timestamp",
		}
		if err := st.Validate(); err == nil {
			t.Error("expected error for malformed last_success_at")
		}
	})

	t.Run("trailing junk after valid JSON", func(t *testing.T) {
		dir := t.TempDir()
		store := NewStore(dir)
		_ = os.WriteFile(store.ConfigPath(), []byte(`{"version": 1} extra junk`), 0600)
		if _, err := store.LoadConfig(); err == nil {
			t.Error("expected error for trailing content after JSON")
		}
	})
}
