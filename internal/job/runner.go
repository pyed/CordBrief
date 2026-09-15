package job

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/pyed/CordBrief/internal/brief"
	"github.com/pyed/CordBrief/internal/dce"
	"github.com/pyed/CordBrief/internal/llm"
	"github.com/pyed/CordBrief/internal/state"
)

// DefaultCompleterFactory creates a standard llm.Client for the briefing engine.
func DefaultCompleterFactory(baseURL, model, apiKey string) (brief.Completer, error) {
	return llm.NewClient(baseURL, model, apiKey, nil)
}

// Runner coordinates the end-to-end brief transaction:
// followed channel + persisted cursor -> fixed cutoff -> DCE export -> brief engine -> Telegram delivery -> commit cursor.
type Runner struct {
	store            *state.Store
	dceClient        DCEExporter
	completerFactory CompleterFactory
	deliverer        Deliverer
	llmAPIKey        string
	now              func() time.Time

	mu      sync.Mutex
	running bool
	wg      sync.WaitGroup
}

// Option configures Runner instances.
type Option func(*Runner)

// WithNow sets a custom clock function (used for testing).
func WithNow(now func() time.Time) Option {
	return func(r *Runner) {
		r.now = now
	}
}

// WithCompleterFactory sets a custom CompleterFactory (used for testing).
func WithCompleterFactory(f CompleterFactory) Option {
	return func(r *Runner) {
		r.completerFactory = f
	}
}

// WithDCEClient sets a custom DCEExporter (used for testing).
func WithDCEClient(client DCEExporter) Option {
	return func(r *Runner) {
		r.dceClient = client
	}
}

// WithDeliverer sets a custom Deliverer (used for testing).
func WithDeliverer(d Deliverer) Option {
	return func(r *Runner) {
		r.deliverer = d
	}
}

// WithLLMAPIKey sets the LLM API key.
func WithLLMAPIKey(apiKey string) Option {
	return func(r *Runner) {
		r.llmAPIKey = apiKey
	}
}

// NewRunner creates a new Runner instance.
func NewRunner(store *state.Store, opts ...Option) (*Runner, error) {
	if store == nil {
		return nil, errors.New("state store is required")
	}

	r := &Runner{
		store:            store,
		completerFactory: DefaultCompleterFactory,
		now:              time.Now,
	}

	for _, opt := range opts {
		opt(r)
	}

	return r, nil
}

// acquire is the only ownership path for both synchronous and background runs.
func (r *Runner) acquire() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return false
	}
	r.running = true
	r.wg.Add(1)
	return true
}

// Start atomically claims and launches a background job using the application context.
func (r *Runner) Start(ctx context.Context, targetChannelID string) bool {
	if !r.acquire() {
		return false
	}
	go func() { _ = r.run(ctx, targetChannelID) }()
	return true
}

func (r *Runner) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running = false
	r.wg.Done()
}

// IsRunning reports whether a brief job is currently executing.
func (r *Runner) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// Wait blocks until any active background job finishes.
func (r *Runner) Wait() {
	r.wg.Wait()
}

// Run executes a brief synchronously, rejecting an already active job.
func (r *Runner) Run(ctx context.Context, targetChannelID string) error {
	if !r.acquire() {
		return errors.New("brief already running")
	}
	return r.run(ctx, targetChannelID)
}

func (r *Runner) run(ctx context.Context, targetChannelID string) error {
	defer r.finish()

	if r.deliverer == nil {
		return errors.New("deliverer is not configured")
	}

	// 1. Load latest non-secret configuration
	cfg, err := r.store.LoadConfig()
	if err != nil {
		msg := "Failed to load configuration: " + r.sanitize(err.Error())
		_ = r.deliverer.Deliver(ctx, msg)
		return err
	}

	// 2. Select target channels in config order
	var targetChannels []state.ChannelConfig
	if targetChannelID != "" {
		for _, ch := range cfg.Channels {
			if ch.ID == targetChannelID {
				targetChannels = []state.ChannelConfig{ch}
				break
			}
		}
		if len(targetChannels) == 0 {
			msg := fmt.Sprintf("Channel %s is not currently followed.", targetChannelID)
			_ = r.deliverer.Deliver(ctx, msg)
			return errors.New("channel not followed")
		}
	} else {
		targetChannels = cfg.Channels
	}

	if len(targetChannels) == 0 {
		_ = r.deliverer.Deliver(ctx, "No channels are currently followed.")
		return nil
	}

	// 3. Check collector readiness
	if r.dceClient == nil || !r.dceClient.IsConfigured() {
		msg := "Discord exporter is not ready. Run cordbrief --setup and restart; use --dce-version to recover a rejected release."
		_ = r.deliverer.Deliver(ctx, msg)
		return errors.New("dce not configured")
	}

	// 4. Initialize LLM completer with bounded retry
	baseCompleter, err := r.completerFactory(cfg.LLM.BaseURL, cfg.LLM.Model, r.llmAPIKey)
	if err != nil {
		msg := "Failed to initialize LLM client: " + r.sanitize(err.Error())
		_ = r.deliverer.Deliver(ctx, msg)
		return err
	}
	retryComp := NewRetryCompleter(baseCompleter)
	engine := brief.NewEngine(retryComp, brief.WithPrompt(cfg.EffectiveBriefPrompt()))

	// 5. Capture fixed cutoff ONCE for the entire run
	cutoff := r.now().UTC()

	// 6. Process channels sequentially in config order
	for _, ch := range targetChannels {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		r.processChannel(ctx, ch, cutoff, engine)
	}

	return nil
}

