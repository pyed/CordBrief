package state_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyed/CordBrief/internal/state"
)

func TestPromptConfig(t *testing.T) {
	t.Run("default config has empty override and returns built-in default", func(t *testing.T) {
		cfg := state.DefaultConfig()
		if cfg.Brief != nil && cfg.Brief.Prompt != "" {
			t.Fatalf("expected nil/empty brief prompt in default config, got %+v", cfg.Brief)
		}
		if cfg.EffectiveBriefPrompt() != state.DefaultBriefPrompt {
			t.Fatalf("expected built-in default prompt, got %q", cfg.EffectiveBriefPrompt())
		}
	})

	t.Run("custom prompt override returns exact custom text", func(t *testing.T) {
		cfg := state.DefaultConfig()
		custom := "Prioritize technical architecture, performance benchmarks, and release notes."
		cfg.Brief = &state.BriefConfig{Prompt: custom}
		if cfg.EffectiveBriefPrompt() != custom {
			t.Fatalf("expected custom prompt %q, got %q", custom, cfg.EffectiveBriefPrompt())
		}
	})

	t.Run("resetting prompt to empty string reverts to built-in default", func(t *testing.T) {
		cfg := state.DefaultConfig()
		cfg.Brief = &state.BriefConfig{Prompt: "Custom instructions"}
		if cfg.EffectiveBriefPrompt() != "Custom instructions" {
			t.Fatalf("unexpected prompt: %s", cfg.EffectiveBriefPrompt())
		}

		cfg.Brief.Prompt = ""
		if cfg.EffectiveBriefPrompt() != state.DefaultBriefPrompt {
			t.Fatalf("expected built-in default after reset, got %q", cfg.EffectiveBriefPrompt())
		}

		cfg.Brief = nil
		if cfg.EffectiveBriefPrompt() != state.DefaultBriefPrompt {
			t.Fatalf("expected built-in default when nil, got %q", cfg.EffectiveBriefPrompt())
		}
	})

	t.Run("validation enforces 8192 character bound", func(t *testing.T) {
		cfg := state.DefaultConfig()

		// Exact max boundary (8192 bytes)
		cfg.Brief = &state.BriefConfig{Prompt: strings.Repeat("a", state.MaxBriefPromptBytes)}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected 8192-byte prompt to be valid, got: %v", err)
		}

		// Over limit (8193 bytes)
		cfg.Brief = &state.BriefConfig{Prompt: strings.Repeat("a", state.MaxBriefPromptBytes+1)}
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected 8193-byte prompt to fail validation, got nil")
		} else if !strings.Contains(err.Error(), "exceeds maximum allowed size") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("validation rejects whitespace-only non-empty prompt", func(t *testing.T) {
		cfg := state.DefaultConfig()
		cfg.Brief = &state.BriefConfig{Prompt: "   \t\n  "}
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected whitespace-only prompt to fail validation, got nil")
		} else if !strings.Contains(err.Error(), "whitespace-only") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("validation accepts multiline tabs and rejects invalid control chars", func(t *testing.T) {
		cfg := state.DefaultConfig()

		// Valid multiline with newlines and tabs
		cfg.Brief = &state.BriefConfig{Prompt: "Line 1: Summary\nLine 2:\t- Bullet A\r\nLine 3: Details"}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected valid multiline prompt, got: %v", err)
		}

		// Control character NUL (\x00)
		cfg.Brief = &state.BriefConfig{Prompt: "Hello\x00World"}
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected NUL character to fail validation")
		}

		// Control character Bell (\x07)
		cfg.Brief = &state.BriefConfig{Prompt: "Hello\x07World"}
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected Bell character to fail validation")
		}

		// Delete (\x7f)
		cfg.Brief = &state.BriefConfig{Prompt: "Hello\x7fWorld"}
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected Delete character to fail validation")
		}
	})

	t.Run("round trip preserves multiline unicode prompt", func(t *testing.T) {
		dir := t.TempDir()
		store := state.NewStore(dir)

		cfg := state.DefaultConfig()
		custom := "📌 **ملخص القناة**:\n1. التركيز على المعايير التقنية 🚀\n2. تجاهل النكات والميمز.\n3. English: Keep links <https://example.com>."
		cfg.Brief = &state.BriefConfig{Prompt: custom}

		if err := store.SaveConfig(cfg); err != nil {
			t.Fatalf("failed to save config: %v", err)
		}

		loaded, err := store.LoadConfig()
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		if loaded.Brief == nil || loaded.Brief.Prompt != custom {
			t.Fatalf("prompt mismatch after round trip:\nexpected: %q\ngot:      %+v", custom, loaded.Brief)
		}
		if loaded.EffectiveBriefPrompt() != custom {
			t.Fatalf("effective prompt mismatch: %q", loaded.EffectiveBriefPrompt())
		}
	})

	t.Run("legacy v2 config migrates safely to v3 with no custom override", func(t *testing.T) {
		dir := t.TempDir()
		store := state.NewStore(dir)

		v2Content := `{
  "version": 2,
  "channels": [
    {"id": "123456789", "name": "general"}
  ],
  "schedule": {
    "enabled": true,
    "time": "09:30"
  },
  "timezone": "Asia/Riyadh",
  "llm": {
    "base_url": "https://api.openai.com/v1",
    "model": "gpt-4o"
  }
}`
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(v2Content), 0600); err != nil {
			t.Fatalf("failed to write legacy v2 config: %v", err)
		}

		loaded, err := store.LoadConfig()
		if err != nil {
			t.Fatalf("failed to load v2 config: %v", err)
		}

		if loaded.Version != state.CurrentConfigVersion {
			t.Fatalf("expected current version, got %d", loaded.Version)
		}
		if loaded.Brief != nil {
			t.Fatalf("expected nil brief after migration, got %+v", loaded.Brief)
		}
		if loaded.EffectiveBriefPrompt() != state.DefaultBriefPrompt {
			t.Fatalf("expected default prompt, got %q", loaded.EffectiveBriefPrompt())
		}

		// Re-read disk bytes to ensure migration was persisted
		diskBytes, err := os.ReadFile(store.ConfigPath())
		if err != nil {
			t.Fatalf("failed to read migrated disk config: %v", err)
		}
		if strings.Contains(string(diskBytes), `"brief"`) {
			t.Fatalf("expected no 'brief' key on disk after v2->v3 migration: %s", string(diskBytes))
		}
	})

	t.Run("exact serialized JSON for default, custom, and reset configs", func(t *testing.T) {
		dir := t.TempDir()
		store := state.NewStore(dir)

		// 1. Default config: no "brief" key
		defaultCfg := state.DefaultConfig()
		if err := store.SaveConfig(defaultCfg); err != nil {
			t.Fatalf("failed to save default config: %v", err)
		}
		diskBytes, err := os.ReadFile(store.ConfigPath())
		if err != nil {
			t.Fatalf("failed to read default config: %v", err)
		}
		if strings.Contains(string(diskBytes), `"brief"`) {
			t.Fatalf("expected 'brief' to be omitted in default config JSON, got:\n%s", string(diskBytes))
		}

		// 2. Custom override: "brief": { "prompt": "..." } is present
		customCfg, err := store.LoadConfig()
		if err != nil {
			t.Fatalf("failed to reload config: %v", err)
		}
		customText := "Custom instructions for brief"
		customCfg.Brief = &state.BriefConfig{Prompt: customText}
		if err := store.SaveConfig(customCfg); err != nil {
			t.Fatalf("failed to save custom config: %v", err)
		}
		diskBytes, err = os.ReadFile(store.ConfigPath())
		if err != nil {
			t.Fatalf("failed to read custom config: %v", err)
		}
		if !strings.Contains(string(diskBytes), `"brief"`) || !strings.Contains(string(diskBytes), `"prompt": "Custom instructions for brief"`) {
			t.Fatalf("expected 'brief' with custom prompt in JSON, got:\n%s", string(diskBytes))
		}

		// 3. Reset: restoring Brief to nil or empty prompt removes "brief" key from serialized JSON
		loadedCustom, err := store.LoadConfig()
		if err != nil {
			t.Fatalf("failed to load custom config: %v", err)
		}
		loadedCustom.Brief = nil
		if err := store.SaveConfig(loadedCustom); err != nil {
			t.Fatalf("failed to save reset config: %v", err)
		}
		diskBytes, err = os.ReadFile(store.ConfigPath())
		if err != nil {
			t.Fatalf("failed to read reset config: %v", err)
		}
		if strings.Contains(string(diskBytes), `"brief"`) {
			t.Fatalf("expected 'brief' to be absent after reset, got:\n%s", string(diskBytes))
		}
	})
}
