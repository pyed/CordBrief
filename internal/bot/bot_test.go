package bot

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/pyed/CordBrief/internal/dce"
	"github.com/pyed/CordBrief/internal/job"
	"github.com/pyed/CordBrief/internal/scheduler"
	"github.com/pyed/CordBrief/internal/state"
)

// fakeSender records all Telegram client interactions in memory.
type fakeSender struct {
	mu       sync.Mutex
	sent     []*bot.SendMessageParams
	answered []*bot.AnswerCallbackQueryParams
	edited   []*bot.EditMessageTextParams
}

func (f *fakeSender) SendMessage(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, params)
	return &models.Message{ID: len(f.sent), Text: params.Text}, nil
}

func (f *fakeSender) AnswerCallbackQuery(ctx context.Context, params *bot.AnswerCallbackQueryParams) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answered = append(f.answered, params)
	return true, nil
}

func (f *fakeSender) EditMessageText(ctx context.Context, params *bot.EditMessageTextParams) (*models.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edited = append(f.edited, params)
	return &models.Message{ID: params.MessageID, Text: params.Text}, nil
}

func (f *fakeSender) lastSent() *bot.SendMessageParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return nil
	}
	return f.sent[len(f.sent)-1]
}

func (f *fakeSender) lastEdited() *bot.EditMessageTextParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.edited) == 0 {
		return nil
	}
	return f.edited[len(f.edited)-1]
}

func (f *fakeSender) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeSender) answeredCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.answered)
}

func getInlineKeyboard(params *bot.SendMessageParams) [][]models.InlineKeyboardButton {
	if params == nil || params.ReplyMarkup == nil {
		return nil
	}
	if ikm, ok := params.ReplyMarkup.(*models.InlineKeyboardMarkup); ok {
		return ikm.InlineKeyboard
	}
	return nil
}

func getEditedInlineKeyboard(params *bot.EditMessageTextParams) [][]models.InlineKeyboardButton {
	if params == nil || params.ReplyMarkup == nil {
		return nil
	}
	if ikm, ok := params.ReplyMarkup.(*models.InlineKeyboardMarkup); ok {
		return ikm.InlineKeyboard
	}
	return nil
}

func setupTestBot(t *testing.T) (*Bot, *fakeSender, *state.Store, func() time.Time) {
	t.Helper()
	dir := t.TempDir()
	store := state.NewStore(dir)
	sender := &fakeSender{}

	fixedTime := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	nowFunc := func() time.Time { return fixedTime }

	b, err := New(context.Background(), &EnvConfig{
		BotToken: "test-token",
		OwnerID:  12345,
		DataDir:  dir,
	}, store, WithSender(sender), WithNow(nowFunc))
	if err != nil {
		t.Fatalf("setupTestBot failed: %v", err)
	}

	return b, sender, store, nowFunc
}

func makeMsg(userID int64, chatType string, text string) *models.Update {
	return &models.Update{
		ID: 1,
		Message: &models.Message{
			ID:   10,
			From: &models.User{ID: userID, FirstName: "TestUser"},
			Chat: models.Chat{ID: 100, Type: models.ChatType(chatType)},
			Text: text,
		},
	}
}

func makeCallback(userID int64, chatType string, data string) *models.Update {
	return &models.Update{
		ID: 2,
		CallbackQuery: &models.CallbackQuery{
			ID:   "cb1",
			From: models.User{ID: userID, FirstName: "TestUser"},
			Data: data,
			Message: models.MaybeInaccessibleMessage{
				Message: &models.Message{
					ID:   10,
					Chat: models.Chat{ID: 100, Type: models.ChatType(chatType)},
					Text: "original text",
				},
			},
		},
	}
}

// 1. missing TELEGRAM_BOT_TOKEN causes startup/config validation error
func TestLoadEnv_MissingBotToken(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_OWNER_ID", "12345")

	_, err := LoadEnv()
	if err == nil {
		t.Fatal("expected error on missing TELEGRAM_BOT_TOKEN, got nil")
	}
	if !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") {
		t.Errorf("expected error message mentioning TELEGRAM_BOT_TOKEN, got: %v", err)
	}
}

// 2. missing/invalid TELEGRAM_OWNER_ID rejected
func TestLoadEnv_MissingOrInvalidOwnerID(t *testing.T) {
	cases := []struct {
		name    string
		ownerID string
	}{
		{"missing", ""},
		{"non_numeric", "not-a-number"},
		{"zero", "0"},
		{"negative", "-123"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TELEGRAM_BOT_TOKEN", "valid-token")
			t.Setenv("TELEGRAM_OWNER_ID", tc.ownerID)

			_, err := LoadEnv()
			if err == nil {
				t.Fatalf("expected error for owner ID %q, got nil", tc.ownerID)
			}
			if !strings.Contains(err.Error(), "TELEGRAM_OWNER_ID") {
				t.Errorf("expected error mentioning TELEGRAM_OWNER_ID, got: %v", err)
			}
		})
	}
}

