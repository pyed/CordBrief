package digest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cordbrief/internal/durable"
)

// ArtifactPath returns the canonical filesystem path for a digest artifact.
// It verifies that batchID strictly matches 64 lowercase hexadecimal characters.
func ArtifactPath(dir, batchID string) (string, error) {
	if err := ValidateBatchID(batchID); err != nil {
		return "", err
	}
	return filepath.Join(dir, batchID+".json"), nil
}

// SaveArtifact writes the digest artifact atomically with sync and rename using durable.AtomicWriteJSON.
func SaveArtifact(dir string, art *Artifact) error {
	if art == nil {
		return fmt.Errorf("artifact cannot be nil")
	}
	if art.BatchID == "" {
		return fmt.Errorf("artifact batch_id cannot be empty")
	}

	targetPath, err := ArtifactPath(dir, art.BatchID)
	if err != nil {
		return fmt.Errorf("invalid artifact batch_id: %w", err)
	}

	return durable.AtomicWriteJSON(targetPath, art, 0644)
}

// LoadArtifact reads a persisted digest artifact from disk.
func LoadArtifact(dir, batchID string) (*Artifact, error) {
	path, err := ArtifactPath(dir, batchID)
	if err != nil {
		return nil, err
	}
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

// BuildSourceRefs extracts minimal SourceRef identities for all grounded source IDs cited in d.
// It records strictly identity metadata (guild_id, channel_id, message_id) and NO message content.
// If d is nil, it returns an empty map.
func BuildSourceRefs(d *Digest, sourceMap map[string]SourceMessage) map[string]SourceRef {
	refs := make(map[string]SourceRef)
	if d == nil {
		return refs
	}
	for _, item := range d.Items {
		for _, sID := range item.SourceIDs {
			sIDTrim := strings.TrimSpace(sID)
			if sIDTrim == "" {
				continue
			}
			if _, exists := refs[sIDTrim]; exists {
				continue
			}
			if sm, ok := sourceMap[sIDTrim]; ok {
				refs[sIDTrim] = SourceRef{
					GuildID:   strings.TrimSpace(sm.GuildID),
					ChannelID: strings.TrimSpace(sm.ChannelID),
					MessageID: strings.TrimSpace(sm.MessageID),
				}
			}
		}
	}
	return refs
}

