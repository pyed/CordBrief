package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// Credentials have their own private file, never the public config or cursor state.
type credentials struct {
	BotToken       string `json:"telegram_bot_token"`
	OwnerID        string `json:"telegram_owner_id"`
	DiscordToken   string `json:"discord_token"`
	LLMAPIKey      string `json:"llm_api_key"`
	FallbackAPIKey string `json:"fallback_llm_api_key,omitempty"`
}

func credentialDataDir() string {
	if dir := strings.TrimSpace(os.Getenv("CORDBRIEF_DATA_DIR")); dir != "" {
		return dir
	}
	return "./data"
}

func loadCredentials() (credentials, error) {
	c, err := loadStoredCredentials()
	if err != nil {
		return credentials{}, err
	}
	// Environment values are runtime overrides and are never part of setup persistence.
	for key, dst := range map[string]*string{
		"TELEGRAM_BOT_TOKEN": &c.BotToken, "TELEGRAM_OWNER_ID": &c.OwnerID,
		"DISCORD_TOKEN": &c.DiscordToken, "LLM_API_KEY": &c.LLMAPIKey,
		"FALLBACK_LLM_API_KEY": &c.FallbackAPIKey,
	} {
		if value, ok := os.LookupEnv(key); ok {
			*dst = strings.TrimSpace(value)
		}
	}
	return c, c.validateValues()
}

func loadStoredCredentials() (credentials, error) {
	var c credentials
	path := filepath.Join(credentialDataDir(), "credentials.json")
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return c, errors.New("credentials.json must be a regular file, not a link")
		}
		if fi.Size() > 64*1024 {
			return c, errors.New("credentials.json exceeds the size limit")
		}
		if err := protectCredentials(path); err != nil {
			return c, errors.New("cannot restrict access to credentials.json")
		}
		f, err := os.Open(path)
		if err != nil {
			return c, errors.New("cannot read credentials.json")
		}
		defer f.Close()
		dec := json.NewDecoder(io.LimitReader(f, 64*1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return credentials{}, errors.New("invalid credentials.json; fix or remove it and run --setup")
		}
		var extra any
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			return credentials{}, errors.New("invalid trailing data in credentials.json")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return c, errors.New("cannot access credentials.json")
	}
	return c, c.validateValues()
}

func (c credentials) validateValues() error {
	for _, value := range []string{c.BotToken, c.OwnerID, c.DiscordToken, c.LLMAPIKey, c.FallbackAPIKey} {
		if len(value) > 8192 || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("credential value is too long or contains a line break/control character")
		}
	}
	return nil
}

func saveCredentials(c credentials) error {
	if err := c.validateValues(); err != nil {
		return err
	}
	dir := credentialDataDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("cannot create credential directory")
	}
	f, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return errors.New("cannot create credential file")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	// Set the Windows ACL / Unix mode BEFORE writing any secret bytes.
	if err := protectCredentials(f.Name()); err != nil {
		return errors.New("cannot restrict access to credential file")
	}
	if err := json.NewEncoder(f).Encode(c); err != nil {
		return errors.New("cannot write credentials")
	}
	if err := f.Sync(); err != nil {
		return errors.New("cannot flush credentials")
	}
	if err := f.Close(); err != nil {
		return errors.New("cannot close credential file")
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, "credentials.json")); err != nil {
		return errors.New("cannot replace credentials.json; previous credentials were kept")
	}
	return nil
}

// Interactive requires a real input and output terminal; pipes and services never prompt.
func Interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// Configure collects credentials without echoing them. It makes no network requests.
func Configure() error {
	if !Interactive() {
		return errors.New("setup needs an interactive terminal; run cordbrief --setup as the service account, or provide environment variables")
	}
	fmt.Println("CordBrief setup. Input is hidden. Enter keeps an existing value; '-' clears either optional AI key.")
	fmt.Println("Get a Telegram bot token from @BotFather and your numeric Telegram user ID.")
	fmt.Println("Use a Discord token that can read your channels. The AI key is for Gemini by default.")
	return configure(func(label string) (string, error) {
		fmt.Print(label + ": ")
		value, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", errors.New("setup input interrupted; credentials were not changed")
		}
		return string(value), nil
	})
}

func configure(read func(string) (string, error)) error {
	c, err := loadStoredCredentials()
	if err != nil {
		return err
	}
	for _, field := range []struct {
		label string
		value *string
	}{
		{"Telegram bot token", &c.BotToken}, {"Telegram user ID", &c.OwnerID},
		{"Discord token", &c.DiscordToken}, {"AI API key (optional for local servers)", &c.LLMAPIKey},
		{"Fallback AI API key (optional; never inherits primary key)", &c.FallbackAPIKey},
	} {
		label := field.label
		if *field.value != "" {
			label += " [saved/environment value available]"
		}
		value, err := read(label)
		if err != nil {
			return errors.New("setup input interrupted; credentials were not changed")
		}
		if value = strings.TrimSpace(value); value != "" {
			*field.value = value
		}
	}
	if c.LLMAPIKey == "-" {
		c.LLMAPIKey = ""
	}
	if c.FallbackAPIKey == "-" {
		c.FallbackAPIKey = ""
	}
	if c.BotToken == "" || c.DiscordToken == "" {
		return errors.New("Telegram and Discord tokens are required; run --setup again")
	}
	if id, err := strconv.ParseInt(c.OwnerID, 10, 64); err != nil || id <= 0 {
		return errors.New("Telegram user ID must be a positive integer; run --setup again")
	}
	return saveCredentials(c)
}
