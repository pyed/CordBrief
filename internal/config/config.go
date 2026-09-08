package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
	_ "time/tzdata"
)

const (
	EnvGeminiKey = "GEMINI_API_KEY"

	ProviderGemini = "gemini"
	ProviderLocal  = "local"

	DefaultGeminiBaseURL   = "https://generativelanguage.googleapis.com/v1beta/openai/"
	DefaultGeminiModel     = "gemini-3.7-flash"
	DefaultGeminiKeyEnv    = "GEMINI_API_KEY"
	DefaultGeminiMaxInput  = 200000
	DefaultGeminiMaxOutput = 8192
	DefaultGeminiTimeout   = 120

	DefaultLocalMaxInput  = 200000
	DefaultLocalMaxOutput = 8192
	DefaultLocalTimeout   = 180

	DefaultMaxInputChars = DefaultGeminiMaxInput
	DefaultMaxOutputToks = DefaultGeminiMaxOutput

	DefaultCorePort  = 28741
	DefaultSetupPort = 28742

	EnvTelegramBotToken = "TELEGRAM_BOT_TOKEN"

	DefaultLanguage = "en"
)

// TelegramConfig defines Telegram digest delivery destination.
type TelegramConfig struct {
	Enabled   bool   `json:"enabled"`
	ChatID    string `json:"chat_id"`
	ChatLabel string `json:"chat_label,omitempty"`
}

// DeliveryConfig defines destination endpoints for digest delivery.
type DeliveryConfig struct {
	Telegram TelegramConfig `json:"telegram"`
}

// ScheduleConfig defines scheduling and timezone properties.
type ScheduleConfig struct {
	Enabled  bool   `json:"enabled"`
	Time     string `json:"time"`
	Timezone string `json:"timezone"`

	// Parsed cache populated by Validate
	Location *time.Location `json:"-"`
	Hour     int            `json:"-"`
	Minute   int            `json:"-"`
}

// Validate validates the schedule configuration fields and caches parsed location and time.
func (s *ScheduleConfig) Validate() error {
	trimmedTime := strings.TrimSpace(s.Time)
	if trimmedTime == "" {
		return errors.New("schedule.time is required (format HH:MM)")
	}
	t, err := time.Parse("15:04", trimmedTime)
	if err != nil {
		return fmt.Errorf("invalid schedule.time %q: must be HH:MM (24-hour)", s.Time)
	}
	s.Hour = t.Hour()
	s.Minute = t.Minute()

	trimmedTz := strings.TrimSpace(s.Timezone)
	if trimmedTz == "" {
		return errors.New("schedule.timezone is required (e.g. 'America/New_York' or 'UTC')")
	}
	loc, err := time.LoadLocation(trimmedTz)
	if err != nil {
		return fmt.Errorf("invalid schedule.timezone %q: %w", s.Timezone, err)
	}
	s.Location = loc
	return nil
}

// LLMConfig defines language model connection settings for Gemini and Local LLM.
type LLMConfig struct {
	Provider        string `json:"provider"` // "gemini" or "local"
	BaseURL         string `json:"base_url,omitempty"`
	Model           string `json:"model,omitempty"`
	APIKeyEnv       string `json:"api_key_env,omitempty"`
	MaxInputChars   int    `json:"max_input_chars,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
}

// DigestConfig defines summarization parameters and filtering.
type DigestConfig struct {
	OutputLanguage string   `json:"output_language"`
	Focus          []string `json:"focus,omitempty"`
	IgnoreBots     bool     `json:"ignore_bots"`
}

// Config represents the top-level configuration for CordBrief, grouped conceptually.
type Config struct {
	Schedule ScheduleConfig `json:"schedule"`
	LLM      LLMConfig      `json:"llm"`
	Digest   DigestConfig   `json:"digest"`
	Delivery DeliveryConfig `json:"delivery"`
}

// Load reads and validates a JSON configuration file from disk.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config file: %w", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config json: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}

// Validate checks the file-based digest configuration.
func (c *Config) Validate() error {
	var errs []string

	// Schedule
	if err := c.Schedule.Validate(); err != nil {
		errs = append(errs, err.Error())
	}

	// LLM validation
	p := strings.ToLower(strings.TrimSpace(c.LLM.Provider))
	if p == "" {
		p = ProviderGemini
	}

	switch p {
	case ProviderGemini:
		c.LLM.Provider = ProviderGemini
		if strings.TrimSpace(c.LLM.BaseURL) == "" {
			c.LLM.BaseURL = DefaultGeminiBaseURL
		} else {
			u, err := url.Parse(c.LLM.BaseURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				errs = append(errs, fmt.Sprintf("invalid llm.base_url %q: must be a valid http or https URL", c.LLM.BaseURL))
			}
		}
		if strings.TrimSpace(c.LLM.Model) == "" {
			c.LLM.Model = DefaultGeminiModel
		}
		if strings.TrimSpace(c.LLM.APIKeyEnv) == "" {
			c.LLM.APIKeyEnv = DefaultGeminiKeyEnv
		}
		if c.LLM.MaxInputChars <= 0 {
			c.LLM.MaxInputChars = DefaultGeminiMaxInput
		}
		if c.LLM.MaxOutputTokens <= 0 {
			c.LLM.MaxOutputTokens = DefaultGeminiMaxOutput
		}
		if c.LLM.TimeoutSeconds <= 0 {
			c.LLM.TimeoutSeconds = DefaultGeminiTimeout
		}

	case ProviderLocal:
		c.LLM.Provider = ProviderLocal
		if strings.TrimSpace(c.LLM.BaseURL) == "" {
			errs = append(errs, "llm.base_url is required for provider 'local' (e.g. 'http://host.docker.internal:8081/v1')")
		} else {
			u, err := url.Parse(c.LLM.BaseURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				errs = append(errs, fmt.Sprintf("invalid llm.base_url %q: must be a valid http or https URL", c.LLM.BaseURL))
			}
		}
		if strings.TrimSpace(c.LLM.Model) == "" {
			errs = append(errs, "llm.model is required for provider 'local'")
		}
		if c.LLM.MaxInputChars <= 0 {
			c.LLM.MaxInputChars = DefaultLocalMaxInput
		}
		if c.LLM.MaxOutputTokens <= 0 {
			c.LLM.MaxOutputTokens = DefaultLocalMaxOutput
		}
		if c.LLM.TimeoutSeconds <= 0 {
			c.LLM.TimeoutSeconds = DefaultLocalTimeout
		}

	default:
		errs = append(errs, fmt.Sprintf("invalid llm.provider %q: must be %q or %q", c.LLM.Provider, ProviderGemini, ProviderLocal))
	}

	// Digest
	if strings.TrimSpace(c.Digest.OutputLanguage) == "" {
		c.Digest.OutputLanguage = DefaultLanguage
	}

	// Delivery validation
	if c.Delivery.Telegram.Enabled && strings.TrimSpace(c.Delivery.Telegram.ChatID) == "" {
		errs = append(errs, "delivery.telegram.chat_id is required when delivery.telegram.enabled is true")
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}

	return nil
}
