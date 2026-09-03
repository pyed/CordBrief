package digest

import (
	"encoding/json"
	"testing"
)

func TestDigest_JSONRoundtrip(t *testing.T) {
	d := &Digest{
		Overview: "Summary of major events",
		Items: []Item{
			{
				Category:  "announcement",
				Text:      "New release deployed",
				SourceIDs: []string{"m0001", "m0002"},
			},
		},
		WorthOpening: []WorthOpeningItem{
			{
				Text:     "Debate on architecture",
				SourceID: "m0003",
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

	if decoded.Overview != d.Overview {
		t.Errorf("expected overview %q, got %q", d.Overview, decoded.Overview)
	}
	if len(decoded.Items) != 1 || decoded.Items[0].Category != "announcement" {
		t.Fatalf("unexpected items: %+v", decoded.Items)
	}
	if len(decoded.Items[0].SourceIDs) != 2 || decoded.Items[0].SourceIDs[0] != "m0001" {
		t.Errorf("unexpected source IDs: %+v", decoded.Items[0].SourceIDs)
	}
	if len(decoded.WorthOpening) != 1 || decoded.WorthOpening[0].SourceID != "m0003" {
		t.Fatalf("unexpected worth_opening: %+v", decoded.WorthOpening)
	}
}
