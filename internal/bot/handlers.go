package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/pyed/CordBrief/internal/scheduler"
	"github.com/pyed/CordBrief/internal/state"
)

// HandleUpdate routes incoming Telegram updates to appropriate command or callback handlers.
func (b *Bot) HandleUpdate(ctx context.Context, _ *bot.Bot, update *models.Update) {
	if update == nil {
		return
	}

	if update.Message != nil {
		b.handleMessage(ctx, update.Message)
		return
	}

	if update.CallbackQuery != nil {
		b.handleCallbackQuery(ctx, update.CallbackQuery)
		return
	}
}

func (b *Bot) handleMessage(ctx context.Context, msg *models.Message) {
	// Single-owner authorization: silently ignore any message not from the owner.
	if msg.From == nil || msg.From.ID != b.ownerID {
		return
	}

	// Enforce private chat for administrative interaction.
	if msg.Chat.Type != models.ChatTypePrivate {
		return
	}

	b.mu.Lock()
	isPendingPrompt := b.pendingPromptEdit
	b.mu.Unlock()

	if isPendingPrompt {
		if strings.EqualFold(strings.TrimSpace(msg.Text), "/cancel") {
			b.mu.Lock()
			b.pendingPromptEdit = false
			b.mu.Unlock()
			b.sendTextMessage(ctx, msg.Chat.ID, "Prompt edit cancelled.")
			return
		}
		b.submitPromptEdit(ctx, msg.Chat.ID, msg.Text)
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	parts := strings.Fields(text)
	cmd := parts[0]
	if idx := strings.Index(cmd, "@"); idx != -1 {
		cmd = cmd[:idx]
	}

	switch cmd {
	case "/start":
		b.handleStart(ctx, msg.Chat.ID)
	case "/status":
		b.handleStatus(ctx, msg.Chat.ID)
	case "/channels":
		b.handleChannels(ctx, msg.Chat.ID)
	case "/follow":
		b.handleFollow(ctx, msg.Chat.ID, parts[1:])
	case "/unfollow":
		b.handleUnfollow(ctx, msg.Chat.ID, parts[1:])
	case "/brief":
		b.handleBrief(ctx, msg.Chat.ID, parts[1:])
	case "/schedule":
		b.handleSchedule(ctx, msg.Chat.ID, parts[1:])
	case "/model":
		b.handleModel(ctx, msg.Chat.ID, parts[1:])
	case "/prompt":
		b.handlePrompt(ctx, msg.Chat.ID, parts[1:])
	case "/cancel":
		b.sendTextMessage(ctx, msg.Chat.ID, "Nothing to cancel.")
	}
}

func (b *Bot) handleStart(ctx context.Context, chatID int64) {
	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.sendTextMessage(ctx, chatID, "Error loading configuration: "+err.Error())
		return
	}

	schedStatus := "disabled"
	if cfg.Schedule.Enabled {
		schedStatus = fmt.Sprintf("daily at %s %s", cfg.Schedule.Time, cfg.Timezone)
	}

	text := fmt.Sprintf("CordBrief\n\nChannels: %d\nSchedule: %s\nAI: %s",
		len(cfg.Channels), schedStatus, cfg.LLM.Model)

	markup := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "Run brief now", CallbackData: "action=brief"},
			},
			{
				{Text: "Status", CallbackData: "action=status"},
				{Text: "Channels", CallbackData: "action=channels"},
			},
		},
	}

	b.sendMessageWithMarkup(ctx, chatID, text, markup)
}

