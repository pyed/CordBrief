package digest

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"cordbrief/internal/journal"
)

func TestArtifact_SaveLoadAndVerifyMatch(t *testing.T) {
	tmpDir := t.TempDir()
	validBatchID := strings.Repeat("a", 64)

	art := &Artifact{
		Version:              1,
		BatchID:              validBatchID,
		CreatedAt:            time.Now().UTC(),
		CursorStart:          journal.Cursor{Segment: 1, Offset: 0},
		CursorEnd:            journal.Cursor{Segment: 1, Offset: 500},
		InputMessageCount:    5,
		IncludedMessageCount: 4,
		Provider:             "openai",
		Model:                "gpt-4o",
		Digest: &Digest{
			Title:    "Title",
			Overview: "Overview",
			Items: []Item{
				{Kind: KindFinding, Text: "Test", SourceIDs: []string{"S000001"}},
			},
		},
	}

	// 1. Save artifact
	if err := SaveArtifact(tmpDir, art); err != nil {
		t.Fatalf("SaveArtifact failed: %v", err)
	}

	// 2. Load artifact
	loaded, err := LoadArtifact(tmpDir, validBatchID)
	if err != nil {
		t.Fatalf("LoadArtifact failed: %v", err)
	}
	if loaded.BatchID != art.BatchID || loaded.CursorEnd.Offset != 500 {
		t.Errorf("loaded artifact mismatch: %+v", loaded)
	}

	// 3. Verify match with identical batch
	bMatch := &Batch{
		BatchID:          validBatchID,
		StartCursor:      journal.Cursor{Segment: 1, Offset: 0},
		EndCursor:        journal.Cursor{Segment: 1, Offset: 500},
		IncludedMessages: make([]SourceMessage, 4),
	}
	if err := VerifyArtifactMatch(loaded, bMatch); err != nil {
		t.Errorf("expected match to pass, got: %v", err)
	}

	// 4. Verify match fails on boundary mismatch
	bMismatch := &Batch{
		BatchID:          validBatchID,
		StartCursor:      journal.Cursor{Segment: 1, Offset: 100}, // Mismatch!
		EndCursor:        journal.Cursor{Segment: 1, Offset: 500},
		IncludedMessages: make([]SourceMessage, 4),
	}
	err = VerifyArtifactMatch(loaded, bMismatch)
	if err == nil || !strings.Contains(err.Error(), "cursor_start mismatch") {
		t.Errorf("expected cursor_start mismatch error, got: %v", err)
	}
}

func TestArtifact_TriggerMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	schedBatchID := strings.Repeat("b", 64)

	// 1. Artifact with scheduled trigger
	artScheduled := &Artifact{
		Version:              1,
		BatchID:              schedBatchID,
		CreatedAt:            time.Now().UTC(),
		CursorStart:          journal.Cursor{Segment: 1, Offset: 0},
		CursorEnd:            journal.Cursor{Segment: 1, Offset: 100},
		InputMessageCount:    2,
		IncludedMessageCount: 2,
		Provider:             "gemini",
		Model:                "gemini-3.7-flash",
		Trigger: &TriggerInfo{
			Type:   "scheduled",
			SlotID: "Asia/Riyadh/2026-09-04/08:00",
		},
		Digest: &Digest{Title: "Scheduled Title", Overview: "Overview"},
	}

	if err := SaveArtifact(tmpDir, artScheduled); err != nil {
		t.Fatalf("SaveArtifact failed: %v", err)
	}

	loadedScheduled, err := LoadArtifact(tmpDir, schedBatchID)
	if err != nil {
		t.Fatalf("LoadArtifact failed: %v", err)
	}
	if loadedScheduled.Trigger == nil || loadedScheduled.Trigger.Type != "scheduled" || loadedScheduled.Trigger.SlotID != "Asia/Riyadh/2026-09-04/08:00" {
		t.Fatalf("unexpected trigger metadata: %+v", loadedScheduled.Trigger)
	}

	// 2. Historical M6 artifact WITHOUT trigger field (backward compatibility)
	historicalM6JSON := `{
  "version": 1,
  "batch_id": "94fcca18b1a221c13c910504e9d529b5f9b1b4d41ae431db4ae8999206729339",
  "created_at": "2026-09-03T21:42:04.99616233Z",
  "cursor_start": {
    "version": 1,
    "segment": 1,
    "offset": 3462
  },
  "cursor_end": {
    "version": 1,
    "segment": 1,
    "offset": 5325
  },
  "input_message_count": 4,
  "included_message_count": 4,
  "provider": "gemini",
  "model": "gemini-3.7-flash",
  "digest": {
    "title": "Historical M6 Digest",
    "overview": "Overview from M6",
    "items": [
      {
        "kind": "finding",
        "text": "Historical item",
        "source_ids": ["S000001"]
      }
    ]
  }
}`
	m6Path, err := ArtifactPath(tmpDir, "94fcca18b1a221c13c910504e9d529b5f9b1b4d41ae431db4ae8999206729339")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m6Path, []byte(historicalM6JSON), 0644); err != nil {
		t.Fatal(err)
	}

	loadedM6, err := LoadArtifact(tmpDir, "94fcca18b1a221c13c910504e9d529b5f9b1b4d41ae431db4ae8999206729339")
	if err != nil {
		t.Fatalf("failed loading historical M6 artifact: %v", err)
	}
	if loadedM6.Trigger != nil {
		t.Errorf("expected Trigger to be nil for historical artifact, got: %+v", loadedM6.Trigger)
	}
	if loadedM6.Digest.Title != "Historical M6 Digest" {
		t.Errorf("unexpected title in historical artifact: %s", loadedM6.Digest.Title)
	}
}

