package delivery

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cordbrief/internal/catalog"
	"cordbrief/internal/config"
	"cordbrief/internal/digest"
	"cordbrief/internal/inbox"
	"cordbrief/internal/journal"
)

// testHookBeforeSuccessPersist allows deterministic testing of process death
// immediately after network receipt but prior to local disk persistence.
var testHookBeforeSuccessPersist func()

// testHookConfirmationSave allows deterministic testing of local disk persistence failure
// specifically when persisting the confirmed delivery state after network success.
var testHookConfirmationSave func(rec *DeliveryRecord) error

// Service coordinates Telegram delivery of digests.
type Service struct {
	mu           sync.Mutex
	dataDir      string
	exchangeDir  string
	store        *config.Store
	clientGetter func(token string) *TelegramClient
	inboxService *inbox.Service

	queue     chan string
	done      chan struct{}
	closeOnce sync.Once
}

// ServiceOptions configures the delivery service.
type ServiceOptions struct {
	DataDir      string
	ExchangeDir  string
	Store        *config.Store
	ClientGetter func(token string) *TelegramClient
}

// NewService initializes the delivery service and in-process background delivery worker.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Store == nil {
		return nil, errors.New("config.Store is required")
	}
	if opts.DataDir == "" {
		opts.DataDir = "/var/cordbrief/data"
	}
	if opts.ExchangeDir == "" {
		opts.ExchangeDir = "/var/cordbrief/exchange"
	}

	clientGetter := opts.ClientGetter
	if clientGetter == nil {
		clientGetter = func(token string) *TelegramClient {
			return NewTelegramClient(token)
		}
	}

	s := &Service{
		dataDir:      opts.DataDir,
		exchangeDir:  opts.ExchangeDir,
		store:        opts.Store,
		clientGetter: clientGetter,
		inboxService: inbox.NewService(filepath.Join(opts.DataDir, "digests")),
		queue:        make(chan string, 64),
		done:         make(chan struct{}),
	}

	return s, nil
}

// StartWorker launches the background worker in the Core process.
func (s *Service) StartWorker(ctx context.Context) {
	// 1. Scan and resume safe pending deliveries on startup
	s.ScanAndResume(ctx)

	// 2. Consume from queue
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case batchID := <-s.queue:
			if _, err := s.DeliverBatch(ctx, batchID, false); err != nil {
				log.Printf("[Delivery] Failed processing batch %s: %v", batchID, err)
			}
		}
	}
}

// Close stops the background worker.
func (s *Service) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
	})
}

