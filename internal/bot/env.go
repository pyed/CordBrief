package bot

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// EnvConfig holds startup settings after environment overrides saved credentials.
type EnvConfig struct {
	BotToken       string
	OwnerID        int64
	DataDir        string
	DiscordToken   string
	DCEPath        string
	DCEVersion     string
	LLMAPIKey      string
	FallbackAPIKey string
}

// Redact also covers URL-escaped tokens in HTTP transport errors.
func (c *EnvConfig) Redact(text string) string {
	for _, secret := range []string{c.BotToken, c.DiscordToken, c.LLMAPIKey, c.FallbackAPIKey} {
		if secret != "" {
			for _, value := range []string{secret, url.PathEscape(secret), url.QueryEscape(secret)} {
				text = strings.ReplaceAll(text, value, "[REDACTED]")
			}
		}
	}
	return text
}

var ErrCredentialsMissing = errors.New("credentials are incomplete; run cordbrief --setup in a terminal")

// LoadEnv reads private credentials and applies environment overrides without prompting.
// Telegram credentials remain mandatory; Discord and the AI key are optional for
// compatibility with existing environment-only deployments and local AI servers.
func LoadEnv() (*EnvConfig, error) {
	c, err := loadCredentials()
	if err != nil {
		return nil, err
	}
	if c.BotToken == "" {
		return nil, fmt.Errorf("TELEGRAM_BOT_TOKEN is required: %w", ErrCredentialsMissing)
	}
	ownerID, err := strconv.ParseInt(c.OwnerID, 10, 64)
	if err != nil || ownerID <= 0 {
		return nil, fmt.Errorf("TELEGRAM_OWNER_ID must be a positive integer: %w", ErrCredentialsMissing)
	}

	return &EnvConfig{
		BotToken:       c.BotToken,
		OwnerID:        ownerID,
		DataDir:        credentialDataDir(),
		DiscordToken:   c.DiscordToken,
		DCEPath:        strings.TrimSpace(os.Getenv("CORDBRIEF_DCE_PATH")),
		DCEVersion:     strings.TrimSpace(os.Getenv("CORDBRIEF_DCE_VERSION")),
		LLMAPIKey:      c.LLMAPIKey,
		FallbackAPIKey: c.FallbackAPIKey,
	}, nil
}