// 3. authorization accepts owner
func TestAuth_AcceptsOwner(t *testing.T) {
	b, sender, _, _ := setupTestBot(t)
	ctx := context.Background()

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/start"))

	if sender.sentCount() != 1 {
		t.Fatalf("expected 1 message sent to owner, got %d", sender.sentCount())
	}
	if !strings.Contains(sender.lastSent().Text, "CordBrief") {
		t.Errorf("unexpected message text: %s", sender.lastSent().Text)
	}
}

// 4. authorization rejects non-owner
func TestAuth_RejectsNonOwner(t *testing.T) {
	b, sender, _, _ := setupTestBot(t)
	ctx := context.Background()

	// Different user ID
	b.HandleUpdate(ctx, nil, makeMsg(99999, "private", "/start"))

	if sender.sentCount() != 0 {
		t.Errorf("expected 0 messages sent to unauthorized user, got %d", sender.sentCount())
	}
}

// 5. non-private interaction is rejected if you enforce private-only
func TestAuth_RejectsNonPrivateChat(t *testing.T) {
	b, sender, _, _ := setupTestBot(t)
	ctx := context.Background()

	for _, chatType := range []string{"group", "supergroup", "channel"} {
		b.HandleUpdate(ctx, nil, makeMsg(12345, chatType, "/start"))
		if sender.sentCount() != 0 {
			t.Errorf("expected 0 messages sent for chat type %q, got %d", chatType, sender.sentCount())
		}
	}
}

// 6. follow rejects malformed Discord channel ID
func TestFollow_RejectsMalformedChannelID(t *testing.T) {
	b, sender, _, _ := setupTestBot(t)
	ctx := context.Background()

	malformedIDs := []string{"abc", "12-34", "ch12345", "123a456"}
	for _, badID := range malformedIDs {
		sender.sent = nil
		b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/follow "+badID))

		if sender.sentCount() != 1 {
			t.Fatalf("expected 1 response for malformed ID %q, got %d", badID, sender.sentCount())
		}
		if !strings.Contains(sender.lastSent().Text, "decimal digits") {
			t.Errorf("expected validation error message, got: %s", sender.lastSent().Text)
		}
	}
}

// 7. duplicate follow rejected without changing durable data
func TestFollow_DuplicateRejectedWithoutStateChange(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	// Seed existing channel in config
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{
		{ID: "178281233233608705", Name: "ExistingChannel"},
	}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/follow 178281233233608705"))

	if sender.sentCount() != 1 {
		t.Fatalf("expected 1 reply, got %d", sender.sentCount())
	}
	if !strings.Contains(sender.lastSent().Text, "already followed") {
		t.Errorf("expected 'already followed' message, got: %s", sender.lastSent().Text)
	}

	// Verify durable data unchanged
	afterCfg, _ := store.LoadConfig()
	if len(afterCfg.Channels) != 1 {
		t.Errorf("expected channel count 1, got %d", len(afterCfg.Channels))
	}
}

// 8. follow "from now" creates timestamp cursor
func TestFollow_FromNowCreatesTimestampCursor(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	// Initiate follow
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/follow 178281233233608705 General"))

	// Extract follow ID from callback button data
	msg := sender.lastSent()
	kb := getInlineKeyboard(msg)
	if len(kb) == 0 || len(kb[0]) == 0 {
		t.Fatal("expected reply markup with inline keyboard")
	}
	nowBtnData := kb[0][0].CallbackData
	if !strings.HasPrefix(nowBtnData, "f:now:") {
		t.Fatalf("expected 'f:now:' callback data, got %q", nowBtnData)
	}

	// Click "From now"
	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", nowBtnData))

	st, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	chState, exists := st.Channels["178281233233608705"]
	if !exists {
		t.Fatal("expected channel state to exist")
	}
	if chState.Cursor.Kind != state.CursorKindTimestamp {
		t.Errorf("expected cursor kind 'timestamp', got %q", chState.Cursor.Kind)
	}
	// Fixed time is 2026-09-13T12:00:00Z
	if chState.Cursor.Value != "2026-09-13T12:00:00Z" {
		t.Errorf("expected cursor value '2026-09-13T12:00:00Z', got %q", chState.Cursor.Value)
	}
}

