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

// Enqueue adds a batch to the delivery queue.
func (s *Service) Enqueue(batchID string) error {
	trimmed := strings.TrimSpace(batchID)
	if err := digest.ValidateBatchID(trimmed); err != nil {
		return fmt.Errorf("invalid batch_id: %w", err)
	}

	// Create or ensure initial pending delivery record
	rec, err := s.GetDelivery(trimmed)
	if err != nil {
		return err
	}

	cfg := s.store.GetDeliveryConfig()
	if rec == nil {
		rec = &DeliveryRecord{
			DigestBatchID:    trimmed,
			DestinationID:    cfg.Telegram.ChatID,
			DestinationLabel: cfg.Telegram.ChatLabel,
			State:            StatePending,
			CreatedAt:        time.Now().UTC(),
			UpdatedAt:        time.Now().UTC(),
		}
		if err := SaveDeliveryRecord(s.dataDir, rec); err != nil {
			return err
		}
	}

	select {
	case s.queue <- trimmed:
		return nil
	default:
		// Queue full, state is safely persisted as pending and will be picked up
		return nil
	}
}

// ScanAndResume inspects persisted delivery records on disk and resumes pending attempts.
// CRITICAL: Does NOT automatically touch StateUncertain, StateSent, or StateFailed.
func (s *Service) ScanAndResume(ctx context.Context) {
	deliveriesDir := filepath.Join(s.dataDir, "deliveries")
	entries, err := os.ReadDir(deliveriesDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		batchID := entry.Name()
		rec, err := LoadDeliveryRecord(s.dataDir, batchID)
		if err != nil || rec == nil {
			continue
		}

		// Only resume pending, or sending (which was interrupted by process restart)
		if rec.State == StatePending || rec.State == StateSending {
			log.Printf("[Delivery] Resuming pending delivery for batch %s", batchID)
			_ = s.Enqueue(batchID)
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

	// 1. Guard against duplicate sending of already sent digests
	if rec.State == StateSent && !force {
		return rec, nil
	}

	// 2. Guard against automatic retry of ambiguous/uncertain outcomes
	if rec.State == StateUncertain && !force {
		return rec, fmt.Errorf("delivery outcome is uncertain; operator confirmation required")
	}

	// If forced retry on uncertain or failed, reset state to pending and attempt progress
	if force && (rec.State == StateUncertain || rec.State == StateFailed) {
		rec.State = StatePending
		rec.LastSafeError = ""
	}

	// Ensure destination is current
	if rec.DestinationID == "" {
		rec.DestinationID = delCfg.Telegram.ChatID
		rec.DestinationLabel = delCfg.Telegram.ChatLabel
	}

	// 3. Verify Telegram configuration
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

	// 4. Load durable digest artifact
	art, err := s.inboxService.GetDigest(trimmedID)
	if err != nil {
		rec.State = StateFailed
		rec.LastSafeError = fmt.Sprintf("failed loading digest artifact: %v", err)
		_ = SaveDeliveryRecord(s.dataDir, rec)
		return rec, errors.New(rec.LastSafeError)
	}

	// 5. Reconstruct source message jump links if journal available
	sourceMap := s.reconstructSourceMap(art)

	// 6. Render HTML chunks deterministically
	chunks := RenderTelegramHTML(art.Digest, sourceMap)
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

	// 7. Send multipart chunks sequentially; NEVER resend already-confirmed parts
	for i := rec.NextPart; i < len(chunks); i++ {
		chunkText := chunks[i]

		result, err := client.SendMessage(ctx, rec.DestinationID, chunkText)
		if err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.ErrorCode == 429 && apiErr.RetryAfter > 0 {
				// Rate limit: back off if reasonable
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

		// Confirmed part delivered! Record progress immediately
		rec.TelegramMessageIDs = append(rec.TelegramMessageIDs, result.MessageID)
		rec.NextPart = i + 1
		rec.LastSafeError = ""
		if rec.NextPart >= rec.TotalParts {
			rec.State = StateSent
		} else {
			rec.State = StateSending
		}
		if saveErr := SaveDeliveryRecord(s.dataDir, rec); saveErr != nil {
			log.Printf("[Delivery] Warning: failed saving multipart delivery progress: %v", saveErr)
		}
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