func (b *Bot) handleStatus(ctx context.Context, chatID int64) {
	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.sendTextMessage(ctx, chatID, "Error loading configuration: "+err.Error())
		return
	}

	schedEnabled := "disabled"
	if cfg.Schedule.Enabled {
		schedEnabled = "enabled"
	}

	dceStatus := "not configured"
	if b.dceClient != nil && b.dceClient.IsConfigured() {
		dceStatus = "configured"
	}

	jobStatus := "idle"
	if b.runner != nil && b.runner.IsRunning() {
		jobStatus = "running"
	}

	promptStatus := "default"
	if cfg.Brief != nil && strings.TrimSpace(cfg.Brief.Prompt) != "" {
		promptStatus = "custom"
	}

	text := fmt.Sprintf("CordBrief status\n\nChannels: %d\nSchedule: %s · %s · %s\nLLM: %s\nBrief prompt: %s\nDiscord exporter: %s\nBrief job: %s",
		len(cfg.Channels), schedEnabled, cfg.Schedule.Time, cfg.Timezone, cfg.LLM.Model, promptStatus, dceStatus, jobStatus)

	markup := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "Run brief now", CallbackData: "action=brief"},
				{Text: "Channels", CallbackData: "action=channels"},
			},
		},
	}

	b.sendMessageWithMarkup(ctx, chatID, text, markup)
}

func (b *Bot) handleChannels(ctx context.Context, chatID int64) {
	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.sendTextMessage(ctx, chatID, "Error loading configuration: "+err.Error())
		return
	}

	if len(cfg.Channels) == 0 {
		text := "No followed channels.\n\nSend /follow <channel_id> [name] to add one."
		markup := &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{
					{Text: "Follow channel", CallbackData: "action=help_follow"},
				},
			},
		}
		b.sendMessageWithMarkup(ctx, chatID, text, markup)
		return
	}

	var sb strings.Builder
	sb.WriteString("Followed channels:\n\n")
	for _, ch := range cfg.Channels {
		sb.WriteString(fmt.Sprintf("• #%s (%s)\n", ch.Name, ch.ID))
	}

	markup := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "Follow channel", CallbackData: "action=help_follow"},
				{Text: "Unfollow channel", CallbackData: "action=unfollow_menu"},
			},
		},
	}

	b.sendMessageWithMarkup(ctx, chatID, sb.String(), markup)
}

func (b *Bot) handleFollow(ctx context.Context, chatID int64, args []string) {
	if len(args) == 0 {
		b.sendTextMessage(ctx, chatID, "Usage: /follow <channel_id> [display name]\n\nExample:\n/follow 178281233233608705 General")
		return
	}

	channelID := args[0]
	if !state.IsDecimalString(channelID) {
		b.sendTextMessage(ctx, chatID, "Invalid channel ID: must contain only decimal digits.")
		return
	}

	displayName := channelID
	if len(args) > 1 {
		displayName = strings.TrimSpace(strings.Join(args[1:], " "))
	}

	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.sendTextMessage(ctx, chatID, "Error loading configuration: "+err.Error())
		return
	}

	for _, ch := range cfg.Channels {
		if ch.ID == channelID {
			b.sendTextMessage(ctx, chatID, fmt.Sprintf("Channel %s (%s) is already followed.", ch.Name, ch.ID))
			return
		}
	}

	// Store pending follow in memory until start mode is confirmed
	b.mu.Lock()
	b.nextFollowID++
	followKey := fmt.Sprintf("f%d", b.nextFollowID)
	b.pendingFollows[followKey] = PendingFollow{
		ChannelID:   channelID,
		DisplayName: displayName,
		CreatedAt:   b.now(),
	}
	b.mu.Unlock()

	text := fmt.Sprintf("Follow channel:\nID: %s\nName: %s\n\nSelect start mode:", channelID, displayName)
	markup := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "From now", CallbackData: fmt.Sprintf("f:now:%s", followKey)},
				{Text: "Last 24 hours", CallbackData: fmt.Sprintf("f:24h:%s", followKey)},
			},
			{
				{Text: "Cancel", CallbackData: fmt.Sprintf("f:cancel:%s", followKey)},
			},
		},
	}

	b.sendMessageWithMarkup(ctx, chatID, text, markup)
}

