package digest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ArtifactPath returns the canonical filesystem path for a digest artifact.
func ArtifactPath(dir, batchID string) string {
	return filepath.Join(dir, batchID+".json")
}

// SaveArtifact writes the digest artifact atomically with sync and rename.
func SaveArtifact(dir string, art *Artifact) error {
	if art == nil {
		return fmt.Errorf("artifact cannot be nil")
	}
	if art.BatchID == "" {
		return fmt.Errorf("artifact batch_id cannot be empty")
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create digest artifact directory: %w", err)
	}

	targetPath := ArtifactPath(dir, art.BatchID)
	tmpPath := fmt.Sprintf("%s.tmp.%d", targetPath, os.Getpid())

	data, err := json.MarshalIndent(art, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal digest artifact: %w", err)
	}
	data = append(data, '\n')

	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("create tmp digest artifact: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write tmp digest artifact: %w", err)
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync tmp digest artifact: %w", err)
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close tmp digest artifact: %w", err)
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename digest artifact to destination: %w", err)
	}

	return nil
}

// LoadArtifact reads a persisted digest artifact from disk.
func LoadArtifact(dir, batchID string) (*Artifact, error) {
	path := ArtifactPath(dir, batchID)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var art Artifact
	if err := json.Unmarshal(data, &art); err != nil {
		return nil, fmt.Errorf("unmarshal digest artifact %s: %w", path, err)
	}

	return &art, nil
}

// VerifyArtifactMatch validates that an existing artifact corresponds exactly to the requested batch.
func VerifyArtifactMatch(art *Artifact, b *Batch) error {
	if art.BatchID != b.BatchID {
		return fmt.Errorf("batch_id mismatch: artifact has %q, batch has %q", art.BatchID, b.BatchID)
	}
	if art.CursorStart != b.StartCursor {
		return fmt.Errorf("cursor_start mismatch for batch %s: artifact has %+v, batch has %+v",
			b.BatchID, art.CursorStart, b.StartCursor)
	}
	if art.CursorEnd != b.EndCursor {
		return fmt.Errorf("cursor_end mismatch for batch %s: artifact has %+v, batch has %+v",
			b.BatchID, art.CursorEnd, b.EndCursor)
	}
	if art.IncludedMessageCount != len(b.IncludedMessages) {
		return fmt.Errorf("included message count mismatch for batch %s: artifact has %d, batch has %d",
			b.BatchID, art.IncludedMessageCount, len(b.IncludedMessages))
	}
	return nil
}