// 9. follow "last 24 hours" creates timestamp approximately 24h earlier
func TestFollow_Last24HoursCreatesTimestamp24hEarlier(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/follow 178281233233608705 General"))

	msg := sender.lastSent()
	kb := getInlineKeyboard(msg)
	if len(kb) == 0 || len(kb[0]) < 2 {
		t.Fatal("expected reply markup with start mode buttons")
	}
	h24BtnData := kb[0][1].CallbackData
	if !strings.HasPrefix(h24BtnData, "f:24h:") {
		t.Fatalf("expected 'f:24h:' callback data, got %q", h24BtnData)
	}

	// Click "Last 24 hours"
	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", h24BtnData))

	st, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	chState := st.Channels["178281233233608705"]
	if chState.Cursor.Kind != state.CursorKindTimestamp {
		t.Errorf("expected cursor kind 'timestamp', got %q", chState.Cursor.Kind)
	}
	// Fixed time 2026-09-13T12:00:00Z - 24h = 2026-09-12T12:00:00Z
	if chState.Cursor.Value != "2026-09-12T12:00:00Z" {
		t.Errorf("expected cursor value '2026-09-12T12:00:00Z', got %q", chState.Cursor.Value)
	}
}

// 10. successful follow persists state cursor and config membership
func TestFollow_PersistsStateCursorAndConfigMembership(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/follow 178281233233608705 General"))
	kb := getInlineKeyboard(sender.lastSent())
	nowBtnData := kb[0][0].CallbackData
	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", nowBtnData))

	// Verify state cursor persisted
	st, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if _, ok := st.Channels["178281233233608705"]; !ok {
		t.Error("channel not found in state.json")
	}

	// Verify config membership persisted
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if len(cfg.Channels) != 1 || cfg.Channels[0].ID != "178281233233608705" {
		t.Errorf("channel not found in config.json: %+v", cfg.Channels)
	}
}

// 11. channel without supplied display name uses ID as label
func TestFollow_WithoutDisplayNameUsesID(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/follow 178281233233608705"))
	kb := getInlineKeyboard(sender.lastSent())
	nowBtnData := kb[0][0].CallbackData
	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", nowBtnData))

	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if len(cfg.Channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(cfg.Channels))
	}
	if cfg.Channels[0].Name != "178281233233608705" {
		t.Errorf("expected display name to be channel ID '178281233233608705', got %q", cfg.Channels[0].Name)
	}
}

// 12 & 13. successful unfollow removes config membership and channel state
func TestUnfollow_RemovesConfigMembershipAndChannelState(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	// Seed followed channel in config and state
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{
		{ID: "178281233233608705", Name: "General"},
	}
	_ = store.SaveConfig(cfg)

	st := state.NewEmptyState()
	st.Channels["178281233233608705"] = state.ChannelState{
		Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-13T12:00:00Z"},
	}
	_ = store.SaveState(st)

	// Send /unfollow 178281233233608705
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/unfollow 178281233233608705"))

	// Confirm button
	kb := getInlineKeyboard(sender.lastSent())
	confirmBtn := kb[0][0].CallbackData
	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", confirmBtn))

	// 12: Verify removed from config
	afterCfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if len(afterCfg.Channels) != 0 {
		t.Errorf("expected 0 channels in config, got %d", len(afterCfg.Channels))
	}

	// 13: Verify removed from state
	afterSt, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if _, exists := afterSt.Channels["178281233233608705"]; exists {
		t.Error("expected channel to be deleted from state, but still exists")
	}
}

// 14. malformed callback data cannot panic
func TestCallback_MalformedDataCannotPanic(t *testing.T) {
	b, _, _, _ := setupTestBot(t)
	ctx := context.Background()

	malformedCallbacks := []string{
		"",
		":",
		":::",
		"f",
		"f:",
		"f:now",
		"f:now:extra:parts",
		"u",
		"u:",
		"u:confirm",
		"u:ask",
		"action",
		"action=",
		"garbage_string_with_no_delimiters",
	}

	for _, badData := range malformedCallbacks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic on callback data %q: %v", badData, r)
				}
			}()
			b.HandleUpdate(ctx, nil, makeCallback(12345, "private", badData))
		}()
	}
}

// 15. unknown/expired pending follow is safely rejected
func TestFollow_UnknownOrExpiredPendingFollowRejected(t *testing.T) {
	b, sender, _, _ := setupTestBot(t)
	ctx := context.Background()

	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", "f:now:expiredID999"))

	edited := sender.lastEdited()
	if edited == nil {
		t.Fatal("expected message to be edited with expiration notice")
	}
	if !strings.Contains(edited.Text, "expired") {
		t.Errorf("expected expiration message, got: %s", edited.Text)
	}
}

// 16. pending follow is NOT persisted before user selects start mode
func TestFollow_NotPersistedBeforeSelection(t *testing.T) {
	b, _, store, _ := setupTestBot(t)
	ctx := context.Background()

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/follow 178281233233608705 General"))

	cfg, _ := store.LoadConfig()
	if len(cfg.Channels) != 0 {
		t.Errorf("expected 0 channels in config before confirmation, got %d", len(cfg.Channels))
	}

	st, _ := store.LoadState()
	if len(st.Channels) != 0 {
		t.Errorf("expected 0 channels in state before confirmation, got %d", len(st.Channels))
	}
}

