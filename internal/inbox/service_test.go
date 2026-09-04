package inbox

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cordbrief/internal/digest"
	"cordbrief/internal/journal"
)

func TestInbox_EmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistentDir := filepath.Join(tmpDir, "missing")

	// Missing directory returns empty list without error
	summaries, corrupt, err := ListDigests(nonExistentDir)
	if err != nil {
		t.Fatalf("expected nil error on missing dir, got: %v", err)
	}
	if len(summaries) != 0 || corrupt != 0 {
		t.Fatalf("expected 0 summaries and 0 corrupt, got %d summaries, %d corrupt", len(summaries), corrupt)
	}

	// Empty existing directory
	emptyDir := filepath.Join(tmpDir, "empty")
	if err := os.MkdirAll(emptyDir, 0755); err != nil {
		t.Fatal(err)
	}
	svc := NewService(emptyDir)
	summaries, corrupt, err = svc.ListDigests()
	if err != nil {
		t.Fatalf("expected nil error on empty dir, got: %v", err)
	}
	if len(summaries) != 0 || corrupt != 0 {
		t.Fatalf("expected 0 summaries and 0 corrupt, got %d summaries, %d corrupt", len(summaries), corrupt)
	}
}

func TestInbox_ListAndSortMultiple(t *testing.T) {
	tmpDir := t.TempDir()

	id1 := strings.Repeat("a", 64)
	id2 := strings.Repeat("b", 64)
	id3 := strings.Repeat("c", 64)

	t1 := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)

	writeArtifact(t, tmpDir, &digest.Artifact{
		Version:              1,
		BatchID:              id1,
		CreatedAt:            t1,
		CursorStart:          journal.Cursor{Segment: 1, Offset: 100},
		CursorEnd:            journal.Cursor{Segment: 1, Offset: 200},
		InputMessageCount:    5,
		IncludedMessageCount: 5,
		Provider:             "gemini",
		Model:                "gemini-3.7-flash",
		Digest: &digest.Digest{
			Title:    "Middle Digest",
			Overview: "Overview 1",
			Items:    []digest.Item{{Kind: "important", Text: "text 1"}},
		},
	})

	writeArtifact(t, tmpDir, &digest.Artifact{
		Version:              1,
		BatchID:              id2,
		CreatedAt:            t2,
		CursorStart:          journal.Cursor{Segment: 1, Offset: 200},
		CursorEnd:            journal.Cursor{Segment: 1, Offset: 300},
		InputMessageCount:    10,
		IncludedMessageCount: 8,
		Provider:             "local",
		Model:                "local-llm",
		Trigger: &digest.TriggerInfo{
			Type:   "scheduled",
			SlotID: "Asia/Riyadh/2026-09-04/12:00",
		},
		Digest: &digest.Digest{
			Title:    "Newest Digest",
			Overview: "Overview 2",
			Items:    []digest.Item{{Kind: "finding", Text: "text 2"}},
		},
	})

	writeArtifact(t, tmpDir, &digest.Artifact{
		Version:              1,
		BatchID:              id3,
		CreatedAt:            t3,
		CursorStart:          journal.Cursor{Segment: 1, Offset: 0},
		CursorEnd:            journal.Cursor{Segment: 1, Offset: 100},
		InputMessageCount:    2,
		IncludedMessageCount: 2,
		Provider:             "gemini",
		Model:                "gemini-3.7-flash",
		Digest: &digest.Digest{
			Title:    "Oldest Digest",
			Overview: "Overview 3",
			Items:    []digest.Item{{Kind: "question", Text: "text 3"}},
		},
	})

	summaries, corrupt, err := ListDigests(tmpDir)
	if err != nil {
		t.Fatalf("ListDigests failed: %v", err)
	}
	if corrupt != 0 {
		t.Fatalf("expected 0 corrupt files, got %d", corrupt)
	}
	if len(summaries) != 3 {
		t.Fatalf("expected 3 summaries, got %d", len(summaries))
	}

	// Verify newest first: id2 (12:00), id1 (10:00), id3 (08:00)
	if summaries[0].BatchID != id2 || summaries[0].Title != "Newest Digest" {
		t.Errorf("expected newest digest at index 0, got %s (%s)", summaries[0].BatchID, summaries[0].Title)
	}
	if summaries[0].Trigger == nil || summaries[0].Trigger.Type != "scheduled" {
		t.Errorf("expected scheduled trigger metadata on index 0, got %+v", summaries[0].Trigger)
	}

	if summaries[1].BatchID != id1 || summaries[1].Title != "Middle Digest" {
		t.Errorf("expected middle digest at index 1, got %s (%s)", summaries[1].BatchID, summaries[1].Title)
	}
	if summaries[1].Trigger != nil {
		t.Errorf("expected nil trigger on index 1, got %+v", summaries[1].Trigger)
	}

	if summaries[2].BatchID != id3 || summaries[2].Title != "Oldest Digest" {
		t.Errorf("expected oldest digest at index 2, got %s (%s)", summaries[2].BatchID, summaries[2].Title)
	}
}

