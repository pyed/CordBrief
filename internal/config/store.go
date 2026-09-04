package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Secrets holds private credentials, strictly isolated from public application configuration.
// Persisted exclusively to /var/cordbrief/data/secrets.json (mode 0600).
type Secrets struct {
	GeminiAPIKey string `json:"gemini_api_key,omitempty"`
}

// AppConfig represents non-secret runtime configuration for Schedule, LLM, and Digest parameters.
type AppConfig struct {
	Schedule ScheduleConfig `json:"schedule"`
	LLM      LLMConfig      `json:"llm"`
	Digest   DigestConfig   `json:"digest"`
}

// DefaultAppConfig returns a safe, production-ready default application configuration.
func DefaultAppConfig() AppConfig {
	return AppConfig{
		Schedule: ScheduleConfig{
			Enabled:  false,
			Time:     "08:00",
			Timezone: "UTC",
		},
		LLM: LLMConfig{
			Provider:        ProviderGemini,
			BaseURL:         DefaultGeminiBaseURL,
			Model:           DefaultGeminiModel,
			APIKeyEnv:       DefaultGeminiKeyEnv,
			MaxInputChars:   DefaultGeminiMaxInput,
			MaxOutputTokens: DefaultGeminiMaxOutput,
			TimeoutSeconds:  DefaultGeminiTimeout,
		},
		Digest: DigestConfig{
			OutputLanguage:        DefaultLanguage,
			Focus:                 nil,
			IgnoreBots:            true,
			FirstRunLookbackRaw:   DefaultLookback,
			MaxCatchupRaw:         DefaultCatchup,
			MaxMessagesPerChannel: DefaultMaxMessagesPerChannel,
		},
	}
}

// Store manages non-secret AppConfig and private Secrets.
type Store struct {
	mu       sync.RWMutex
	dataDir  string
	config   AppConfig
	secrets  Secrets
}

// NewStore initializes the configuration store, reading from dataDir or initialConfigPath.
func NewStore(dataDir, initialConfigPath string) (*Store, error) {
	if dataDir == "" {
		dataDir = "/var/cordbrief/data"
	}
	_ = os.MkdirAll(dataDir, 0755)

	s := &Store{
		dataDir: dataDir,
		config:  DefaultAppConfig(),
	}

	// 1. Load non-secret config: check dataDir/config.json first, then initialConfigPath
	persistedConfigPath := filepath.Join(dataDir, "config.json")
	var loaded bool

	if data, err := os.ReadFile(persistedConfigPath); err == nil {
		var cfg AppConfig
		if err := json.Unmarshal(data, &cfg); err == nil {
			s.config = cfg
			loaded = true
		}
	}

	if !loaded && initialConfigPath != "" {
		if cfg, err := Load(initialConfigPath); err == nil {
			s.config = AppConfig{
				Schedule: cfg.Schedule,
				LLM:      cfg.LLM,
				Digest:   cfg.Digest,
			}
			loaded = true
		}
	}

	// Apply defaults and validate
	s.applyDefaultsAndValidate(&s.config)

	// 2. Load private secrets: check dataDir/secrets.json
	secretsPath := filepath.Join(dataDir, "secrets.json")
	if data, err := os.ReadFile(secretsPath); err == nil {
		var sec Secrets
		_ = json.Unmarshal(data, &sec)
		s.secrets = sec
	}

	return s, nil
}

func (s *Store) applyDefaultsAndValidate(cfg *AppConfig) {
	p := strings.ToLower(strings.TrimSpace(cfg.LLM.Provider))
	if p == "" {
		p = ProviderGemini
	}
	cfg.LLM.Provider = p

	if p == ProviderGemini {
		if strings.TrimSpace(cfg.LLM.BaseURL) == "" {
			cfg.LLM.BaseURL = DefaultGeminiBaseURL
		}
		if strings.TrimSpace(cfg.LLM.Model) == "" {
			cfg.LLM.Model = DefaultGeminiModel
		}
		if cfg.LLM.MaxInputChars <= 0 {
			cfg.LLM.MaxInputChars = DefaultGeminiMaxInput
		}
		if cfg.LLM.MaxOutputTokens <= 0 {
			cfg.LLM.MaxOutputTokens = DefaultGeminiMaxOutput
		}
		if cfg.LLM.TimeoutSeconds <= 0 {
			cfg.LLM.TimeoutSeconds = DefaultGeminiTimeout
		}
	} else if p == ProviderLocal {
		if cfg.LLM.MaxInputChars <= 0 {
			cfg.LLM.MaxInputChars = DefaultLocalMaxInput
		}
		if cfg.LLM.MaxOutputTokens <= 0 {
			cfg.LLM.MaxOutputTokens = DefaultLocalMaxOutput
		}
		if cfg.LLM.TimeoutSeconds <= 0 {
			cfg.LLM.TimeoutSeconds = DefaultLocalTimeout
		}
	}

	if strings.TrimSpace(cfg.Digest.OutputLanguage) == "" {
		cfg.Digest.OutputLanguage = DefaultLanguage
	}
	if cfg.Digest.FirstRunLookbackRaw == "" {
		cfg.Digest.FirstRunLookbackRaw = DefaultLookback
	}
	if cfg.Digest.MaxCatchupRaw == "" {
		cfg.Digest.MaxCatchupRaw = DefaultCatchup
	}
	if cfg.Digest.MaxMessagesPerChannel <= 0 {
		cfg.Digest.MaxMessagesPerChannel = DefaultMaxMessagesPerChannel
	}

	// Schedule validation & defaults
	if strings.TrimSpace(cfg.Schedule.Time) == "" {
		cfg.Schedule.Time = "08:00"
	}
	t, err := time.Parse("15:04", strings.TrimSpace(cfg.Schedule.Time))
	if err == nil {
		cfg.Schedule.Hour = t.Hour()
		cfg.Schedule.Minute = t.Minute()
	} else {
		cfg.Schedule.Time = "08:00"
		cfg.Schedule.Hour = 8
		cfg.Schedule.Minute = 0
	}

	if strings.TrimSpace(cfg.Schedule.Timezone) == "" {
		cfg.Schedule.Timezone = "UTC"
	}
	loc, err := time.LoadLocation(strings.TrimSpace(cfg.Schedule.Timezone))
	if err == nil {
		cfg.Schedule.Location = loc
	} else {
		cfg.Schedule.Timezone = "UTC"
		cfg.Schedule.Location = time.UTC
	}
}

