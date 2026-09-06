package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultTelegramAPIBase = "https://api.telegram.org"
	DefaultClientTimeout   = 15 * time.Second
	MaxResponseBytes       = 1 << 20 // 1MB response bound
)

var (
	botURLRegex = regexp.MustCompile(`(?i)/bot[^/]+/`)

	// ErrAmbiguousTransport is returned when a network timeout, connection reset,
	// or unproven 5xx response leaves delivery outcome uncertain.
	ErrAmbiguousTransport = errors.New("ambiguous transport condition: request outcome uncertain")
)

// APIError represents an explicit rejected response from the Telegram Bot API.
type APIError struct {
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	RetryAfter  int    `json:"retry_after,omitempty"`
}

func (e *APIError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("telegram api error %d: %s (retry after %d seconds)", e.ErrorCode, e.Description, e.RetryAfter)
	}
	return fmt.Sprintf("telegram api error %d: %s", e.ErrorCode, e.Description)
}

// BotUser represents metadata returned by getMe.
type BotUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username,omitempty"`
}

// ChatInfo represents safe metadata extracted from getUpdates without storing message bodies.
type ChatInfo struct {
	ID        string `json:"id"`
	Type      string `json:"type"` // "private", "group", "supergroup", "channel"
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

// Label returns a friendly descriptive label for UI display.
func (c *ChatInfo) Label() string {
	if c.Title != "" {
		return c.Title
	}
	name := strings.TrimSpace(c.FirstName + " " + c.LastName)
	if name != "" {
		if c.Username != "" {
			return fmt.Sprintf("%s (@%s)", name, c.Username)
		}
		return name
	}
	if c.Username != "" {
		return "@" + c.Username
	}
	return c.ID
}

// SendResult records returned identifier from a successful sendMessage call.
type SendResult struct {
	MessageID int64 `json:"message_id"`
}

// TelegramClient communicates with the Telegram Bot API over HTTPS.
type TelegramClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// ClientOption configures a TelegramClient.
type ClientOption func(*TelegramClient)

// WithBaseURL overrides the API base URL (useful for mock tests with httptest).
func WithBaseURL(u string) ClientOption {
	return func(c *TelegramClient) {
		if u != "" {
			c.baseURL = strings.TrimRight(u, "/")
		}
	}
}

// WithHTTPClient overrides the underlying http.Client.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *TelegramClient) {
		if client != nil {
			c.httpClient = client
		}
	}
}

// NewTelegramClient creates an outbound Telegram client.
func NewTelegramClient(token string, opts ...ClientOption) *TelegramClient {
	baseURL := DefaultTelegramAPIBase
	if env := os.Getenv("CORDBRIEF_TELEGRAM_API_BASE"); env != "" {
		baseURL = strings.TrimRight(env, "/")
	}
	c := &TelegramClient{
		baseURL: baseURL,
		token:   strings.TrimSpace(token),
		httpClient: &http.Client{
			Timeout: DefaultClientTimeout,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// sanitizeError redacts secret bot tokens from any URL or error string.
func (c *TelegramClient) sanitizeError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if c.token != "" {
		msg = strings.ReplaceAll(msg, c.token, "[REDACTED]")
	}
	msg = botURLRegex.ReplaceAllString(msg, "/bot[REDACTED]/")
	return errors.New(msg)
}

// do executes an HTTP request to the Telegram Bot API, safely decoding responses and redacting errors.
func (c *TelegramClient) do(ctx context.Context, method, apiMethod string, body []byte) ([]byte, error) {
	if c.token == "" {
		return nil, errors.New("telegram bot token is not configured")
	}

	targetURL := fmt.Sprintf("%s/bot%s/%s", c.baseURL, c.token, apiMethod)
	var reqBody io.Reader
	if len(body) > 0 {
		reqBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL, reqBody)
	if err != nil {
		return nil, c.sanitizeError(err)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Check for timeout or transport failure after submission
		if isAmbiguousError(err) {
			return nil, fmt.Errorf("%w: %v", ErrAmbiguousTransport, c.sanitizeError(err))
		}
		return nil, c.sanitizeError(err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes))
	if err != nil {
		if isAmbiguousError(err) {
			return nil, fmt.Errorf("%w: %v", ErrAmbiguousTransport, c.sanitizeError(err))
		}
		return nil, c.sanitizeError(err)
	}

	var baseResp struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result,omitempty"`
		ErrorCode   int             `json:"error_code,omitempty"`
		Description string          `json:"description,omitempty"`
		Parameters  *struct {
			RetryAfter int `json:"retry_after,omitempty"`
		} `json:"parameters,omitempty"`
	}

	if err := json.Unmarshal(respData, &baseResp); err != nil {
		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("%w: server returned HTTP %d", ErrAmbiguousTransport, resp.StatusCode)
		}
		return nil, fmt.Errorf("decode telegram response: %w", c.sanitizeError(err))
	}

	if !baseResp.OK {
		apiErr := &APIError{
			ErrorCode:   baseResp.ErrorCode,
			Description: sanitizeDescription(baseResp.Description, c.token),
		}
		if baseResp.Parameters != nil {
			apiErr.RetryAfter = baseResp.Parameters.RetryAfter
		}
		if apiErr.ErrorCode == 0 {
			apiErr.ErrorCode = resp.StatusCode
		}
		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("%w: %v", ErrAmbiguousTransport, apiErr)
		}
		return nil, apiErr
	}

	return baseResp.Result, nil
}

