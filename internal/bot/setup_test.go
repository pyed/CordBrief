package bot

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func credentialTestEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CORDBRIEF_DATA_DIR", dir)
	for _, key := range []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_OWNER_ID", "DISCORD_TOKEN", "LLM_API_KEY"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	return dir
}

func TestCredentialSetupAndPrecedence(t *testing.T) {
	dir := credentialTestEnv(t)
	values := []string{"test-telegram-secret", "12345", "test-discord-secret", "test-ai-secret"}
	i := 0
	if err := configure(func(label string) (string, error) {
		for _, secret := range values {
			if strings.Contains(label, secret) {
				t.Fatal("prompt leaked a credential")
			}
		}
		value := values[i]
		i++
		return value, nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BotToken != values[0] || cfg.OwnerID != 12345 || cfg.DiscordToken != values[2] || cfg.LLMAPIKey != values[3] {
		t.Fatal("saved credentials did not load")
	}
	path := filepath.Join(dir, "credentials.json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0600 {
		t.Fatal("credential file is not private")
	}
	for _, name := range []string{"config.json", "state.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("setup wrote a public configuration file")
		}
	}
	before, _ := os.ReadFile(path)
	t.Setenv("TELEGRAM_BOT_TOKEN", "environment-token")
	t.Setenv("LLM_API_KEY", "")
	cfg, err = LoadEnv()
	if err != nil || cfg.BotToken != "environment-token" || cfg.LLMAPIKey != "" {
		t.Fatal("environment precedence failed")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("loading environment values rewrote saved credentials")
	}
}

func TestCredentialReconfigureAtomicAndSecretSafe(t *testing.T) {
	dir := credentialTestEnv(t)
	original := credentials{"telegram-secret", "123", "discord-secret", "ai-secret"}
	if err := saveCredentials(original); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credentials.json")
	before, _ := os.ReadFile(path)
	err := configure(func(string) (string, error) { return "", errors.New("telegram-secret") })
	if err == nil || strings.Contains(err.Error(), "telegram-secret") {
		t.Fatal("input failure leaked a secret")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("interrupted setup changed credentials")
	}
	i := 0
	if err := configure(func(label string) (string, error) {
		if strings.Contains(label, "secret") {
			t.Fatal("saved value appeared in a prompt")
		}
		i++
		if i == 4 {
			return "-", nil
		}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	c, err := loadCredentials()
	if err != nil || c.BotToken != original.BotToken || c.LLMAPIKey != "" {
		t.Fatal("reconfiguration did not retain/clear values")
	}

	// The JSON decoder must not echo an invalid key that might itself be a secret.
	if err := os.WriteFile(path, []byte(`{"telegram-secret":"value"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnv(); err == nil || strings.Contains(err.Error(), "telegram-secret") {
		t.Fatal("corrupt credential error was not safe")
	}
	if err := os.WriteFile(path, []byte(`{} trailing-secret`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnv(); err == nil || strings.Contains(err.Error(), "trailing-secret") {
		t.Fatal("trailing data was accepted or leaked")
	}
}

func TestCredentialValidationAndFailedReplacement(t *testing.T) {
	dir := credentialTestEnv(t)
	for _, owner := range []string{"", "0", "-1", "not-numeric-secret"} {
		i := 0
		values := []string{"telegram-secret", owner, "discord-secret", ""}
		err := configure(func(string) (string, error) { value := values[i]; i++; return value, nil })
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("invalid owner was accepted or echoed")
		}
	}
	path := filepath.Join(dir, "credentials.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveCredentials(credentials{BotToken: "secret"}); err == nil {
		t.Fatal("replacement should fail")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "credentials.json" {
		t.Fatal("failed save left temporary secret files")
	}
	if _, err := LoadEnv(); err == nil {
		t.Fatal("directory accepted as credentials")
	}
}

func TestEnvironmentOnlyNeedsNoCredentialFile(t *testing.T) {
	dir := credentialTestEnv(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "environment-token")
	t.Setenv("TELEGRAM_OWNER_ID", "12345")
	if _, err := LoadEnv(); err != nil {
		t.Fatal(err)
	}
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Fatal("environment deployment created credential files")
	}
}

type secretErrorSender struct{ fakeSender }

func (s *secretErrorSender) SendMessage(context.Context, *bot.SendMessageParams) (*models.Message, error) {
	return nil, errors.New("request failed: token%3Asecret")
}

func TestTelegramErrorsRedactTokens(t *testing.T) {
	cfg := &EnvConfig{BotToken: "token:secret", DiscordToken: "discord/secret", LLMAPIKey: "api+secret"}
	for _, value := range []string{cfg.BotToken, cfg.DiscordToken, cfg.LLMAPIKey} {
		for _, form := range []string{value, url.PathEscape(value), url.QueryEscape(value)} {
			if strings.Contains(cfg.Redact("failure "+form), form) {
				t.Fatal("error still contains a credential")
			}
		}
	}
	b := &Bot{client: &secretErrorSender{}, redact: cfg.Redact}
	if err := b.Deliver(context.Background(), "brief"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("delivery error leaked token")
	}
}

func TestConfigureDoesNotPersistEnvironmentOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CORDBRIEF_DATA_DIR", dir)
	for _, key := range []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_OWNER_ID", "DISCORD_TOKEN", "LLM_API_KEY"} {
		t.Setenv(key, "environment-value")
	}
	original := credentials{
		BotToken:     "saved-bot",
		OwnerID:      "12345",
		DiscordToken: "saved-discord",
		LLMAPIKey:    "saved-llm",
	}
	if err := saveCredentials(original); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELEGRAM_OWNER_ID", "67890")

	if err := configure(func(string) (string, error) { return "", nil }); err != nil {
		t.Fatal(err)
	}
	stored, err := loadStoredCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if stored != original {
		t.Fatalf("setup persisted runtime overrides: stored credentials changed")
	}
	cfg, err := LoadEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BotToken != "environment-value" || cfg.OwnerID != 67890 || cfg.DiscordToken != "environment-value" || cfg.LLMAPIKey != "environment-value" {
		t.Fatal("runtime environment overrides were not applied")
	}
}
