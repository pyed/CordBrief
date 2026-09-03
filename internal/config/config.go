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
	EnvDiscordToken = "CORDBRIEF_DISCORD_TOKEN"

	DefaultLanguage      = "en"
	DefaultMaxInputChars = 30000
	DefaultMaxOutputToks = 1024
	DefaultLookback              = "24h"
	DefaultCatchup               = "48h"
	DefaultMaxMessagesPerChannel = 20000
)

// ScheduleConfig defines scheduling and timezone properties.
type ScheduleConfig struct {
	Time     string `json:"time"`
	Timezone string `json:"timezone"`

	// Parsed cache populated by Validate
	Location *time.Location `json:"-"`
	Hour     int            `json:"-"`
	Minute   int            `json:"-"`
}

// LLMConfig defines OpenAI-compatible language model connection settings.
type LLMConfig struct {
	BaseURL         string `json:"base_url"`
	Model           string `json:"model"`
	APIKeyEnv       string `json:"api_key_env,omitempty"`
	MaxInputChars   int    `json:"max_input_chars"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

// DigestConfig defines summarization parameters, filtering, and time bounds.
type DigestConfig struct {
	OutputLanguage      string   `json:"output_language"`
	Focus               []string `json:"focus,omitempty"`
	IgnoreBots          bool     `json:"ignore_bots"`
	FirstRunLookbackRaw    string   `json:"first_run_lookback"`
	MaxCatchupRaw          string   `json:"max_catchup"`
	MaxMessagesPerChannel  int      `json:"max_messages_per_channel"`

	// Parsed cache populated by Validate
	FirstRunLookback time.Duration `json:"-"`
	MaxCatchup       time.Duration `json:"-"`
}

// Config represents the top-level configuration for CordBrief, grouped conceptually.
type Config struct {
	GuildID          string         `json:"guild_id"`
	SourceChannelIDs []string       `json:"source_channel_ids"`
	DigestChannelID  string         `json:"digest_channel_id"`
	Schedule         ScheduleConfig `json:"schedule"`
	LLM              LLMConfig      `json:"llm"`
	Digest           DigestConfig   `json:"digest"`
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

// Validate checks all configuration constraints per SPEC.md.
func (c *Config) Validate() error {
	var errs []string

	// Root Discord identity & channels
	if strings.TrimSpace(c.GuildID) == "" {
		errs = append(errs, "guild_id is required")
	}

	if len(c.SourceChannelIDs) == 0 {
		errs = append(errs, "source_channel_ids must contain at least one channel ID")
	} else {
		for i, id := range c.SourceChannelIDs {
			if strings.TrimSpace(id) == "" {
				errs = append(errs, fmt.Sprintf("source_channel_ids[%d] cannot be empty", i))
			}
		}
	}

	if strings.TrimSpace(c.DigestChannelID) == "" {
		errs = append(errs, "digest_channel_id is required")
	}

	// Schedule
	if strings.TrimSpace(c.Schedule.Time) == "" {
		errs = append(errs, "schedule.time is required (format HH:MM)")
	} else {
		t, err := time.Parse("15:04", strings.TrimSpace(c.Schedule.Time))
		if err != nil {
			errs = append(errs, fmt.Sprintf("invalid schedule.time %q: must be HH:MM (24-hour)", c.Schedule.Time))
		} else {
			c.Schedule.Hour = t.Hour()
			c.Schedule.Minute = t.Minute()
		}
	}

	if strings.TrimSpace(c.Schedule.Timezone) == "" {
		errs = append(errs, "schedule.timezone is required (e.g. 'America/New_York' or 'UTC')")
	} else {
		loc, err := time.LoadLocation(strings.TrimSpace(c.Schedule.Timezone))
		if err != nil {
			errs = append(errs, fmt.Sprintf("invalid schedule.timezone %q: %v", c.Schedule.Timezone, err))
		} else {
			c.Schedule.Location = loc
		}
	}

	// LLM
	if strings.TrimSpace(c.LLM.BaseURL) == "" {
		errs = append(errs, "llm.base_url is required")
	} else {
		u, err := url.Parse(c.LLM.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Sprintf("invalid llm.base_url %q: must be a valid http or https URL", c.LLM.BaseURL))
		}
	}

	if strings.TrimSpace(c.LLM.Model) == "" {
		errs = append(errs, "llm.model is required")
	}

	if c.LLM.MaxInputChars <= 0 {
		c.LLM.MaxInputChars = DefaultMaxInputChars
	}

	if c.LLM.MaxOutputTokens <= 0 {
		c.LLM.MaxOutputTokens = DefaultMaxOutputToks
	}

	// Digest
	if strings.TrimSpace(c.Digest.OutputLanguage) == "" {
		c.Digest.OutputLanguage = DefaultLanguage
	}

	if strings.TrimSpace(c.Digest.FirstRunLookbackRaw) == "" {
		c.Digest.FirstRunLookbackRaw = DefaultLookback
	}
	dur, err := time.ParseDuration(c.Digest.FirstRunLookbackRaw)
	if err != nil || dur <= 0 {
		errs = append(errs, fmt.Sprintf("invalid digest.first_run_lookback %q: must be a positive duration (e.g. 24h)", c.Digest.FirstRunLookbackRaw))
	} else {
		c.Digest.FirstRunLookback = dur
	}

	if strings.TrimSpace(c.Digest.MaxCatchupRaw) == "" {
		c.Digest.MaxCatchupRaw = DefaultCatchup
	}
	catchupDur, err := time.ParseDuration(c.Digest.MaxCatchupRaw)
	if err != nil || catchupDur <= 0 {
		errs = append(errs, fmt.Sprintf("invalid digest.max_catchup %q: must be a positive duration (e.g. 48h)", c.Digest.MaxCatchupRaw))
	} else {
		c.Digest.MaxCatchup = catchupDur
	}

	if c.Digest.FirstRunLookback > 0 && c.Digest.MaxCatchup > 0 && c.Digest.MaxCatchup < c.Digest.FirstRunLookback {
		errs = append(errs, "digest.max_catchup must be greater than or equal to digest.first_run_lookback")
	}

	if c.Digest.MaxMessagesPerChannel <= 0 {
		c.Digest.MaxMessagesPerChannel = DefaultMaxMessagesPerChannel
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}

	return nil
}

// ValidateForChannels validates only the fields required by the 'channels' command,
// allowing discovery before channel IDs are populated in config.
func (c *Config) ValidateForChannels() error {
	if strings.TrimSpace(c.GuildID) == "" {
		return errors.New("guild_id is required")
	}
	return nil
}

// DiscordToken retrieves the Discord bot token from the environment.
// Per SPEC.md, tokens must never come from config files.
func (c *Config) DiscordToken() (string, error) {
	tok := strings.TrimSpace(os.Getenv(EnvDiscordToken))
	if tok == "" {
		return "", fmt.Errorf("environment variable %s is not set or empty", EnvDiscordToken)
	}
	return tok, nil
}

// LLMAPIKey retrieves the LLM API key from the environment variable specified
// in LLM.APIKeyEnv, if configured. If not configured, returns empty string.
func (c *Config) LLMAPIKey() string {
	if strings.TrimSpace(c.LLM.APIKeyEnv) == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(c.LLM.APIKeyEnv))
}
