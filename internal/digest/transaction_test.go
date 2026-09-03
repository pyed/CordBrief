package digest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cordbrief/internal/journal"
)

type mockSummarizer struct {
	calls  int
	digest *Digest
	err    error
}

func (m *mockSummarizer) GenerateDigest(ctx context.Context, batch *Batch) (*Digest, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.digest, nil
}

func setupTransactionEnv(t *testing.T) (string, string) {
	tmp := t.TempDir()
	exchangeDir := filepath.Join(tmp, "exchange")
	eventsDir := filepath.Join(exchangeDir, "events")
	dataDir := filepath.Join(tmp, "data")

	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write initial core-ack.json
	ackPath := filepath.Join(exchangeDir, journal.DefaultAckFilename)
	initCur := &journal.Cursor{Version: 1, Segment: 1, Offset: 0}
	if err := journal.SaveCursor(ackPath, initCur); err != nil {
		t.Fatal(err)
	}

	// Write a segment with one message
	seg1 := filepath.Join(eventsDir, "0000000000000001.ndjson")
	msgJSON := `{"version":1,"event":"message_create","message_id":"msg-1","guild_id":"g1","channel_id":"ch-1","timestamp":"2026-09-03T12:00:00Z","captured_at":"2026-09-03T12:00:00Z","author":{"id":"u1","name":"alice","display_name":"Alice","bot":false},"content":"Test observation"}` + "\n"
	if err := os.WriteFile(seg1, []byte(msgJSON), 0644); err != nil {
		t.Fatal(err)
	}

	return exchangeDir, dataDir
}

func TestTransaction_Success(t *testing.T) {
	exchangeDir, dataDir := setupTransactionEnv(t)
	ackPath := filepath.Join(exchangeDir, journal.DefaultAckFilename)

	summarizer := &mockSummarizer{
		digest: &Digest{
			Title:    "Valid Title",
			Overview: "Valid Overview",
			Items: []Item{
				{Kind: KindFinding, Text: "Finding", SourceIDs: []string{"S000001"}},
			},
		},
	}

	opts := TransactionOptions{
		ExchangeDir:  exchangeDir,
		DataDir:      dataDir,
		ProviderName: "mock",
		ModelName:    "test-model",
		Commit:       true,
	}

	res, err := RunTransaction(context.Background(), summarizer, opts)
	if err != nil {
		t.Fatalf("RunTransaction failed: %v", err)
	}

	if summarizer.calls != 1 {
		t.Errorf("expected 1 summarizer call, got %d", summarizer.calls)
	}

	if res.CommittedCursor == nil || res.CommittedCursor.Offset == 0 {
		t.Errorf("expected advanced committed cursor, got %+v", res.CommittedCursor)
	}

	// Verify cursor in core-ack.json on disk
	curOnDisk, err := journal.LoadCursor(ackPath)
	if err != nil {
		t.Fatal(err)
	}
	if curOnDisk.Offset != res.CommittedCursor.Offset {
		t.Errorf("cursor on disk mismatch: %d vs %d", curOnDisk.Offset, res.CommittedCursor.Offset)
	}

	// Verify artifact exists on disk
	if _, err := os.Stat(res.ArtifactPath); err != nil {
		t.Errorf("expected artifact at %s, got err: %v", res.ArtifactPath, err)
	}
}

func TestTransaction_ProviderFailureLeavesCursorUntouched(t *testing.T) {
	exchangeDir, dataDir := setupTransactionEnv(t)
	ackPath := filepath.Join(exchangeDir, journal.DefaultAckFilename)

	summarizer := &mockSummarizer{
		err: errors.New("network timeout communicating with LLM"),
	}

	opts := TransactionOptions{
		ExchangeDir: exchangeDir,
		DataDir:     dataDir,
		Commit:      true,
	}

	_, err := RunTransaction(context.Background(), summarizer, opts)
	if err == nil {
		t.Fatal("expected RunTransaction to fail on provider error")
	}

	// Cursor must remain at 0
	curOnDisk, err := journal.LoadCursor(ackPath)
	if err != nil {
		t.Fatal(err)
	}
	if curOnDisk.Offset != 0 {
		t.Errorf("expected cursor to remain at 0, got %d", curOnDisk.Offset)
	}
}

func TestTransaction_ValidationFailureLeavesCursorUntouched(t *testing.T) {
	exchangeDir, dataDir := setupTransactionEnv(t)
	ackPath := filepath.Join(exchangeDir, journal.DefaultAckFilename)

	// Summarizer returns unknown source ID
	summarizer := &mockSummarizer{
		digest: &Digest{
			Title:    "Title",
			Overview: "Overview",
			Items: []Item{
				{Kind: KindFinding, Text: "Finding", SourceIDs: []string{"S999999"}},
			},
		},
	}

	opts := TransactionOptions{
		ExchangeDir: exchangeDir,
		DataDir:     dataDir,
		Commit:      true,
	}

	_, err := RunTransaction(context.Background(), summarizer, opts)
	if err == nil || !strings.Contains(err.Error(), "unknown source_id") {
		t.Fatalf("expected unknown source_id error, got: %v", err)
	}

	// Cursor must remain at 0
	curOnDisk, err := journal.LoadCursor(ackPath)
	if err != nil {
		t.Fatal(err)
	}
	if curOnDisk.Offset != 0 {
		t.Errorf("expected cursor to remain at 0, got %d", curOnDisk.Offset)
	}
}

