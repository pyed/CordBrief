package state

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Default non-secret configuration values.
const (
	DefaultLLMBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai/"
	DefaultLLMModel   = "gemini-2.5-flash"
	DefaultTimezone   = "UTC"
	DefaultSchedule   = "08:00"
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

// Config represents user intent stored in config.json.
type Config struct {
	Version  int             `json:"version"`
	Channels []ChannelConfig `json:"channels"`
	Schedule ScheduleConfig  `json:"schedule"`
	Timezone string          `json:"timezone"`
	LLM      LLMConfig       `json:"llm"`
}

// DefaultConfig returns the standard initial configuration.
// Timezone defaults to UTC for portability. LLM defaults to the Gemini OpenAI-compatible endpoint.
func DefaultConfig() *Config {
	return &Config{
		Version:  1,
		Channels: []ChannelConfig{},
		Schedule: ScheduleConfig{
			Enabled: true,
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
	if c.Version != 1 {
		return fmt.Errorf("unsupported config version: %d (expected 1)", c.Version)
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

	seen := make(map[string]struct{}, len(c.Channels))
	for i, ch := range c.Channels {
		if ch.ID == "" {
			return fmt.Errorf("channel [%d]: id cannot be empty", i)
		}
		if !isDecimalString(ch.ID) {
			return fmt.Errorf("channel [%d]: id %q must contain only decimal digits", i, ch.ID)
		}
		if _, exists := seen[ch.ID]; exists {
			return fmt.Errorf("duplicate channel id: %s", ch.ID)
		}
		seen[ch.ID] = struct{}{}
	}

	return nil
}
