package journal

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"cordbrief/internal/catalog"
)

func TestRPCCollectorInterop(t *testing.T) {
	exchangeDir := t.TempDir()

	// Locate repo root
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))
	runnerScript := filepath.Join(repoRoot, "collector", "test", "rpc_interop_runner.mjs")

	cmd := exec.Command("node", runnerScript, exchangeDir)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node interop script failed: %v\nOutput:\n%s", err, string(out))
	}

	// 1. Validate catalog.json with Go Core catalog.Load
	cat, err := catalog.Load(exchangeDir)
	if err != nil {
		t.Fatalf("catalog.Load failed on RPC collector catalog: %v", err)
	}
	if cat.Version != 1 {
		t.Fatalf("unexpected catalog version: %d", cat.Version)
	}
	if len(cat.Guilds) != 1 || cat.Guilds[0].Name != "Alpha Guild" {
		t.Fatalf("unexpected guilds in catalog: %+v", cat.Guilds)
	}
	if len(cat.Guilds[0].Channels) != 1 || cat.Guilds[0].Channels[0].Name != "general" {
		t.Fatalf("unexpected channels in catalog: %+v", cat.Guilds[0].Channels)
	}

	// 2. Validate collector-status.json with Go Core ReadCollectorStatus
	statusPath := filepath.Join(exchangeDir, "collector-status.json")
	status, err := ReadCollectorStatus(statusPath)
	if err != nil {
		t.Fatalf("ReadCollectorStatus failed on RPC collector status: %v", err)
	}
	if status.Version != CurrentSchemaVersion {
		t.Fatalf("unexpected status version: %d", status.Version)
	}
	if status.CollectorState != "stopped" && status.CollectorState != "running" {
		t.Fatalf("unexpected collector state: %s", status.CollectorState)
	}
	if status.DiscordAuthenticated == nil || !*status.DiscordAuthenticated {
		t.Fatalf("expected discord_authenticated=true, got %+v", status.DiscordAuthenticated)
	}
	if status.CatalogState != "ready" {
		t.Fatalf("expected catalog_state='ready', got %s", status.CatalogState)
	}
	if status.WatchedChannelCount != 1 {
		t.Fatalf("expected watched_channel_count=1, got %d", status.WatchedChannelCount)
	}
	if status.ActiveSegment != 1 {
		t.Fatalf("expected active_segment=1, got %d", status.ActiveSegment)
	}

	// 3. Validate watchlist.json with Go Core ReadWatchlist
	watchlistPath := filepath.Join(exchangeDir, "watchlist.json")
	wl, err := ReadWatchlist(watchlistPath)
	if err != nil {
		t.Fatalf("ReadWatchlist failed on watchlist: %v", err)
	}
	if len(wl.ChannelIDs) != 1 || wl.ChannelIDs[0] != "2001" {
		t.Fatalf("unexpected channel IDs in watchlist: %+v", wl.ChannelIDs)
	}

	// 4. Validate journal events with Go Core Reader
	eventsDir := filepath.Join(exchangeDir, "events")
	wm, err := CaptureWatermark(eventsDir)
	if err != nil {
		t.Fatalf("CaptureWatermark failed: %v", err)
	}
	if len(wm.Segments) != 1 || wm.Segments[0] != 1 {
		t.Fatalf("unexpected segments watermark: %+v", wm)
	}

	reader := NewReader(eventsDir, wm)
	records, _, err := reader.ReadBatch(Cursor{Version: 1, Segment: 1, Offset: 0}, 10)
	if err != nil {
		t.Fatalf("ReadBatch failed on RPC journal: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}

	// Check record 1 (recovered snapshot)
	r1 := records[0].Event
	if r1.MessageID != "100000000000000001" || r1.ChannelID != "2001" || r1.GuildID != "1001" {
		t.Fatalf("unexpected record 1 identity: %+v", r1)
	}
	if r1.Author.Name != "alice" || r1.Author.DisplayName != "Alice" || r1.Author.Bot != false {
		t.Fatalf("unexpected record 1 author: %+v", r1.Author)
	}
	if r1.Content != "Historical snapshot message" {
		t.Fatalf("unexpected record 1 content: %s", r1.Content)
	}

	// Check record 2 (live message with reply and attachment)
	r2 := records[1].Event
	if r2.MessageID != "100000000000000002" || r2.ChannelID != "2001" {
		t.Fatalf("unexpected record 2 identity: %+v", r2)
	}
	if r2.ReplyToMessageID == nil || *r2.ReplyToMessageID != "100000000000000001" {
		t.Fatalf("expected reply_to_message_id=100000000000000001, got %+v", r2.ReplyToMessageID)
	}
	if len(r2.Attachments) != 1 || r2.Attachments[0].Filename != "test.png" || r2.Attachments[0].Size != 1024 {
		t.Fatalf("unexpected record 2 attachments: %+v", r2.Attachments)
	}

	// Verify timestamp parsed as valid non-zero UTC time
	if r1.Timestamp.IsZero() || r1.Timestamp.Location() != time.UTC {
		t.Fatalf("record 1 timestamp invalid: %v", r1.Timestamp)
	}
	if r2.Timestamp.IsZero() || r2.Timestamp.Location() != time.UTC {
		t.Fatalf("record 2 timestamp invalid: %v", r2.Timestamp)
	}

	t.Log("PASS: TestRPCCollectorInterop complete, full bidirectional schema compatibility confirmed")
}
