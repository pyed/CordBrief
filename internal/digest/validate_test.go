package digest

import (
	"strings"
	"testing"
)

func TestValidateDigest_SuccessAndDeduplication(t *testing.T) {
	batch := &Batch{
		SourceMap: map[string]SourceMessage{
			"S000001": {SourceID: "S000001", GuildID: "g1"},
			"S000002": {SourceID: "S000002", GuildID: "g1"},
		},
	}

	d := &Digest{
		Title:    "Daily Summary",
		Overview: "Major progress on testing",
		Items: []Item{
			{
				Kind:      KindFinding,
				Text:      "Test passed",
				SourceIDs: []string{"S000002", "S000001", "S000001"},
			},
		},
	}

	if err := ValidateDigest(d, batch); err != nil {
		t.Fatalf("expected validation to pass, got: %v", err)
	}

	// Verify deduplicated and sorted
	if len(d.Items[0].SourceIDs) != 2 || d.Items[0].SourceIDs[0] != "S000001" || d.Items[0].SourceIDs[1] != "S000002" {
		t.Errorf("expected deduplicated sorted source IDs [S000001 S000002], got: %v", d.Items[0].SourceIDs)
	}
}

func TestValidateDigest_InvalidKind(t *testing.T) {
	batch := &Batch{
		SourceMap: map[string]SourceMessage{
			"S000001": {SourceID: "S000001", GuildID: "g1"},
		},
	}

	d := &Digest{
		Title:    "Title",
		Overview: "Overview",
		Items: []Item{
			{
				Kind:      "unsupported_kind",
				Text:      "Some text",
				SourceIDs: []string{"S000001"},
			},
		},
	}

	err := ValidateDigest(d, batch)
	if err == nil || !strings.Contains(err.Error(), "invalid kind") {
		t.Errorf("expected invalid kind error, got: %v", err)
	}
}

func TestValidateDigest_UnknownSourceID(t *testing.T) {
	batch := &Batch{
		SourceMap: map[string]SourceMessage{
			"S000001": {SourceID: "S000001", GuildID: "g1"},
		},
	}

	d := &Digest{
		Title:    "Title",
		Overview: "Overview",
		Items: []Item{
			{
				Kind:      KindFinding,
				Text:      "Some text",
				SourceIDs: []string{"S999999"}, // Unknown
			},
		},
	}

	err := ValidateDigest(d, batch)
	if err == nil || !strings.Contains(err.Error(), "unknown source_id") {
		t.Errorf("expected unknown source_id error, got: %v", err)
	}
}

func TestValidateDigest_EmptySourceIDs(t *testing.T) {
	batch := &Batch{
		SourceMap: map[string]SourceMessage{
			"S000001": {SourceID: "S000001", GuildID: "g1"},
		},
	}

	d := &Digest{
		Title:    "Title",
		Overview: "Overview",
		Items: []Item{
			{
				Kind:      KindFinding,
				Text:      "Some text without citation",
				SourceIDs: []string{},
			},
		},
	}

	err := ValidateDigest(d, batch)
	if err == nil || !strings.Contains(err.Error(), "no source grounding") {
		t.Errorf("expected no source grounding error, got: %v", err)
	}
}

func TestValidateDigest_UnresolvedGuildID(t *testing.T) {
	batch := &Batch{
		SourceMap: map[string]SourceMessage{
			"S000001": {SourceID: "S000001", GuildID: ""}, // Empty GuildID
		},
	}

	d := &Digest{
		Title:    "Title",
		Overview: "Overview",
		Items: []Item{
			{
				Kind:      KindFinding,
				Text:      "Insight citing message with unresolved guild",
				SourceIDs: []string{"S000001"},
			},
		},
	}

	err := ValidateDigest(d, batch)
	if err == nil || !strings.Contains(err.Error(), "unresolved guild_id") {
		t.Fatalf("expected unresolved guild_id error, got: %v", err)
	}
}
