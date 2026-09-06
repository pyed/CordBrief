package inbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"cordbrief/internal/digest"
)

var (
	// ValidBatchIDRegex ensures batch IDs strictly match 64 hex characters (SHA-256).
	ValidBatchIDRegex = regexp.MustCompile(`^[a-f0-9]{64}$`)

	// ErrInvalidBatchID is returned when a requested batch ID fails format validation.
	ErrInvalidBatchID = errors.New("invalid batch ID format")
)

// Service provides access to persisted digests.
type Service struct {
	digestsDir string
}

// NewService creates a new inbox service reading from digestsDir.
func NewService(digestsDir string) *Service {
	return &Service{digestsDir: digestsDir}
}

// ListDigests returns all valid digest summaries sorted newest first.
func (s *Service) ListDigests() ([]DigestSummary, int, error) {
	return ListDigests(s.digestsDir)
}

// GetDigest retrieves a full digest artifact by its 64-character hex batch ID.
func (s *Service) GetDigest(batchID string) (*digest.Artifact, error) {
	return GetDigest(s.digestsDir, batchID)
}

// ListDigests scans the digests directory and returns valid digest summaries
// sorted newest first, along with a count of any corrupted files skipped.
func ListDigests(digestsDir string) ([]DigestSummary, int, error) {
	entries, err := os.ReadDir(digestsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []DigestSummary{}, 0, nil
		}
		return nil, 0, fmt.Errorf("read digests directory: %w", err)
	}

	var summaries []DigestSummary
	corruptCount := 0

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		filePath := filepath.Join(digestsDir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			corruptCount++
			continue
		}

		var art digest.Artifact
		if err := json.Unmarshal(data, &art); err != nil {
			corruptCount++
			continue
		}

		if art.BatchID == "" || art.Digest == nil {
			corruptCount++
			continue
		}

		summaries = append(summaries, DigestSummary{
			BatchID:              art.BatchID,
			Title:                art.Digest.Title,
			Overview:             art.Digest.Overview,
			CreatedAt:            art.CreatedAt,
			Provider:             art.Provider,
			Model:                art.Model,
			IncludedMessageCount: art.IncludedMessageCount,
			Trigger:              art.Trigger,
		})
	}

	// Sort newest first by CreatedAt, breaking ties by BatchID descending.
	sort.Slice(summaries, func(i, j int) bool {
		if !summaries[i].CreatedAt.Equal(summaries[j].CreatedAt) {
			return summaries[i].CreatedAt.After(summaries[j].CreatedAt)
		}
		return summaries[i].BatchID > summaries[j].BatchID
	})

	if summaries == nil {
		summaries = []DigestSummary{}
	}

	return summaries, corruptCount, nil
}

// GetDigest retrieves and unmarshals a digest artifact by its batch ID.
// It verifies that batchID conforms to the canonical 64 lowercase hex characters.
func GetDigest(digestsDir string, batchID string) (*digest.Artifact, error) {
	if err := digest.ValidateBatchID(batchID); err != nil {
		return nil, ErrInvalidBatchID
	}
	return digest.LoadArtifact(digestsDir, batchID)
}
