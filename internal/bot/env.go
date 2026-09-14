package bot

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// EnvConfig holds configuration loaded from process environment variables.
type EnvConfig struct {
	BotToken     string
	OwnerID      int64
	DataDir      string
	DiscordToken string
	DCEPath      string
}

// LoadEnv reads and validates required environment variables for CordBrief.
// Returns an error if TELEGRAM_BOT_TOKEN or TELEGRAM_OWNER_ID is missing or invalid.
// Never includes the bot token in error messages.
func LoadEnv() (*EnvConfig, error) {
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if token == "" {
		return nil, errors.New("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	ownerStr := strings.TrimSpace(os.Getenv("TELEGRAM_OWNER_ID"))
	if ownerStr == "" {
		return nil, errors.New("TELEGRAM_OWNER_ID environment variable is required")
	}

	ownerID, err := strconv.ParseInt(ownerStr, 10, 64)
	if err != nil || ownerID <= 0 {
		return nil, errors.New("TELEGRAM_OWNER_ID must be a positive integer")
	}

	dataDir := strings.TrimSpace(os.Getenv("CORDBRIEF_DATA_DIR"))
	if dataDir == "" {
		dataDir = "./data"
	}

	discordToken := strings.TrimSpace(os.Getenv("DISCORD_TOKEN"))
	dcePath := strings.TrimSpace(os.Getenv("CORDBRIEF_DCE_PATH"))

	return &EnvConfig{
		BotToken:     token,
		OwnerID:      ownerID,
		DataDir:      dataDir,
		DiscordToken: discordToken,
		DCEPath:      dcePath,
	}, nil
}