// 17. pending follow can be cancelled without persistence
func TestFollow_CancelDoesNotPersist(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/follow 178281233233608705 General"))
	kb := getInlineKeyboard(sender.lastSent())
	cancelBtnData := kb[1][0].CallbackData
	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", cancelBtnData))

	edited := sender.lastEdited()
	if edited == nil || !strings.Contains(edited.Text, "cancelled") {
		t.Errorf("expected cancellation message, got: %+v", edited)
	}

	cfg, _ := store.LoadConfig()
	if len(cfg.Channels) != 0 {
		t.Errorf("expected 0 channels in config after cancel, got %d", len(cfg.Channels))
	}
	st, _ := store.LoadState()
	if len(st.Channels) != 0 {
		t.Errorf("expected 0 channels in state after cancel, got %d", len(st.Channels))
	}
}

// 18. unauthorized callback cannot mutate state
func TestAuth_UnauthorizedCallbackCannotMutateState(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	// Owner starts follow
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/follow 178281233233608705 General"))
	kb := getInlineKeyboard(sender.lastSent())
	nowBtnData := kb[0][0].CallbackData

	// Unauthorized user clicks it
	b.HandleUpdate(ctx, nil, makeCallback(99999, "private", nowBtnData))

	if sender.answeredCount() != 0 {
		t.Errorf("expected 0 answered callbacks for unauthorized user, got %d", sender.answeredCount())
	}
	cfg, _ := store.LoadConfig()
	if len(cfg.Channels) != 0 {
		t.Error("unauthorized user caused config to be modified!")
	}
	st, _ := store.LoadState()
	if len(st.Channels) != 0 {
		t.Error("unauthorized user caused state to be modified!")
	}
}

// 19. config remains authoritative if orphan state exists
func TestConfig_RemainsAuthoritativeIfOrphanStateExists(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	// Create orphan state for channel 99999999 (not in config.json)
	st := state.NewEmptyState()
	st.Channels["99999999"] = state.ChannelState{
		Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-13T12:00:00Z"},
	}
	_ = store.SaveState(st)

	// Clean config with 0 channels
	_ = store.SaveConfig(state.DefaultConfig())

	// /channels should show 0 channels
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/channels"))
	lastMsg := sender.lastSent()
	if !strings.Contains(lastMsg.Text, "No followed channels") {
		t.Errorf("expected 'No followed channels', got: %s", lastMsg.Text)
	}

	// /status should show Channels: 0
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/status"))
	statusMsg := sender.lastSent()
	if !strings.Contains(statusMsg.Text, "Channels: 0") {
		t.Errorf("expected 'Channels: 0', got: %s", statusMsg.Text)
	}
}

// 20. bot/control logic behaves correctly when persistence returns an error
func TestPersistence_ErrorHandledGracefully(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	// Write corrupted config.json to trigger load error
	_ = os.WriteFile(store.ConfigPath(), []byte("{ corrupt json"), 0600)

	// Commands should report error gracefully, not panic
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/channels"))
	msg := sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Error loading configuration") {
		t.Errorf("expected error message to user, got: %+v", msg)
	}
}

// Additional test: Unfollow flow with no arguments shows list of channels
func TestUnfollow_NoArgsShowsChannelButtons(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{
		{ID: "111", Name: "One"},
		{ID: "222", Name: "Two"},
	}
	_ = store.SaveConfig(cfg)

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/unfollow"))
	msg := sender.lastSent()
	kb := getInlineKeyboard(msg)
	if len(kb) < 2 {
		t.Fatal("expected channel buttons in unfollow menu")
	}
	if !strings.Contains(kb[0][0].Text, "One") {
		t.Errorf("expected first button to mention 'One', got %q", kb[0][0].Text)
	}
}

// Additional test: Authorized callbacks on all paths (valid, cancel, expired, malformed) trigger AnswerCallbackQuery
func TestCallback_AuthorizedPathsAlwaysAnswered(t *testing.T) {
	b, sender, _, _ := setupTestBot(t)
	ctx := context.Background()

	callbacks := []string{
		"action=status",
		"action=channels",
		"action=help_follow",
		"action=unfollow_menu",
		"f:now:expired123",
		"u:cancel",
		"garbage-callback",
	}

	for i, cb := range callbacks {
		sender.answered = nil
		b.HandleUpdate(ctx, nil, makeCallback(12345, "private", cb))
		if sender.answeredCount() != 1 {
			t.Errorf("[%d] expected 1 AnswerCallbackQuery for %q, got %d", i, cb, sender.answeredCount())
		}
	}
}

func TestStatus_ReportsDiscordExporterState(t *testing.T) {
	b, sender, _, _ := setupTestBot(t)
	ctx := context.Background()

	// Without DCE client configured
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/status"))
	msg := sender.lastSent()
	if !strings.Contains(msg.Text, "Discord exporter: not configured") {
		t.Errorf("expected 'Discord exporter: not configured', got: %s", msg.Text)
	}

	// With DCE client configured
	client := dce.NewMockClient("dce.exe", "test-token", nil)
	b.dceClient = client
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/status"))
	msg = sender.lastSent()
	if !strings.Contains(msg.Text, "Discord exporter: configured") {
		t.Errorf("expected 'Discord exporter: configured', got: %s", msg.Text)
	}
}