func (b *Bot) handleUnfollow(ctx context.Context, chatID int64, args []string) {
	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.sendTextMessage(ctx, chatID, "Error loading configuration: "+err.Error())
		return
	}

	if len(cfg.Channels) == 0 {
		b.sendTextMessage(ctx, chatID, "No channels are currently followed.")
		return
	}

	// If channel ID was provided directly in arguments
	if len(args) > 0 {
		channelID := args[0]
		var target *state.ChannelConfig
		for _, ch := range cfg.Channels {
			if ch.ID == channelID {
				target = &ch
				break
			}
		}
		if target == nil {
			b.sendTextMessage(ctx, chatID, fmt.Sprintf("Channel %s is not currently followed.", channelID))
			return
		}

		text := fmt.Sprintf("Unfollow #%s (%s)?", target.Name, target.ID)
		markup := &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{
					{Text: "Unfollow", CallbackData: "u:confirm:" + target.ID},
					{Text: "Cancel", CallbackData: "u:cancel"},
				},
			},
		}
		b.sendMessageWithMarkup(ctx, chatID, text, markup)
		return
	}

	// No argument: display inline menu of all followed channels
	var rows [][]models.InlineKeyboardButton
	for _, ch := range cfg.Channels {
		btnText := fmt.Sprintf("Unfollow #%s (%s)", ch.Name, ch.ID)
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: btnText, CallbackData: "u:ask:" + ch.ID},
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "Cancel", CallbackData: "u:cancel"},
	})

	b.sendMessageWithMarkup(ctx, chatID, "Select a channel to unfollow:", &models.InlineKeyboardMarkup{
		InlineKeyboard: rows,
	})
}

func (b *Bot) handleCallbackQuery(ctx context.Context, q *models.CallbackQuery) {
	// Single-owner authorization: silently ignore callbacks from unauthorized users or non-private chats.
	if q.From.ID != b.ownerID {
		return
	}
	if q.Message.Message == nil || q.Message.Message.Chat.Type != models.ChatTypePrivate {
		return
	}

	// Always answer authorized callback queries to clear Telegram's loading indicator.
	defer func() {
		_, _ = b.client.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: q.ID,
		})
	}()

	chatID := q.Message.Message.Chat.ID
	messageID := q.Message.Message.ID
	data := q.Data

	switch {
	case data == "action=brief":
		b.handleBrief(ctx, chatID, nil)
	case data == "action=status":
		b.handleStatus(ctx, chatID)
	case data == "action=channels":
		b.handleChannels(ctx, chatID)
	case data == "action=help_follow":
		b.sendTextMessage(ctx, chatID, "To follow a channel, send:\n/follow <channel_id> [display name]\n\nExample:\n/follow 178281233233608705 General")
	case data == "action=unfollow_menu":
		b.handleUnfollow(ctx, chatID, nil)
	case strings.HasPrefix(data, "f:"):
		b.handleFollowCallback(ctx, chatID, messageID, data)
	case strings.HasPrefix(data, "u:"):
		b.handleUnfollowCallback(ctx, chatID, messageID, data)
	case strings.HasPrefix(data, "m:"):
		b.handleModelCallback(ctx, chatID, messageID, data)
	case strings.HasPrefix(data, "pr:"):
		b.handlePromptCallback(ctx, chatID, messageID, data)
	case data == "noop":
		// No-op (e.g. page indicator)
	}
}

