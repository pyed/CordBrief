package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/pyed/CordBrief/internal/llm"
)

const modelPageSize = 8

func (b *Bot) getModelLister(baseURL, model string) (ModelLister, error) {
	if b.modelLister != nil {
		return b.modelLister, nil
	}
	return llm.NewClient(baseURL, model, b.llmAPIKey, nil)
}

func (b *Bot) renderOrEditModel(ctx context.Context, chatID int64, messageID int, text string, markup *models.InlineKeyboardMarkup) {
	if messageID > 0 {
		b.editMessage(ctx, chatID, messageID, text, markup)
	} else {
		b.sendMessageWithMarkup(ctx, chatID, text, markup)
	}
}

func (b *Bot) handleModel(ctx context.Context, chatID int64, args []string) {
	if len(args) == 0 {
		b.showModelBrowser(ctx, chatID, 0, 0, false)
		return
	}

	newModel := strings.TrimSpace(strings.Join(args, " "))
	if newModel == "" {
		b.sendTextMessage(ctx, chatID, "Model identifier cannot be empty.")
		return
	}
	if len(newModel) > 128 || strings.ContainsAny(newModel, "\r\n\t") {
		b.sendTextMessage(ctx, chatID, "Invalid model identifier: must be 1-128 printable characters without newlines.")
		return
	}

	if b.runner != nil && b.runner.IsRunning() {
		b.sendTextMessage(ctx, chatID, "A brief is currently running. Change the model after it finishes.")
		return
	}

	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.sendTextMessage(ctx, chatID, "Error loading configuration: "+err.Error())
		return
	}

	cfg.LLM.Model = newModel
	if err := cfg.Validate(); err != nil {
		b.sendTextMessage(ctx, chatID, "Invalid configuration: "+err.Error())
		return
	}

	if err := b.store.SaveConfig(cfg); err != nil {
		b.sendTextMessage(ctx, chatID, "Failed to save configuration: "+err.Error())
		return
	}

	b.sendTextMessage(ctx, chatID, fmt.Sprintf("LLM model updated to: %s\n\nSubsequent briefs will use this model.", newModel))
}

func (b *Bot) showModelBrowser(ctx context.Context, chatID int64, messageID int, page int, forceRefresh bool) {
	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.renderOrEditModel(ctx, chatID, messageID, "Error loading configuration: "+err.Error(), nil)
		return
	}

	modelsList, gen, cached := b.modelCache.Get(cfg.LLM.BaseURL)
	if !cached || forceRefresh {
		if b.runner != nil && b.runner.IsRunning() {
			b.renderOrEditModel(ctx, chatID, messageID, "A brief is currently running. Cannot refresh models while a brief is active.", nil)
			return
		}

		lister, err := b.getModelLister(cfg.LLM.BaseURL, cfg.LLM.Model)
		if err != nil {
			b.renderOrEditModel(ctx, chatID, messageID, "Failed to initialize LLM client: "+err.Error(), nil)
			return
		}

		discovered, err := lister.ListModels(ctx)
		if err != nil {
			b.renderOrEditModel(ctx, chatID, messageID, "Failed to discover models: "+err.Error(), nil)
			return
		}

		var idList []string
		for _, m := range discovered {
			idList = append(idList, m.ModelID)
		}
		gen = b.modelCache.Set(cfg.LLM.BaseURL, idList)
		modelsList = idList
	}

	if len(modelsList) == 0 {
		text := fmt.Sprintf("LLM Model\n\nCurrent: %s\n\nNo models reported by provider.", cfg.LLM.Model)
		markup := &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{
					{Text: "↻ Refresh models", CallbackData: "m:ref"},
					{Text: "Cancel", CallbackData: "m:cancel"},
				},
			},
		}
		b.renderOrEditModel(ctx, chatID, messageID, text, markup)
		return
	}

	totalPages := (len(modelsList) + modelPageSize - 1) / modelPageSize
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}

	start := page * modelPageSize
	end := start + modelPageSize
	if end > len(modelsList) {
		end = len(modelsList)
	}
	pageItems := modelsList[start:end]

	var rows [][]models.InlineKeyboardButton
	for i, m := range pageItems {
		globalIdx := start + i
		btnText := m
		if m == cfg.LLM.Model {
			btnText = "✓ " + m
		}
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: btnText, CallbackData: fmt.Sprintf("m:s:%d:%d", gen, globalIdx)},
		})
	}

	// Pagination row if multiple pages exist
	if totalPages > 1 {
		var navRow []models.InlineKeyboardButton
		if page > 0 {
			navRow = append(navRow, models.InlineKeyboardButton{
				Text:         "«",
				CallbackData: fmt.Sprintf("m:p:%d:%d", gen, page-1),
			})
		}
		navRow = append(navRow, models.InlineKeyboardButton{
			Text:         fmt.Sprintf("%d / %d", page+1, totalPages),
			CallbackData: "noop",
		})
		if page < totalPages-1 {
			navRow = append(navRow, models.InlineKeyboardButton{
				Text:         "»",
				CallbackData: fmt.Sprintf("m:p:%d:%d", gen, page+1),
			})
		}
		rows = append(rows, navRow)
	}

	// Action row
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "↻ Refresh models", CallbackData: "m:ref"},
		{Text: "Cancel", CallbackData: "m:cancel"},
	})

	text := fmt.Sprintf("LLM Model\n\nCurrent: %s\n\nModels reported by provider:\n(Note: Some provider models may not support chat summaries.)", cfg.LLM.Model)
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: rows}
	b.renderOrEditModel(ctx, chatID, messageID, text, markup)
}

