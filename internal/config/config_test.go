package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleValidConfig = `{
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
    "ignore_bots": true
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
}

func TestLoad_Defaults(t *testing.T) {
	minimalConfig := `{
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
}

func TestLoad_DisallowUnknownFields(t *testing.T) {
	configWithExtra := `{
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
			name: "invalid llm provider",
			modify: func(c *Config) {
				c.LLM.Provider = "unsupported"
			},
			expectError: "must be \"gemini\" or \"local\"",
		},
		{
			name: "invalid local llm base url",
			modify: func(c *Config) {
				c.LLM.Provider = "local"
				c.LLM.BaseURL = "not-a-url"
			},
			expectError: "invalid llm.base_url",
		},
		{
			name: "missing local llm base url",
			modify: func(c *Config) {
				c.LLM.Provider = "local"
				c.LLM.BaseURL = ""
			},
			expectError: "llm.base_url is required for provider 'local'",
		},
		{
			name: "missing local llm model",
			modify: func(c *Config) {
				c.LLM.Provider = "local"
				c.LLM.Model = ""
			},
			expectError: "llm.model is required for provider 'local'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Schedule: ScheduleConfig{
					Time:     "08:00",
					Timezone: "UTC",
				},
				LLM: LLMConfig{
					Provider: "local",
					BaseURL:  "http://localhost:8080/v1",
					Model:    "model",
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

func TestLLM_GeminiDefaults(t *testing.T) {
	cfg := &Config{
		Schedule: ScheduleConfig{
			Time:     "08:00",
			Timezone: "UTC",
		},
		LLM: LLMConfig{
			Provider: "gemini",
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	if cfg.LLM.BaseURL != DefaultGeminiBaseURL {
		t.Errorf("expected default gemini base URL %s, got %s", DefaultGeminiBaseURL, cfg.LLM.BaseURL)
	}
	if cfg.LLM.Model != DefaultGeminiModel {
		t.Errorf("expected default gemini model %s, got %s", DefaultGeminiModel, cfg.LLM.Model)
	}
	if cfg.LLM.APIKeyEnv != DefaultGeminiKeyEnv {
		t.Errorf("expected default gemini api key env %s, got %s", DefaultGeminiKeyEnv, cfg.LLM.APIKeyEnv)
	}
	if cfg.LLM.MaxInputChars != DefaultGeminiMaxInput {
		t.Errorf("expected max input chars %d, got %d", DefaultGeminiMaxInput, cfg.LLM.MaxInputChars)
	}
	if cfg.LLM.MaxOutputTokens != DefaultGeminiMaxOutput {
		t.Errorf("expected max output tokens %d, got %d", DefaultGeminiMaxOutput, cfg.LLM.MaxOutputTokens)
	}
	if cfg.LLM.TimeoutSeconds != DefaultGeminiTimeout {
		t.Errorf("expected timeout %d, got %d", DefaultGeminiTimeout, cfg.LLM.TimeoutSeconds)
	}
}

func TestLLM_LocalConfiguration(t *testing.T) {
	cfg := &Config{
		Schedule: ScheduleConfig{
			Time:     "08:00",
			Timezone: "UTC",
		},
		LLM: LLMConfig{
			Provider: "local",
			BaseURL:  "http://host.docker.internal:8081/v1",
			Model:    "Qwen-custom",
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	if cfg.LLM.BaseURL != "http://host.docker.internal:8081/v1" {
		t.Errorf("unexpected base URL: %s", cfg.LLM.BaseURL)
	}
	if cfg.LLM.Model != "Qwen-custom" {
		t.Errorf("unexpected model: %s", cfg.LLM.Model)
	}
	// API key is optional for local
	if cfg.LLM.APIKeyEnv != "" {
		t.Errorf("expected empty APIKeyEnv for local by default, got %s", cfg.LLM.APIKeyEnv)
	}
	if cfg.LLM.TimeoutSeconds != DefaultLocalTimeout {
		t.Errorf("expected default local timeout %d, got %d", DefaultLocalTimeout, cfg.LLM.TimeoutSeconds)
	}
}

func TestPortHardeningDefaults(t *testing.T) {
	if DefaultCorePort != 28741 {
		t.Fatalf("expected DefaultCorePort 28741, got %d", DefaultCorePort)
	}
	if DefaultSetupPort != 28742 {
		t.Fatalf("expected DefaultSetupPort 28742, got %d", DefaultSetupPort)
	}
}

func TestDeliveryConfigValidation(t *testing.T) {
	cfg := &Config{
		Schedule: ScheduleConfig{
			Time:     "08:00",
			Timezone: "UTC",
		},
		Delivery: DeliveryConfig{
			Telegram: TelegramConfig{
				Enabled: true,
				ChatID:  "",
			},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when delivery.telegram.enabled is true but chat_id is empty, got nil")
	}

	cfg.Delivery.Telegram.ChatID = "-1001234567890"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config when chat_id is populated, got: %v", err)
	}
}

func TestComposeConfigurationInvariants(t *testing.T) {
	composeBytes, err := os.ReadFile("../../docker/compose.yml")
	if err != nil {
		t.Fatalf("failed to read docker/compose.yml: %v", err)
	}
	content := string(composeBytes)

	// 1. Secret forwarding: env_file must point to ../.env (required: false)
	if !strings.Contains(content, "path: ../.env") || !strings.Contains(content, "required: false") {
		t.Errorf("docker/compose.yml missing env_file forwarding for ../.env")
	}

	// 3. Port hardening: Core web port
	if !strings.Contains(content, `"127.0.0.1:28741:28741"`) {
		t.Errorf("docker/compose.yml missing hardened Core port binding 127.0.0.1:28741:28741")
	}

	// 4. Port hardening: Setup port
	if !strings.Contains(content, `"127.0.0.1:28742:28742"`) {
		t.Errorf("docker/compose.yml missing hardened Setup port binding 127.0.0.1:28742:28742")
	}

	// 5. Port hardening: Collector has zero published host ports
	collectorBlockIdx := strings.Index(content, "cordbrief-collector:")
	setupBlockIdx := strings.Index(content, "cordbrief-setup:")
	if collectorBlockIdx == -1 || setupBlockIdx == -1 || setupBlockIdx <= collectorBlockIdx {
		t.Fatalf("unexpected compose structure: cordbrief-collector / cordbrief-setup blocks")
	}
	collectorBlock := content[collectorBlockIdx:setupBlockIdx]
	if strings.Contains(collectorBlock, "ports:") {
		t.Errorf("POLICY VIOLATION: cordbrief-collector must have zero published host ports, found 'ports:' in block")
	}
}