func sanitizeDescription(desc, token string) string {
	if token != "" {
		desc = strings.ReplaceAll(desc, token, "[REDACTED]")
	}
	return botURLRegex.ReplaceAllString(desc, "/bot[REDACTED]/")
}

func isAmbiguousError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return true
	}
	return false
}

// GetMe calls getMe to verify bot token credentials.
func (c *TelegramClient) GetMe(ctx context.Context) (*BotUser, error) {
	data, err := c.do(ctx, http.MethodGet, "getMe", nil)
	if err != nil {
		return nil, err
	}

	var user BotUser
	if err := json.Unmarshal(data, &user); err != nil {
		return nil, fmt.Errorf("decode getMe: %w", c.sanitizeError(err))
	}
	return &user, nil
}

// GetUpdates calls getUpdates to discover chats where users/groups have interacted with the bot.
// It extracts ONLY safe chat metadata and explicitly discards all message content.
func (c *TelegramClient) GetUpdates(ctx context.Context, offset int) ([]ChatInfo, error) {
	method := "getUpdates?limit=100"
	if offset > 0 {
		method = fmt.Sprintf("getUpdates?limit=100&offset=%d", offset)
	}

	data, err := c.do(ctx, http.MethodGet, method, nil)
	if err != nil {
		return nil, err
	}

	var updates []struct {
		UpdateID int `json:"update_id"`
		Message  *struct {
			Chat struct {
				ID        any    `json:"id"`
				Type      string `json:"type"`
				Title     string `json:"title,omitempty"`
				Username  string `json:"username,omitempty"`
				FirstName string `json:"first_name,omitempty"`
				LastName  string `json:"last_name,omitempty"`
			} `json:"chat"`
		} `json:"message,omitempty"`
		ChannelPost *struct {
			Chat struct {
				ID        any    `json:"id"`
				Type      string `json:"type"`
				Title     string `json:"title,omitempty"`
				Username  string `json:"username,omitempty"`
				FirstName string `json:"first_name,omitempty"`
				LastName  string `json:"last_name,omitempty"`
			} `json:"chat"`
		} `json:"channel_post,omitempty"`
		MyChatMember *struct {
			Chat struct {
				ID        any    `json:"id"`
				Type      string `json:"type"`
				Title     string `json:"title,omitempty"`
				Username  string `json:"username,omitempty"`
				FirstName string `json:"first_name,omitempty"`
				LastName  string `json:"last_name,omitempty"`
			} `json:"chat"`
		} `json:"my_chat_member,omitempty"`
	}

	if err := json.Unmarshal(data, &updates); err != nil {
		return nil, fmt.Errorf("decode getUpdates: %w", c.sanitizeError(err))
	}

	seen := make(map[string]bool)
	var chats []ChatInfo

	addChat := func(rawID any, chatType, title, username, first, last string) {
		idStr := formatChatID(rawID)
		if idStr == "" || seen[idStr] {
			return
		}
		seen[idStr] = true
		chats = append(chats, ChatInfo{
			ID:        idStr,
			Type:      chatType,
			Title:     title,
			Username:  username,
			FirstName: first,
			LastName:  last,
		})
	}

	for _, u := range updates {
		if u.Message != nil && u.Message.Chat.ID != nil {
			addChat(u.Message.Chat.ID, u.Message.Chat.Type, u.Message.Chat.Title, u.Message.Chat.Username, u.Message.Chat.FirstName, u.Message.Chat.LastName)
		}
		if u.ChannelPost != nil && u.ChannelPost.Chat.ID != nil {
			addChat(u.ChannelPost.Chat.ID, u.ChannelPost.Chat.Type, u.ChannelPost.Chat.Title, u.ChannelPost.Chat.Username, u.ChannelPost.Chat.FirstName, u.ChannelPost.Chat.LastName)
		}
		if u.MyChatMember != nil && u.MyChatMember.Chat.ID != nil {
			addChat(u.MyChatMember.Chat.ID, u.MyChatMember.Chat.Type, u.MyChatMember.Chat.Title, u.MyChatMember.Chat.Username, u.MyChatMember.Chat.FirstName, u.MyChatMember.Chat.LastName)
		}
	}

	return chats, nil
}

func formatChatID(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	case json.Number:
		return v.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

// SendMessage sends an HTML-formatted message to the destination chat.
func (c *TelegramClient) SendMessage(ctx context.Context, chatID string, text string) (*SendResult, error) {
	if strings.TrimSpace(chatID) == "" {
		return nil, errors.New("chat_id is required")
	}

	payload := map[string]any{
		"chat_id":                  strings.TrimSpace(chatID),
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
		"link_preview_options": map[string]any{
			"is_disabled": true,
		},
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal sendMessage: %w", err)
	}

	resData, err := c.do(ctx, http.MethodPost, "sendMessage", bodyBytes)
	if err != nil {
		return nil, err
	}

	var sent struct {
		MessageID int64 `json:"message_id"`
	}
	if err := json.Unmarshal(resData, &sent); err != nil {
		return nil, fmt.Errorf("decode sendMessage result: %w", c.sanitizeError(err))
	}

	return &SendResult{MessageID: sent.MessageID}, nil
}
