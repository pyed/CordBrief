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
// followed channel + persisted cursor -> DCE export -> brief engine -> Telegram delivery -> commit cursor.
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
	return r.start(ctx, targetChannelID, time.Time{})
}

// StartScheduled prepares now and holds all channel briefs until deliveryAt.
func (r *Runner) StartScheduled(ctx context.Context, deliveryAt time.Time) bool {
	return r.start(ctx, "", deliveryAt)
}

func (r *Runner) start(ctx context.Context, targetChannelID string, deliveryAt time.Time) bool {
	if !r.acquire() {
		return false
	}
	go func() { _ = r.run(ctx, targetChannelID, deliveryAt) }()
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
	return r.run(ctx, targetChannelID, time.Time{})
}

func (r *Runner) run(ctx context.Context, targetChannelID string, deliveryAt time.Time) error {
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

	// 5. Scheduled runs retain only finished text and message-ID boundaries.
	var prepared []*preparedChannel

	// 6. Process channels sequentially in config order
	for _, ch := range targetChannels {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		result := r.prepareChannel(ctx, ch, engine)
		if deliveryAt.IsZero() {
			r.deliverChannel(ctx, result)
		} else {
			prepared = append(prepared, result)
		}
	}

	if !deliveryAt.IsZero() {
		timer := time.NewTimer(max(0, deliveryAt.Sub(r.now())))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		for _, result := range prepared {
			if err := ctx.Err(); err != nil {
				return err
			}
			r.deliverChannel(ctx, result)
		}
	}
	return ctx.Err()
}

type preparedChannel struct {
	channel      state.ChannelConfig
	parts        []string
	maxMessageID string
}

// prepareChannel collects and summarizes sequentially; it never advances a cursor.
func (r *Runner) prepareChannel(ctx context.Context, ch state.ChannelConfig, engine *brief.Engine) *preparedChannel {
	result := &preparedChannel{channel: ch}
	// A. Load durable state
	st, err := r.store.LoadState()
	if err != nil {
		log.Printf("[job] failed to load state for #%s (%s): %v", ch.Name, ch.ID, err)
		result.parts = []string{fmt.Sprintf("#%s\nFailed to load state: %s", ch.Name, r.sanitize(err.Error()))}
		return result
	}

	chState, exists := st.Channels[ch.ID]
	if !exists || chState.Cursor.Value == "" {
		log.Printf("[job] no valid cursor for #%s (%s)", ch.Name, ch.ID)
		result.parts = []string{fmt.Sprintf("#%s\nNo valid cursor found; channel skipped.", ch.Name)}
		return result
	}

	// B. Bounded Discord collection via DCE
	req := dce.ExportRequest{
		ChannelID: ch.ID,
		After:     chState.Cursor,
	}

	dceRes, err := r.dceClient.Export(ctx, req)
	if err != nil {
		log.Printf("[job] DCE export failed for #%s (%s): %v", ch.Name, ch.ID, err)
		// Record sanitized error without altering cursor
		chState.LastError = r.sanitize(err.Error())
		st.Channels[ch.ID] = chState
		_ = r.store.SaveState(st)

		notice := fmt.Sprintf("#%s\nBrief failed during Discord collection.\nNothing was consumed; it will be retried next time.", ch.Name)
		result.parts = []string{notice}
		return result
	}

	serverName := strings.TrimSpace(dceRes.Guild.Name)
	chName := ch.Name
	if dceRes.Channel.ID == ch.ID && strings.TrimSpace(dceRes.Channel.Name) != "" {
		chName = strings.TrimSpace(dceRes.Channel.Name)
	}

	// C. Zero-message export is success: no LLM call, no cursor change
	if len(dceRes.Messages) == 0 {
		result.parts = []string{FormatNoMessages(serverName, chName)}
		return result
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
		result.parts = []string{notice}
		return result
	}

	// F. Split into Telegram plain-text parts
	result.parts = SplitBrief(serverName, chName, len(dceRes.Messages), briefText, DefaultMaxTelegramRunes)
	result.maxMessageID = dceRes.MaxMessageID
	return result
}

// deliverChannel commits only after every part of this channel's brief succeeds.
func (r *Runner) deliverChannel(ctx context.Context, result *preparedChannel) {
	ch := result.channel
	var deliveryErr error
	for _, part := range result.parts {
		if deliveryErr = r.deliverer.Deliver(ctx, part); deliveryErr != nil {
			log.Printf("[job] Telegram delivery failed for #%s (%s): %s", ch.Name, ch.ID, r.sanitize(deliveryErr.Error()))
			break
		}
	}
	if result.maxMessageID == "" {
		return
	}

	st, err := r.store.LoadState()
	if err == nil {
		chState := st.Channels[ch.ID]
		if deliveryErr != nil {
			chState.LastError = r.sanitize(deliveryErr.Error())
		} else {
			chState.Cursor = state.Cursor{Kind: state.CursorKindMessageID, Value: result.maxMessageID}
			chState.LastSuccessAt = r.now().UTC().Format(time.RFC3339)
			chState.LastError = ""
		}
		st.Channels[ch.ID] = chState
		err = r.store.SaveState(st)
	}
	if err != nil {
		log.Printf("[job] failed to save delivery state for #%s: %v", ch.Name, err)
		_ = r.deliverer.Deliver(ctx, fmt.Sprintf("#%s\nWarning: saving state failed. The next brief run may duplicate messages.", ch.Name))
	} else if deliveryErr == nil {
		log.Printf("[job] successfully delivered brief and committed cursor %s for #%s (%s)", result.maxMessageID, ch.Name, ch.ID)
	}
}

// sanitize strips the LLM API key from user-facing error text.
func (r *Runner) sanitize(msg string) string {
	if r.llmAPIKey != "" && strings.Contains(msg, r.llmAPIKey) {
		msg = strings.ReplaceAll(msg, r.llmAPIKey, "[REDACTED]")
	}
	return msg
}