// GetAppConfig returns a safe copy of non-secret application settings.
func (s *Store) GetAppConfig() AppConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

// SaveAppConfig updates non-secret configuration and persists it to dataDir/config.json.
func (s *Store) SaveAppConfig(newCfg AppConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate schedule time & timezone format before saving
	if newCfg.Schedule.Time != "" {
		if _, err := time.Parse("15:04", strings.TrimSpace(newCfg.Schedule.Time)); err != nil {
			return fmt.Errorf("invalid schedule time %q (must be HH:MM in 24-hour format): %w", newCfg.Schedule.Time, err)
		}
	}
	if newCfg.Schedule.Timezone != "" {
		if _, err := time.LoadLocation(strings.TrimSpace(newCfg.Schedule.Timezone)); err != nil {
			return fmt.Errorf("invalid schedule timezone %q: %w", newCfg.Schedule.Timezone, err)
		}
	}

	s.applyDefaultsAndValidate(&newCfg)

	// Validate required fields
	if newCfg.LLM.Provider == ProviderLocal {
		if strings.TrimSpace(newCfg.LLM.BaseURL) == "" {
			return errors.New("base_url is required for provider 'local'")
		}
		if strings.TrimSpace(newCfg.LLM.Model) == "" {
			return errors.New("model is required for provider 'local'")
		}
	}

	persistedConfigPath := filepath.Join(s.dataDir, "config.json")
	if err := safeWriteJSON(persistedConfigPath, newCfg, 0644); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	s.config = newCfg
	return nil
}

// GetScheduleConfig returns a safe copy of the schedule configuration.
func (s *Store) GetScheduleConfig() ScheduleConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.Schedule
}

// SaveScheduleConfig updates schedule configuration and persists it.
func (s *Store) SaveScheduleConfig(sch ScheduleConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sch.Time != "" {
		if _, err := time.Parse("15:04", strings.TrimSpace(sch.Time)); err != nil {
			return fmt.Errorf("invalid schedule time %q: %w", sch.Time, err)
		}
	}
	if sch.Timezone != "" {
		if _, err := time.LoadLocation(strings.TrimSpace(sch.Timezone)); err != nil {
			return fmt.Errorf("invalid schedule timezone %q: %w", sch.Timezone, err)
		}
	}

	cfg := s.config
	cfg.Schedule = sch
	s.applyDefaultsAndValidate(&cfg)

	persistedConfigPath := filepath.Join(s.dataDir, "config.json")
	if err := safeWriteJSON(persistedConfigPath, cfg, 0644); err != nil {
		return fmt.Errorf("saving schedule config: %w", err)
	}

	s.config = cfg
	return nil
}

// SecretSource indicates the origin of a credential.
type SecretSource string

const (
	SecretSourceEnvironment SecretSource = "environment"
	SecretSourceStored      SecretSource = "stored"
	SecretSourceNone        SecretSource = "none"
)

// IsGeminiConfigured reports whether a Gemini API key is available via environment or private storage.
func (s *Store) IsGeminiConfigured() bool {
	return s.GetGeminiKey() != ""
}

// GetGeminiKeySource returns the provenance of the active Gemini API key:
// - SecretSourceEnvironment if GEMINI_API_KEY or CORDBRIEF_LLM_API_KEY is set in the environment.
// - SecretSourceStored if saved in private secrets.json.
// - SecretSourceNone if not configured.
// Precedence rule: Environment variables take precedence over stored secrets.
func (s *Store) GetGeminiKeySource() SecretSource {
	if os.Getenv(EnvGeminiKey) != "" || os.Getenv("CORDBRIEF_LLM_API_KEY") != "" {
		return SecretSourceEnvironment
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if strings.TrimSpace(s.secrets.GeminiAPIKey) != "" {
		return SecretSourceStored
	}
	return SecretSourceNone
}

// GetGeminiKey returns the active Gemini API key.
// Priority 1: GEMINI_API_KEY environment variable.
// Priority 2: Private persisted secrets.json in data directory.
// Never logged or returned in public API payloads.
func (s *Store) GetGeminiKey() string {
	if env := os.Getenv(EnvGeminiKey); env != "" {
		return env
	}
	if env := os.Getenv("CORDBRIEF_LLM_API_KEY"); env != "" {
		return env
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.secrets.GeminiAPIKey
}

// SaveGeminiKey saves the Gemini API key into private dataDir/secrets.json with mode 0600.
func (s *Store) SaveGeminiKey(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.secrets.GeminiAPIKey = strings.TrimSpace(key)
	secretsPath := filepath.Join(s.dataDir, "secrets.json")
	return safeWriteJSON(secretsPath, s.secrets, 0600)
}

func safeWriteJSON(dest string, data any, mode os.FileMode) error {
	dir := filepath.Dir(dest)
	_ = os.MkdirAll(dir, 0755)

	tmp := fmt.Sprintf("%s.%d.tmp", dest, time.Now().UnixNano())
	bytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	bytes = append(bytes, '\n')

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}

	if _, err := f.Write(bytes); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	return os.Rename(tmp, dest)
}
