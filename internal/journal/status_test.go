package journal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatus_ReadCollectorStatus(t *testing.T) {
	tmpDir := t.TempDir()
	statusPath := filepath.Join(tmpDir, "collector-status.json")

	data := `{
  "version": 1,
  "updated_at": "2026-09-03T12:00:00Z",
  "collector_state": "running",
  "discord_authenticated": true,
  "catalog_state": "ready",
  "catalog_updated_at": "2026-09-03T12:00:00Z",
  "watched_generation": 42,
  "watched_channel_count": 2,
  "active_segment": 3,
  "recovery_state": "ready",
  "recovery_last_at": "2026-09-03T12:00:00Z",
  "recovery_pending_channels": 0,
  "recovery_last_error": null
}`
	if err := os.WriteFile(statusPath, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	st, err := ReadCollectorStatus(statusPath)
	if err != nil {
		t.Fatalf("ReadCollectorStatus error: %v", err)
	}

	if st.Version != 1 || st.CollectorState != "running" || st.DiscordAuthenticated == nil || !*st.DiscordAuthenticated || st.CatalogState != "ready" || st.CatalogUpdatedAt == nil || st.WatchedGeneration != 42 || st.ActiveSegment != 3 || st.RecoveryState != "ready" || st.RecoveryLastAt == nil || st.RecoveryPendingChannels != 0 {
		t.Fatalf("unexpected collector status values: %+v", st)
	}

	// Unsupported version
	badVersion := `{"version": 99, "updated_at": "2026-09-03T12:00:00Z", "collector_state": "running", "discord_authenticated": true, "watched_generation": 1, "watched_channel_count": 1, "active_segment": 1}`
	_ = os.WriteFile(statusPath, []byte(badVersion), 0644)
	if _, err := ReadCollectorStatus(statusPath); err == nil {
		t.Fatal("expected error on unsupported version, got nil")
	}
}

func TestStatus_ReadCatalog(t *testing.T) {
	tmpDir := t.TempDir()
	catalogPath := filepath.Join(tmpDir, "catalog.json")

	data := `{
  "version": 1,
  "updated_at": "2026-09-03T12:00:00Z",
  "guilds": [
    {
      "guild_id": "g1",
      "guild_name": "Test Guild",
      "channels": [
        {
          "channel_id": "c1",
          "channel_name": "general",
          "channel_type": "text"
        }
      ]
    }
  ]
}`
	if err := os.WriteFile(catalogPath, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cat, err := ReadCatalog(catalogPath)
	if err != nil {
		t.Fatalf("ReadCatalog error: %v", err)
	}

	if cat.Version != 1 || len(cat.Guilds) != 1 || cat.Guilds[0].GuildName != "Test Guild" {
		t.Fatalf("unexpected catalog values: %+v", cat)
	}
}
