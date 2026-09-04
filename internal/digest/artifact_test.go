package digest

import (
	"os"
	"strings"
	"testing"
	"time"

	"cordbrief/internal/journal"
)

func TestArtifact_SaveLoadAndVerifyMatch(t *testing.T) {
	tmpDir := t.TempDir()

	art := &Artifact{
		Version:              1,
		BatchID:              "batch-12345",
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
	loaded, err := LoadArtifact(tmpDir, "batch-12345")
	if err != nil {
		t.Fatalf("LoadArtifact failed: %v", err)
	}
	if loaded.BatchID != art.BatchID || loaded.CursorEnd.Offset != 500 {
		t.Errorf("loaded artifact mismatch: %+v", loaded)
	}

	// 3. Verify match with identical batch
	bMatch := &Batch{
		BatchID:          "batch-12345",
		StartCursor:      journal.Cursor{Segment: 1, Offset: 0},
		EndCursor:        journal.Cursor{Segment: 1, Offset: 500},
		IncludedMessages: make([]SourceMessage, 4),
	}
	if err := VerifyArtifactMatch(loaded, bMatch); err != nil {
		t.Errorf("expected match to pass, got: %v", err)
	}

	// 4. Verify match fails on boundary mismatch
	bMismatch := &Batch{
		BatchID:          "batch-12345",
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

	// 1. Artifact with scheduled trigger
	artScheduled := &Artifact{
		Version:              1,
		BatchID:              "batch-scheduled-1",
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

	loadedScheduled, err := LoadArtifact(tmpDir, "batch-scheduled-1")
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
	m6Path := ArtifactPath(tmpDir, "94fcca18b1a221c13c910504e9d529b5f9b1b4d41ae431db4ae8999206729339")
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