type botFakeDCE struct {
	sync.Mutex
	delay   time.Duration
	entered chan context.Context
}

func (f *botFakeDCE) IsConfigured() bool { return true }
func (f *botFakeDCE) Export(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error) {
	if f.entered != nil {
		f.entered <- ctx
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.delay):
		}
	}
	return &dce.ExportResult{
		Channel:  dce.ChannelInfo{ID: req.ChannelID, Name: "general"},
		Messages: []dce.Message{},
	}, nil
}

func TestBrief_UsesApplicationContextAndCancelsOnShutdown(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	appCtx, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	b.appCtx = appCtx
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "10001", Name: "general"}}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	st := state.NewEmptyState()
	st.Channels["10001"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindMessageID, Value: "100"}}
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	fakeDCE := &botFakeDCE{entered: make(chan context.Context, 1)}
	b.runner, _ = job.NewRunner(store, job.WithDCEClient(fakeDCE), job.WithDeliverer(b))
	handlerCtx, cancelHandler := context.WithCancel(appCtx)
	b.HandleUpdate(handlerCtx, nil, makeMsg(12345, "private", "/brief"))
	cancelHandler()
	var jobCtx context.Context
	select {
	case jobCtx = <-fakeDCE.entered:
	case <-time.After(time.Second):
		t.Fatal("background job never started")
	}
	if jobCtx.Err() != nil {
		t.Fatal("handler return canceled job")
	}
	b.HandleUpdate(appCtx, nil, makeMsg(12345, "private", "/status"))
	if !strings.Contains(sender.lastSent().Text, "Brief job: running") {
		t.Fatal("status unresponsive during job")
	}
	b.HandleUpdate(appCtx, nil, makeMsg(12345, "private", "/channels"))
	if !strings.Contains(sender.lastSent().Text, "#general") {
		t.Fatal("channels unresponsive during job")
	}
	shutdown()
	done := make(chan struct{})
	go func() { b.runner.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not stop job")
	}
	if jobCtx.Err() != context.Canceled || b.runner.IsRunning() {
		t.Fatal("application cancellation did not release job")
	}
	after, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if after.Channels["10001"].Cursor != st.Channels["10001"].Cursor {
		t.Fatal("canceled export consumed cursor")
	}
}

func TestBrief_NoChannelsFollowed(t *testing.T) {
	b, sender, _, _ := setupTestBot(t)
	ctx := context.Background()

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/brief"))
	msg := sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "No channels are currently followed") {
		t.Errorf("expected 'No channels are currently followed', got: %v", msg)
	}
}

func TestBrief_NoRunnerConfigured(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	cfg, _ := store.LoadConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "10001", Name: "general"}}
	_ = store.SaveConfig(cfg)

	b.runner = nil

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/brief"))
	msg := sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Brief engine is not initialized") {
		t.Errorf("expected 'Brief engine is not initialized', got: %v", msg)
	}
}

func TestBrief_Trigger_AndBlocksConcurrentRun(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	cfg, _ := store.LoadConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "10001", Name: "general"}}
	_ = store.SaveConfig(cfg)
	st := state.NewEmptyState()
	st.Channels["10001"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T00:00:00Z"}}
	_ = store.SaveState(st)

	fakeDCE := &botFakeDCE{delay: 200 * time.Millisecond}
	runner, err := job.NewRunner(store,
		job.WithDCEClient(fakeDCE),
		job.WithDeliverer(b),
	)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	b.runner = runner

	// Send /brief
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/brief"))
	msg := sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Starting brief for 1 channel...") {
		t.Errorf("expected 'Starting brief for 1 channel...', got: %v", msg)
	}

	// Immediate second /brief while running
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/brief"))
	msg2 := sender.lastSent()
	if msg2 == nil || !strings.Contains(msg2.Text, "Brief already running") {
		t.Errorf("expected 'Brief already running', got: %v", msg2)
	}

	// Wait for runner to finish
	runner.Wait()
}

func TestBrief_ButtonCallback(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	cfg, _ := store.LoadConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "10001", Name: "general"}}
	_ = store.SaveConfig(cfg)
	st := state.NewEmptyState()
	st.Channels["10001"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T00:00:00Z"}}
	_ = store.SaveState(st)

	fakeDCE := &botFakeDCE{}
	runner, _ := job.NewRunner(store,
		job.WithDCEClient(fakeDCE),
		job.WithDeliverer(b),
	)
	b.runner = runner

	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", "action=brief"))
	runner.Wait()

	if sender.answeredCount() < 1 {
		t.Errorf("expected callback answered")
	}
	sentFound := false
	for _, m := range sender.sent {
		if strings.Contains(m.Text, "Starting brief for 1 channel...") {
			sentFound = true
			break
		}
	}
	if !sentFound {
		t.Errorf("expected 'Starting brief for 1 channel...' in sent messages")
	}
}

