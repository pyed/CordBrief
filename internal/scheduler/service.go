package scheduler

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"cordbrief/internal/config"
	"cordbrief/internal/digest"
	"cordbrief/internal/journal"
	"cordbrief/internal/llm"
)

var (
	bearerRegex = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/-]+=*`)
	keyRegex    = regexp.MustCompile(`(?i)(key|token|secret|password)[=:\s]+["']?([A-Za-z0-9_\-\.]{8,})["']?`)
	geminiRegex = regexp.MustCompile(`AIzaSy[A-Za-z0-9_-]{33}`)
)

// sanitizeLastError returns a bounded, redacted operational error message suitable for durable persistence.
func sanitizeLastError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()

	// Redact known secret and token patterns
	msg = bearerRegex.ReplaceAllString(msg, "Bearer [REDACTED]")
	msg = geminiRegex.ReplaceAllString(msg, "[REDACTED_GEMINI_KEY]")
	msg = keyRegex.ReplaceAllString(msg, "$1=[REDACTED]")

	// Strip newlines/carriage returns to keep single-line JSON log-friendly
	msg = strings.ReplaceAll(msg, "\r", " ")
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.Join(strings.Fields(msg), " ")

	// Bound max length to 256 characters
	const maxLen = 256
	if len(msg) > maxLen {
		msg = msg[:maxLen-3] + "..."
	}
	return msg
}

// TransactionRunner abstracts the execution of a digest transaction.
type TransactionRunner interface {
	RunDigest(ctx context.Context, trigger *digest.TriggerInfo) (*digest.TransactionResult, error)
}

// StateSaver defines the signature for persisting scheduler state.
type StateSaver func(dataDir string, s *State) error

// DeliveryEnqueuer abstracts enqueuing a completed digest batch for external delivery.
type DeliveryEnqueuer interface {
	Enqueue(batchID string) error
}

// ServiceOptions configures the scheduler service.
type ServiceOptions struct {
	Store            *config.Store
	DataDir          string
	Runner           TransactionRunner
	DeliveryEnqueuer DeliveryEnqueuer
	Clock            Clock
	CheckInterval    time.Duration
	// Lock serializes scheduler and web commits. The CLI's commit lock excludes other processes.
	Lock        *sync.Mutex
	SaveStateFn StateSaver // Optional override for testing persistence failures
}

// Service manages the background daily scheduler loop.
type Service struct {
	mu               sync.RWMutex
	store            *config.Store
	dataDir          string
	runner           TransactionRunner
	deliveryEnqueuer DeliveryEnqueuer
	clock            Clock
	checkInterval    time.Duration
	lock             *sync.Mutex
	saveStateFn      StateSaver

	state *State
}

// NewService initializes a new scheduler service.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("config.Store is required")
	}
	if opts.DataDir == "" {
		opts.DataDir = "/var/cordbrief/data"
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.CheckInterval <= 0 {
		opts.CheckInterval = 30 * time.Second
	}
	if opts.Lock == nil {
		opts.Lock = &sync.Mutex{}
	}
	saveFn := opts.SaveStateFn
	if saveFn == nil {
		saveFn = SaveState
	}

	st, err := LoadState(opts.DataDir)
	if err != nil {
		return nil, fmt.Errorf("initializing scheduler state: %w", err)
	}

	return &Service{
		store:            opts.Store,
		dataDir:          opts.DataDir,
		runner:           opts.Runner,
		deliveryEnqueuer: opts.DeliveryEnqueuer,
		clock:            opts.Clock,
		checkInterval:    opts.CheckInterval,
		lock:             opts.Lock,
		saveStateFn:      saveFn,
		state:            st,
	}, nil
}

// GetStatus returns the current scheduling evaluation and persisted state.
func (s *Service) GetStatus() (SlotStatus, State) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg := s.store.GetScheduleConfig()
	now := s.clock()
	slot := EvaluateSlot(cfg, s.state, now)
	return slot, *s.state
}

func (s *Service) saveStateLocked() error {
	return s.saveStateFn(s.dataDir, s.state)
}

