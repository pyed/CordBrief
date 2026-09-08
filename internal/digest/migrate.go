package digest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cordbrief/internal/catalog"
	"cordbrief/internal/journal"
)

// MigrationReport records the dry-run or live migration status for an artifact.
type MigrationReport struct {
	BatchID         string `json:"batch_id"`
	SourceCount     int    `json:"source_count"`
	ResolvedCount   int    `json:"resolved_count"`
	Eligible        bool   `json:"eligible"`
	AlreadyMigrated bool   `json:"already_migrated"`
}

// MigrateArtifacts scans all digest artifacts in dataDir and populates minimal SourceRefs
// reconstructed from the journal range. In dryRun mode, no files are modified.
func MigrateArtifacts(exchangeDir, dataDir string, dryRun bool) ([]MigrationReport, error) {
	digestsDir := filepath.Join(dataDir, "digests")
	entries, err := os.ReadDir(digestsDir)
	if err != nil {
		return nil, fmt.Errorf("read digests dir: %w", err)
	}

	eventsDir := filepath.Join(exchangeDir, "events")
	cat, _ := catalog.Load(exchangeDir)

	var reports []MigrationReport

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		batchID := strings.TrimSuffix(entry.Name(), ".json")
		if err := ValidateBatchID(batchID); err != nil {
			continue
		}

		art, err := LoadArtifact(digestsDir, batchID)
		if err != nil {
			return nil, fmt.Errorf("load artifact %s: %w", batchID, err)
		}

		// Identify all unique cited source IDs in the digest
		citedSet := make(map[string]struct{})
		if art.Digest != nil {
			for _, item := range art.Digest.Items {
				for _, sID := range item.SourceIDs {
					s := strings.TrimSpace(sID)
					if s != "" {
						citedSet[s] = struct{}{}
					}
				}
			}
		}

		sourceCount := len(citedSet)

		// Check if already migrated
		if len(art.SourceRefs) > 0 || sourceCount == 0 {
			resolved := 0
			for sID := range citedSet {
				if ref, ok := art.SourceRefs[sID]; ok && ref.JumpLink() != "" {
					resolved++
				}
			}
			reports = append(reports, MigrationReport{
				BatchID:         batchID,
				SourceCount:     sourceCount,
				ResolvedCount:   resolved,
				Eligible:        resolved == sourceCount,
				AlreadyMigrated: true,
			})
			continue
		}

		// Reconstruct from journal
		startCur := art.CursorStart
		if startCur.Version == 0 {
			startCur.Version = journal.CurrentSchemaVersion
		}
		endCur := art.CursorEnd
		if endCur.Version == 0 {
			endCur.Version = journal.CurrentSchemaVersion
		}

		wm, err := journal.CaptureWatermark(eventsDir)
		if err != nil {
			return nil, fmt.Errorf("capture watermark for batch %s: %w", batchID, err)
		}

		reader := journal.NewReader(eventsDir, wm)
		records, _, err := reader.ReadBatch(startCur, art.InputMessageCount+100)
		if err != nil {
			return nil, fmt.Errorf("read journal records for batch %s: %w", batchID, err)
		}

		batch, err := BuildBatch(records, startCur, endCur, wm, false, cat)
		if err != nil {
			return nil, fmt.Errorf("build batch for %s: %w", batchID, err)
		}

		refs := BuildSourceRefs(art.Digest, batch.SourceMap)
		resolvedCount := 0
		for sID := range citedSet {
			if ref, ok := refs[sID]; ok && ref.JumpLink() != "" {
				resolvedCount++
			}
		}

		eligible := (resolvedCount == sourceCount)
		reports = append(reports, MigrationReport{
			BatchID:         batchID,
			SourceCount:     sourceCount,
			ResolvedCount:   resolvedCount,
			Eligible:        eligible,
			AlreadyMigrated: false,
		})

		if !dryRun && eligible {
			art.SourceRefs = refs
			if err := SaveArtifact(digestsDir, art); err != nil {
				return nil, fmt.Errorf("save migrated artifact %s: %w", batchID, err)
			}
		}
	}

	return reports, nil
}
