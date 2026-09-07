package digest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cordbrief/internal/journal"
)

func TestArtifactMigrationAndJournalIndependence(t *testing.T) {
	tmpDir := t.TempDir()
	exchangeDir := filepath.Join(tmpDir, "exchange")
	eventsDir := filepath.Join(exchangeDir, "events")
	dataDir := filepath.Join(tmpDir, "data")
	digestsDir := filepath.Join(dataDir, "digests")

	_ = os.MkdirAll(eventsDir, 0755)
	_ = os.MkdirAll(digestsDir, 0755)

	// 1. Create a realistic journal file in exchangeDir/events/0000000000000001.ndjson
	segPath := filepath.Join(eventsDir, "0000000000000001.ndjson")
	var recordsJSON strings.Builder
	for i := 1; i <= 4; i++ {
		ev := journal.Event{
			Version:   1,
			Event:     "message_create",
			MessageID: fmt.Sprintf("15452249757604947%02d", i),
			ChannelID: "1545115236619518014",
			GuildID:   "1545114461868658862",
			Timestamp: time.Date(2026, 9, 6, 10, i, 0, 0, time.UTC),
			Author: journal.Author{
				ID:   fmt.Sprintf("user-%d", i),
				Name: fmt.Sprintf("User%d", i),
			},
			Content: fmt.Sprintf("Discussion message number %d", i),
		}
		b, _ := json.Marshal(ev)
		recordsJSON.Write(b)
		recordsJSON.WriteByte('\n')
	}
	if err := os.WriteFile(segPath, []byte(recordsJSON.String()), 0644); err != nil {
		t.Fatalf("failed writing journal: %v", err)
	}

	fi, _ := os.Stat(segPath)
	segSize := fi.Size()

	// 2. Create an unmigrated historical artifact (SourceRefs == nil)
	batchID := "2222222222222222222222222222222222222222222222222222222222222222"
	art := &Artifact{
		Version:              journal.CurrentSchemaVersion,
		BatchID:              batchID,
		CreatedAt:            time.Now().UTC(),
		CursorStart:          journal.Cursor{Version: 1, Segment: 1, Offset: 0},
		CursorEnd:            journal.Cursor{Version: 1, Segment: 1, Offset: segSize},
		InputMessageCount:    4,
		IncludedMessageCount: 4,
		Provider:             "gemini",
		Model:                "gemini-3.7-flash",
		SourceRefs:           nil, // unmigrated
		Digest: &Digest{
			Title:    "Espresso Grind Dynamics",
			Overview: "Comparison between cold and warm burrs.",
			Items: []Item{
				{
					Kind:      KindFinding,
					Text:      "Cold burrs produce narrower particle size distribution.",
					SourceIDs: []string{"S000001", "S000002"},
				},
				{
					Kind:      KindExperiment,
					Text:      "Temperature shift accounted for 3s shot difference.",
					SourceIDs: []string{"S000003", "S000004"},
				},
			},
		},
	}

	if err := SaveArtifact(digestsDir, art); err != nil {
		t.Fatalf("failed saving test artifact: %v", err)
	}

	// 3. Dry-Run Migration
	reports, err := MigrateArtifacts(exchangeDir, dataDir, true)
	if err != nil {
		t.Fatalf("MigrateArtifacts dry-run failed: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	rep := reports[0]
	if rep.BatchID != batchID || rep.SourceCount != 4 || rep.ResolvedCount != 4 || !rep.Eligible || rep.AlreadyMigrated {
		t.Fatalf("unexpected dry-run report: %+v", rep)
	}

	// Verify artifact still has nil SourceRefs after dry-run
	checkArt, _ := LoadArtifact(digestsDir, batchID)
	if len(checkArt.SourceRefs) != 0 {
		t.Fatalf("dry-run unexpectedly modified artifact: %+v", checkArt.SourceRefs)
	}

	// 4. Live Migration
	liveReports, err := MigrateArtifacts(exchangeDir, dataDir, false)
	if err != nil {
		t.Fatalf("MigrateArtifacts live failed: %v", err)
	}
	if len(liveReports) != 1 || !liveReports[0].Eligible {
		t.Fatalf("unexpected live report: %+v", liveReports)
	}

	// Verify artifact now has durable SourceRefs
	migratedArt, err := LoadArtifact(digestsDir, batchID)
	if err != nil {
		t.Fatalf("failed loading migrated artifact: %v", err)
	}
	if len(migratedArt.SourceRefs) != 4 {
		t.Fatalf("expected 4 source refs, got %d", len(migratedArt.SourceRefs))
	}
	for i := 1; i <= 4; i++ {
		sID := fmt.Sprintf("S%06d", i)
		ref, ok := migratedArt.SourceRefs[sID]
		if !ok {
			t.Fatalf("missing ref for %s", sID)
		}
		if ref.GuildID != "1545114461868658862" || ref.ChannelID != "1545115236619518014" || ref.MessageID != fmt.Sprintf("15452249757604947%02d", i) {
			t.Fatalf("unexpected ref content for %s: %+v", sID, ref)
		}
		expectedURL := fmt.Sprintf("https://discord.com/channels/1545114461868658862/1545115236619518014/15452249757604947%02d", i)
		if ref.JumpLink() != expectedURL {
			t.Fatalf("unexpected jump link for %s: got %s, want %s", sID, ref.JumpLink(), expectedURL)
		}
	}

	// 5. Idempotent rerun: must report AlreadyMigrated = true and not corrupt
	idempReports, err := MigrateArtifacts(exchangeDir, dataDir, false)
	if err != nil {
		t.Fatalf("idempotent migration failed: %v", err)
	}
	if len(idempReports) != 1 || !idempReports[0].AlreadyMigrated || !idempReports[0].Eligible {
		t.Fatalf("unexpected idempotent report: %+v", idempReports)
	}

	// 6. JOURNAL INDEPENDENCE TEST:
	// Remove the journal files completely from disk!
	if err := os.RemoveAll(eventsDir); err != nil {
		t.Fatalf("failed removing events dir: %v", err)
	}

	// Load artifact again and verify all source jump links can be constructed WITHOUT journal
	independentArt, err := LoadArtifact(digestsDir, batchID)
	if err != nil {
		t.Fatalf("failed loading artifact: %v", err)
	}

	for _, item := range independentArt.Digest.Items {
		for _, sID := range item.SourceIDs {
			ref, ok := independentArt.SourceRefs[sID]
			if !ok {
				t.Fatalf("source ref %s missing after journal deletion", sID)
			}
			link := ref.JumpLink()
			if !strings.HasPrefix(link, "https://discord.com/channels/1545114461868658862/1545115236619518014/") {
				t.Fatalf("invalid jump link %s", link)
			}
		}
	}
}