// GetDelivery returns the current delivery state for a batch, or nil if none exists.
func (s *Service) GetDelivery(batchID string) (*DeliveryRecord, error) {
	rec, err := LoadDeliveryRecord(s.dataDir, batchID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return rec, nil
}

// PrepareDeliveryIntent atomically creates a non-sendable PREPARED delivery record on disk.
// It snapshots destination identity and the intended target cursor boundary.
func (s *Service) PrepareDeliveryIntent(batchID string, targetCur journal.Cursor, req *digest.DeliveryRequest) (*DeliveryRecord, error) {
	trimmed := strings.TrimSpace(batchID)
	if err := digest.ValidateBatchID(trimmed); err != nil {
		return nil, fmt.Errorf("invalid batch_id: %w", err)
	}

	rec, err := s.GetDelivery(trimmed)
	if err != nil {
		return nil, err
	}
	if rec != nil {
		return rec, nil
	}

	destID := ""
	destLabel := ""
	if req != nil {
		destID = req.ChatID
		destLabel = req.ChatLabel
	} else {
		delCfg := s.store.GetDeliveryConfig()
		destID = delCfg.Telegram.ChatID
		destLabel = delCfg.Telegram.ChatLabel
	}

	rec = &DeliveryRecord{
		Version:          CurrentDeliveryRecordVersion,
		DigestBatchID:    trimmed,
		DestinationID:    destID,
		DestinationLabel: destLabel,
		State:            StatePrepared,
		TargetCursorEnd:  &targetCur,
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}
	if err := SaveDeliveryRecord(s.dataDir, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// PromoteDeliveryIntent atomically transitions a PREPARED delivery record to sendable PENDING
// and signals the background worker.
func (s *Service) PromoteDeliveryIntent(batchID string) (*DeliveryRecord, error) {
	trimmed := strings.TrimSpace(batchID)
	if err := digest.ValidateBatchID(trimmed); err != nil {
		return nil, fmt.Errorf("invalid batch_id: %w", err)
	}

	rec, err := s.GetDelivery(trimmed)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, fmt.Errorf("delivery record not found for batch %s", trimmed)
	}

	if rec.State == StatePrepared {
		rec.State = StatePending
		rec.UpdatedAt = time.Now().UTC()
		if err := SaveDeliveryRecord(s.dataDir, rec); err != nil {
			return nil, fmt.Errorf("promote to pending: %w", err)
		}
		s.WakeWorker(trimmed)
	} else if rec.State == StatePending {
		s.WakeWorker(trimmed)
	}
	return rec, nil
}

// EnsureDeliveryIntent is a convenience helper for manual/CLI operations.
// Creates prepared intent and promotes to pending if transaction is committed.
func (s *Service) EnsureDeliveryIntent(batchID string) (*DeliveryRecord, error) {
	trimmed := strings.TrimSpace(batchID)
	if err := digest.ValidateBatchID(trimmed); err != nil {
		return nil, fmt.Errorf("invalid batch_id: %w", err)
	}

	rec, err := s.GetDelivery(trimmed)
	if err != nil {
		return nil, err
	}
	if rec != nil {
		if rec.State == StatePrepared {
			return s.PromoteDeliveryIntent(trimmed)
		}
		return rec, nil
	}

	delCfg := s.store.GetDeliveryConfig()
	req := &digest.DeliveryRequest{
		Provider:  "telegram",
		ChatID:    delCfg.Telegram.ChatID,
		ChatLabel: delCfg.Telegram.ChatLabel,
	}

	var targetCur journal.Cursor
	if art, err := s.inboxService.GetDigest(trimmed); err == nil && art != nil {
		targetCur = art.CursorEnd
		if art.DeliveryRequest != nil {
			req = art.DeliveryRequest
		}
	}

	if _, err := s.PrepareDeliveryIntent(trimmed, targetCur, req); err != nil {
		return nil, err
	}
	return s.PromoteDeliveryIntent(trimmed)
}

// WakeWorker wakes the background delivery worker for batchID if queue has capacity.
// If queue is full, the pending state remains safely durable on disk and is picked up by ScanAndResume.
func (s *Service) WakeWorker(batchID string) {
	trimmed := strings.TrimSpace(batchID)
	select {
	case s.queue <- trimmed:
	default:
		// Queue full; pending record is durable and discovered by ScanAndResume
	}
}

// Enqueue ensures durable delivery intent on disk and signals the background worker.
func (s *Service) Enqueue(batchID string) error {
	trimmed := strings.TrimSpace(batchID)
	if _, err := s.EnsureDeliveryIntent(trimmed); err != nil {
		return err
	}
	s.WakeWorker(trimmed)
	return nil
}

// ScanAndResume inspects persisted delivery records on disk and resumes safe pending attempts.
// CRITICAL: Does NOT automatically touch StateUncertain, StateSent, or StateFailed.
// Promotes StatePrepared ONLY if Core ack has advanced to or past target cursor end.
// Marks StateSending as StateUncertain if an unresolved in-flight attempt is detected.
func (s *Service) ScanAndResume(ctx context.Context) {
	ackPath := filepath.Join(s.exchangeDir, journal.DefaultAckFilename)
	ackCur, ackErr := journal.LoadCursor(ackPath)

	deliveriesDir := filepath.Join(s.dataDir, "deliveries")
	entries, err := os.ReadDir(deliveriesDir)
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			batchID := entry.Name()
			rec, err := LoadDeliveryRecord(s.dataDir, batchID)
			if err != nil || rec == nil {
				continue
			}

			switch rec.State {
			case StatePrepared:
				// Check Core ack against target cursor end
				targetCur := rec.TargetCursorEnd
				if targetCur == nil {
					if art, err := s.inboxService.GetDigest(batchID); err == nil && art != nil {
						targetCur = &art.CursorEnd
					}
				}
				if ackErr == nil && targetCur != nil && CursorAtOrPast(*ackCur, *targetCur) {
					log.Printf("[Delivery] Promoting committed prepared delivery for batch %s", batchID)
					_, _ = s.PromoteDeliveryIntent(batchID)
				} else {
					log.Printf("[Delivery] Batch %s remains PREPARED (uncommitted cursor)", batchID)
				}

			case StatePending:
				log.Printf("[Delivery] Resuming pending delivery for batch %s", batchID)
				s.WakeWorker(batchID)

			case StateSending:
				if rec.InFlightPart != nil {
					log.Printf("[Delivery] Batch %s has unresolved in-flight part %d -> marking UNCERTAIN", batchID, *rec.InFlightPart)
					rec.State = StateUncertain
					rec.LastSafeError = fmt.Sprintf("interrupted while part %d was in-flight; outcome is uncertain", *rec.InFlightPart)
					rec.InFlightPart = nil
					_ = SaveDeliveryRecord(s.dataDir, rec)
				} else {
					log.Printf("[Delivery] Resuming sending delivery for batch %s from part %d", batchID, rec.NextPart)
					s.WakeWorker(batchID)
				}

			case StateSent, StateFailed, StateUncertain:
				// Do not touch
			}
		}
	}

	// Reconcile artifacts where durable metadata proves delivery was requested
	// Do NOT automatically infer historical intent if DeliveryRequest is absent
	digestsDir := filepath.Join(s.dataDir, "digests")
	artEntries, err := os.ReadDir(digestsDir)
	if err == nil {
		for _, entry := range artEntries {
			if !entry.IsDir() {
				continue
			}
			batchID := entry.Name()
			art, err := digest.LoadArtifact(digestsDir, batchID)
			if err != nil || art == nil {
				continue
			}
			if art.DeliveryRequest != nil {
				rec, err := s.GetDelivery(batchID)
				if err == nil && rec == nil {
					log.Printf("[Delivery] Reconciling missing outbox for requested delivery batch %s", batchID)
					_, _ = s.PrepareDeliveryIntent(batchID, art.CursorEnd, art.DeliveryRequest)
					if ackErr == nil && CursorAtOrPast(*ackCur, art.CursorEnd) {
						_, _ = s.PromoteDeliveryIntent(batchID)
					}
				}
			}
		}
	}
}

