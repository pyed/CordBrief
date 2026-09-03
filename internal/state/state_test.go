package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.json")

	s, err := Load(path)
	if err != nil {
		t.Fatalf("expected nil error on missing file, got: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil State struct")
	}
	if !s.LastWindowEnd.IsZero() {
		t.Fatalf("expected zero LastWindowEnd on fresh state, got: %v", s.LastWindowEnd)
	}
}

func TestSaveAndLoad_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	timestamp := time.Date(2026, 9, 3, 12, 30, 0, 0, time.UTC)
	s := &State{
		LastWindowEnd: timestamp,
	}

	if err := Save(path, s); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if !loaded.LastWindowEnd.Equal(timestamp) {
		t.Fatalf("expected timestamp %v, got %v", timestamp, loaded.LastWindowEnd)
	}
}

func TestSave_SafeReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	t1 := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	if err := Save(path, &State{LastWindowEnd: t1}); err != nil {
		t.Fatalf("first Save failed: %v", err)
	}

	t2 := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	if err := Save(path, &State{LastWindowEnd: t2}); err != nil {
		t.Fatalf("second Save failed: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !loaded.LastWindowEnd.Equal(t2) {
		t.Fatalf("expected overwritten timestamp %v, got %v", t2, loaded.LastWindowEnd)
	}

	// Verify no stray .tmp files left in the directory
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("found leftover temp file: %s", e.Name())
		}
	}
}

func TestLoad_StrictParsing(t *testing.T) {
	dir := t.TempDir()

	t.Run("malformed json", func(t *testing.T) {
		path := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(path, []byte("{invalid-json"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil {
			t.Fatal("expected error on malformed json, got nil")
		}
	})

	t.Run("unknown field rejected", func(t *testing.T) {
		path := filepath.Join(dir, "unknown.json")
		badData := `{"last_window_end": "2026-09-03T00:00:00Z", "raw_messages": ["secret"]}`
		if err := os.WriteFile(path, []byte(badData), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil {
			t.Fatal("expected error on unknown field, got nil")
		}
	})
}

func TestSave_NilState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nil.json")
	if err := Save(path, nil); err == nil {
		t.Fatal("expected error when saving nil state, got nil")
	}
}