func (b *Bot) handleFollowCallback(ctx context.Context, chatID int64, messageID int, data string) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 {
		return
	}

	action := parts[1]
	followKey := parts[2]

	b.mu.Lock()
	pf, exists := b.pendingFollows[followKey]
	delete(b.pendingFollows, followKey)
	b.mu.Unlock()

	if !exists {
		b.editMessage(ctx, chatID, messageID, "This request expired. Run /follow again.", nil)
		return
	}

	if action == "cancel" {
		b.editMessage(ctx, chatID, messageID, "Follow cancelled.", nil)
		return
	}

	if b.runner != nil && b.runner.IsRunning() {
		b.editMessage(ctx, chatID, messageID, "A brief is currently running. Try again when it finishes.", nil)
		return
	}

	now := b.now().UTC()
	var cursorVal string
	var modeLabel string
	switch action {
	case "now":
		cursorVal = now.Format(time.RFC3339)
		modeLabel = "From now (" + cursorVal + ")"
	case "24h":
		cursorVal = now.Add(-24 * time.Hour).Format(time.RFC3339)
		modeLabel = "Last 24 hours (" + cursorVal + ")"
	default:
		b.editMessage(ctx, chatID, messageID, "Unknown action.", nil)
		return
	}

	// Safe Follow Persistence Order:
	// 1. Load latest state & config
	st, err := b.store.LoadState()
	if err != nil {
		b.editMessage(ctx, chatID, messageID, "Failed to load state: "+err.Error(), nil)
		return
	}
	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.editMessage(ctx, chatID, messageID, "Failed to load config: "+err.Error(), nil)
		return
	}

	// 2. Validate proposed updates in memory
	st.Channels[pf.ChannelID] = state.ChannelState{
		Cursor: state.Cursor{
			Kind:  state.CursorKindTimestamp,
			Value: cursorVal,
		},
		LastSuccessAt: "",
		LastError:     "",
	}
	if err := st.Validate(); err != nil {
		b.editMessage(ctx, chatID, messageID, "State validation error: "+err.Error(), nil)
		return
	}

	cfg.Channels = append(cfg.Channels, state.ChannelConfig{
		ID:   pf.ChannelID,
		Name: pf.DisplayName,
	})
	if err := cfg.Validate(); err != nil {
		b.editMessage(ctx, chatID, messageID, "Config validation error: "+err.Error(), nil)
		return
	}

	// 3. Save STATE FIRST
	if err := b.store.SaveState(st); err != nil {
		b.editMessage(ctx, chatID, messageID, "Failed to save channel state. Follow aborted: "+err.Error(), nil)
		return
	}

	// 4. Save CONFIG SECOND
	if err := b.store.SaveConfig(cfg); err != nil {
		b.editMessage(ctx, chatID, messageID, "Saved channel state, but failed to save configuration. Follow not completed: "+err.Error(), nil)
		return
	}

	b.editMessage(ctx, chatID, messageID, fmt.Sprintf("Now following %s (%s)\nStart mode: %s", pf.DisplayName, pf.ChannelID, modeLabel), nil)
}

