package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cordbrief/internal/config"
	"cordbrief/internal/digest"
	"cordbrief/internal/journal"
)

type matrixSummarizer struct {
	calls  atomic.Int32
	digest *digest.Digest
}

func (m *matrixSummarizer) GenerateDigest(ctx context.Context, batch *digest.Batch) (*digest.Digest, error) {
	m.calls.Add(1)
	return m.digest, nil
}

func setupMatrixEnv(t *testing.T) (string, string, *config.Store) {
	t.Helper()
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

	ackPath := filepath.Join(exchangeDir, journal.DefaultAckFilename)
	initCur := &journal.Cursor{Version: 1, Segment: 1, Offset: 0}
	if err := journal.SaveCursor(ackPath, initCur); err != nil {
		t.Fatal(err)
	}

	seg1 := filepath.Join(eventsDir, "0000000000000001.ndjson")
	msgJSON := `{"version":1,"event":"message_create","message_id":"1545224975760494701","guild_id":"1545114461868658862","channel_id":"1545115236619518014","timestamp":"2026-09-03T12:00:00Z","captured_at":"2026-09-03T12:00:00Z","author":{"id":"u1","name":"alice","display_name":"Alice","bot":false},"content":"Test observation"}` + "\n"
	if err := os.WriteFile(seg1, []byte(msgJSON), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := config.NewStore(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = store.SaveDeliveryConfig(config.DeliveryConfig{
		Telegram: config.TelegramConfig{
			Enabled:   true,
			ChatID:    "123456",
			ChatLabel: "Test Chat",
		},
	})
	_ = store.SaveTelegramBotToken("fake-token")

	return exchangeDir, dataDir, store
}

// Scenario A:
// artifact durable -> crash before PREPARED -> retry uses artifact destination snapshot -> no second LLM -> PREPARED created -> cursor commit -> PENDING
func TestCrashInjection_ScenarioA(t *testing.T) {
	exchangeDir, dataDir, store := setupMatrixEnv(t)
	ackPath := filepath.Join(exchangeDir, journal.DefaultAckFilename)

	delSvc, err := NewService(ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token)
		},
	})
	if err != nil {
		t.Fatalf("NewService error: %v", err)
	}

	s := &matrixSummarizer{
		digest: &digest.Digest{
			Title:    "Valid Title",
			Overview: "Valid Overview",
			Items: []digest.Item{
				{Kind: digest.KindFinding, Text: "Finding", SourceIDs: []string{"S000001"}},
			},
		},
	}

	delReq := &digest.DeliveryRequest{
		Provider:  "telegram",
		ChatID:    "chat-orig-111",
		ChatLabel: "Original Chat",
	}

	firstAttempt := true
	prepareFn := func(batchID string, targetCur journal.Cursor, req *digest.DeliveryRequest) error {
		if firstAttempt {
			firstAttempt = false
			return errors.New("simulated crash before PREPARED outbox")
		}
		_, err := delSvc.PrepareDeliveryIntent(batchID, targetCur, req)
		return err
	}

	opts := digest.TransactionOptions{
		ExchangeDir:             exchangeDir,
		DataDir:                 dataDir,
		ProviderName:            "mock",
		ModelName:               "test-model",
		Commit:                  true,
		DeliveryRequest:         delReq,
		PrepareDeliveryIntentFn: prepareFn,
		PromoteDeliveryIntentFn: func(batchID string) error {
			_, err := delSvc.PromoteDeliveryIntent(batchID)
			return err
		},
	}

	// 1. First run: crashes before PREPARED creation
	res1, err1 := digest.RunTransaction(context.Background(), s, opts)
	if err1 == nil || !strings.Contains(err1.Error(), "simulated crash before PREPARED outbox") {
		t.Fatalf("expected simulated crash error, got %v", err1)
	}
	if res1 != nil {
		t.Fatalf("expected nil result on crash, got %+v", res1)
	}

	// Verify Step 1 state:
	if s.calls.Load() != 1 {
		t.Fatalf("expected 1 LLM call, got %d", s.calls.Load())
	}
	cur, err := journal.LoadCursor(ackPath)
	if err != nil || cur.Offset != 0 {
		t.Fatalf("cursor advanced despite crash: %v", cur)
	}

	// 2. Retry: reuses artifact, recovers DeliveryRequest snapshot, no second LLM call, commits cursor, promotes to PENDING
	res2, err2 := digest.RunTransaction(context.Background(), s, opts)
	if err2 != nil {
		t.Fatalf("retry failed: %v", err2)
	}
	if !res2.WasIdempotentHit {
		t.Fatal("expected WasIdempotentHit to be true on retry")
	}
	if s.calls.Load() != 1 {
		t.Fatalf("LLM call count changed: expected 1, got %d", s.calls.Load())
	}

	rec, err := delSvc.GetDelivery(res2.Batch.BatchID)
	if err != nil || rec == nil {
		t.Fatalf("expected delivery record, got %v, rec: %+v", err, rec)
	}
	if rec.State != StatePending {
		t.Fatalf("expected StatePending, got %s", rec.State)
	}
	if rec.DestinationID != "chat-orig-111" {
		t.Fatalf("expected destination chat-orig-111 preserved from artifact snapshot, got %s", rec.DestinationID)
	}

	cur2, err := journal.LoadCursor(ackPath)
	if err != nil || cur2.Offset == 0 {
		t.Fatal("expected cursor to be committed after retry")
	}
}

