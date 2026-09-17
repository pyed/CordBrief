package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pyed/CordBrief/internal/state"
)

// Endpoint and secret changes stay in the existing offline setup/config path.
func (b *Bot) handleFallback(ctx context.Context, chatID int64, args []string) {
	if len(args) > 0 {
		err := b.mutateState(func() error {
			cfg, err := b.store.LoadConfig()
			if err != nil {
				return err
			}
			if cfg.Fallback == nil {
				return errors.New("Configure fallback.base_url and fallback.model in config.json while stopped, and its separate key with --setup. Never send keys here.")
			}
			switch {
			case len(args) == 1 && args[0] == "on":
				cfg.Fallback.Enabled = true
			case len(args) == 1 && args[0] == "off":
				cfg.Fallback.Enabled = false
			case len(args) == 2 && args[0] == "model" && len(args[1]) <= 128 && strings.TrimSpace(args[1]) != "":
				cfg.Fallback.Model = args[1]
			default:
				return errors.New("Usage: /fallback [on|off|model <model_id>]. Set keys only with --setup or FALLBACK_LLM_API_KEY.")
			}
			return b.store.SaveConfig(cfg)
		})
		if err != nil {
			b.sendTextMessage(ctx, chatID, err.Error())
			return
		}
	}
	cfg, err := b.store.LoadConfig()
	if err != nil {
		b.sendTextMessage(ctx, chatID, "Cannot load fallback configuration.")
		return
	}
	b.sendTextMessage(ctx, chatID, fallbackStatus(cfg)+"\n\n/fallback on · /fallback off · /fallback model <model_id>\nEndpoint: edit fallback in config.json while stopped. Separate key: --setup or FALLBACK_LLM_API_KEY, then restart. Never send API keys in Telegram.")
}

func fallbackStatus(cfg *state.Config) string {
	if cfg.Fallback == nil {
		return "Fallback LLM: not configured"
	}
	status := "disabled"
	if cfg.Fallback.Enabled {
		status = "enabled"
	}
	return fmt.Sprintf("Fallback LLM: %s · %s\nEndpoint: %s", status, cfg.Fallback.Model, cfg.Fallback.BaseURL)
}