func TestBatchIDValidation(t *testing.T) {
	valid64 := strings.Repeat("0", 64)
	validMixedHex := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	if err := ValidateBatchID(valid64); err != nil {
		t.Errorf("expected valid for all zeros, got: %v", err)
	}
	if err := ValidateBatchID(validMixedHex); err != nil {
		t.Errorf("expected valid for mixed hex, got: %v", err)
	}
	if !IsValidBatchID(valid64) || !IsValidBatchID(validMixedHex) {
		t.Errorf("expected IsValidBatchID to return true for valid IDs")
	}

	invalidCases := []struct {
		name string
		id   string
	}{
		{"uppercase_hex", strings.Repeat("A", 64)},
		{"mixed_case_hex", strings.Repeat("a", 32) + strings.Repeat("B", 32)},
		{"too_short", strings.Repeat("a", 63)},
		{"too_long", strings.Repeat("a", 65)},
		{"empty", ""},
		{"dot_dot", ".."},
		{"traversal_path", "../../etc/passwd"},
		{"percent_dot_dot", "%2e%2e"},
		{"slash", "/"},
		{"backslash", "\\"},
		{"null_byte", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde\x00"},
		{"non_hex_characters", strings.Repeat("z", 64)},
		{"special_symbols", strings.Repeat("$", 64)},
		{"spaces", " " + strings.Repeat("a", 63)},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateBatchID(tc.id); err == nil {
				t.Errorf("expected validation failure for %q (%s), got nil", tc.id, tc.name)
			}
			if IsValidBatchID(tc.id) {
				t.Errorf("expected IsValidBatchID to return false for %q", tc.id)
			}
			// Verify ArtifactPath fails closed
			if _, err := ArtifactPath("/tmp/digests", tc.id); err == nil {
				t.Errorf("expected ArtifactPath to reject %q, got nil", tc.id)
			}
		})
	}
}

func TestArtifactConcurrentTempSafety(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Concurrent writes with distinct batch IDs in the same process:
	// Prior implementation derived temp names using only PID (%s.tmp.%d),
	// which risked temp file naming collisions across concurrent workers.
	// os.CreateTemp guarantees collision-free unique temporary files.
	const numGoroutines = 8
	startGate := make(chan struct{})
	errs := make(chan error, numGoroutines)
	batchIDs := make([]string, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		batchIDs[i] = fmt.Sprintf("%063xa", i)
		go func(idx int, bID string) {
			<-startGate
			art := &Artifact{
				Version:              1,
				BatchID:              bID,
				CreatedAt:            time.Now().UTC(),
				CursorStart:          journal.Cursor{Segment: 1, Offset: 0},
				CursorEnd:            journal.Cursor{Segment: 1, Offset: int64(idx * 100)},
				InputMessageCount:    idx,
				IncludedMessageCount: idx,
				Provider:             "test",
				Model:                "test-model",
				Digest: &Digest{
					Title:    fmt.Sprintf("Concurrent Title %d", idx),
					Overview: "Overview",
				},
			}
			errs <- SaveArtifact(tmpDir, art)
		}(i, batchIDs[i])
	}

	close(startGate)

	for i := 0; i < numGoroutines; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent SaveArtifact failed: %v", err)
		}
	}

	for _, bID := range batchIDs {
		loaded, err := LoadArtifact(tmpDir, bID)
		if err != nil {
			t.Fatalf("failed loading artifact %s: %v", bID, err)
		}
		if loaded.BatchID != bID {
			t.Fatalf("unexpected loaded batch ID: %s", loaded.BatchID)
		}
	}
}