// Scenario B:
// PREPARED durable -> crash before cursor -> fresh Core startup -> PREPARED remains non-sendable -> ZERO Telegram calls
func TestCrashInjection_ScenarioB(t *testing.T) {
	exchangeDir, dataDir, store := setupMatrixEnv(t)
	ackPath := filepath.Join(exchangeDir, journal.DefaultAckFilename)

	var telegramCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/botfake-token/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		telegramCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1234}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	delSvc, err := NewService(ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token, WithBaseURL(srv.URL))
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	batchID := strings.Repeat("b", 64)
	targetCur := journal.Cursor{Version: 1, Segment: 1, Offset: 500}
	req := &digest.DeliveryRequest{
		Provider: "telegram",
		ChatID:   "123456",
	}

	// Persist PREPARED record targeting cursor offset 500
	_, err = delSvc.PrepareDeliveryIntent(batchID, targetCur, req)
	if err != nil {
		t.Fatalf("PrepareDeliveryIntent failed: %v", err)
	}

	// Simulate crash before cursor commit: ackPath remains at offset 0
	cur, _ := journal.LoadCursor(ackPath)
	if cur.Offset != 0 {
		t.Fatalf("expected cursor offset 0, got %d", cur.Offset)
	}

	// Fresh Core startup: run ScanAndResume
	freshSvc, err := NewService(ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token, WithBaseURL(srv.URL))
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	freshSvc.ScanAndResume(context.Background())

	// Verify PREPARED remains non-sendable, NOT queued, and ZERO Telegram calls
	if len(freshSvc.queue) != 0 {
		t.Fatalf("expected empty queue for uncommitted PREPARED, got %d items", len(freshSvc.queue))
	}
	rec, err := freshSvc.GetDelivery(batchID)
	if err != nil || rec.State != StatePrepared {
		t.Fatalf("expected StatePrepared preserved, got: %+v", rec)
	}
	if telegramCalls.Load() != 0 {
		t.Fatalf("expected 0 Telegram calls, got %d", telegramCalls.Load())
	}
}

