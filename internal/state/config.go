package state

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Default non-secret configuration values.
const (
	DefaultLLMBaseURL         = "https://generativelanguage.googleapis.com/v1beta/openai/"
	DefaultLLMModel           = "gemini-3.8-flash"
	DefaultTimezone           = "UTC"
	DefaultSchedule           = "08:00"
	DefaultBriefPrompt        = "Create a short, high-signal digest of this Discord discussion. Include only what would matter to someone catching up: important developments, decisions, conclusions, solutions, technical findings, useful recommendations, notable releases or announcements, unresolved problems, and meaningful disagreements. Group related messages into topics instead of summarizing message-by-message, and order the brief by importance. Preserve concrete details when they matter—names, versions, numbers, benchmarks, links, errors, constraints, and attribution when it changes the meaning. Omit greetings, jokes, reactions, repetition, and low-value chatter. Compress aggressively, but never omit a detail that changes the meaning, outcome, risk, or next action. If little happened, keep the brief very short rather than padding it."
	MaxBriefPromptBytes       = 8192
	DefaultDCECooldownSeconds = 15 * 60
)

// ChannelConfig represents a Discord channel followed by CordBrief.
// The channel ID is authoritative; name is display metadata only.
type ChannelConfig struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ScheduleConfig defines whether and when daily briefs are generated.
type ScheduleConfig struct {
	Enabled bool   `json:"enabled"`
	Time    string `json:"time"`
}

// LLMConfig stores non-secret LLM endpoint settings.
type LLMConfig struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
}

type FallbackConfig struct {
	Enabled bool `json:"enabled"`
	LLMConfig
}

// BriefConfig stores user-customizable brief instructions.
// An empty Prompt or nil pointer indicates the built-in DefaultBriefPrompt should be used.
type BriefConfig struct {
	Prompt string `json:"prompt,omitempty"`
}

// Config represents user intent stored in config.json.
type Config struct {
	Version            int             `json:"version"`
	Channels           []ChannelConfig `json:"channels"`
	Schedule           ScheduleConfig  `json:"schedule"`
	Timezone           string          `json:"timezone"`
	LLM                LLMConfig       `json:"llm"`
	Fallback           *FallbackConfig `json:"fallback,omitempty"`
	HiddenChannels     []ChannelConfig `json:"hidden_channels,omitempty"`
	Brief              *BriefConfig    `json:"brief,omitempty"`
	DCECooldownSeconds int             `json:"dce_cooldown_seconds"`
}

// CurrentConfigVersion defines the active config.json schema version.
// Version 4 adds optional fallback and durable discovery hiding.
const CurrentConfigVersion = 4

// DefaultConfig returns the standard initial configuration.
// Timezone defaults to UTC for portability. LLM defaults to the Gemini OpenAI-compatible endpoint.
// Schedule defaults to disabled until explicitly configured by the operator.
func DefaultConfig() *Config {
	return &Config{
		Version:            CurrentConfigVersion,
		DCECooldownSeconds: DefaultDCECooldownSeconds,
		Channels:           []ChannelConfig{},
		Schedule: ScheduleConfig{
			Enabled: false,
			Time:    DefaultSchedule,
		},
		Timezone: DefaultTimezone,
		LLM: LLMConfig{
			BaseURL: DefaultLLMBaseURL,
			Model:   DefaultLLMModel,
		},
	}
}

// Validate ensures all config fields satisfy required invariants.
func (c *Config) Validate() error {
	if c.DCECooldownSeconds < 0 || c.DCECooldownSeconds > 3600 {
		return errors.New("DCE cooldown must be between 0 and 1 hour")
	}
	if c.Version != CurrentConfigVersion {
		return fmt.Errorf("unsupported config version: %d (expected %d)", c.Version, CurrentConfigVersion)
	}

	if strings.TrimSpace(c.Timezone) == "" {
		return errors.New("timezone cannot be empty")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("invalid timezone %q: %w", c.Timezone, err)
	}

	if len(c.Schedule.Time) != 5 || c.Schedule.Time[2] != ':' {
		return fmt.Errorf("invalid schedule time %q: must be HH:MM format", c.Schedule.Time)
	}
	if _, err := time.Parse("15:04", c.Schedule.Time); err != nil {
		return fmt.Errorf("invalid schedule time %q: %w", c.Schedule.Time, err)
	}

	if strings.TrimSpace(c.LLM.BaseURL) == "" {
		return errors.New("llm base_url cannot be empty")
	}
	if strings.TrimSpace(c.LLM.Model) == "" {
		return errors.New("llm model cannot be empty")
	}
	if c.Fallback != nil {
		u, err := url.Parse(c.Fallback.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("fallback base_url must be an HTTP(S) endpoint without credentials, query or fragment")
		}
		if strings.TrimSpace(c.Fallback.Model) == "" {
			return errors.New("fallback model cannot be empty")
		}
	}
	hidden := make(map[string]bool)
	for _, ch := range c.HiddenChannels {
		if !IsDecimalString(ch.ID) || hidden[ch.ID] {
			return errors.New("hidden channel IDs must be unique decimal strings")
		}
		hidden[ch.ID] = true
	}

	seen := make(map[string]struct{}, len(c.Channels))
	for i, ch := range c.Channels {
		if ch.ID == "" {
			return fmt.Errorf("channel [%d]: id cannot be empty", i)
		}
		if !IsDecimalString(ch.ID) {
			return fmt.Errorf("channel [%d]: id %q must contain only decimal digits", i, ch.ID)
		}
		if _, exists := seen[ch.ID]; exists {
			return fmt.Errorf("duplicate channel id: %s", ch.ID)
		}
		seen[ch.ID] = struct{}{}
	}

	if c.Brief != nil {
		if len(c.Brief.Prompt) > MaxBriefPromptBytes {
			return fmt.Errorf("brief prompt exceeds maximum allowed size (%d characters)", MaxBriefPromptBytes)
		}
		if c.Brief.Prompt != "" && strings.TrimSpace(c.Brief.Prompt) == "" {
			return errors.New("brief prompt cannot be whitespace-only")
		}
		for i, r := range c.Brief.Prompt {
			if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
				return fmt.Errorf("brief prompt contains invalid control character at byte %d", i)
			}
			if r == 0x7f {
				return fmt.Errorf("brief prompt contains invalid delete character at byte %d", i)
			}
		}
	}

	return nil
}

func (c *Config) DCECooldown() time.Duration {
	return time.Duration(c.DCECooldownSeconds) * time.Second
}

// EffectiveBriefPrompt returns the custom brief prompt if non-empty, otherwise DefaultBriefPrompt.
func (c *Config) EffectiveBriefPrompt() string {
	if c == nil || c.Brief == nil || strings.TrimSpace(c.Brief.Prompt) == "" {
		return DefaultBriefPrompt
	}
	return c.Brief.Prompt
}