func TestStatus_ReportsBriefJobState(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	// 1. Idle runner
	fakeDCE := &botFakeDCE{delay: 300 * time.Millisecond}
	runner, _ := job.NewRunner(store,
		job.WithDCEClient(fakeDCE),
		job.WithDeliverer(b),
	)
	b.runner = runner

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/status"))
	msg := sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Brief job: idle") {
		t.Errorf("expected 'Brief job: idle', got: %v", msg)
	}

	// 2. Running
	cfg, _ := store.LoadConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "10001", Name: "general"}}
	_ = store.SaveConfig(cfg)
	st := state.NewEmptyState()
	st.Channels["10001"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T00:00:00Z"}}
	_ = store.SaveState(st)

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/brief"))

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/status"))
	msg = sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Brief job: running") {
		t.Errorf("expected 'Brief job: running', got: %v", msg)
	}

	runner.Wait()
}

func TestFollowAndUnfollow_BlockedWhileBriefRunning(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	cfg, _ := store.LoadConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "10001", Name: "general"}}
	_ = store.SaveConfig(cfg)
	st := state.NewEmptyState()
	st.Channels["10001"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T00:00:00Z"}}
	_ = store.SaveState(st)

	fakeDCE := &botFakeDCE{delay: 300 * time.Millisecond}
	runner, _ := job.NewRunner(store,
		job.WithDCEClient(fakeDCE),
		job.WithDeliverer(b),
	)
	b.runner = runner

	// Start brief
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/brief"))

	// Try confirming a follow while brief is running
	b.mu.Lock()
	b.pendingFollows["temp_pending"] = PendingFollow{
		ChannelID:   "20002",
		DisplayName: "dev",
	}
	b.mu.Unlock()

	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", "f:now:temp_pending"))
	edited := sender.lastEdited()
	if edited == nil || !strings.Contains(edited.Text, "A brief is currently running. Try again when it finishes.") {
		t.Errorf("expected follow blocked message, got %v", edited)
	}

	// Verify channel was NOT added to config
	cfgAfter, _ := store.LoadConfig()
	if len(cfgAfter.Channels) != 1 {
		t.Errorf("channel was added to config despite brief running: %v", cfgAfter.Channels)
	}

	// Try confirming an unfollow while brief is running
	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", "u:confirm:10001"))
	edited = sender.lastEdited()
	if edited == nil || !strings.Contains(edited.Text, "A brief is currently running. Try again when it finishes.") {
		t.Errorf("expected unfollow blocked message, got %v", edited)
	}

	// Verify channel was NOT removed from config
	cfgAfter2, _ := store.LoadConfig()
	if len(cfgAfter2.Channels) != 1 {
		t.Errorf("channel was removed from config despite brief running: %v", cfgAfter2.Channels)
	}

	runner.Wait()
}

func TestSchedule_View(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	// Fixed time: 2026-09-15 06:00:00 UTC
	loc, _ := time.LoadLocation("UTC")
	fixedTime := time.Date(2026, 9, 15, 6, 0, 0, 0, loc)
	b.now = func() time.Time { return fixedTime }

	// 1. Enabled schedule
	cfg, _ := store.LoadConfig()
	cfg.Schedule.Enabled = true
	cfg.Schedule.Time = "08:00"
	cfg.Timezone = "UTC"
	_ = store.SaveConfig(cfg)

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/schedule"))
	msg := sender.lastSent()
	if msg == nil {
		t.Fatal("expected message sent for /schedule")
	}
	expected := "Daily brief: enabled\nTime: 08:00\nTimezone: UTC\nNext run: 2026-09-15 08:00 +00"
	if msg.Text != expected {
		t.Errorf("got:\n%s\nwant:\n%s", msg.Text, expected)
	}

	// 2. Disabled schedule
	cfg.Schedule.Enabled = false
	_ = store.SaveConfig(cfg)

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/schedule"))
	msg = sender.lastSent()
	expectedDisabled := "Daily brief: disabled\nTime: 08:00\nTimezone: UTC\nNext run: none"
	if msg.Text != expectedDisabled {
		t.Errorf("got:\n%s\nwant:\n%s", msg.Text, expectedDisabled)
	}
}

func TestSchedule_Off(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	cfg, _ := store.LoadConfig()
	cfg.Schedule.Enabled = true
	_ = store.SaveConfig(cfg)

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/schedule off"))
	msg := sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Daily brief: disabled") {
		t.Fatalf("expected disabled response, got %v", msg)
	}

	// Verify persisted config
	savedCfg, _ := store.LoadConfig()
	if savedCfg.Schedule.Enabled {
		t.Errorf("expected schedule to be disabled in config")
	}
}