func (b *Bot) handleModelCallback(ctx context.Context, chatID int64, messageID int, data string) {
	switch {
	case data == "m:cancel":
		b.editMessage(ctx, chatID, messageID, "Model selection cancelled.", nil)
	case data == "m:ref":
		b.showModelBrowser(ctx, chatID, messageID, 0, true)
	case strings.HasPrefix(data, "m:p:"):
		parts := strings.Split(data, ":")
		if len(parts) != 4 {
			return
		}
		page, err := strconv.Atoi(parts[3])
		if err != nil {
			return
		}
		b.showModelBrowser(ctx, chatID, messageID, page, false)
	case strings.HasPrefix(data, "m:s:"):
		parts := strings.Split(data, ":")
		if len(parts) != 4 {
			return
		}
		gen, err1 := strconv.ParseInt(parts[2], 10, 64)
		idx, err2 := strconv.Atoi(parts[3])
		if err1 != nil || err2 != nil {
			return
		}

		if b.runner != nil && b.runner.IsRunning() {
			b.editMessage(ctx, chatID, messageID, "A brief is currently running. Change the model after it finishes.", nil)
			return
		}

		cfg, err := b.store.LoadConfig()
		if err != nil {
			b.editMessage(ctx, chatID, messageID, "Error loading configuration: "+err.Error(), nil)
			return
		}

		selectedModel, ok := b.modelCache.Validate(cfg.LLM.BaseURL, gen, idx)
		if !ok {
			b.editMessage(ctx, chatID, messageID, "Model list changed. Open /model again.", nil)
			return
		}

		if cfg.LLM.Model == selectedModel {
			b.editMessage(ctx, chatID, messageID, fmt.Sprintf("Model is already set to %s.", selectedModel), nil)
			return
		}

		cfg.LLM.Model = selectedModel
		if err := cfg.Validate(); err != nil {
			b.editMessage(ctx, chatID, messageID, "Invalid configuration: "+err.Error(), nil)
			return
		}

		if err := b.store.SaveConfig(cfg); err != nil {
			b.editMessage(ctx, chatID, messageID, "Failed to save configuration: "+err.Error(), nil)
			return
		}

		b.editMessage(ctx, chatID, messageID, fmt.Sprintf("LLM model updated to:\n%s\n\nSubsequent briefs will use this model.", selectedModel), nil)
	}
}
