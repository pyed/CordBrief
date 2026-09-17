package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/pyed/CordBrief/internal/state"
)

func TestFallbackControls(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()
	message := func(text string) { b.HandleUpdate(ctx, nil, makeMsg(12345, "private", text)) }
	message("/fallback")
	if !strings.Contains(sender.lastSent().Text, "not configured") {
		t.Fatal("missing status")
	}
	message("/fallback on")
	if !strings.Contains(sender.lastSent().Text, "--setup") {
		t.Fatal("missing secure setup guidance")
	}
	cfg := state.DefaultConfig()
	cfg.Fallback = &state.FallbackConfig{LLMConfig: state.LLMConfig{BaseURL: "https://fallback.example/v1", Model: "alternate"}}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	message("/fallback on")
	message("/fallback model new-model")
	cfg, _ = store.LoadConfig()
	if !cfg.Fallback.Enabled || cfg.Fallback.Model != "new-model" {
		t.Fatal("config not saved")
	}
	message("/fallback key secret-do-not-accept")
	if strings.Contains(sender.lastSent().Text, "secret-do-not-accept") {
		t.Fatal("invalid input echoed")
	}
	b.HandleUpdate(ctx, nil, makeMsg(777, "private", "/fallback off"))
	cfg, _ = store.LoadConfig()
	if !cfg.Fallback.Enabled {
		t.Fatal("unauthorized mutation")
	}
	message("/fallback off")
	cfg, _ = store.LoadConfig()
	if cfg.Fallback.Enabled || cfg.Fallback.Model != "new-model" {
		t.Fatal("disable discarded profile")
	}
}

func TestFallbackCredentialSetupAndOverrides(t *testing.T) {
	credentialTestEnv(t)
	values := []string{"telegram", "123", "discord", "primary", "fallback"}
	i := 0
	if err := configure(func(string) (string, error) { value := values[i]; i++; return value, nil }); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FALLBACK_LLM_API_KEY", "override")
	cfg, err := LoadEnv()
	if err != nil || cfg.LLMAPIKey != "primary" || cfg.FallbackAPIKey != "override" {
		t.Fatal("separate override failed", err)
	}
	stored, _ := loadStoredCredentials()
	if stored.FallbackAPIKey != "fallback" {
		t.Fatal("override persisted")
	}
	t.Setenv("FALLBACK_LLM_API_KEY", "")
	cfg, _ = LoadEnv()
	if cfg.FallbackAPIKey != "" || cfg.LLMAPIKey != "primary" {
		t.Fatal("empty fallback inherited primary")
	}
	i = 0
	if err := configure(func(string) (string, error) {
		i++
		if i == 5 {
			return "-", nil
		}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	stored, _ = loadStoredCredentials()
	if stored.FallbackAPIKey != "" || stored.LLMAPIKey != "primary" {
		t.Fatal("clear affected wrong key")
	}
	secretCfg := &EnvConfig{FallbackAPIKey: "fallback-secret"}
	if strings.Contains(secretCfg.Redact("error fallback-secret"), "fallback-secret") {
		t.Fatal("fallback not redacted")
	}
}
