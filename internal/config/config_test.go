package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleValidConfig = `{
  "guild_id": "1234567890",
  "source_channel_ids": ["111", "222"],
  "digest_channel_id": "999",
  "schedule": {
    "time": "08:30",
    "timezone": "UTC"
  },
  "llm": {
    "base_url": "http://localhost:11434/v1",
    "model": "llama3",
    "api_key_env": "TEST_LLM_KEY",
    "max_input_chars": 25000,
    "max_output_tokens": 1000
  },
  "digest": {
    "output_language": "en",
    "focus": ["alerts"],
    "ignore_bots": true,
    "first_run_lookback": "12h",
    "max_catchup": "24h"
  }
}`

func TestLoad_Valid(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(sampleValidConfig), 0600); err != nil {
		t.Fatalf("failed writing test config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("unexpected Load error: %v", err)
	}

	if cfg.GuildID != "1234567890" {
		t.Errorf("expected GuildID '1234567890', got %q", cfg.GuildID)
	}
	if len(cfg.SourceChannelIDs) != 2 {
		t.Errorf("expected 2 source channels, got %d", len(cfg.SourceChannelIDs))
	}
	if cfg.DigestChannelID != "999" {
		t.Errorf("expected DigestChannelID '999', got %q", cfg.DigestChannelID)
	}
	if cfg.Schedule.Hour != 8 || cfg.Schedule.Minute != 30 {
		t.Errorf("expected schedule 08:30, got %02d:%02d", cfg.Schedule.Hour, cfg.Schedule.Minute)
	}
	if cfg.Schedule.Location != time.UTC {
		t.Errorf("expected UTC location, got %v", cfg.Schedule.Location)
	}
	if cfg.LLM.MaxInputChars != 25000 {
		t.Errorf("expected max_input_chars 25000, got %d", cfg.LLM.MaxInputChars)
	}
	if cfg.LLM.MaxOutputTokens != 1000 {
		t.Errorf("expected max_output_tokens 1000, got %d", cfg.LLM.MaxOutputTokens)
	}
	if cfg.Digest.FirstRunLookback != 12*time.Hour {
		t.Errorf("expected 12h lookback, got %v", cfg.Digest.FirstRunLookback)
	}
	if cfg.Digest.MaxCatchup != 24*time.Hour {
		t.Errorf("expected 24h catchup, got %v", cfg.Digest.MaxCatchup)
	}
}

func TestLoad_Defaults(t *testing.T) {
	minimalConfig := `{
		"guild_id": "123",
		"source_channel_ids": ["ch1"],
		"digest_channel_id": "ch2",
		"schedule": {
			"time": "14:00",
			"timezone": "America/New_York"
		},
		"llm": {
			"base_url": "http://127.0.0.1:8000/v1",
			"model": "mistral"
		},
		"digest": {}
	}`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(minimalConfig), 0600); err != nil {
		t.Fatalf("failed writing test config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error on minimal config: %v", err)
	}

	if cfg.LLM.MaxInputChars != DefaultMaxInputChars {
		t.Errorf("expected default max input chars %d, got %d", DefaultMaxInputChars, cfg.LLM.MaxInputChars)
	}
	if cfg.LLM.MaxOutputTokens != DefaultMaxOutputToks {
		t.Errorf("expected default max output tokens %d, got %d", DefaultMaxOutputToks, cfg.LLM.MaxOutputTokens)
	}
	if cfg.Digest.OutputLanguage != DefaultLanguage {
		t.Errorf("expected default output language %q, got %q", DefaultLanguage, cfg.Digest.OutputLanguage)
	}
	if cfg.Digest.FirstRunLookback != 24*time.Hour {
		t.Errorf("expected default lookback 24h, got %v", cfg.Digest.FirstRunLookback)
	}
	if cfg.Digest.MaxCatchup != 48*time.Hour {
		t.Errorf("expected default catchup 48h, got %v", cfg.Digest.MaxCatchup)
	}
	if cfg.Digest.MaxMessagesPerChannel != DefaultMaxMessagesPerChannel {
		t.Errorf("expected default max messages per channel %d, got %d", DefaultMaxMessagesPerChannel, cfg.Digest.MaxMessagesPerChannel)
	}
}