func (b *Bot) handleUnfollowCallback(ctx context.Context, chatID int64, messageID int, data string) {
	parts := strings.Split(data, ":")
	if len(parts) < 2 {
		return
	}

	action := parts[1]
	switch action {
	case "cancel":
		b.editMessage(ctx, chatID, messageID, "Unfollow cancelled.", nil)
		return

	case "ask":
		if len(parts) < 3 {
			return
		}
		channelID := parts[2]
		cfg, err := b.store.LoadConfig()
		if err != nil {
			b.editMessage(ctx, chatID, messageID, "Failed to load config: "+err.Error(), nil)
			return
		}

		var target *state.ChannelConfig
		for _, ch := range cfg.Channels {
			if ch.ID == channelID {
				target = &ch
				break
			}
		}
		if target == nil {
			b.editMessage(ctx, chatID, messageID, fmt.Sprintf("Channel %s is not currently followed.", channelID), nil)
			return
		}

		text := fmt.Sprintf("Unfollow #%s (%s)?", target.Name, target.ID)
		markup := &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{
					{Text: "Unfollow", CallbackData: "u:confirm:" + target.ID},
					{Text: "Cancel", CallbackData: "u:cancel"},
				},
			},
		}
		b.editMessage(ctx, chatID, messageID, text, markup)
		return

	case "confirm":
		if len(parts) < 3 {
			return
		}
		channelID := parts[2]

		if b.runner != nil && b.runner.IsRunning() {
			b.editMessage(ctx, chatID, messageID, "A brief is currently running. Try again when it finishes.", nil)
			return
		}

		// Safe Unfollow Persistence Order:
		// 1. Load latest config & state
		cfg, err := b.store.LoadConfig()
		if err != nil {
			b.editMessage(ctx, chatID, messageID, "Failed to load config: "+err.Error(), nil)
			return
		}
		st, err := b.store.LoadState()
		if err != nil {
			b.editMessage(ctx, chatID, messageID, "Failed to load state: "+err.Error(), nil)
			return
		}

		// 2. Remove channel from config
		var newChannels []state.ChannelConfig
		found := false
		for _, ch := range cfg.Channels {
			if ch.ID == channelID {
				found = true
				continue
			}
			newChannels = append(newChannels, ch)
		}
		if !found {
			b.editMessage(ctx, chatID, messageID, fmt.Sprintf("Channel %s is not currently followed.", channelID), nil)
			return
		}
		cfg.Channels = newChannels

		// 3. Remove channel from state
		delete(st.Channels, channelID)

		// 4. Save CONFIG FIRST
		if err := b.store.SaveConfig(cfg); err != nil {
			b.editMessage(ctx, chatID, messageID, "Failed to update configuration. Unfollow aborted: "+err.Error(), nil)
			return
		}

		// 5. Save STATE SECOND
		if err := b.store.SaveState(st); err != nil {
			b.editMessage(ctx, chatID, messageID, fmt.Sprintf("Unfollowed channel %s, but failed to clean up state: %v. Config is authoritative.", channelID, err), nil)
			return
		}

		b.editMessage(ctx, chatID, messageID, fmt.Sprintf("Unfollowed channel %s.", channelID), nil)
	}
}

func (b *Bot) handleBrief(ctx context.Context, chatID int64, args []string) {
	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.sendTextMessage(ctx, chatID, "Error loading configuration: "+err.Error())
		return
	}

	if len(cfg.Channels) == 0 {
		b.sendTextMessage(ctx, chatID, "No channels are currently followed.")
		return
	}

	var targetChannelID string
	var targetName string
	if len(args) > 0 {
		targetChannelID = args[0]
		found := false
		for _, ch := range cfg.Channels {
			if ch.ID == targetChannelID {
				found = true
				targetName = ch.Name
				break
			}
		}
		if !found {
			b.sendTextMessage(ctx, chatID, fmt.Sprintf("Channel %s is not currently followed.", targetChannelID))
			return
		}
	}

	if b.runner == nil {
		b.sendTextMessage(ctx, chatID, "Brief engine is not initialized.")
		return
	}

	if !b.runner.Start(b.appCtx, targetChannelID) {
		b.sendTextMessage(ctx, chatID, "Brief already running.")
		return
	}

	// Immediate acknowledgement
	if targetChannelID != "" {
		b.sendTextMessage(ctx, chatID, fmt.Sprintf("Starting brief for #%s...", targetName))
	} else {
		count := len(cfg.Channels)
		if count == 1 {
			b.sendTextMessage(ctx, chatID, "Starting brief for 1 channel...")
		} else {
			b.sendTextMessage(ctx, chatID, fmt.Sprintf("Starting brief for %d channels...", count))
		}
	}
}