// Scenario C:
// PREPARED durable -> cursor committed -> crash before PENDING promotion -> fresh startup -> detects Core ack beyond cursor_end -> promotes PENDING -> becomes sendable
func TestCrashInjection_ScenarioC(t *testing.T) {
	exchangeDir, dataDir, store := setupMatrixEnv(t)
	ackPath := filepath.Join(exchangeDir, journal.DefaultAckFilename)

	delSvc, err := NewService(ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	batchID := strings.Repeat("c", 64)
	targetCur := journal.Cursor{Version: 1, Segment: 1, Offset: 500}
	req := &digest.DeliveryRequest{
		Provider: "telegram",
		ChatID:   "123456",
	}

	// 1. PREPARED is durable on disk
	_, err = delSvc.PrepareDeliveryIntent(batchID, targetCur, req)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Cursor commits to targetCur
	if err := journal.SaveCursor(ackPath, &targetCur); err != nil {
		t.Fatal(err)
	}

	// 3. Crash before PromoteDeliveryIntent was called (record on disk is still StatePrepared)
	recBefore, _ := delSvc.GetDelivery(batchID)
	if recBefore.State != StatePrepared {
		t.Fatalf("expected StatePrepared before restart, got %s", recBefore.State)
	}

	// 4. Fresh startup runs ScanAndResume
	freshSvc, err := NewService(ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	freshSvc.ScanAndResume(context.Background())

	// 5. Must detect Core ack >= targetCur, promote to StatePending, and enqueue
	recAfter, err := freshSvc.GetDelivery(batchID)
	if err != nil || recAfter.State != StatePending {
		t.Fatalf("expected StatePending after promotion, got %+v", recAfter)
	}

	select {
	case qID := <-freshSvc.queue:
		if qID != batchID {
			t.Fatalf("expected enqueued batch %s, got %s", batchID, qID)
		}
	default:
		t.Fatal("expected batch to be enqueued after promotion")
	}
}

// Scenario D:
// PENDING durable -> crash before worker wake -> startup discovers it
func TestCrashInjection_ScenarioD(t *testing.T) {
	exchangeDir, dataDir, store := setupMatrixEnv(t)

	batchID := strings.Repeat("d", 64)
	rec := &DeliveryRecord{
		Version:       CurrentDeliveryRecordVersion,
		DigestBatchID: batchID,
		DestinationID: "123456",
		State:         StatePending,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := SaveDeliveryRecord(dataDir, rec); err != nil {
		t.Fatal(err)
	}

	// Simulate fresh startup
	freshSvc, err := NewService(ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	freshSvc.ScanAndResume(context.Background())

	select {
	case qID := <-freshSvc.queue:
		if qID != batchID {
			t.Fatalf("expected %s, got %s", batchID, qID)
		}
	default:
		t.Fatal("expected pending delivery to be discovered on startup")
	}
}

// Scenario E:
// queue full -> PENDING stays durable/discoverable
func TestCrashInjection_ScenarioE(t *testing.T) {
	exchangeDir, dataDir, store := setupMatrixEnv(t)
	delSvc, err := NewService(ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Fill queue to capacity (64)
	for i := 0; i < 64; i++ {
		dummyID := fmt.Sprintf("000000000000000000000000000000000000000000000000000000000000%04d", i)
		delSvc.queue <- dummyID
	}

	targetBatchID := strings.Repeat("e", 64)
	err = delSvc.Enqueue(targetBatchID)
	if err != nil {
		t.Fatalf("Enqueue blocked or failed on full queue: %v", err)
	}

	rec, err := delSvc.GetDelivery(targetBatchID)
	if err != nil || rec == nil || rec.State != StatePending {
		t.Fatalf("expected durable StatePending record, got %+v", rec)
	}

	// Drain queue
	for len(delSvc.queue) > 0 {
		<-delSvc.queue
	}

	delSvc.ScanAndResume(context.Background())
	found := false
	for len(delSvc.queue) > 0 {
		b := <-delSvc.queue
		if b == targetBatchID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected target batch to be discoverable after queue drained")
	}
}

// Scenario F:
// destination changes A -> B after artifact creation -> recovered historical intent still targets A
func TestCrashInjection_ScenarioF(t *testing.T) {
	exchangeDir, dataDir, store := setupMatrixEnv(t)
	delSvc, err := NewService(ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	batchID := strings.Repeat("f", 64)
	digestsDir := filepath.Join(dataDir, "digests")
	_ = os.MkdirAll(digestsDir, 0755)
	art := &digest.Artifact{
		Version: journal.CurrentSchemaVersion,
		BatchID: batchID,
		DeliveryRequest: &digest.DeliveryRequest{
			Provider:  "telegram",
			ChatID:    "chat-A-original",
			ChatLabel: "Channel A",
		},
		Digest: &digest.Digest{Title: "Title A"},
	}
	if err := digest.SaveArtifact(digestsDir, art); err != nil {
		t.Fatal(err)
	}

	// Change active configuration to Chat B
	_ = store.SaveDeliveryConfig(config.DeliveryConfig{
		Telegram: config.TelegramConfig{
			Enabled:   true,
			ChatID:    "chat-B-new",
			ChatLabel: "Channel B",
		},
	})

	// Recover / Ensure delivery intent for batchID
	rec, err := delSvc.EnsureDeliveryIntent(batchID)
	if err != nil {
		t.Fatalf("EnsureDeliveryIntent failed: %v", err)
	}

	// Must target Chat A from artifact snapshot, NOT Chat B from current config!
	if rec.DestinationID != "chat-A-original" {
		t.Fatalf("expected destination to remain chat-A-original, got %s", rec.DestinationID)
	}
}

// Scenario G:
// outbox preparation failure -> cursor does NOT advance
func TestCrashInjection_ScenarioG(t *testing.T) {
	exchangeDir, dataDir, _ := setupMatrixEnv(t)
	ackPath := filepath.Join(exchangeDir, journal.DefaultAckFilename)

	s := &matrixSummarizer{
		digest: &digest.Digest{
			Title:    "Valid Title",
			Overview: "Valid Overview",
			Items: []digest.Item{
				{Kind: digest.KindFinding, Text: "Finding", SourceIDs: []string{"S000001"}},
			},
		},
	}

	opts := digest.TransactionOptions{
		ExchangeDir:  exchangeDir,
		DataDir:      dataDir,
		ProviderName: "mock",
		ModelName:    "test-model",
		Commit:       true,
		DeliveryRequest: &digest.DeliveryRequest{
			Provider: "telegram",
			ChatID:   "123",
		},
		PrepareDeliveryIntentFn: func(batchID string, targetCur journal.Cursor, req *digest.DeliveryRequest) error {
			return errors.New("simulated disk write failure during outbox preparation")
		},
	}

	_, err := digest.RunTransaction(context.Background(), s, opts)
	if err == nil || !strings.Contains(err.Error(), "simulated disk write failure") {
		t.Fatalf("expected outbox failure error, got: %v", err)
	}

	cur, err := journal.LoadCursor(ackPath)
	if err != nil || cur.Offset != 0 {
		t.Fatalf("cursor advanced despite outbox preparation failure: %+v", cur)
	}
}

// Scenario H:
// artifact replay after failure -> LLM call count remains exactly 1
func TestCrashInjection_ScenarioH(t *testing.T) {
	exchangeDir, dataDir, store := setupMatrixEnv(t)
	delSvc, err := NewService(ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	s := &matrixSummarizer{
		digest: &digest.Digest{
			Title:    "Valid Title",
			Overview: "Valid Overview",
			Items: []digest.Item{
				{Kind: digest.KindFinding, Text: "Finding", SourceIDs: []string{"S000001"}},
			},
		},
	}

	failOnce := true
	opts := digest.TransactionOptions{
		ExchangeDir:  exchangeDir,
		DataDir:      dataDir,
		ProviderName: "mock",
		ModelName:    "test-model",
		Commit:       true,
		DeliveryRequest: &digest.DeliveryRequest{
			Provider: "telegram",
			ChatID:   "123",
		},
		PrepareDeliveryIntentFn: func(batchID string, targetCur journal.Cursor, req *digest.DeliveryRequest) error {
			if failOnce {
				failOnce = false
				return errors.New("fail once")
			}
			_, err := delSvc.PrepareDeliveryIntent(batchID, targetCur, req)
			return err
		},
		PromoteDeliveryIntentFn: func(batchID string) error {
			_, err := delSvc.PromoteDeliveryIntent(batchID)
			return err
		},
	}

	// 1. First run fails
	_, err = digest.RunTransaction(context.Background(), s, opts)
	if err == nil {
		t.Fatal("expected error on first run")
	}
	if s.calls.Load() != 1 {
		t.Fatalf("expected 1 call after first run, got %d", s.calls.Load())
	}

	// 2. Retry succeeds without second LLM call
	res2, err2 := digest.RunTransaction(context.Background(), s, opts)
	if err2 != nil {
		t.Fatalf("retry failed: %v", err2)
	}
	if !res2.WasIdempotentHit {
		t.Fatal("expected WasIdempotentHit")
	}
	if s.calls.Load() != 1 {
		t.Fatalf("LLM call count changed: expected 1, got %d", s.calls.Load())
	}
}

// Scenario I:
// Telegram success -> process death before success persistence -> UNCERTAIN -> ZERO automatic duplicate request
func TestCrashInjection_ScenarioI(t *testing.T) {
	exchangeDir, dataDir, store := setupMatrixEnv(t)
	var sendCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/botfake-token/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		sendCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"message_id": 9999},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	batchID := strings.Repeat("1", 64)
	digestsDir := filepath.Join(dataDir, "digests")
	_ = os.MkdirAll(digestsDir, 0755)
	art := &digest.Artifact{
		BatchID: batchID,
		Digest:  &digest.Digest{Title: "Test Single Chunk"},
	}
	_ = digest.SaveArtifact(digestsDir, art)

	// Simulate state where Telegram received the request (part 0 was in-flight),
	// but process died before the confirmation could be persisted (InFlightPart is still 0)
	inFlight := 0
	rec := &DeliveryRecord{
		Version:       CurrentDeliveryRecordVersion,
		DigestBatchID: batchID,
		DestinationID: "123456",
		State:         StateSending,
		InFlightPart:  &inFlight,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := SaveDeliveryRecord(dataDir, rec); err != nil {
		t.Fatal(err)
	}

	// Boot fresh service instance (simulating restart after process death)
	freshSvc, err := NewService(ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token, WithBaseURL(srv.URL))
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. ScanAndResume must detect unresolved in-flight attempt and transition to UNCERTAIN
	freshSvc.ScanAndResume(context.Background())

	recAfter, err := freshSvc.GetDelivery(batchID)
	if err != nil || recAfter.State != StateUncertain {
		t.Fatalf("expected StateUncertain, got %+v", recAfter)
	}

	// 2. Unforced deliver attempt MUST be refused
	_, err = freshSvc.DeliverBatch(context.Background(), batchID, false)
	if err == nil || !strings.Contains(err.Error(), "operator confirmation required") {
		t.Fatalf("expected operator confirmation required error, got: %v", err)
	}

	// 3. Telegram API must NOT be called again
	if sendCalls.Load() != 0 {
		t.Fatalf("expected 0 subsequent Telegram calls, got %d", sendCalls.Load())
	}
}

// Scenario J:
// multipart confirmed part boundary -> next unattempted part resumes safely
func TestCrashInjection_ScenarioJ(t *testing.T) {
	exchangeDir, dataDir, store := setupMatrixEnv(t)
	var sentParts []int
	mux := http.NewServeMux()
	mux.HandleFunc("/botfake-token/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		txt := fmt.Sprintf("%v", body["text"])
		if strings.Contains(txt, "1/2") {
			sentParts = append(sentParts, 0)
		} else if strings.Contains(txt, "2/2") {
			sentParts = append(sentParts, 1)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"message_id": 7000 + len(sentParts)},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	largeItems := make([]digest.Item, 40)
	for i := range largeItems {
		largeItems[i] = digest.Item{
			Kind:      digest.KindFinding,
			Text:      fmt.Sprintf("Item %d: %s", i+1, strings.Repeat("Data replication. ", 5)),
			SourceIDs: []string{fmt.Sprintf("S%06d", i+1)},
		}
	}
	batchID := strings.Repeat("2", 64)
	digestsDir := filepath.Join(dataDir, "digests")
	_ = os.MkdirAll(digestsDir, 0755)
	art := &digest.Artifact{
		BatchID: batchID,
		Digest: &digest.Digest{
			Title: "Multipart Digest",
			Items: largeItems,
		},
	}
	_ = digest.SaveArtifact(digestsDir, art)

	chunks := RenderTelegramHTML(art.Digest, nil, nil)
	totalParts := len(chunks)

	// Safe boundary: Part 0 confirmed (NextPart = 1, InFlightPart = nil)
	rec := &DeliveryRecord{
		Version:            CurrentDeliveryRecordVersion,
		DigestBatchID:      batchID,
		DestinationID:      "123456",
		State:              StateSending,
		NextPart:           1, // Part 0 already confirmed
		TotalParts:         totalParts,
		TelegramMessageIDs: []int64{7001},
		InFlightPart:       nil, // Not in-flight!
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
	}
	if err := SaveDeliveryRecord(dataDir, rec); err != nil {
		t.Fatal(err)
	}

	freshSvc, err := NewService(ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token, WithBaseURL(srv.URL))
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Safe boundary resumes remaining parts
	resRec, err := freshSvc.DeliverBatch(context.Background(), batchID, false)
	if err != nil {
		t.Fatalf("DeliverBatch failed on safe boundary resumption: %v", err)
	}

	if resRec.State != StateSent {
		t.Fatalf("expected StateSent, got %s", resRec.State)
	}

	// Part 0 was NOT sent again
	for _, p := range sentParts {
		if p == 0 {
			t.Fatal("Part 0 was resent across safe boundary!")
		}
	}
	if len(resRec.TelegramMessageIDs) != totalParts {
		t.Fatalf("expected %d total confirmed parts, got %d", totalParts, len(resRec.TelegramMessageIDs))
	}
}

// Scenario K:
// multipart in-flight ambiguous part -> UNCERTAIN -> no resend
func TestCrashInjection_ScenarioK(t *testing.T) {
	exchangeDir, dataDir, store := setupMatrixEnv(t)
	var callCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/botfake-token/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 8888}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	batchID := strings.Repeat("3", 64)
	digestsDir := filepath.Join(dataDir, "digests")
	_ = os.MkdirAll(digestsDir, 0755)
	art := &digest.Artifact{
		BatchID: batchID,
		Digest:  &digest.Digest{Title: "Multipart Digest K"},
	}
	_ = digest.SaveArtifact(digestsDir, art)

	// Part 0 confirmed, but Part 1 was in-flight when process died
	inFlight := 1
	rec := &DeliveryRecord{
		Version:            CurrentDeliveryRecordVersion,
		DigestBatchID:      batchID,
		DestinationID:      "123456",
		State:              StateSending,
		NextPart:           1,
		TotalParts:         2,
		TelegramMessageIDs: []int64{7001},
		InFlightPart:       &inFlight, // Part 1 ambiguous!
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
	}
	if err := SaveDeliveryRecord(dataDir, rec); err != nil {
		t.Fatal(err)
	}

	freshSvc, err := NewService(ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token, WithBaseURL(srv.URL))
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	freshSvc.ScanAndResume(context.Background())

	recAfter, err := freshSvc.GetDelivery(batchID)
	if err != nil || recAfter.State != StateUncertain {
		t.Fatalf("expected StateUncertain on ambiguous in-flight part, got: %+v", recAfter)
	}

	_, err = freshSvc.DeliverBatch(context.Background(), batchID, false)
	if err == nil || !strings.Contains(err.Error(), "operator confirmation required") {
		t.Fatalf("expected operator confirmation required error, got: %v", err)
	}

	if callCount.Load() != 0 {
		t.Fatalf("expected zero calls to Telegram for ambiguous part, got %d", callCount.Load())
	}
}

// Invariant Test: PREPARED must be structurally non-sendable, even when force=true.
// Only Core ack commit + reconciliation may promote to PENDING and enable delivery.
func TestPrepared_StructurallyNonSendable(t *testing.T) {
	exchangeDir, dataDir, store := setupMatrixEnv(t)
	ackPath := filepath.Join(exchangeDir, journal.DefaultAckFilename)

	var callCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/botfake-token/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 12345}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	delSvc, err := NewService(ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token, WithBaseURL(srv.URL))
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	batchID := strings.Repeat("5", 64)
	digestsDir := filepath.Join(dataDir, "digests")
	_ = os.MkdirAll(digestsDir, 0755)
	art := &digest.Artifact{
		BatchID: batchID,
		Digest: &digest.Digest{
			Title: "Test Prepared Digest",
			Items: []digest.Item{
				{Kind: digest.KindFinding, Text: "Finding 1", SourceIDs: []string{"S000001"}},
			},
		},
	}
	_ = digest.SaveArtifact(digestsDir, art)

	targetCur := journal.Cursor{Version: 1, Segment: 1, Offset: 500}
	req := &digest.DeliveryRequest{Provider: "telegram", ChatID: "123456"}
	_, err = delSvc.PrepareDeliveryIntent(batchID, targetCur, req)
	if err != nil {
		t.Fatal(err)
	}

	// Invariant A: PREPARED + force=false -> REFUSED, zero Telegram calls
	recA, errA := delSvc.DeliverBatch(context.Background(), batchID, false)
	if errA == nil || !strings.Contains(errA.Error(), "transaction is not yet committed") {
		t.Fatalf("expected uncommitted transaction error for force=false, got: %v", errA)
	}
	if recA == nil || recA.State != StatePrepared {
		t.Fatalf("expected rec state to remain StatePrepared, got %+v", recA)
	}
	if callCount.Load() != 0 {
		t.Fatalf("expected zero Telegram calls for force=false, got %d", callCount.Load())
	}

	// Invariant B: PREPARED + force=true -> ALSO REFUSED, zero Telegram calls
	recB, errB := delSvc.DeliverBatch(context.Background(), batchID, true)
	if errB == nil || !strings.Contains(errB.Error(), "transaction is not yet committed") {
		t.Fatalf("expected uncommitted transaction error for force=true, got: %v", errB)
	}
	if recB == nil || recB.State != StatePrepared {
		t.Fatalf("expected rec state to remain StatePrepared, got %+v", recB)
	}
	if callCount.Load() != 0 {
		t.Fatalf("expected zero Telegram calls for force=true, got %d", callCount.Load())
	}

	// Invariant C: Committed Core ack (ack >= target) -> reconciliation promotes to PENDING -> normal delivery becomes eligible
	cur := &journal.Cursor{Version: 1, Segment: 1, Offset: 500}
	if err := journal.SaveCursor(ackPath, cur); err != nil {
		t.Fatal(err)
	}

	delSvc.ScanAndResume(context.Background())

	recAfterReconcile, err := delSvc.GetDelivery(batchID)
	if err != nil || recAfterReconcile.State != StatePending {
		t.Fatalf("expected StatePending after committed reconciliation, got: %+v", recAfterReconcile)
	}

	recDelivered, err := delSvc.DeliverBatch(context.Background(), batchID, false)
	if err != nil {
		t.Fatalf("DeliverBatch failed after reconciliation to PENDING: %v", err)
	}
	if recDelivered.State != StateSent {
		t.Fatalf("expected StateSent after delivery, got: %s", recDelivered.State)
	}
	if callCount.Load() != 1 {
		t.Fatalf("expected exactly 1 Telegram call after promotion, got %d", callCount.Load())
	}
}

// Invariant Test: Telegram returns 200 success, but local disk persistence of confirmation fails.
// Asserts: DeliverBatch stops immediately, part 1 is never attempted, exactly 1 HTTP request made,
// durable state retains unresolved in-flight marker, and fresh ScanAndResume marks UNCERTAIN.
func TestMultipart_SaveConfirmationFailure(t *testing.T) {
	exchangeDir, dataDir, store := setupMatrixEnv(t)

	var callCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/botfake-token/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"message_id": 9991},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Create multipart digest with at least 2 parts (40 large items)
	largeItems := make([]digest.Item, 40)
	for i := range largeItems {
		largeItems[i] = digest.Item{
			Kind:      digest.KindFinding,
			Text:      fmt.Sprintf("Item %d: %s", i+1, strings.Repeat("Data replication integrity. ", 5)),
			SourceIDs: []string{fmt.Sprintf("S%06d", i+1)},
		}
	}
	batchID := strings.Repeat("4", 64)
	digestsDir := filepath.Join(dataDir, "digests")
	_ = os.MkdirAll(digestsDir, 0755)
	art := &digest.Artifact{
		BatchID: batchID,
		Digest: &digest.Digest{
			Title: "Multipart Save Failure Digest",
			Items: largeItems,
		},
	}
	if err := digest.SaveArtifact(digestsDir, art); err != nil {
		t.Fatal(err)
	}

	chunks := RenderTelegramHTML(art.Digest, nil, nil)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	delSvc, err := NewService(ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token, WithBaseURL(srv.URL))
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	targetCur := journal.Cursor{Version: 1, Segment: 1, Offset: 500}
	req := &digest.DeliveryRequest{Provider: "telegram", ChatID: "123456"}
	if _, err := delSvc.PrepareDeliveryIntent(batchID, targetCur, req); err != nil {
		t.Fatal(err)
	}
	if _, err := delSvc.PromoteDeliveryIntent(batchID); err != nil {
		t.Fatal(err)
	}

	// Inject SaveDeliveryRecord failure specifically at confirmation persistence
	testHookConfirmationSave = func(candidate *DeliveryRecord) error {
		return errors.New("simulated confirmation disk write failure")
	}
	defer func() { testHookConfirmationSave = nil }()

	// Execute DeliverBatch
	resRec, err := delSvc.DeliverBatch(context.Background(), batchID, false)
	if err == nil || !strings.Contains(err.Error(), "simulated confirmation disk write failure") {
		t.Fatalf("expected simulated confirmation disk write failure error, got: %v", err)
	}

	// 1. Delivery function stopped immediately and part 1 was NEVER attempted
	if callCount.Load() != 1 {
		t.Fatalf("expected fake Telegram to receive exactly ONE request, got %d", callCount.Load())
	}

	// 2. Returned in-memory rec must retain in-flight marker (not optimistically cleared)
	if resRec.InFlightPart == nil || *resRec.InFlightPart != 0 {
		t.Fatalf("expected in-memory record to preserve in-flight marker 0, got: %+v", resRec)
	}

	// 3. Durable on-disk record still represents part 0 as in-flight / unresolved
	recDisk, err := LoadDeliveryRecord(dataDir, batchID)
	if err != nil {
		t.Fatalf("failed loading delivery record from disk: %v", err)
	}
	if recDisk.State != StateSending {
		t.Fatalf("expected durable record StateSending, got %s", recDisk.State)
	}
	if recDisk.InFlightPart == nil || *recDisk.InFlightPart != 0 {
		t.Fatalf("expected durable record to retain InFlightPart=0, got: %v", recDisk.InFlightPart)
	}
	if recDisk.NextPart != 0 {
		t.Fatalf("expected durable NextPart=0, got %d", recDisk.NextPart)
	}

	// 4. Fresh service ScanAndResume classifies unresolved in-flight attempt as UNCERTAIN
	freshSvc, err := NewService(ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token, WithBaseURL(srv.URL))
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	freshSvc.ScanAndResume(context.Background())

	recAfter, err := freshSvc.GetDelivery(batchID)
	if err != nil || recAfter == nil {
		t.Fatalf("failed loading record after ScanAndResume: %v", err)
	}
	if recAfter.State != StateUncertain {
		t.Fatalf("expected StateUncertain on fresh ScanAndResume, got: %s", recAfter.State)
	}
	if recAfter.InFlightPart != nil {
		t.Fatalf("expected InFlightPart nil after being marked uncertain, got %v", *recAfter.InFlightPart)
	}

	// 5. Subsequent unforced delivery attempts make ZERO additional Telegram requests
	_, retryErr := freshSvc.DeliverBatch(context.Background(), batchID, false)
	if retryErr == nil || !strings.Contains(retryErr.Error(), "operator confirmation required") {
		t.Fatalf("expected operator confirmation required error on unforced retry, got: %v", retryErr)
	}
	if callCount.Load() != 1 {
		t.Fatalf("expected call count to remain exactly 1 (zero subsequent requests), got %d", callCount.Load())
	}
}