func TestTransaction_CrashBoundaryIdempotency(t *testing.T) {
	exchangeDir, dataDir := setupTransactionEnv(t)
	ackPath := filepath.Join(exchangeDir, journal.DefaultAckFilename)

	// Step 1: Pre-create the artifact as if a previous process wrote it but crashed before updating core-ack.json
	// First preview to get the deterministic batch
	previewOpts := TransactionOptions{
		ExchangeDir: exchangeDir,
		DataDir:     dataDir,
		Commit:      false,
	}
	summarizer := &mockSummarizer{
		digest: &Digest{
			Title:    "Title",
			Overview: "Overview",
			Items: []Item{
				{Kind: KindFinding, Text: "Finding", SourceIDs: []string{"S000001"}},
			},
		},
	}

	prevRes, err := RunTransaction(context.Background(), summarizer, previewOpts)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate crash: write the artifact to disk manually, leaving cursor at offset 0
	digestsDir := filepath.Join(dataDir, "digests")
	if err := SaveArtifact(digestsDir, prevRes.Artifact); err != nil {
		t.Fatal(err)
	}

	// Reset summarizer call count
	summarizer.calls = 0

	// Step 2: Run transaction with Commit = true
	runOpts := TransactionOptions{
		ExchangeDir: exchangeDir,
		DataDir:     dataDir,
		Commit:      true,
	}

	res, err := RunTransaction(context.Background(), summarizer, runOpts)
	if err != nil {
		t.Fatalf("RunTransaction with existing artifact failed: %v", err)
	}

	// MUST be an idempotent hit without calling the LLM
	if !res.WasIdempotentHit {
		t.Error("expected WasIdempotentHit to be true")
	}
	if summarizer.calls != 0 {
		t.Errorf("expected 0 summarizer calls on idempotent replay, got %d", summarizer.calls)
	}

	// Cursor must now be committed to end offset
	curOnDisk, err := journal.LoadCursor(ackPath)
	if err != nil {
		t.Fatal(err)
	}
	if curOnDisk.Offset == 0 {
		t.Error("expected cursor to advance on idempotent replay")
	}
}

func TestTransaction_ConflictingArtifactFailsLoudly(t *testing.T) {
	exchangeDir, dataDir := setupTransactionEnv(t)

	// Pre-create artifact with same batch ID but conflicting cursor bounds
	previewOpts := TransactionOptions{
		ExchangeDir: exchangeDir,
		DataDir:     dataDir,
		Commit:      false,
	}
	summarizer := &mockSummarizer{
		digest: &Digest{
			Title:    "Title",
			Overview: "Overview",
			Items: []Item{
				{Kind: KindFinding, Text: "Finding", SourceIDs: []string{"S000001"}},
			},
		},
	}
	prevRes, err := RunTransaction(context.Background(), summarizer, previewOpts)
	if err != nil {
		t.Fatal(err)
	}

	// Tamper cursor start
	prevRes.Artifact.CursorStart.Offset = 9999
	digestsDir := filepath.Join(dataDir, "digests")
	if err := SaveArtifact(digestsDir, prevRes.Artifact); err != nil {
		t.Fatal(err)
	}

	// Run transaction
	opts := TransactionOptions{
		ExchangeDir: exchangeDir,
		DataDir:     dataDir,
		Commit:      true,
	}
	_, err = RunTransaction(context.Background(), summarizer, opts)
	if err == nil || !strings.Contains(err.Error(), "idempotency conflict") {
		t.Fatalf("expected idempotency conflict error, got: %v", err)
	}
}

func TestTransaction_EmptyBatchZeroCalls(t *testing.T) {
	tmp := t.TempDir()
	exchangeDir := filepath.Join(tmp, "exchange")
	eventsDir := filepath.Join(exchangeDir, "events")
	dataDir := filepath.Join(tmp, "data")
	_ = os.MkdirAll(eventsDir, 0755)
	_ = os.MkdirAll(dataDir, 0755)
	ackPath := filepath.Join(exchangeDir, journal.DefaultAckFilename)
	_ = journal.SaveCursor(ackPath, &journal.Cursor{Version: 1, Segment: 1, Offset: 0})

	summarizer := &mockSummarizer{}
	opts := TransactionOptions{
		ExchangeDir: exchangeDir,
		DataDir:     dataDir,
		Commit:      true,
	}

	res, err := RunTransaction(context.Background(), summarizer, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Empty {
		t.Error("expected res.Empty = true")
	}
	if summarizer.calls != 0 {
		t.Errorf("expected 0 summarizer calls, got %d", summarizer.calls)
	}
}
