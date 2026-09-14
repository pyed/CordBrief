package bot

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/pyed/CordBrief/internal/dce"
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

// Bot is the Telegram control plane for CordBrief.
type Bot struct {
	client         Sender
	rawBot         *bot.Bot
	store          *state.Store
	dceClient      *dce.Client
	ownerID        int64
	now            func() time.Time
	mu             sync.Mutex
	pendingFollows map[string]PendingFollow
	nextFollowID   int64
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

// New constructs a Bot instance with the provided environment config and state store.
func New(cfg *EnvConfig, store *state.Store, opts ...Option) (*Bot, error) {
	if cfg == nil {
		return nil, fmt.Errorf("env config is required")
	}
	if store == nil {
		return nil, fmt.Errorf("state store is required")
	}

	b := &Bot{
		store:          store,
		ownerID:        cfg.OwnerID,
		now:            time.Now,
		pendingFollows: make(map[string]PendingFollow),
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

	return b, nil
}

// Start launches Telegram long polling and blocks until ctx is canceled.
func (b *Bot) Start(ctx context.Context) {
	if b.rawBot != nil {
		b.rawBot.Start(ctx)
	}
}

// OwnerID returns the authorized owner's Telegram ID.
func (b *Bot) OwnerID() int64 {
	return b.ownerID
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
