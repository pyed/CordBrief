package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/pyed/CordBrief/internal/job"
	"github.com/pyed/CordBrief/internal/state"
)

func (b *Bot) handlePrompt(ctx context.Context, chatID int64, args []string) {
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "reset":
			b.executePromptReset(ctx, chatID, 0)
			return
		case "view", "show":
			b.showPromptView(ctx, chatID, 0)
			return
		default:
			b.sendTextMessage(ctx, chatID, "Usage: /prompt [view|reset]")
			return
		}
	}

	b.showPromptBrowser(ctx, chatID, 0)
}

func (b *Bot) showPromptBrowser(ctx context.Context, chatID int64, messageID int) {
	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.renderOrEditPrompt(ctx, chatID, messageID, "Error loading configuration: "+err.Error(), nil)
		return
	}

	status := "Default"
	if cfg.Brief != nil && strings.TrimSpace(cfg.Brief.Prompt) != "" {
		status = "Custom"
	}

	text := fmt.Sprintf("Brief prompt\n\nUsing: %s", status)
	markup := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "View prompt", CallbackData: "pr:view"}},
			{{Text: "Edit prompt", CallbackData: "pr:edit"}},
			{{Text: "Reset to default", CallbackData: "pr:reset"}},
			{{Text: "Cancel", CallbackData: "pr:cancel"}},
		},
	}

	b.renderOrEditPrompt(ctx, chatID, messageID, text, markup)
}

func (b *Bot) renderOrEditPrompt(ctx context.Context, chatID int64, messageID int, text string, markup *models.InlineKeyboardMarkup) {
	if messageID > 0 {
		b.editMessage(ctx, chatID, messageID, text, markup)
	} else {
		b.sendMessageWithMarkup(ctx, chatID, text, markup)
	}
}

func (b *Bot) handlePromptCallback(ctx context.Context, chatID int64, messageID int, data string) {
	switch data {
	case "pr:cancel":
		b.mu.Lock()
		b.pendingPromptEdit = false
		b.mu.Unlock()
		b.editMessage(ctx, chatID, messageID, "Prompt settings closed.", nil)

	case "pr:view":
		b.showPromptView(ctx, chatID, messageID)

	case "pr:edit":
		if b.runner != nil && b.runner.IsRunning() {
			b.editMessage(ctx, chatID, messageID, "A brief is currently running. Change the prompt after it finishes.", nil)
			return
		}

		b.mu.Lock()
		b.pendingPromptEdit = true
		b.mu.Unlock()

		b.editMessage(ctx, chatID, messageID, "Send the new brief prompt in your next message.\nSend /cancel to abort.", nil)

	case "pr:reset":
		b.executePromptReset(ctx, chatID, messageID)
	}
}

func (b *Bot) showPromptView(ctx context.Context, chatID int64, messageID int) {
	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.renderOrEditPrompt(ctx, chatID, messageID, "Error loading configuration: "+err.Error(), nil)
		return
	}

	effectivePrompt := cfg.EffectiveBriefPrompt()
	viewText := fmt.Sprintf("Effective Brief Prompt:\n\n%s", effectivePrompt)

	parts := job.SplitText(viewText, job.DefaultMaxTelegramRunes)
	if len(parts) == 0 {
		return
	}

	b.renderOrEditPrompt(ctx, chatID, messageID, parts[0], nil)
	for _, part := range parts[1:] {
		b.sendTextMessage(ctx, chatID, part)
	}
}

func (b *Bot) executePromptReset(ctx context.Context, chatID int64, messageID int) {
	b.mu.Lock()
	b.pendingPromptEdit = false
	b.mu.Unlock()

	if b.runner != nil && b.runner.IsRunning() {
		msg := "A brief is currently running. Change the prompt after it finishes."
		b.renderOrEditPrompt(ctx, chatID, messageID, msg, nil)
		return
	}

	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.renderOrEditPrompt(ctx, chatID, messageID, "Error loading configuration: "+err.Error(), nil)
		return
	}

	cfg.Brief = nil
	if err := cfg.Validate(); err != nil {
		b.renderOrEditPrompt(ctx, chatID, messageID, "Invalid configuration: "+err.Error(), nil)
		return
	}

	if err := b.store.SaveConfig(cfg); err != nil {
		b.renderOrEditPrompt(ctx, chatID, messageID, "Failed to save configuration: "+err.Error(), nil)
		return
	}

	b.renderOrEditPrompt(ctx, chatID, messageID, "Brief prompt reset to default.", nil)
}

func (b *Bot) submitPromptEdit(ctx context.Context, chatID int64, rawText string) {
	b.mu.Lock()
	b.pendingPromptEdit = false
	b.mu.Unlock()

	if b.runner != nil && b.runner.IsRunning() {
		b.sendTextMessage(ctx, chatID, "A brief is currently running. Change the prompt after it finishes.")
		return
	}

	newPrompt := strings.TrimSpace(rawText)
	if newPrompt == "" {
		b.sendTextMessage(ctx, chatID, "Brief prompt cannot be empty.")
		return
	}

	if len(newPrompt) > state.MaxBriefPromptBytes {
		b.sendTextMessage(ctx, chatID, fmt.Sprintf("Brief prompt exceeds maximum allowed size (%d characters).", state.MaxBriefPromptBytes))
		return
	}

	for i, r := range newPrompt {
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7f {
			b.sendTextMessage(ctx, chatID, fmt.Sprintf("Brief prompt contains invalid control character at byte %d.", i))
			return
		}
	}

	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.sendTextMessage(ctx, chatID, "Error loading configuration: "+err.Error())
		return
	}

	cfg.Brief = &state.BriefConfig{Prompt: newPrompt}
	if err := cfg.Validate(); err != nil {
		b.sendTextMessage(ctx, chatID, "Invalid configuration: "+err.Error())
		return
	}

	if err := b.store.SaveConfig(cfg); err != nil {
		b.sendTextMessage(ctx, chatID, "Failed to save configuration: "+err.Error())
		return
	}

	b.sendTextMessage(ctx, chatID, "Brief prompt updated.\n\nSubsequent briefs will use the custom prompt.")
}