func (b *Bot) handleSchedule(ctx context.Context, chatID int64, args []string) {
	if len(args) == 0 {
		// View current schedule state
		cfg, err := b.store.LoadConfig()
		if err != nil {
			b.sendTextMessage(ctx, chatID, "Error loading configuration: "+err.Error())
			return
		}

		schedState := "disabled"
		nextRunStr := "none"
		if cfg.Schedule.Enabled {
			schedState = "enabled"
			nextTime, _, err := scheduler.NextRunForConfig(cfg, b.now())
			if err != nil {
				nextRunStr = "error: " + err.Error()
			} else {
				nextRunStr = nextTime.Format("2006-01-02 15:04 -07")
			}
		}

		text := fmt.Sprintf("Daily brief: %s\nTime: %s\nTimezone: %s\nNext run: %s",
			schedState, cfg.Schedule.Time, cfg.Timezone, nextRunStr)
		b.sendTextMessage(ctx, chatID, text)
		return
	}

	// Schedule mutation requested
	if b.runner != nil && b.runner.IsRunning() {
		b.sendTextMessage(ctx, chatID, "A brief is currently running. Configuration changes are frozen until it finishes.")
		return
	}

	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.sendTextMessage(ctx, chatID, "Error loading configuration: "+err.Error())
		return
	}

	// /schedule off
	if len(args) == 1 && strings.EqualFold(args[0], "off") {
		cfg.Schedule.Enabled = false
		if err := cfg.Validate(); err != nil {
			b.sendTextMessage(ctx, chatID, "Invalid schedule configuration: "+err.Error())
			return
		}
		if err := b.store.SaveConfig(cfg); err != nil {
			b.sendTextMessage(ctx, chatID, "Failed to save configuration: "+err.Error())
			return
		}
		if b.scheduler != nil {
			b.scheduler.Wake()
		}
		text := fmt.Sprintf("Daily brief: disabled\nTime: %s\nTimezone: %s\nNext run: none",
			cfg.Schedule.Time, cfg.Timezone)
		b.sendTextMessage(ctx, chatID, text)
		return
	}

	// /schedule HH:MM
	if len(args) == 1 {
		cfg.Schedule.Time = args[0]
		cfg.Schedule.Enabled = true
		if err := cfg.Validate(); err != nil {
			b.sendTextMessage(ctx, chatID, fmt.Sprintf("Invalid schedule time %q: must be HH:MM format (e.g. 08:00).", args[0]))
			return
		}
		if err := b.store.SaveConfig(cfg); err != nil {
			b.sendTextMessage(ctx, chatID, "Failed to save configuration: "+err.Error())
			return
		}
		if b.scheduler != nil {
			b.scheduler.Wake()
		}
		nextTime, _, err := scheduler.NextRunForConfig(cfg, b.now())
		nextRunStr := "none"
		if err == nil {
			nextRunStr = nextTime.Format("2006-01-02 15:04 -07")
		}
		text := fmt.Sprintf("Daily brief: enabled\nTime: %s\nTimezone: %s\nNext run: %s",
			cfg.Schedule.Time, cfg.Timezone, nextRunStr)
		b.sendTextMessage(ctx, chatID, text)
		return
	}

	// /schedule HH:MM <timezone>
	timeVal := args[0]
	tzVal := strings.Join(args[1:], " ")
	cfg.Schedule.Time = timeVal
	cfg.Timezone = tzVal
	cfg.Schedule.Enabled = true
	if err := cfg.Validate(); err != nil {
		b.sendTextMessage(ctx, chatID, fmt.Sprintf("Invalid schedule parameters: %v\nUsage: /schedule HH:MM [timezone] (e.g. /schedule 08:00 Asia/Riyadh)", err))
		return
	}
	if err := b.store.SaveConfig(cfg); err != nil {
		b.sendTextMessage(ctx, chatID, "Failed to save configuration: "+err.Error())
		return
	}
	if b.scheduler != nil {
		b.scheduler.Wake()
	}
	nextTime, _, err := scheduler.NextRunForConfig(cfg, b.now())
	nextRunStr := "none"
	if err == nil {
		nextRunStr = nextTime.Format("2006-01-02 15:04 -07")
	}
	text := fmt.Sprintf("Daily brief: enabled\nTime: %s\nTimezone: %s\nNext run: %s",
		cfg.Schedule.Time, cfg.Timezone, nextRunStr)
	b.sendTextMessage(ctx, chatID, text)
}
