package dce

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyed/CordBrief/internal/state"
)

// TestLiveIntegration exercises the production dce.Client against real Discord channels.
// It is automatically skipped if CORDBRIEF_DCE_PATH or DISCORD_TOKEN is not set.
func TestLiveIntegration(t *testing.T) {
	dcePath := os.Getenv("CORDBRIEF_DCE_PATH")
	token := os.Getenv("DISCORD_TOKEN")
	if dcePath == "" || token == "" {
		t.Skip("skipping live integration test: CORDBRIEF_DCE_PATH or DISCORD_TOKEN not set")
	}

	client, err := NewClient(dcePath, token)
	if err != nil {
		t.Fatalf("failed to create live dce client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 0. Version probe
	ver, err := client.Version(ctx)
	if err != nil {
		t.Fatalf("failed to query dce version: %v", err)
	}
	t.Logf("DCE live version: %s", ver)
	if !strings.HasPrefix(ver, "v2.48") {
		t.Logf("Notice: DCE version %s (expected v2.48.x)", ver)
	}

	const testChannelA = "1391912303376728155"
	cutoffAfter := "2026-09-14T12:00:00Z"
	cutoffBefore := time.Date(2026, 9, 14, 13, 0, 0, 0, time.UTC)

	// 1. Timestamp bounded export
	req1 := ExportRequest{
		ChannelID: testChannelA,
		After: state.Cursor{
			Kind:  state.CursorKindTimestamp,
			Value: cutoffAfter,
		},
		Before: cutoffBefore,
	}

	res1, err := client.Export(ctx, req1)
	if err != nil {
		t.Fatalf("live export 1 failed: %v", err)
	}

	t.Logf("Export 1: channel %s (%s), guild %s (%s), messages: %d, maxID: %s",
		res1.Channel.Name, res1.Channel.ID, res1.Guild.Name, res1.Guild.ID, len(res1.Messages), res1.MaxMessageID)

	if len(res1.Messages) == 0 {
		t.Fatalf("expected messages in live channel A window, got 0")
	}
	if res1.MaxMessageID == "" {
		t.Fatal("expected non-empty MaxMessageID")
	}

	firstMsg := res1.Messages[0]
	lastMsg := res1.Messages[len(res1.Messages)-1]
	t.Logf("First message: ID=%s Time=%s Author=%s Content=%q",
		firstMsg.ID, firstMsg.Timestamp.Format(time.RFC3339), firstMsg.Author.DisplayName(), firstMsg.Content)
	t.Logf("Last message: ID=%s Time=%s Author=%s Content=%q",
		lastMsg.ID, lastMsg.Timestamp.Format(time.RFC3339), lastMsg.Author.DisplayName(), lastMsg.Content)

	// 2. Determinism: repeat identical window
	res2, err := client.Export(ctx, req1)
	if err != nil {
		t.Fatalf("live repeat export failed: %v", err)
	}
	if len(res2.Messages) != len(res1.Messages) {
		t.Fatalf("repeat count mismatch: %d vs %d", len(res2.Messages), len(res1.Messages))
	}
	for i := range res1.Messages {
		if res1.Messages[i].ID != res2.Messages[i].ID {
			t.Fatalf("message ID mismatch at index %d: %s vs %s", i, res1.Messages[i].ID, res2.Messages[i].ID)
		}
		if !res1.Messages[i].Timestamp.Equal(res2.Messages[i].Timestamp) {
			t.Fatalf("timestamp mismatch at index %d: %s vs %s", i, res1.Messages[i].Timestamp, res2.Messages[i].Timestamp)
		}
	}
	if res1.MaxMessageID != res2.MaxMessageID {
		t.Fatalf("MaxMessageID mismatch: %s vs %s", res1.MaxMessageID, res2.MaxMessageID)
	}
	t.Log("Determinism verified: identical message IDs and timestamps in identical order")

	// 3. Empty range
	reqEmpty := ExportRequest{
		ChannelID: testChannelA,
		After: state.Cursor{
			Kind:  state.CursorKindTimestamp,
			Value: "2026-09-14T12:01:00Z",
		},
		Before: time.Date(2026, 9, 14, 12, 2, 0, 0, time.UTC),
	}
	resEmpty, err := client.Export(ctx, reqEmpty)
	if err != nil {
		t.Fatalf("empty range export failed: %v", err)
	}
	if len(resEmpty.Messages) != 0 {
		t.Fatalf("expected 0 messages for empty range, got %d", len(resEmpty.Messages))
	}
	if resEmpty.MaxMessageID != "" {
		t.Fatalf("expected empty MaxMessageID for empty range, got %q", resEmpty.MaxMessageID)
	}
	t.Log("Empty-range behavior verified: returns 0 messages, empty MaxMessageID, exit 0")

	// 4. Inaccessible / invalid channel
	reqInvalid := ExportRequest{
		ChannelID: "999999999999999999",
	}
	_, err = client.Export(ctx, reqInvalid)
	if err == nil {
		t.Fatal("expected error for invalid channel ID, got nil")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("CRITICAL: DISCORD_TOKEN leaked in invalid channel error!")
	}
	t.Logf("Invalid channel error safely handled: %v", err)

	// 5. Temp file ownership & cleanup verification
	customDir := t.TempDir()
	reqClean := ExportRequest{
		ChannelID: testChannelA,
		After: state.Cursor{
			Kind:  state.CursorKindTimestamp,
			Value: "2026-09-14T12:01:00Z",
		},
		Before:    time.Date(2026, 9, 14, 12, 2, 0, 0, time.UTC),
		OutputDir: customDir,
	}
	_, err = client.Export(ctx, reqClean)
	if err != nil {
		t.Fatalf("cleanup test export failed: %v", err)
	}
	remainingFiles, err := filepath.Glob(filepath.Join(customDir, "*.json"))
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	if len(remainingFiles) > 0 {
		t.Fatalf("expected 0 remaining files in output dir, found: %v", remainingFiles)
	}
	t.Log("Temporary file cleanup verified: raw export JSON deleted immediately after parse")

	// 6. Sequential second channel export
	const testChannelB = "1403480179195515060" // off-topic channel
	reqB := ExportRequest{
		ChannelID: testChannelB,
		After: state.Cursor{
			Kind:  state.CursorKindTimestamp,
			Value: "2026-09-14T00:00:00Z",
		},
		Before: time.Date(2026, 9, 14, 15, 0, 0, 0, time.UTC),
	}
	resB, err := client.Export(ctx, reqB)
	if err != nil {
		t.Fatalf("second channel export failed: %v", err)
	}
	t.Logf("Second channel export succeeded: %s (%s), messages: %d, maxID: %s",
		resB.Channel.Name, resB.Channel.ID, len(resB.Messages), resB.MaxMessageID)
	if len(resB.Messages) == 0 {
		t.Fatal("expected messages in second channel window")
	}
}
