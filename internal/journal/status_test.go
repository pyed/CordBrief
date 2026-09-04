package journal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestCollectorStatus_Freshness(t *testing.T) {
	now := time.Now().UTC()

	// 1. Nil status
	var nilStat *CollectorStatus
	if nilStat.IsFresh(now, 30*time.Second) {
		t.Error("nil status should not be fresh")
	}

	// 2. Fresh status (10s old)
	freshStat := &CollectorStatus{
		UpdatedAt: now.Add(-10 * time.Second),
	}
	if !freshStat.IsFresh(now, 30*time.Second) {
		t.Error("expected status updated 10s ago to be fresh for 30s threshold")
	}

	// 3. Stale status (45s old)
	staleStat := &CollectorStatus{
		UpdatedAt: now.Add(-45 * time.Second),
	}
	if staleStat.IsFresh(now, 30*time.Second) {
		t.Error("expected status updated 45s ago to be stale for 30s threshold")
	}

	// 4. Minor future clock skew (3s in future)
	futureStat := &CollectorStatus{
		UpdatedAt: now.Add(3 * time.Second),
	}
	if !futureStat.IsFresh(now, 30*time.Second) {
		t.Error("expected 3s clock skew to be tolerated")
	}

	// 5. Excessive future clock skew (20s in future)
	excessiveFutureStat := &CollectorStatus{
		UpdatedAt: now.Add(20 * time.Second),
	}
	if excessiveFutureStat.IsFresh(now, 30*time.Second) {
		t.Error("expected 20s future clock skew to be rejected as not fresh")
	}
}

func TestCollectorCommand_WriteAndReadAck(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Write command
	cmd := CollectorCommand{
		Version:     1,
		Command:     "enter_reauth",
		RequestID:   "req-12345",
		RequestedAt: time.Now().UTC(),
	}
	if err := WriteCollectorCommand(tmpDir, cmd); err != nil {
		t.Fatalf("WriteCollectorCommand failed: %v", err)
	}

	// Verify command written
	cmdPath := filepath.Join(tmpDir, DefaultCommandFilename)
	data, err := os.ReadFile(cmdPath)
	if err != nil {
		t.Fatalf("failed to read command file: %v", err)
	}
	if !strings.Contains(string(data), `"command": "enter_reauth"`) {
		t.Errorf("command file does not contain command: %s", string(data))
	}

	// 2. Second command while first is pending returns ErrCommandPending
	secondCmd := CollectorCommand{
		Version:     1,
		Command:     "return_normal",
		RequestID:   "req-second",
		RequestedAt: time.Now().UTC(),
	}
	if err := WriteCollectorCommand(tmpDir, secondCmd); !errors.Is(err, ErrCommandPending) {
		t.Fatalf("expected ErrCommandPending when command file exists, got: %v", err)
	}

	// Clean up command file for subsequent tests
	_ = os.Remove(cmdPath)

	// 3. Missing fields fail
	if err := WriteCollectorCommand(tmpDir, CollectorCommand{Command: "enter_reauth"}); err == nil {
		t.Error("expected error for empty request_id, got nil")
	}
	if err := WriteCollectorCommand(tmpDir, CollectorCommand{RequestID: "req-1"}); err == nil {
		t.Error("expected error for empty command, got nil")
	}

	// 3. Write ack file and read it back
	ackData := `{
  "version": 1,
  "request_id": "req-12345",
  "command": "enter_reauth",
  "status": "applied",
  "applied_at": "2026-09-04T12:00:00Z",
  "error": null
}`
	ackPath := filepath.Join(tmpDir, DefaultCommandAckFilename)
	if err := os.WriteFile(ackPath, []byte(ackData), 0644); err != nil {
		t.Fatal(err)
	}

	ack, err := ReadCollectorCommandAck(tmpDir)
	if err != nil {
		t.Fatalf("ReadCollectorCommandAck failed: %v", err)
	}
	if ack.RequestID != "req-12345" || ack.Status != "applied" || ack.Command != "enter_reauth" || ack.Error != nil {
		t.Fatalf("unexpected ack contents: %+v", ack)
	}
}
