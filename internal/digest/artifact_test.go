package digest

import (
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