func TestSchedule_SetTime(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	loc, _ := time.LoadLocation("UTC")
	fixedTime := time.Date(2026, 9, 15, 6, 0, 0, 0, loc)
	b.now = func() time.Time { return fixedTime }

	cfg, _ := store.LoadConfig()
	cfg.Schedule.Enabled = false
	cfg.Schedule.Time = "08:00"
	cfg.Timezone = "UTC"
	_ = store.SaveConfig(cfg)

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/schedule 09:30"))
	msg := sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Daily brief: enabled") || !strings.Contains(msg.Text, "Time: 09:30") {
		t.Fatalf("expected enabled 09:30, got %v", msg)
	}

	savedCfg, _ := store.LoadConfig()
	if !savedCfg.Schedule.Enabled || savedCfg.Schedule.Time != "09:30" || savedCfg.Timezone != "UTC" {
		t.Errorf("unexpected saved config: %+v", savedCfg)
	}
}

func TestSchedule_SetTimeAndTimezone(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	locRiyadh, _ := time.LoadLocation("Asia/Riyadh")
	fixedTime := time.Date(2026, 9, 15, 6, 0, 0, 0, locRiyadh)
	b.now = func() time.Time { return fixedTime }

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/schedule 08:00 Asia/Riyadh"))
	msg := sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Daily brief: enabled") {
		t.Fatalf("expected enabled, got %v", msg)
	}
	if !strings.Contains(msg.Text, "Timezone: Asia/Riyadh") {
		t.Errorf("expected timezone Asia/Riyadh in response, got %s", msg.Text)
	}

	savedCfg, _ := store.LoadConfig()
	if !savedCfg.Schedule.Enabled || savedCfg.Schedule.Time != "08:00" || savedCfg.Timezone != "Asia/Riyadh" {
		t.Errorf("unexpected saved config: %+v", savedCfg)
	}
}

func TestSchedule_InvalidInputs(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	cfg, _ := store.LoadConfig()
	originalTime := cfg.Schedule.Time
	originalTz := cfg.Timezone

	// 1. Invalid time format
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/schedule 25:00"))
	msg := sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Invalid schedule parameters") {
		t.Fatalf("expected invalid schedule time error, got: %v", msg)
	}

	// 2. Invalid timezone
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/schedule 08:00 NonExistent/Timezone"))
	msg = sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Invalid schedule") {
		t.Fatalf("expected invalid schedule error, got: %v", msg)
	}

	// Verify config was not altered
	savedCfg, _ := store.LoadConfig()
	if savedCfg.Schedule.Time != originalTime || savedCfg.Timezone != originalTz {
		t.Errorf("config mutated on invalid input: %+v", savedCfg)
	}
}

func TestSchedule_BlockedWhileBriefRunning(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	cfg, _ := store.LoadConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "10001", Name: "general"}}
	cfg.Schedule.Enabled = true
	_ = store.SaveConfig(cfg)
	st := state.NewEmptyState()
	st.Channels["10001"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T00:00:00Z"}}
	_ = store.SaveState(st)

	fakeDCE := &botFakeDCE{delay: 300 * time.Millisecond}
	runner, _ := job.NewRunner(store,
		job.WithDCEClient(fakeDCE),
		job.WithDeliverer(b),
	)
	b.runner = runner

	// Start brief
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/brief"))

	// Attempt schedule mutation while brief is running
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/schedule off"))
	msg := sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "A brief is currently running. Configuration changes are frozen until it finishes.") {
		t.Errorf("expected schedule mutation blocked message, got: %v", msg)
	}

	// Verify config was not mutated
	savedCfg, _ := store.LoadConfig()
	if !savedCfg.Schedule.Enabled {
		t.Errorf("schedule was disabled despite brief running")
	}

	runner.Wait()
}