// CheckAndRunSlot evaluates current time and executes the digest transaction if due.
// It acquires the single-flight lock first, then performs fresh re-evaluation under s.mu.
// Returns true if a slot was executed.
func (s *Service) CheckAndRunSlot(ctx context.Context) (bool, error) {
	// 1. Acquire single-flight lock across Core process first
	s.lock.Lock()
	defer s.lock.Unlock()

	// 2. Fresh evaluation under s.mu lock
	s.mu.Lock()
	now := s.clock()
	cfg := s.store.GetScheduleConfig()
	eval := EvaluateSlot(cfg, s.state, now)
	if !eval.Due {
		s.mu.Unlock()
		return false, nil
	}

	slotID := eval.SlotID

	// A. BEFORE invoking the digest transaction:
	// Record LastAttemptedSlot / LastAttemptAt and durably persist state.
	// If persistence fails, do NOT call runner and return an operational error.
	s.state.LastAttemptedSlot = slotID
	attemptTime := now
	s.state.LastAttemptAt = &attemptTime

	if err := s.saveStateLocked(); err != nil {
		s.mu.Unlock()
		return false, fmt.Errorf("failed persisting pre-attempt scheduler state: %w", err)
	}

	trigger := &digest.TriggerInfo{
		Type:   "scheduled",
		SlotID: slotID,
	}

	if s.runner == nil {
		s.mu.Unlock()
		return false, fmt.Errorf("transaction runner is nil")
	}

	// Release s.mu while running transaction so readers can inspect state,
	// while s.lock remains held to prevent any concurrent transaction.
	s.mu.Unlock()
	res, runErr := s.runner.RunDigest(ctx, trigger)
	s.mu.Lock()
	defer s.mu.Unlock()

	nowAfterRun := s.clock()

	// B. DIGEST TRANSACTION FAILURE
	if runErr != nil {
		s.state.LastResult = ResultError
		s.state.LastError = sanitizeLastError(runErr)
		s.state.LastBatchID = ""
		nextRetry := nowAfterRun.Add(RetryInterval)
		s.state.NextRetryAt = &nextRetry

		if saveErr := s.saveStateLocked(); saveErr != nil {
			return true, fmt.Errorf("digest run failed: %v; subsequent scheduler state save also failed: %w", runErr, saveErr)
		}
		return true, runErr
	}

	// C. DIGEST TRANSACTION SUCCESS
	s.state.LastCompletedSlot = slotID
	s.state.NextRetryAt = nil
	s.state.LastError = ""

	if res != nil && (res.Empty || res.AllExcluded) {
		s.state.LastResult = ResultEmpty
		s.state.LastBatchID = ""
	} else {
		s.state.LastResult = ResultSuccess
		if res != nil && res.Artifact != nil {
			s.state.LastBatchID = res.Artifact.BatchID
		} else if res != nil && res.Batch != nil {
			s.state.LastBatchID = res.Batch.BatchID
		}
	}

	if saveErr := s.saveStateLocked(); saveErr != nil {
		batchDesc := s.state.LastBatchID
		if batchDesc == "" {
			batchDesc = "empty"
		}
		return true, fmt.Errorf("transaction completed (%s) but saving scheduler state failed: %w", batchDesc, saveErr)
	}

	// Post-transaction delivery: enqueue ONLY after transaction and scheduler state succeed
	if s.deliveryEnqueuer != nil && res != nil && res.Artifact != nil {
		delCfg := s.store.GetDeliveryConfig()
		if delCfg.Telegram.Enabled && s.store.IsTelegramConfigured() && delCfg.Telegram.ChatID != "" {
			_ = s.deliveryEnqueuer.Enqueue(res.Artifact.BatchID)
		}
	}

	return true, nil
}

// Start runs the scheduler loop until ctx is cancelled.
func (s *Service) Start(ctx context.Context) {
	ticker := time.NewTicker(s.checkInterval)
	defer ticker.Stop()

	// Initial check on boot
	if _, err := s.CheckAndRunSlot(ctx); err != nil {
		log.Printf("[Scheduler] Initial check error: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.CheckAndRunSlot(ctx); err != nil {
				log.Printf("[Scheduler] Slot execution error: %v", err)
			}
		}
	}
}

// CoreDigestRunner implements TransactionRunner using the Core pipeline and store.
type CoreDigestRunner struct {
	ExchangeDir      string
	DataDir          string
	Store            *config.Store
	DeliveryPreparer func(batchID string, targetCur journal.Cursor, req *digest.DeliveryRequest) error
	DeliveryPromoter func(batchID string) error
}

// RunDigest constructs the pipeline and executes digest.RunTransaction with commit=true.
func (r *CoreDigestRunner) RunDigest(ctx context.Context, trigger *digest.TriggerInfo) (*digest.TransactionResult, error) {
	appCfg := r.Store.GetAppConfig()
	var apiKey string
	if appCfg.LLM.Provider == config.ProviderGemini {
		apiKey = r.Store.GetGeminiKey()
		if apiKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY is not configured")
		}
	}

	provider := llm.NewOpenAICompatibleProvider(
		appCfg.LLM.BaseURL,
		appCfg.LLM.Model,
		apiKey,
		appCfg.LLM.MaxOutputTokens,
		time.Duration(appCfg.LLM.TimeoutSeconds)*time.Second,
	)

	pipe := llm.NewPipeline(provider, appCfg.LLM.MaxInputChars, llm.PromptConfig{
		Language: appCfg.Digest.OutputLanguage,
		Focus:    appCfg.Digest.Focus,
	})

	delCfg := r.Store.GetDeliveryConfig()
	shouldDeliver := delCfg.Telegram.Enabled && r.Store.IsTelegramConfigured() && delCfg.Telegram.ChatID != ""
	var delReq *digest.DeliveryRequest
	if shouldDeliver {
		delReq = &digest.DeliveryRequest{
			Provider:  "telegram",
			ChatID:    delCfg.Telegram.ChatID,
			ChatLabel: delCfg.Telegram.ChatLabel,
		}
	}

	opts := digest.TransactionOptions{
		ExchangeDir:             r.ExchangeDir,
		DataDir:                 r.DataDir,
		IgnoreBots:              appCfg.Digest.IgnoreBots,
		BatchLimit:              1000,
		ProviderName:            appCfg.LLM.Provider,
		ModelName:               appCfg.LLM.Model,
		Commit:                  true, // Real committing transaction
		Trigger:                 trigger,
		DeliveryRequest:         delReq,
		PrepareDeliveryIntentFn: r.DeliveryPreparer,
		PromoteDeliveryIntentFn: r.DeliveryPromoter,
	}

	return digest.RunTransaction(ctx, pipe, opts)
}
