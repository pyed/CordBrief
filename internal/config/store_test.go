package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStore_RejectsUnreadableSettings(t *testing.T) {
	for _, name := range []string{"config.json", "secrets.json"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, name)
			broken := []byte(`{"broken":`)
			if err := os.WriteFile(file, broken, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewStore(dir, ""); err == nil {
				t.Fatal("corrupt settings silently replaced by defaults")
			}
			if data, err := os.ReadFile(file); err != nil || !bytes.Equal(data, broken) {
				t.Fatalf("original settings changed: %v", err)
			}
		})
	}
	if _, err := NewStore(t.TempDir(), filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("explicit missing seed config was ignored")
	}
}

func TestStore_DefaultsAndPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(t.TempDir())

	store, err := NewStore(tmpDir, "config.json")
	if err != nil {
		t.Fatalf("failed creating store: %v", err)
	}

	cfg := store.GetAppConfig()
	if cfg.LLM.Provider != ProviderGemini {
		t.Errorf("expected default provider gemini, got %s", cfg.LLM.Provider)
	}
	if cfg.LLM.Model != DefaultGeminiModel {
		t.Errorf("expected default model %s, got %s", DefaultGeminiModel, cfg.LLM.Model)
	}

	// Modify and save
	cfg.Digest.OutputLanguage = "fr"
	cfg.Digest.Focus = []string{"announcements"}
	if err := store.SaveAppConfig(cfg); err != nil {
		t.Fatalf("SaveAppConfig failed: %v", err)
	}

	// Verify persistence across restart
	store2, err := NewStore(tmpDir, "")
	if err != nil {
		t.Fatalf("failed reloading store: %v", err)
	}
	cfg2 := store2.GetAppConfig()
	if cfg2.Digest.OutputLanguage != "fr" {
		t.Errorf("expected persisted language 'fr', got %s", cfg2.Digest.OutputLanguage)
	}
	if len(cfg2.Digest.Focus) != 1 || cfg2.Digest.Focus[0] != "announcements" {
		t.Errorf("unexpected focus: %v", cfg2.Digest.Focus)
	}
}

func TestStore_SecretsHandling(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStore(tmpDir, "")
	if err != nil {
		t.Fatalf("failed creating store: %v", err)
	}

	if store.IsGeminiConfigured() {
		t.Error("expected Gemini not configured by default in temp dir")
	}

	// Save secret to store
	secretVal := "test-secret-key-12345"
	if err := store.SaveGeminiKey(secretVal); err != nil {
		t.Fatalf("SaveGeminiKey failed: %v", err)
	}

	if !store.IsGeminiConfigured() {
		t.Error("expected Gemini to be configured after save")
	}
	if store.GetGeminiKey() != secretVal {
		t.Errorf("expected secret %s, got %s", secretVal, store.GetGeminiKey())
	}

	// Environment variable precedence
	t.Setenv(EnvGeminiKey, "env-override-key")
	if store.GetGeminiKey() != "env-override-key" {
		t.Errorf("expected environment key to take precedence, got %s", store.GetGeminiKey())
	}

	// Verify secret is NOT in public config.json
	cfgFile := filepath.Join(tmpDir, "config.json")
	if data, err := os.ReadFile(cfgFile); err == nil {
		if string(data) == secretVal {
			t.Fatal("SECURITY VIOLATION: secret key leaked into config.json")
		}
	}
}

func TestStore_LocalProviderValidation(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := NewStore(tmpDir, "")

	cfg := store.GetAppConfig()
	cfg.LLM.Provider = ProviderLocal
	cfg.LLM.BaseURL = "" // Missing

	err := store.SaveAppConfig(cfg)
	if err == nil {
		t.Fatal("expected error saving local provider with missing base_url, got nil")
	}

	cfg.LLM.BaseURL = "http://host.docker.internal:8081/v1"
	cfg.LLM.Model = "my-model"
	if err := store.SaveAppConfig(cfg); err != nil {
		t.Fatalf("expected valid local provider config to save, got: %v", err)
	}
}

func TestStore_SecretSource(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir, "")
	if err != nil {
		t.Fatal(err)
	}

	// 1. None
	if src := store.GetGeminiKeySource(); src != SecretSourceNone {
		t.Errorf("expected SecretSourceNone initially, got %s", src)
	}

	// 2. Stored
	if err := store.SaveGeminiKey("stored-secret"); err != nil {
		t.Fatal(err)
	}
	if src := store.GetGeminiKeySource(); src != SecretSourceStored {
		t.Errorf("expected SecretSourceStored, got %s", src)
	}

	// 3. Environment override
	t.Setenv(EnvGeminiKey, "env-secret")
	if src := store.GetGeminiKeySource(); src != SecretSourceEnvironment {
		t.Errorf("expected SecretSourceEnvironment when env var is set, got %s", src)
	}
	if key := store.GetGeminiKey(); key != "env-secret" {
		t.Errorf("expected GetGeminiKey to return environment secret, got %s", key)
	}
}

func TestConfig_Schedule(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir, "")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Default schedule
	sch := store.GetScheduleConfig()
	if sch.Enabled {
		t.Errorf("expected schedule disabled by default, got enabled")
	}
	if sch.Time != "08:00" {
		t.Errorf("expected default time 08:00, got %s", sch.Time)
	}
	if sch.Timezone != "UTC" {
		t.Errorf("expected default timezone UTC, got %s", sch.Timezone)
	}

	// 2. Save valid schedule (Asia/Riyadh)
	validSch := ScheduleConfig{
		Enabled:  true,
		Time:     "15:30",
		Timezone: "Asia/Riyadh",
	}
	if err := store.SaveScheduleConfig(validSch); err != nil {
		t.Fatalf("expected valid schedule to save, got error: %v", err)
	}

	// Verify persistence and parsed cache
	store2, err := NewStore(tmpDir, "")
	if err != nil {
		t.Fatal(err)
	}
	sch2 := store2.GetScheduleConfig()
	if !sch2.Enabled || sch2.Time != "15:30" || sch2.Timezone != "Asia/Riyadh" {
		t.Fatalf("persisted schedule mismatch: %+v", sch2)
	}
	if sch2.Hour != 15 || sch2.Minute != 30 || sch2.Location == nil {
		t.Errorf("parsed cache missing on reload: hour=%d min=%d loc=%v", sch2.Hour, sch2.Minute, sch2.Location)
	}

	// 3. Embedded timezone portability test
	embeddedZones := []string{"Asia/Riyadh", "Europe/London", "America/New_York", "UTC"}
	for _, z := range embeddedZones {
		loc, err := time.LoadLocation(z)
		if err != nil || loc == nil {
			t.Errorf("embedded timezone %s failed to load: %v", z, err)
		}
	}

	// 4. Invalid time format
	invalidTime := ScheduleConfig{Enabled: true, Time: "99:99", Timezone: "UTC"}
	if err := store.SaveScheduleConfig(invalidTime); err == nil {
		t.Error("expected error saving invalid time 99:99, got nil")
	}

	// 5. Invalid timezone
	invalidTZ := ScheduleConfig{Enabled: true, Time: "08:00", Timezone: "NonExistent/Zone"}
	if err := store.SaveScheduleConfig(invalidTZ); err == nil {
		t.Error("expected error saving invalid timezone, got nil")
	}
}