// processChannel executes the isolated transaction for a single followed channel.
func (r *Runner) processChannel(ctx context.Context, ch state.ChannelConfig, cutoff time.Time, engine *brief.Engine) {
	// A. Load durable state
	st, err := r.store.LoadState()
	if err != nil {
		log.Printf("[job] failed to load state for #%s (%s): %v", ch.Name, ch.ID, err)
		_ = r.deliverer.Deliver(ctx, fmt.Sprintf("#%s\nFailed to load state: %s", ch.Name, r.sanitize(err.Error())))
		return
	}

	chState, exists := st.Channels[ch.ID]
	if !exists || chState.Cursor.Value == "" {
		log.Printf("[job] no valid cursor for #%s (%s)", ch.Name, ch.ID)
		_ = r.deliverer.Deliver(ctx, fmt.Sprintf("#%s\nNo valid cursor found; channel skipped.", ch.Name))
		return
	}

	// B. Bounded Discord collection via DCE
	req := dce.ExportRequest{
		ChannelID: ch.ID,
		After:     chState.Cursor,
		Before:    cutoff,
	}

	dceRes, err := r.dceClient.Export(ctx, req)
	if err != nil {
		log.Printf("[job] DCE export failed for #%s (%s): %v", ch.Name, ch.ID, err)
		// Record sanitized error without altering cursor
		chState.LastError = r.sanitize(err.Error())
		st.Channels[ch.ID] = chState
		_ = r.store.SaveState(st)

		notice := fmt.Sprintf("#%s\nBrief failed during Discord collection.\nNothing was consumed; it will be retried next time.", ch.Name)
		_ = r.deliverer.Deliver(ctx, notice)
		return
	}

	serverName := strings.TrimSpace(dceRes.Guild.Name)
	chName := ch.Name
	if dceRes.Channel.ID == ch.ID && strings.TrimSpace(dceRes.Channel.Name) != "" {
		chName = strings.TrimSpace(dceRes.Channel.Name)
	}

	// C. Zero-message export is success: no LLM call, no cursor change
	if len(dceRes.Messages) == 0 {
		_ = r.deliverer.Deliver(ctx, FormatNoMessages(serverName, chName))
		return
	}

	// D. Map dce.Message -> brief.Message explicitly
	briefMessages := make([]brief.Message, len(dceRes.Messages))
	for i, m := range dceRes.Messages {
		bm := brief.Message{
			ID:        m.ID,
			Timestamp: m.Timestamp,
			Author:    m.Author.DisplayName(),
			Content:   m.Content,
		}
		if m.ReplyTo != nil {
			bm.ReplyToID = m.ReplyTo.MessageID
		}
		if len(m.Attachments) > 0 {
			bm.Attachments = make([]brief.Attachment, len(m.Attachments))
			for j, a := range m.Attachments {
				bm.Attachments[j] = brief.Attachment{FileName: a.FileName, URL: a.URL}
			}
		}
		if len(m.Embeds) > 0 {
			bm.Embeds = make([]brief.Embed, len(m.Embeds))
			for j, e := range m.Embeds {
				bm.Embeds[j] = brief.Embed{Title: e.Title, URL: e.URL, Description: e.Description}
			}
		}
		briefMessages[i] = bm
	}

	// E. Synthesize brief with bounded LLM retry
	briefText, err := engine.Summarize(ctx, brief.Channel{ID: ch.ID, Name: chName}, briefMessages)
	if err != nil {
		log.Printf("[job] summarization failed for #%s (%s): %v", chName, ch.ID, err)
		chState.LastError = r.sanitize(err.Error())
		st.Channels[ch.ID] = chState
		_ = r.store.SaveState(st)

		notice := fmt.Sprintf("%s\nBrief failed during summarization.\nNothing was consumed; it will be retried next time.", FormatHeading(serverName, chName))
		_ = r.deliverer.Deliver(ctx, notice)
		return
	}

	// F. Split into Telegram plain-text parts
	parts := SplitBrief(serverName, chName, len(dceRes.Messages), briefText, DefaultMaxTelegramRunes)

	// G. Deliver ALL parts to Telegram
	deliveryFailed := false
	for _, part := range parts {
		if dErr := r.deliverer.Deliver(ctx, part); dErr != nil {
			deliveryFailed = true
			log.Printf("[job] Telegram delivery failed for #%s (%s): %v", ch.Name, ch.ID, dErr)
			chState.LastError = r.sanitize(dErr.Error())
			st.Channels[ch.ID] = chState
			_ = r.store.SaveState(st)
			break
		}
	}

	if deliveryFailed {
		// Delivery failed: cursor remains unchanged so next run retries the interval
		return
	}

	// H. ONLY AFTER ALL PARTS DELIVERED: advance cursor to new message-ID
	chState.Cursor = state.Cursor{
		Kind:  state.CursorKindMessageID,
		Value: dceRes.MaxMessageID,
	}
	chState.LastSuccessAt = r.now().UTC().Format(time.RFC3339)
	chState.LastError = ""
	st.Channels[ch.ID] = chState

	// I. Save state atomically
	if sErr := r.store.SaveState(st); sErr != nil {
		log.Printf("[job] CRITICAL: failed to commit state after successful Telegram delivery for #%s: %v", ch.Name, sErr)
		warning := fmt.Sprintf("#%s\nWarning: brief was delivered but saving state failed. The next brief run may duplicate messages.", ch.Name)
		_ = r.deliverer.Deliver(ctx, warning)
		return
	}

	log.Printf("[job] successfully delivered brief and committed cursor %s for #%s (%s)", dceRes.MaxMessageID, ch.Name, ch.ID)
}

// sanitize strips the LLM API key from user-facing error text.
func (r *Runner) sanitize(msg string) string {
	if r.llmAPIKey != "" && strings.Contains(msg, r.llmAPIKey) {
		msg = strings.ReplaceAll(msg, r.llmAPIKey, "[REDACTED]")
	}
	return msg
}
