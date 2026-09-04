package digest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cordbrief/internal/catalog"
	"cordbrief/internal/journal"
)

// Summarizer abstracts the LLM digest generation for the transaction runner.
type Summarizer interface {
	GenerateDigest(ctx context.Context, batch *Batch) (*Digest, error)
}

// TransactionOptions contains parameters for running the digest transaction.
type TransactionOptions struct {
	ExchangeDir  string
	DataDir      string
	IgnoreBots   bool
	BatchLimit   int
	ProviderName string
	ModelName    string
	Commit       bool // true for 'run', false for 'preview'
	Trigger      *TriggerInfo
}

// TransactionResult encapsulates the outcome of a digest transaction execution.
type TransactionResult struct {
	Batch            *Batch
	Digest           *Digest
	Artifact         *Artifact
	ArtifactPath     string
	CommittedCursor  *journal.Cursor
	WasIdempotentHit bool
	Empty            bool
	AllExcluded      bool
}

// RunTransaction coordinates the 10-step atomic processing transaction.
func RunTransaction(ctx context.Context, s Summarizer, opts TransactionOptions) (*TransactionResult, error) {
	if opts.BatchLimit <= 0 {
		opts.BatchLimit = 1000
	}
	if opts.ExchangeDir == "" {
		opts.ExchangeDir = "/var/cordbrief/exchange"
	}
	if opts.DataDir == "" {
		opts.DataDir = "/var/cordbrief/data"
	}

	ackPath := filepath.Join(opts.ExchangeDir, journal.DefaultAckFilename)
	eventsDir := filepath.Join(opts.ExchangeDir, "events")
	digestsDir := filepath.Join(opts.DataDir, "digests")

	// 1. Load committed cursor
	cur, err := journal.LoadCursor(ackPath)
	if err != nil {
		return nil, fmt.Errorf("load cursor: %w", err)
	}

	// 2. Capture finite watermark
	wm, err := journal.CaptureWatermark(eventsDir)
	if err != nil {
		return nil, fmt.Errorf("capture watermark: %w", err)
	}

	// 3. Read complete records up to watermark
	reader := journal.NewReader(eventsDir, wm)
	records, nextCur, err := reader.ReadBatch(*cur, opts.BatchLimit)
	if err != nil {
		return nil, fmt.Errorf("read batch: %w", err)
	}

	// Case A: Zero new records
	if len(records) == 0 {
		return &TransactionResult{
			Empty:           true,
			CommittedCursor: cur,
		}, nil
	}

	// 4. Construct deterministic DigestBatch
	cat, _ := catalog.Load(opts.ExchangeDir)
	batch, err := BuildBatch(records, *cur, nextCur, wm, opts.IgnoreBots, cat)
	if err != nil {
		return nil, fmt.Errorf("build batch: %w", err)
	}

	// Case B: All records excluded (e.g. all bots)
	if len(batch.IncludedMessages) == 0 {
		if opts.Commit {
			if err := journal.SaveCursor(ackPath, &nextCur); err != nil {
				return nil, fmt.Errorf("commit cursor for excluded batch: %w", err)
			}
		}
		return &TransactionResult{
			Batch:           batch,
			AllExcluded:     true,
			CommittedCursor: &nextCur,
		}, nil
	}

	// 5. Idempotency Check: check if artifact already exists
	existingArt, err := LoadArtifact(digestsDir, batch.BatchID)
	if err == nil {
		// Artifact already exists on disk
		if err := VerifyArtifactMatch(existingArt, batch); err != nil {
			return nil, fmt.Errorf("idempotency conflict: %w", err)
		}

		if opts.Commit {
			if err := journal.SaveCursor(ackPath, &nextCur); err != nil {
				return nil, fmt.Errorf("commit cursor on idempotent replay: %w", err)
			}
		}

		return &TransactionResult{
			Batch:            batch,
			Digest:           existingArt.Digest,
			Artifact:         existingArt,
			ArtifactPath:     ArtifactPath(digestsDir, batch.BatchID),
			CommittedCursor:  &nextCur,
			WasIdempotentHit: true,
		}, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("check existing artifact %s: %w", batch.BatchID, err)
	}

	// 6. Generate digest via LLM
	if s == nil {
		return nil, fmt.Errorf("summarizer cannot be nil")
	}

	d, err := s.GenerateDigest(ctx, batch)
	if err != nil {
		return nil, fmt.Errorf("generate digest: %w", err)
	}

	// 7. Validate structured result
	if err := ValidateDigest(d, batch); err != nil {
		return nil, fmt.Errorf("validate digest: %w", err)
	}

	art := &Artifact{
		Version:              journal.CurrentSchemaVersion,
		BatchID:              batch.BatchID,
		CreatedAt:            time.Now().UTC(),
		CursorStart:          *cur,
		CursorEnd:            nextCur,
		InputMessageCount:    len(records),
		IncludedMessageCount: len(batch.IncludedMessages),
		Provider:             opts.ProviderName,
		Model:                opts.ModelName,
		Trigger:              opts.Trigger,
		Digest:               d,
	}

	// Preview mode: do not save artifact and do not advance cursor
	if !opts.Commit {
		return &TransactionResult{
			Batch:           batch,
			Digest:          d,
			Artifact:        art,
			CommittedCursor: cur,
		}, nil
	}

	// 8. Persist durable digest artifact
	if err := SaveArtifact(digestsDir, art); err != nil {
		return nil, fmt.Errorf("save digest artifact: %w", err)
	}

	// 9. Verify artifact exists on disk
	artPath := ArtifactPath(digestsDir, art.BatchID)
	if _, err := os.Stat(artPath); err != nil {
		return nil, fmt.Errorf("verify saved artifact on disk: %w", err)
	}

	// 10. ONLY AFTER artifact persistence succeeds: commit journal cursor
	if err := journal.SaveCursor(ackPath, &nextCur); err != nil {
		return nil, fmt.Errorf("commit cursor: %w", err)
	}

	return &TransactionResult{
		Batch:           batch,
		Digest:          d,
		Artifact:        art,
		ArtifactPath:    artPath,
		CommittedCursor: &nextCur,
	}, nil
}