// DeliverBatch processes delivery for a specific digest artifact.
// If force is true, allows retrying from an uncertain or failed state.
func (s *Service) DeliverBatch(ctx context.Context, batchID string, force bool) (*DeliveryRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	trimmedID := strings.TrimSpace(batchID)
	if err := digest.ValidateBatchID(trimmedID); err != nil {
		return nil, fmt.Errorf("invalid batch_id: %w", err)
	}

	rec, err := LoadDeliveryRecord(s.dataDir, trimmedID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load delivery record: %w", err)
	}

	delCfg := s.store.GetDeliveryConfig()
	if rec == nil {
		rec = &DeliveryRecord{
			DigestBatchID:    trimmedID,
			DestinationID:    delCfg.Telegram.ChatID,
			DestinationLabel: delCfg.Telegram.ChatLabel,
			State:            StatePending,
			CreatedAt:        time.Now().UTC(),
			UpdatedAt:        time.Now().UTC(),
		}
	}

	// 1. Guard against sending prepared records whose transaction is uncommitted (structurally non-sendable, even with force)
	if rec.State == StatePrepared {
		return rec, fmt.Errorf("delivery intent is prepared but transaction is not yet committed")
	}

	// 2. Guard against duplicate sending of already sent digests
	if rec.State == StateSent && !force {
		return rec, nil
	}

	// 3. Guard against automatic retry of ambiguous/uncertain outcomes
	if rec.State == StateUncertain && !force {
		return rec, fmt.Errorf("delivery outcome is uncertain; operator confirmation required")
	}

	// If forced retry on uncertain or failed, reset state to pending and attempt progress
	if force && (rec.State == StateUncertain || rec.State == StateFailed) {
		rec.State = StatePending
		rec.LastSafeError = ""
		rec.InFlightPart = nil
	}

	// Ensure destination is set
	if rec.DestinationID == "" {
		rec.DestinationID = delCfg.Telegram.ChatID
		rec.DestinationLabel = delCfg.Telegram.ChatLabel
	}

	// 4. Verify Telegram configuration
	token := s.store.GetTelegramBotToken()
	if token == "" {
		rec.State = StateFailed
		rec.LastSafeError = "telegram bot token is not configured"
		_ = SaveDeliveryRecord(s.dataDir, rec)
		return rec, errors.New(rec.LastSafeError)
	}

	if strings.TrimSpace(rec.DestinationID) == "" {
		rec.State = StateFailed
		rec.LastSafeError = "destination chat_id is not configured"
		_ = SaveDeliveryRecord(s.dataDir, rec)
		return rec, errors.New(rec.LastSafeError)
	}

	// 5. Load durable digest artifact
	art, err := s.inboxService.GetDigest(trimmedID)
	if err != nil {
		rec.State = StateFailed
		rec.LastSafeError = fmt.Sprintf("failed loading digest artifact: %v", err)
		_ = SaveDeliveryRecord(s.dataDir, rec)
		return rec, errors.New(rec.LastSafeError)
	}

	// 6. Reconstruct source message jump links only if legacy artifact lacks SourceRefs
	var sourceMap map[string]digest.SourceMessage
	if art.NeedsJournalSources() {
		sourceMap = s.reconstructSourceMap(art)
	}

	// 7. Render HTML chunks deterministically
	chunks := RenderTelegramHTML(art.Digest, sourceMap, art.SourceRefs)
	if len(chunks) == 0 {
		rec.State = StateFailed
		rec.LastSafeError = "digest rendered 0 message chunks"
		_ = SaveDeliveryRecord(s.dataDir, rec)
		return rec, errors.New(rec.LastSafeError)
	}

	rec.TotalParts = len(chunks)
	rec.AttemptCount++
	rec.State = StateSending
	_ = SaveDeliveryRecord(s.dataDir, rec)

	client := s.clientGetter(token)

	// 8. Send multipart chunks sequentially; NEVER resend already-confirmed parts
	for i := rec.NextPart; i < len(chunks); i++ {
		chunkText := chunks[i]

		// Record in-flight attempt on disk BEFORE sending over the network
		partIdx := i
		rec.InFlightPart = &partIdx
		rec.State = StateSending
		rec.UpdatedAt = time.Now().UTC()
		if err := SaveDeliveryRecord(s.dataDir, rec); err != nil {
			return rec, fmt.Errorf("persist in-flight state: %w", err)
		}

		result, err := client.SendMessage(ctx, rec.DestinationID, chunkText)
		if err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.ErrorCode == 429 && apiErr.RetryAfter > 0 {
				if apiErr.RetryAfter <= 5 {
					select {
					case <-ctx.Done():
						return rec, ctx.Err()
					case <-time.After(time.Duration(apiErr.RetryAfter) * time.Second):
						result, err = client.SendMessage(ctx, rec.DestinationID, chunkText)
					}
				}
			}
		}

		if err != nil {
			rec.InFlightPart = nil
			if errors.Is(err, ErrAmbiguousTransport) {
				rec.State = StateUncertain
				rec.LastSafeError = sanitizeDescription(err.Error(), token)
				_ = SaveDeliveryRecord(s.dataDir, rec)
				return rec, err
			}

			rec.State = StateFailed
			rec.LastSafeError = sanitizeDescription(err.Error(), token)
			_ = SaveDeliveryRecord(s.dataDir, rec)
			return rec, err
		}

		// Confirmed part delivered!
		if testHookBeforeSuccessPersist != nil {
			testHookBeforeSuccessPersist()
		}

		// Construct candidate confirmed state without mutating in-memory rec yet
		candidate := *rec
		candidate.TelegramMessageIDs = append(append([]int64(nil), rec.TelegramMessageIDs...), result.MessageID)
		candidate.NextPart = i + 1
		candidate.InFlightPart = nil
		candidate.LastSafeError = ""
		if candidate.NextPart >= candidate.TotalParts {
			candidate.State = StateSent
		} else {
			candidate.State = StateSending
		}
		candidate.UpdatedAt = time.Now().UTC()

		if testHookConfirmationSave != nil {
			if hookErr := testHookConfirmationSave(&candidate); hookErr != nil {
				return rec, fmt.Errorf("persist delivery confirmation for part %d: %w", i, hookErr)
			}
		}

		if saveErr := SaveDeliveryRecord(s.dataDir, &candidate); saveErr != nil {
			return rec, fmt.Errorf("persist delivery confirmation for part %d: %w", i, saveErr)
		}
		*rec = candidate
	}

	return rec, nil
}

