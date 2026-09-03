package digest

import (
	"encoding/json"
	"testing"
	"time"

	"cordbrief/internal/journal"
)

func TestDigest_JSONRoundtrip(t *testing.T) {
	d := &Digest{
		Title:    "Daily Intelligence Digest",
		Overview: "Summary of major events",
		Items: []Item{
			{
				Kind:      KindFinding,
				Text:      "New release deployed",
				SourceIDs: []string{"S000001", "S000002"},
			},
		},
	}

	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded Digest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Title != d.Title {
		t.Errorf("expected title %q, got %q", d.Title, decoded.Title)
	}
	if decoded.Overview != d.Overview {
		t.Errorf("expected overview %q, got %q", d.Overview, decoded.Overview)
	}
	if len(decoded.Items) != 1 || decoded.Items[0].Kind != KindFinding {
		t.Fatalf("unexpected items: %+v", decoded.Items)
	}
	if len(decoded.Items[0].SourceIDs) != 2 || decoded.Items[0].SourceIDs[0] != "S000001" {
		t.Errorf("unexpected source IDs: %+v", decoded.Items[0].SourceIDs)
	}
}

func TestArtifact_JSONRoundtrip(t *testing.T) {
	art := &Artifact{
		Version:              1,
		BatchID:              "abc123hash",
		CreatedAt:            time.Now().UTC().Truncate(time.Millisecond),
		CursorStart:          journal.Cursor{Segment: 1, Offset: 100},
		CursorEnd:            journal.Cursor{Segment: 1, Offset: 500},
		InputMessageCount:    10,
		IncludedMessageCount: 8,
		Provider:             "openai",
		Model:                "gpt-4o",
		Digest: &Digest{
			Title:    "Test Digest",
			Overview: "Test Overview",
			Items: []Item{
				{Kind: KindFinding, Text: "Finding 1", SourceIDs: []string{"S000001"}},
			},
		},
	}

	data, err := json.Marshal(art)
	if err != nil {
		t.Fatal(err)
	}

	var decoded Artifact
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.BatchID != art.BatchID || decoded.CursorEnd.Offset != 500 {
		t.Errorf("mismatch decoded artifact: %+v", decoded)
	}
}