func TestInbox_CorruptFileIsolation(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Valid artifact
	validID := strings.Repeat("f", 64)
	writeArtifact(t, tmpDir, &digest.Artifact{
		Version:              1,
		BatchID:              validID,
		CreatedAt:            time.Now(),
		IncludedMessageCount: 3,
		Provider:             "gemini",
		Model:                "gemini-3.7-flash",
		Digest:               &digest.Digest{Title: "Valid", Overview: "Valid overview"},
	})

	// 2. Corrupt JSON file
	if err := os.WriteFile(filepath.Join(tmpDir, strings.Repeat("e", 64)+".json"), []byte("{broken json"), 0644); err != nil {
		t.Fatal(err)
	}

	// 3. Missing digest field
	if err := os.WriteFile(filepath.Join(tmpDir, strings.Repeat("d", 64)+".json"), []byte(`{"version":1,"batch_id":"`+strings.Repeat("d", 64)+`"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// 4. Non-json file (should be ignored without incrementing corruptCount)
	if err := os.WriteFile(filepath.Join(tmpDir, "README.txt"), []byte("not a json file"), 0644); err != nil {
		t.Fatal(err)
	}

	// 5. Subdirectory (should be ignored)
	if err := os.MkdirAll(filepath.Join(tmpDir, "subfolder"), 0755); err != nil {
		t.Fatal(err)
	}

	summaries, corrupt, err := ListDigests(tmpDir)
	if err != nil {
		t.Fatalf("ListDigests failed: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected exactly 1 valid summary, got %d", len(summaries))
	}
	if summaries[0].BatchID != validID {
		t.Errorf("expected validID, got %s", summaries[0].BatchID)
	}
	if corrupt != 2 {
		t.Errorf("expected 2 corrupt files reported, got %d", corrupt)
	}
}

func TestInbox_GetDigest_SecurityAndValidation(t *testing.T) {
	tmpDir := t.TempDir()
	validID := strings.Repeat("1", 64)

	art := &digest.Artifact{
		Version:              1,
		BatchID:              validID,
		CreatedAt:            time.Now().UTC().Truncate(time.Second),
		CursorStart:          journal.Cursor{Segment: 1, Offset: 100},
		CursorEnd:            journal.Cursor{Segment: 1, Offset: 200},
		InputMessageCount:    4,
		IncludedMessageCount: 4,
		Provider:             "gemini",
		Model:                "gemini-3.7-flash",
		Digest: &digest.Digest{
			Title:    "Target Digest",
			Overview: "Target overview",
			Items: []digest.Item{
				{Kind: "finding", Text: "Key finding", SourceIDs: []string{"S000001"}},
			},
		},
	}
	writeArtifact(t, tmpDir, art)

	svc := NewService(tmpDir)

	// Valid lookup
	retrieved, err := svc.GetDigest(validID)
	if err != nil {
		t.Fatalf("GetDigest failed: %v", err)
	}
	if retrieved.BatchID != validID || retrieved.Digest.Title != "Target Digest" {
		t.Fatalf("retrieved artifact mismatch: %+v", retrieved)
	}

	// Path traversal attempts must return ErrInvalidBatchID
	traversals := []string{
		"../" + validID,
		"..\\something",
		"/etc/passwd",
		"1111",                                             // too short
		strings.Repeat("1", 63),                            // 63 chars
		strings.Repeat("1", 65),                            // 65 chars
		strings.Repeat("G", 64),                            // non-hex uppercase
		strings.Repeat("1", 63) + "/",                      // path separator
		validID + ".json",                                  // contains dot
		"94fcca18b1a221c13c910504e9d529b5f9b1b4d41ae431db4ae8999206729339;rm",
	}

	for _, badID := range traversals {
		_, err := svc.GetDigest(badID)
		if !errors.Is(err, ErrInvalidBatchID) {
			t.Errorf("expected ErrInvalidBatchID for %q, got: %v", badID, err)
		}
	}

	// Non-existent 64-hex ID returns os.ErrNotExist
	missingID := strings.Repeat("9", 64)
	_, err = svc.GetDigest(missingID)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist for missing batch ID, got: %v", err)
	}
}

func TestInbox_HistoricalM6ArtifactCompatibility(t *testing.T) {
	// Recreate exact structure of live M6 artifact: 94fcca18b1a221c13c910504e9d529b5f9b1b4d41ae431db4ae8999206729339
	tmpDir := t.TempDir()
	m6ID := "94fcca18b1a221c13c910504e9d529b5f9b1b4d41ae431db4ae8999206729339"

	m6JSON := `{
  "version": 1,
  "batch_id": "94fcca18b1a221c13c910504e9d529b5f9b1b4d41ae431db4ae8999206729339",
  "created_at": "2026-09-03T21:40:08.5721832+03:00",
  "cursor_start": {"segment": 1, "offset": 3462},
  "cursor_end": {"segment": 1, "offset": 5325},
  "input_message_count": 4,
  "included_message_count": 4,
  "provider": "gemini",
  "model": "gemini-3.7-flash",
  "digest": {
    "title": "Discussion on CordBrief Milestone 6 and Ingestion Flow",
    "overview": "The team discussed verification of Milestone 6.",
    "items": [
      {
        "kind": "finding",
        "text": "The ingestion flow has been confirmed stable.",
        "source_ids": ["S000001", "S000002"]
      }
    ]
  }
}`

	if err := os.WriteFile(filepath.Join(tmpDir, m6ID+".json"), []byte(m6JSON), 0644); err != nil {
		t.Fatal(err)
	}

	summaries, corrupt, err := ListDigests(tmpDir)
	if err != nil {
		t.Fatalf("ListDigests failed: %v", err)
	}
	if corrupt != 0 || len(summaries) != 1 {
		t.Fatalf("expected 1 summary and 0 corrupt, got %d summaries, %d corrupt", len(summaries), corrupt)
	}

	summary := summaries[0]
	if summary.BatchID != m6ID {
		t.Errorf("expected batch ID %s, got %s", m6ID, summary.BatchID)
	}
	if summary.Trigger != nil {
		t.Errorf("historical M6 artifact should have nil Trigger, got: %+v", summary.Trigger)
	}
	if summary.IncludedMessageCount != 4 {
		t.Errorf("expected 4 included messages, got %d", summary.IncludedMessageCount)
	}

	art, err := GetDigest(tmpDir, m6ID)
	if err != nil {
		t.Fatalf("GetDigest on historical M6 artifact failed: %v", err)
	}
	if art.Trigger != nil {
		t.Errorf("expected nil Trigger on artifact, got: %+v", art.Trigger)
	}
	if art.Digest.Title != "Discussion on CordBrief Milestone 6 and Ingestion Flow" {
		t.Errorf("title mismatch: %s", art.Digest.Title)
	}
}

func writeArtifact(t *testing.T, dir string, art *digest.Artifact) {
	t.Helper()
	data, err := json.MarshalIndent(art, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(dir, art.BatchID+".json")
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		t.Fatal(err)
	}
}