// SendTestMessage sends a non-digest test ping to the specified destination chat.
func (s *Service) SendTestMessage(ctx context.Context, chatID string) (int64, error) {
	token := s.store.GetTelegramBotToken()
	if token == "" {
		return 0, errors.New("telegram bot token is not configured")
	}

	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return 0, errors.New("chat_id is required")
	}

	client := s.clientGetter(token)
	text := "<b>CordBrief Connected Successfully</b>\n\nDaily digests will be delivered here."

	res, err := client.SendMessage(ctx, chatID, text)
	if err != nil {
		return 0, err
	}
	return res.MessageID, nil
}

// TestBot tests bot credentials using the provided token (or stored token if empty).
func (s *Service) TestBot(ctx context.Context, tokenOverride string) (*BotUser, error) {
	token := strings.TrimSpace(tokenOverride)
	if token == "" {
		token = s.store.GetTelegramBotToken()
	}
	if token == "" {
		return nil, errors.New("telegram bot token is not configured")
	}
	client := s.clientGetter(token)
	return client.GetMe(ctx)
}

// DiscoverChats queries getUpdates using the provided token (or stored token if empty)
// and returns safe chat metadata deduplicated without message text.
func (s *Service) DiscoverChats(ctx context.Context, tokenOverride string) ([]ChatInfo, error) {
	token := strings.TrimSpace(tokenOverride)
	if token == "" {
		token = s.store.GetTelegramBotToken()
	}
	if token == "" {
		return nil, errors.New("telegram bot token is not configured")
	}
	client := s.clientGetter(token)
	return client.GetUpdates(ctx, 0)
}

func (s *Service) reconstructSourceMap(art *digest.Artifact) map[string]digest.SourceMessage {
	sourceMap := make(map[string]digest.SourceMessage)
	eventsDir := filepath.Join(s.exchangeDir, "events")
	startCur := art.CursorStart
	if startCur.Version == 0 {
		startCur.Version = journal.CurrentSchemaVersion
	}
	endCur := art.CursorEnd
	if endCur.Version == 0 {
		endCur.Version = journal.CurrentSchemaVersion
	}

	wm, err := journal.CaptureWatermark(eventsDir)
	if err != nil || wm == nil {
		return sourceMap
	}

	reader := journal.NewReader(eventsDir, wm)
	records, _, err := reader.ReadBatch(startCur, art.InputMessageCount+100)
	if err != nil || len(records) == 0 {
		return sourceMap
	}

	cat, _ := catalog.Load(s.exchangeDir)
	batch, err := digest.BuildBatch(records, startCur, endCur, wm, false, cat)
	if err == nil && batch != nil {
		return batch.SourceMap
	}

	return sourceMap
}
