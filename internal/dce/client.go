package dce

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pyed/CordBrief/internal/state"
)

// MaxDiagnosticBuffer limits captured stderr/stdout to 64 KB to prevent unbounded memory growth.
const MaxDiagnosticBuffer = 64 * 1024

// CommandRunner defines the function signature for executing the DCE child process.
// This abstraction allows deterministic unit tests without running external binaries.
type CommandRunner func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error

func defaultCommandRunner(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// Client manages bounded exports using the external DiscordChatExporter.Cli executable.
type Client struct {
	dcePath string
	token   string
	runner  CommandRunner
}

// NewClient creates a Client configured with the given executable path and Discord token.
// Returns an error if dcePath is empty or does not exist as a regular file.
func NewClient(dcePath, token string) (*Client, error) {
	return NewClientWithRunner(dcePath, token, defaultCommandRunner)
}

// NewClientWithRunner allows injecting a custom CommandRunner for testing or custom execution.
func NewClientWithRunner(dcePath, token string, runner CommandRunner) (*Client, error) {
	dcePath = strings.TrimSpace(dcePath)
	if dcePath == "" {
		return nil, errors.New("CORDBRIEF_DCE_PATH is required but was empty")
	}

	// Only stat the filesystem if using the default runner
	if runner == nil {
		runner = defaultCommandRunner
	}

	fi, err := os.Stat(dcePath)
	if err != nil {
		return nil, fmt.Errorf("DCE executable not found at %s: %w", dcePath, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("DCE path %s is a directory, expected executable file", dcePath)
	}

	return &Client{
		dcePath: dcePath,
		token:   strings.TrimSpace(token),
		runner:  runner,
	}, nil
}

// NewMockClient creates a Client with a mock runner without filesystem checks, strictly for tests.
func NewMockClient(dcePath, token string, runner CommandRunner) *Client {
	if runner == nil {
		runner = defaultCommandRunner
	}
	return &Client{
		dcePath: strings.TrimSpace(dcePath),
		token:   strings.TrimSpace(token),
		runner:  runner,
	}
}

// IsConfigured reports whether the client has an executable path and non-empty token.
func (c *Client) IsConfigured() bool {
	return c != nil && c.dcePath != "" && c.token != ""
}

// Version executes DCE with --version and returns the trimmed version string.
func (c *Client) Version(ctx context.Context) (string, error) {
	if c == nil || c.dcePath == "" {
		return "", errors.New("dce client is not initialized")
	}

	var stdout, stderr cappedBuffer
	stdout.limit = MaxDiagnosticBuffer
	stderr.limit = MaxDiagnosticBuffer

	err := c.runner(ctx, c.dcePath, []string{"--version"}, commandEnv(), &stdout, &stderr)
	if err != nil {
		return "", c.sanitizeError(fmt.Errorf("failed to query dce version: %w (stderr: %s)", err, stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Export collects messages for a channel within the requested cursor and cutoff boundaries.
// Bounded export files are created temporarily and deleted upon completion.
// Cursors and durable state are NEVER mutated by this method.
func (c *Client) Export(ctx context.Context, req ExportRequest) (*ExportResult, error) {
	if c == nil || c.dcePath == "" {
		return nil, errors.New("dce client is not initialized")
	}
	if c.token == "" {
		return nil, errors.New("DISCORD_TOKEN is required but was empty")
	}

	req.ChannelID = strings.TrimSpace(req.ChannelID)
	if req.ChannelID == "" {
		return nil, errors.New("channel ID cannot be empty")
	}
	if !state.IsDecimalString(req.ChannelID) {
		return nil, fmt.Errorf("invalid channel ID %q: must contain only decimal digits", req.ChannelID)
	}

	// Prepare temporary working file
	var tempDir string
	var outFilePath string
	var err error

	if req.OutputDir != "" {
		if err := os.MkdirAll(req.OutputDir, 0700); err != nil {
			return nil, fmt.Errorf("failed to create export output dir: %w", err)
		}
		outFilePath = filepath.Join(req.OutputDir, fmt.Sprintf("cordbrief-dce-%s-%d.json", req.ChannelID, time.Now().UnixNano()))
	} else {
		tempDir, err = os.MkdirTemp("", "cordbrief-dce-*")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp dir for export: %w", err)
		}
		defer func() {
			_ = os.RemoveAll(tempDir)
		}()
		outFilePath = filepath.Join(tempDir, fmt.Sprintf("export-%s.json", req.ChannelID))
	}

	// Always ensure the disposable export JSON file is removed when done
	defer func() {
		_ = os.Remove(outFilePath)
	}()

	// Build CLI arguments
	args := []string{
		"export",
		"-c", req.ChannelID,
		"-f", "Json",
		"-o", outFilePath,
		"--utc",
		"--fuck-russia",
	}

	// Serialize --after boundary from cursor
	if req.After.Value != "" {
		switch req.After.Kind {
		case state.CursorKindTimestamp:
			t, err := parseTimestamp(req.After.Value)
			if err != nil {
				return nil, fmt.Errorf("invalid timestamp cursor %q: %w", req.After.Value, err)
			}
			args = append(args, "--after", t.UTC().Format("2006-01-02T15:04:05Z"))
		case state.CursorKindMessageID:
			if !state.IsDecimalString(req.After.Value) {
				return nil, fmt.Errorf("invalid message_id cursor %q: must contain only decimal digits", req.After.Value)
			}
			args = append(args, "--after", req.After.Value)
		default:
			return nil, fmt.Errorf("unknown cursor kind: %q", req.After.Kind)
		}
	}

	// Serialize --before boundary
	if !req.Before.IsZero() {
		args = append(args, "--before", req.Before.UTC().Format("2006-01-02T15:04:05Z"))
	}

	// Construct environment containing DISCORD_TOKEN.
	// DISCORD_TOKEN is NEVER added to args/argv.
	env := append(commandEnv(), "DISCORD_TOKEN="+c.token, "FUCK_RUSSIA=true")

	var stdout, stderr cappedBuffer
	stdout.limit = MaxDiagnosticBuffer
	stderr.limit = MaxDiagnosticBuffer

	runErr := c.runner(ctx, c.dcePath, args, env, &stdout, &stderr)
	if runErr != nil {
		diag := strings.TrimSpace(stderr.String())
		if diag == "" {
			diag = strings.TrimSpace(stdout.String())
		}
		if diag != "" {
			return nil, c.sanitizeError(fmt.Errorf("dce export failed: %w: %s", runErr, diag))
		}
		return nil, c.sanitizeError(fmt.Errorf("dce export failed: %w", runErr))
	}

	// Read raw export file
	data, err := os.ReadFile(outFilePath)
	if err != nil {
		return nil, fmt.Errorf("dce completed successfully but output file is missing: %w", err)
	}

	return parseDCEExport(data)
}

// DCE only receives its own credential, never inherited Telegram or AI credentials.
func commandEnv() []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "TELEGRAM_BOT_TOKEN", "TELEGRAM_OWNER_ID", "LLM_API_KEY", "DISCORD_TOKEN":
			continue
		}
		env = append(env, entry)
	}
	return env
}

// sanitizeError replaces any occurrence of the sensitive token in error messages with [REDACTED].
func (c *Client) sanitizeError(err error) error {
	if err == nil {
		return nil
	}
	if c.token == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), c.token, "[REDACTED]")
	return errors.New(msg)
}