func TestLoad_DisallowUnknownFields(t *testing.T) {
	configWithExtra := `{
		"guild_id": "123",
		"source_channel_ids": ["ch1"],
		"digest_channel_id": "ch2",
		"schedule": {
			"time": "14:00",
			"timezone": "UTC"
		},
		"llm": {
			"base_url": "http://127.0.0.1:8000/v1",
			"model": "mistral"
		},
		"digest": {},
		"state_file": "should-fail.json"
	}`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(configWithExtra), 0600); err != nil {
		t.Fatalf("failed writing test config: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error due to unknown field 'state_file', got nil")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("expected 'unknown field' error, got: %v", err)
	}
}

func TestValidate_Errors(t *testing.T) {
	tests := []struct {
		name        string
		modify      func(*Config)
		expectError string
	}{
		{
			name: "missing guild_id",
			modify: func(c *Config) {
				c.GuildID = ""
			},
			expectError: "guild_id is required",
		},
		{
			name: "empty source channels",
			modify: func(c *Config) {
				c.SourceChannelIDs = []string{}
			},
			expectError: "source_channel_ids must contain at least one channel ID",
		},
		{
			name: "blank source channel in list",
			modify: func(c *Config) {
				c.SourceChannelIDs = []string{"123", "   "}
			},
			expectError: "source_channel_ids[1] cannot be empty",
		},
		{
			name: "missing digest channel",
			modify: func(c *Config) {
				c.DigestChannelID = ""
			},
			expectError: "digest_channel_id is required",
		},
		{
			name: "invalid schedule time",
			modify: func(c *Config) {
				c.Schedule.Time = "25:99"
			},
			expectError: "invalid schedule.time",
		},
		{
			name: "invalid timezone",
			modify: func(c *Config) {
				c.Schedule.Timezone = "Mars/Olympus_Mons"
			},
			expectError: "invalid schedule.timezone",
		},
		{
			name: "invalid llm base url",
			modify: func(c *Config) {
				c.LLM.BaseURL = "not-a-url"
			},
			expectError: "invalid llm.base_url",
		},
		{
			name: "missing llm model",
			modify: func(c *Config) {
				c.LLM.Model = ""
			},
			expectError: "llm.model is required",
		},
		{
			name: "invalid first_run_lookback",
			modify: func(c *Config) {
				c.Digest.FirstRunLookbackRaw = "invalid"
			},
			expectError: "invalid digest.first_run_lookback",
		},
		{
			name: "catchup less than lookback",
			modify: func(c *Config) {
				c.Digest.FirstRunLookbackRaw = "48h"
				c.Digest.MaxCatchupRaw = "24h"
			},
			expectError: "digest.max_catchup must be greater than or equal to digest.first_run_lookback",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				GuildID:          "123",
				SourceChannelIDs: []string{"ch1"},
				DigestChannelID:  "ch2",
				Schedule: ScheduleConfig{
					Time:     "08:00",
					Timezone: "UTC",
				},
				LLM: LLMConfig{
					BaseURL: "http://localhost:8080/v1",
					Model:   "model",
				},
				Digest: DigestConfig{},
			}
			tc.modify(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.expectError)
			}
			if !strings.Contains(err.Error(), tc.expectError) {
				t.Errorf("expected error %q, got: %v", tc.expectError, err)
			}
		})
	}
}

func TestConfig_Secrets(t *testing.T) {
	cfg := &Config{
		LLM: LLMConfig{
			APIKeyEnv: "TEST_API_KEY_VAR",
		},
	}

	t.Setenv(EnvDiscordToken, "test-bot-token-12345")
	t.Setenv("TEST_API_KEY_VAR", "secret-llm-key-999")

	tok, err := cfg.DiscordToken()
	if err != nil {
		t.Fatalf("unexpected error getting discord token: %v", err)
	}
	if tok != "test-bot-token-12345" {
		t.Errorf("expected 'test-bot-token-12345', got %q", tok)
	}

	apiKey := cfg.LLMAPIKey()
	if apiKey != "secret-llm-key-999" {
		t.Errorf("expected 'secret-llm-key-999', got %q", apiKey)
	}

	// Unset discord token and verify failure
	t.Setenv(EnvDiscordToken, "")
	_, err = cfg.DiscordToken()
	if err == nil {
		t.Fatal("expected error when Discord token is empty, got nil")
	}
}

func TestValidateForChannels(t *testing.T) {
	cfg := &Config{
		GuildID: "123456",
	}
	if err := cfg.ValidateForChannels(); err != nil {
		t.Fatalf("expected nil error with guild_id set, got: %v", err)
	}

	cfg.GuildID = "   "
	if err := cfg.ValidateForChannels(); err == nil {
		t.Fatal("expected error when guild_id is empty, got nil")
	}
}
