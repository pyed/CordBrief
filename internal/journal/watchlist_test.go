package journal

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWatchlist_NormalizeChannelIDs(t *testing.T) {
	raw := []string{"  200  ", "100", "", "  ", "200", "300", "100"}
	want := []string{"100", "200", "300"}
	got := NormalizeChannelIDs(raw)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeChannelIDs mismatch: got %v, want %v", got, want)
	}
}

func TestWatchlist_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	wlPath := filepath.Join(tmpDir, "watchlist.json")

	w := &Watchlist{
		Version:    CurrentSchemaVersion,
		Generation: 7,
		ChannelIDs: []string{"channel_b", "channel_a", "channel_b"},
	}

	if err := WriteWatchlist(wlPath, w); err != nil {
		t.Fatalf("failed to write watchlist: %v", err)
	}

	loaded, err := ReadWatchlist(wlPath)
	if err != nil {
		t.Fatalf("failed to read watchlist: %v", err)
	}

	if loaded.Version != CurrentSchemaVersion || loaded.Generation != 7 {
		t.Fatalf("unexpected metadata: %+v", loaded)
	}

	wantChannels := []string{"channel_a", "channel_b"}
	if !reflect.DeepEqual(loaded.ChannelIDs, wantChannels) {
		t.Fatalf("channels mismatch: got %v, want %v", loaded.ChannelIDs, wantChannels)
	}
}

func TestWatchlist_FailClosedValidation(t *testing.T) {
	tmpDir := t.TempDir()
	wlPath := filepath.Join(tmpDir, "watchlist.json")

	// 1. Missing file -> fail closed
	if _, err := ReadWatchlist(filepath.Join(tmpDir, "nonexistent.json")); err == nil {
		t.Fatal("expected error on missing watchlist file, got nil")
	}

	// 2. Malformed JSON -> fail closed
	if err := os.WriteFile(wlPath, []byte(`{ not valid json`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWatchlist(wlPath); err == nil {
		t.Fatal("expected error on malformed JSON, got nil")
	}

	// 3. Unsupported version -> fail closed
	v2 := `{"version": 2, "generation": 1, "channel_ids": ["123"]}`
	if err := os.WriteFile(wlPath, []byte(v2), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWatchlist(wlPath); err == nil {
		t.Fatal("expected error on version 2, got nil")
	}

	// 4. Negative generation -> fail closed
	negGen := `{"version": 1, "generation": -1, "channel_ids": ["123"]}`
	if err := os.WriteFile(wlPath, []byte(negGen), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWatchlist(wlPath); err == nil {
		t.Fatal("expected error on negative generation, got nil")
	}

	// 5. Empty channel_ids array -> fail closed
	empty := `{"version": 1, "generation": 1, "channel_ids": []}`
	if err := os.WriteFile(wlPath, []byte(empty), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWatchlist(wlPath); err == nil {
		t.Fatal("expected error on empty channel_ids, got nil")
	}
}