// raw export structures for unmarshaling external DCE JSON without DisallowUnknownFields
type rawDCEExport struct {
	Guild struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"guild"`
	Channel struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		Category string `json:"category"`
	} `json:"channel"`
	Messages []rawDCEMessage `json:"messages"`
}

type rawDCEMessage struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Content   string    `json:"content"`
	Author    struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Nickname string `json:"nickname"`
		IsBot    bool   `json:"isBot"`
	} `json:"author"`
	Reference *struct {
		MessageID string `json:"messageId"`
		ChannelID string `json:"channelId"`
		GuildID   string `json:"guildId"`
	} `json:"reference"`
	Attachments []struct {
		ID            string `json:"id"`
		URL           string `json:"url"`
		FileName      string `json:"fileName"`
		FileSizeBytes int64  `json:"fileSizeBytes"`
	} `json:"attachments"`
	Embeds []struct {
		Title       string `json:"title"`
		URL         string `json:"url"`
		Description string `json:"description"`
	} `json:"embeds"`
}

// parseDCEExport normalizes external DCE JSON into CordBrief domain types.
func parseDCEExport(data []byte) (*ExportResult, error) {
	var raw rawDCEExport
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse dce export json: %w", err)
	}

	res := &ExportResult{
		Guild: GuildInfo{
			ID:   raw.Guild.ID,
			Name: raw.Guild.Name,
		},
		Channel: ChannelInfo{
			ID:       raw.Channel.ID,
			Name:     raw.Channel.Name,
			Type:     raw.Channel.Type,
			Category: raw.Channel.Category,
		},
		Messages: make([]Message, 0, len(raw.Messages)),
	}

	var maxID string
	for _, m := range raw.Messages {
		msg := Message{
			ID:        m.ID,
			Timestamp: m.Timestamp,
			Content:   m.Content,
			Author: Author{
				ID:       m.Author.ID,
				Name:     m.Author.Name,
				Nickname: m.Author.Nickname,
				IsBot:    m.Author.IsBot,
			},
		}

		if m.Reference != nil && m.Reference.MessageID != "" {
			msg.ReplyTo = &ReplyRef{
				MessageID: m.Reference.MessageID,
				ChannelID: m.Reference.ChannelID,
				GuildID:   m.Reference.GuildID,
			}
		}

		if len(m.Attachments) > 0 {
			msg.Attachments = make([]Attachment, len(m.Attachments))
			for i, a := range m.Attachments {
				msg.Attachments[i] = Attachment{
					ID:       a.ID,
					URL:      a.URL,
					FileName: a.FileName,
					Bytes:    a.FileSizeBytes,
				}
			}
		}

		if len(m.Embeds) > 0 {
			msg.Embeds = make([]Embed, len(m.Embeds))
			for i, e := range m.Embeds {
				msg.Embeds[i] = Embed{
					Title:       e.Title,
					URL:         e.URL,
					Description: e.Description,
				}
			}
		}

		res.Messages = append(res.Messages, msg)

		if maxID == "" || CompareSnowflake(m.ID, maxID) > 0 {
			maxID = m.ID
		}
	}

	// Guarantee chronological order: oldest -> newest
	sort.SliceStable(res.Messages, func(i, j int) bool {
		if res.Messages[i].Timestamp.Equal(res.Messages[j].Timestamp) {
			return CompareSnowflake(res.Messages[i].ID, res.Messages[j].ID) < 0
		}
		return res.Messages[i].Timestamp.Before(res.Messages[j].Timestamp)
	})

	res.MaxMessageID = maxID
	return res, nil
}

func parseTimestamp(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

// cappedBuffer implements io.Writer while capping stored bytes to limit.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (n int, err error) {
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		_, _ = b.buf.Write(p[:min(len(p), remaining)])
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	return b.buf.String()
}
