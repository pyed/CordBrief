package dce

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdaterState(t *testing.T) {
	t.Run("missing file returns bootstrap state", func(t *testing.T) {
		tmpDir := t.TempDir()
		statePath := filepath.Join(tmpDir, "updater.json")

		st, err := LoadUpdaterState(statePath, "/bootstrap/dce", "2.48.0")
		if err != nil {
			t.Fatalf("unexpected error loading missing state: %v", err)
		}
		if st.ActiveVersion != "2.48.0" || st.ActivePath != "/bootstrap/dce" {
			t.Fatalf("expected bootstrap version and path, got version=%q path=%q", st.ActiveVersion, st.ActivePath)
		}
		if st.Version != UpdaterStateVersion {
			t.Fatalf("expected state version %d, got %d", UpdaterStateVersion, st.Version)
		}
	})

	t.Run("save and load round-trip", func(t *testing.T) {
		tmpDir := t.TempDir()
		statePath := filepath.Join(tmpDir, "sub", "updater.json")
		now := time.Now().UTC().Truncate(time.Second)

		orig := &UpdaterState{
			ActiveVersion:    "2.48.0",
			ActivePath:       "/path/to/2.48.0/dce",
			CandidateVersion: "2.49.0",
			CandidatePath:    "/path/to/2.49.0/dce",
			RejectedVersion:  "2.47.0",
			LastCheck:        now,
		}

		if err := SaveUpdaterState(statePath, orig); err != nil {
			t.Fatalf("SaveUpdaterState failed: %v", err)
		}

		loaded, err := LoadUpdaterState(statePath, "/bootstrap/dce", "2.48.0")
		if err != nil {
			t.Fatalf("LoadUpdaterState failed: %v", err)
		}
		if loaded.ActiveVersion != orig.ActiveVersion || loaded.ActivePath != orig.ActivePath {
			t.Errorf("active mismatch: got %v, want %v", loaded, orig)
		}
		if loaded.CandidateVersion != orig.CandidateVersion || loaded.CandidatePath != orig.CandidatePath {
			t.Errorf("candidate mismatch: got %v, want %v", loaded, orig)
		}
		if loaded.RejectedVersion != orig.RejectedVersion {
			t.Errorf("rejected mismatch: got %q, want %q", loaded.RejectedVersion, orig.RejectedVersion)
		}
		if !loaded.LastCheck.Equal(orig.LastCheck) {
			t.Errorf("last check mismatch: got %v, want %v", loaded.LastCheck, orig.LastCheck)
		}
	})

	t.Run("corrupt json fails closed to bootstrap", func(t *testing.T) {
		tmpDir := t.TempDir()
		statePath := filepath.Join(tmpDir, "updater.json")
		if err := os.WriteFile(statePath, []byte(`{invalid-json`), 0600); err != nil {
			t.Fatalf("write corrupt file: %v", err)
		}

		st, err := LoadUpdaterState(statePath, "/bootstrap/dce", "2.48.0")
		if err == nil {
			t.Fatal("expected error on corrupt state, got nil")
		}
		if st.ActiveVersion != "2.48.0" || st.ActivePath != "/bootstrap/dce" {
			t.Fatalf("expected fallback to bootstrap on error, got %+v", st)
		}
	})

	t.Run("trailing junk fails closed to bootstrap", func(t *testing.T) {
		tmpDir := t.TempDir()
		statePath := filepath.Join(tmpDir, "updater.json")
		if err := os.WriteFile(statePath, []byte(`{"version":1,"active_version":"2.48.0","active_path":"/foo"} extra`), 0600); err != nil {
			t.Fatalf("write trailing file: %v", err)
		}

		st, err := LoadUpdaterState(statePath, "/bootstrap/dce", "2.48.0")
		if err == nil {
			t.Fatal("expected error on trailing content, got nil")
		}
		if st.ActiveVersion != "2.48.0" || st.ActivePath != "/bootstrap/dce" {
			t.Fatalf("expected fallback to bootstrap on error, got %+v", st)
		}
	})

	t.Run("unknown fields fail closed to bootstrap", func(t *testing.T) {
		tmpDir := t.TempDir()
		statePath := filepath.Join(tmpDir, "updater.json")
		if err := os.WriteFile(statePath, []byte(`{"version":1,"active_version":"2.48.0","active_path":"/foo","unknown_field":"bar"}`), 0600); err != nil {
			t.Fatalf("write unknown field file: %v", err)
		}

		st, err := LoadUpdaterState(statePath, "/bootstrap/dce", "2.48.0")
		if err == nil {
			t.Fatal("expected error on unknown field, got nil")
		}
		if st.ActiveVersion != "2.48.0" || st.ActivePath != "/bootstrap/dce" {
			t.Fatalf("expected fallback to bootstrap on error, got %+v", st)
		}
	})
}
