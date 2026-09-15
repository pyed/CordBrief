package bot

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/pyed/CordBrief/internal/dce"
	"github.com/pyed/CordBrief/internal/job"
	"github.com/pyed/CordBrief/internal/llm"
	"github.com/pyed/CordBrief/internal/scheduler"
	"github.com/pyed/CordBrief/internal/state"
)

// Sender abstracts Telegram client operations for deterministic testing.
type Sender interface {
	SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error)
	AnswerCallbackQuery(ctx context.Context, params *bot.AnswerCallbackQueryParams) (bool, error)
	EditMessageText(ctx context.Context, params *bot.EditMessageTextParams) (*models.Message, error)
}

// PendingFollow tracks a transient in-memory follow request waiting for start mode selection.
type PendingFollow struct {
	ChannelID   string
	DisplayName string
	CreatedAt   time.Time
}

// ModelLister defines the capability to discover available LLM models.
type ModelLister interface {
	ListModels(ctx context.Context) ([]llm.ModelInfo, error)
}

// Bot is the Telegram control plane for CordBrief.
type Bot struct {
	appCtx         context.Context
	client         Sender
	rawBot         *bot.Bot
	store          *state.Store
	dceClient      *dce.Client
	runner         *job.Runner
	scheduler      *scheduler.Scheduler
	ownerID        int64
	now            func() time.Time
	mu             sync.Mutex
	pendingFollows map[string]PendingFollow
	nextFollowID   int64
	llmAPIKey      string
	modelCache     *ModelCache
	modelLister    ModelLister
}

// Option configures Bot instances.
type Option func(*Bot)

// WithSender sets a custom Sender (used for testing without network access).
func WithSender(s Sender) Option {
	return func(b *Bot) {
		b.client = s
	}
}

// WithNow sets a custom clock function (used for deterministic testing).
func WithNow(now func() time.Time) Option {
	return func(b *Bot) {
		b.now = now
	}
}

// WithDCEClient sets a custom DCE client (used for testing or pre-configured clients).
func WithDCEClient(client *dce.Client) Option {
	return func(b *Bot) {
		b.dceClient = client
	}
}

// WithRunner sets a custom job Runner (used for testing).
func WithRunner(r *job.Runner) Option {
	return func(b *Bot) {
		b.runner = r
	}
}

// WithScheduler sets a custom Scheduler (used for testing).
func WithScheduler(s *scheduler.Scheduler) Option {
	return func(b *Bot) {
		b.scheduler = s
	}
}

// WithModelLister sets a custom ModelLister (used for testing without real network calls).
func WithModelLister(lister ModelLister) Option {
	return func(b *Bot) {
		b.modelLister = lister
	}
}

// WithModelCache sets a custom ModelCache (used for testing).
func WithModelCache(cache *ModelCache) Option {
	return func(b *Bot) {
		b.modelCache = cache
	}
}

// New constructs a Bot instance with the provided environment config and state store.
func New(appCtx context.Context, cfg *EnvConfig, store *state.Store, opts ...Option) (*Bot, error) {
	if appCtx == nil {
		return nil, fmt.Errorf("application context is required")
	}
	if cfg == nil {
		return nil, fmt.Errorf("env config is required")
	}
	if store == nil {
		return nil, fmt.Errorf("state store is required")
	}

	b := &Bot{
		appCtx:         appCtx,
		store:          store,
		ownerID:        cfg.OwnerID,
		now:            time.Now,
		pendingFollows: make(map[string]PendingFollow),
		llmAPIKey:      cfg.LLMAPIKey,
		modelCache:     NewModelCache(),
	}

	// If DCE path and token are provided, initialize client fail-open (does not prevent bot startup)
	if cfg.DCEPath != "" && cfg.DiscordToken != "" {
		if dceClient, err := dce.NewClient(cfg.DCEPath, cfg.DiscordToken); err == nil {
			b.dceClient = dceClient
		}
	}

	for _, opt := range opts {
		opt(b)
	}

	// If no custom sender was provided, initialize the real Telegram client.
	if b.client == nil {
		tgBot, err := bot.New(cfg.BotToken,
			bot.WithDefaultHandler(b.HandleUpdate),
			bot.WithNotAsyncHandlers(),
			bot.WithAllowedUpdates(bot.AllowedUpdates{
				"message",
				"callback_query",
			}),
		)
		if err != nil {
			return nil, fmt.Errorf("initialize telegram bot: %w", err)
		}
		b.rawBot = tgBot
		b.client = tgBot
	}

	// Initialize runner if not set via WithRunner
	if b.runner == nil {
		r, err := job.NewRunner(store,
			job.WithDCEClient(b.dceClient),
			job.WithDeliverer(b),
			job.WithLLMAPIKey(cfg.LLMAPIKey),
		)
		if err == nil {
			b.runner = r
		}
	}

	// Initialize scheduler if not set via WithScheduler
	if b.scheduler == nil {
		b.scheduler = scheduler.New(store, func(ctx context.Context) bool {
			if b.runner == nil {
				return false
			}
			return b.runner.Start(ctx, "")
		}, scheduler.WithNow(b.now))
	}

	return b, nil
}

// Start launches Telegram long polling and the background scheduler until the application context is canceled.
func (b *Bot) Start() {
	if b.scheduler != nil {
		go b.scheduler.Run(b.appCtx)
	}
	if b.rawBot != nil {
		b.rawBot.Start(b.appCtx)
	}
}

// OwnerID returns the authorized owner's Telegram ID.
func (b *Bot) OwnerID() int64 {
	return b.ownerID
}

// Deliver implements job.Deliverer by delivering plain-text messages directly to the owner.
func (b *Bot) Deliver(ctx context.Context, text string) error {
	params := &bot.SendMessageParams{
		ChatID: b.ownerID,
		Text:   text,
	}
	_, err := b.client.SendMessage(ctx, params)
	return err
}

// Runner returns the active brief runner.
func (b *Bot) Runner() *job.Runner {
	return b.runner
}

// Scheduler returns the active brief scheduler.
func (b *Bot) Scheduler() *scheduler.Scheduler {
	return b.scheduler
}

func (b *Bot) sendTextMessage(ctx context.Context, chatID int64, text string) {
	b.sendMessageWithMarkup(ctx, chatID, text, nil)
}

func (b *Bot) sendMessageWithMarkup(ctx context.Context, chatID int64, text string, markup *models.InlineKeyboardMarkup) {
	params := &bot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	}
	if markup != nil {
		params.ReplyMarkup = markup
	}
	_, _ = b.client.SendMessage(ctx, params)
}

func (b *Bot) editMessage(ctx context.Context, chatID int64, messageID int, text string, markup *models.InlineKeyboardMarkup) {
	params := &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: messageID,
		Text:      text,
	}
	if markup != nil {
		params.ReplyMarkup = markup
	}
	_, _ = b.client.EditMessageText(ctx, params)
}
