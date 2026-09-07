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
	ExchangeDir             string
	DataDir                 string
	IgnoreBots              bool
	BatchLimit              int
	ProviderName            string
	ModelName               string
	Commit                  bool // true for 'run', false for 'preview'
	Trigger                 *TriggerInfo
	DeliveryRequest         *DeliveryRequest
	PrepareDeliveryIntentFn func(batchID string, targetCur journal.Cursor, req *DeliveryRequest) error
	PromoteDeliveryIntentFn func(batchID string) error
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

// RunTransaction coordinates the 11-step atomic processing transaction.
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

	// 1. Read cursor from exchange/core-ack.json
	cur, err := journal.LoadCursor(ackPath)
	if err != nil {
		return nil, fmt.Errorf("load cursor: %w", err)
	}

	// 2. Capture read watermark
	wm, err := journal.CaptureWatermark(eventsDir)
	if err != nil {
		return nil, fmt.Errorf("capture watermark: %w", err)
	}

	// 3. Sequential segment read up to limit or watermark
	reader := journal.NewReader(eventsDir, wm)
	records, nextCur, err := reader.ReadBatch(*cur, opts.BatchLimit)
	if err != nil {
		return nil, fmt.Errorf("read batch: %w", err)
	}

	if len(records) == 0 {
		return &TransactionResult{
			Empty:           true,
			CommittedCursor: cur,
		}, nil
	}

	// 4. Batch Construction & Ingestion filtering
	cat, _ := catalog.Load(opts.ExchangeDir)
	batch, err := BuildBatch(records, *cur, nextCur, wm, opts.IgnoreBots, cat)
	if err != nil {
		return nil, fmt.Errorf("build batch: %w", err)
	}

	if len(batch.IncludedMessages) == 0 {
		// All messages were excluded by filter (e.g. bots or non-watched channels).
		// Advance cursor without invoking LLM or creating artifact.
		if opts.Commit {
			if err := journal.SaveCursor(ackPath, &nextCur); err != nil {
				return nil, fmt.Errorf("commit cursor for empty filtered batch: %w", err)
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
			// Reconstruct PREPARED intent from ARTIFACT's DeliveryRequest (not current config!)
			if existingArt.DeliveryRequest != nil && opts.PrepareDeliveryIntentFn != nil {
				if err := opts.PrepareDeliveryIntentFn(existingArt.BatchID, nextCur, existingArt.DeliveryRequest); err != nil {
					return nil, fmt.Errorf("prepare delivery intent on idempotent replay: %w", err)
				}
			}
			if err := journal.SaveCursor(ackPath, &nextCur); err != nil {
				return nil, fmt.Errorf("commit cursor on idempotent replay: %w", err)
			}
			if existingArt.DeliveryRequest != nil && opts.PromoteDeliveryIntentFn != nil {
				if err := opts.PromoteDeliveryIntentFn(existingArt.BatchID); err != nil {
					return nil, fmt.Errorf("promote delivery intent on idempotent replay: %w", err)
				}
			}
		}

		artPath, _ := ArtifactPath(digestsDir, batch.BatchID)
		return &TransactionResult{
			Batch:            batch,
			Digest:           existingArt.Digest,
			Artifact:         existingArt,
			ArtifactPath:     artPath,
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
		DeliveryRequest:      opts.DeliveryRequest,
		SourceRefs:           BuildSourceRefs(d, batch.SourceMap),
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
	artPath, err := ArtifactPath(digestsDir, art.BatchID)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact path: %w", err)
	}
	if _, err := os.Stat(artPath); err != nil {
		return nil, fmt.Errorf("verify saved artifact on disk: %w", err)
	}

	// 9b. BEFORE cursor commit: persist durable PREPARED delivery intent (outbox) if requested
	if opts.DeliveryRequest != nil && opts.PrepareDeliveryIntentFn != nil {
		if err := opts.PrepareDeliveryIntentFn(art.BatchID, nextCur, opts.DeliveryRequest); err != nil {
			return nil, fmt.Errorf("prepare delivery intent: %w", err)
		}
	}

	// 10. ONLY AFTER artifact and PREPARED delivery intent persistence succeed: commit journal cursor
	if err := journal.SaveCursor(ackPath, &nextCur); err != nil {
		return nil, fmt.Errorf("commit cursor: %w", err)
	}

	// 11. ONLY AFTER cursor commit: promote PREPARED delivery intent to sendable PENDING
	if opts.DeliveryRequest != nil && opts.PromoteDeliveryIntentFn != nil {
		if err := opts.PromoteDeliveryIntentFn(art.BatchID); err != nil {
			return nil, fmt.Errorf("promote delivery intent: %w", err)
		}
	}

	return &TransactionResult{
		Batch:           batch,
		Digest:          d,
		Artifact:        art,
		ArtifactPath:    artPath,
		CommittedCursor: &nextCur,
	}, nil
}