func TestSchedule_LegacyM5Upgrade_SafeByDefaultAndExplicitOptIn(t *testing.T) {
	tmpDir := t.TempDir()
	store := state.NewStore(tmpDir)

	// Write exact legacy M5 config file (version: 1, schedule.enabled: true)
	legacyM5JSON := `{
  "version": 1,
  "channels": [
    {
      "id": "1391912303376728155",
      "name": "general"
    }
  ],
  "schedule": {
    "enabled": true,
    "time": "08:00"
  },
  "timezone": "UTC",
  "llm": {
    "base_url": "https://generativelanguage.googleapis.com/v1beta/openai/",
    "model": "gemini-3.5-flash"
  }
}`
	if err := os.WriteFile(store.ConfigPath(), []byte(legacyM5JSON), 0600); err != nil {
		t.Fatalf("failed to write legacy M5 config: %v", err)
	}

	// 1. Legacy M5 config loads safely
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed on legacy M5 config: %v", err)
	}
	if cfg.Version != state.CurrentConfigVersion {
		t.Errorf("expected upgraded version %d, got %d", state.CurrentConfigVersion, cfg.Version)
	}

	// 2. Scheduler is inactive (NextRunForConfig returns enabled=false)
	loc, _ := time.LoadLocation("UTC")
	now := time.Date(2026, 9, 15, 6, 0, 0, 0, loc)
	_, enabled, err := scheduler.NextRunForConfig(cfg, now)
	if err != nil {
		t.Fatalf("NextRunForConfig error: %v", err)
	}
	if enabled {
		t.Fatal("CRITICAL INVARIANT VIOLATION: scheduler is active on untouched legacy M5 config!")
	}

	// Build test bot with this store
	sender := &fakeSender{}
	b, err := New(context.Background(),
		&EnvConfig{BotToken: "test-token", OwnerID: 12345},
		store,
		WithSender(sender),
		WithNow(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatalf("New bot: %v", err)
	}

	ctx := context.Background()

	// 3. /schedule reports disabled
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/schedule"))
	msg := sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Daily brief: disabled") || !strings.Contains(msg.Text, "Next run: none") {
		t.Fatalf("expected /schedule to report disabled, got: %v", msg)
	}

	// /start and /status also report disabled
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/start"))
	msg = sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Schedule: disabled") {
		t.Errorf("expected /start to report disabled schedule, got: %v", msg)
	}
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/status"))
	msg = sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Schedule: disabled") {
		t.Errorf("expected /status to report disabled schedule, got: %v", msg)
	}

	// 3b. Invalid /schedule input cannot accidentally mark scheduling as explicitly enabled
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/schedule 25:00"))
	msg = sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Invalid schedule parameters") {
		t.Errorf("expected invalid time error, got: %v", msg)
	}
	cfgCheck, _ := store.LoadConfig()
	if cfgCheck.Schedule.Enabled {
		t.Fatal("invalid input enabled scheduling")
	}

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/schedule 08:00 Invalid/Timezone"))
	msg = sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Invalid schedule") {
		t.Errorf("expected invalid timezone error, got: %v", msg)
	}
	cfgCheck, _ = store.LoadConfig()
	if cfgCheck.Schedule.Enabled {
		t.Fatal("invalid timezone enabled scheduling")
	}

	// 4. /schedule HH:MM <timezone> explicitly enables it
	locRiyadh, _ := time.LoadLocation("Asia/Riyadh")
	nowRiyadh := time.Date(2026, 9, 15, 6, 0, 0, 0, locRiyadh)
	b.now = func() time.Time { return nowRiyadh }

	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/schedule 08:00 Asia/Riyadh"))
	msg = sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Daily brief: enabled") || !strings.Contains(msg.Text, "2026-09-15 08:00 +03") {
		t.Fatalf("expected enabled in Asia/Riyadh, got: %v", msg)
	}

	// 5. The resulting persisted config survives reload/restart semantics
	restartedStore := state.NewStore(tmpDir)
	restartedCfg, err := restartedStore.LoadConfig()
	if err != nil {
		t.Fatalf("reloaded store LoadConfig failed: %v", err)
	}
	if restartedCfg.Version != state.CurrentConfigVersion {
		t.Errorf("expected version %d after restart, got %d", state.CurrentConfigVersion, restartedCfg.Version)
	}
	if !restartedCfg.Schedule.Enabled {
		t.Fatal("explicit opt-in did not survive restart")
	}
	if restartedCfg.Schedule.Time != "08:00" || restartedCfg.Timezone != "Asia/Riyadh" {
		t.Errorf("unexpected restarted config: %+v", restartedCfg)
	}
	if len(restartedCfg.Channels) != 1 || restartedCfg.Channels[0].ID != "1391912303376728155" {
		t.Errorf("followed channel was lost during upgrade/restart: %+v", restartedCfg.Channels)
	}

	// 6. Scheduler then computes/fires normally from that explicitly enabled state
	nextRun, enabledAfter, err := scheduler.NextRunForConfig(restartedCfg, nowRiyadh)
	if err != nil {
		t.Fatalf("NextRunForConfig failed after restart: %v", err)
	}
	if !enabledAfter {
		t.Fatal("scheduler reported disabled after restart")
	}
	expectedNext := time.Date(2026, 9, 15, 8, 0, 0, 0, locRiyadh)
	if !nextRun.Equal(expectedNext) {
		t.Errorf("expected next run %v, got %v", expectedNext, nextRun)
	}

	// 7. /schedule off disables it durably
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/schedule off"))
	msg = sender.lastSent()
	if msg == nil || !strings.Contains(msg.Text, "Daily brief: disabled") || !strings.Contains(msg.Text, "Next run: none") {
		t.Fatalf("expected /schedule off to report disabled, got: %v", msg)
	}

	offStore := state.NewStore(tmpDir)
	offCfg, _ := offStore.LoadConfig()
	if offCfg.Schedule.Enabled {
		t.Fatal("schedule off did not persist to disk")
	}
}
