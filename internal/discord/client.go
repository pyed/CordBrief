package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultBaseURL = "https://discord.com/api/v10"
	MaxRetries     = 3
	InitialBackoff = 500 * time.Millisecond
)

// Sleeper abstracts time delay for rate-limits and retries, allowing deterministic testing.
type Sleeper func(ctx context.Context, d time.Duration) error

// defaultSleeper sleeps using a timer with context cancellation support.
func defaultSleeper(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ClientOption configures a Discord Client.
type ClientOption func(*Client)

// WithBaseURL overrides the production Discord API base URL (useful for httptest.Server).
func WithBaseURL(rawURL string) ClientOption {
	return func(c *Client) {
		c.baseURL = strings.TrimRight(rawURL, "/")
	}
}

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = httpClient
	}
}

// WithLogger sets the logger for Discord operations.
func WithLogger(logger *slog.Logger) ClientOption {
	return func(c *Client) {
		c.logger = logger
	}
}

// WithSleeper overrides the sleep function for rate-limiting and retries.
func WithSleeper(s Sleeper) ClientOption {
	return func(c *Client) {
		c.sleeper = s
	}
}

// Client is a minimal, robust HTTP client for Discord REST API v10.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	logger     *slog.Logger
	sleeper    Sleeper

	mu        sync.Mutex
	resetTime time.Time // Tracks client-level rate limit reset deadline
}

// NewClient creates a new Discord REST API client.
func NewClient(token string, opts ...ClientOption) *Client {
	c := &Client{
		baseURL:    DefaultBaseURL,
		token:      strings.TrimSpace(token),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		logger:     slog.Default(),
		sleeper:    defaultSleeper,
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Raw Discord API structs for decoding responses.
// Note: We DO NOT use DisallowUnknownFields() on third-party API payloads.

type apiUser struct {
	ID         string  `json:"id"`
	Username   string  `json:"username"`
	GlobalName *string `json:"global_name"`
	Bot        bool    `json:"bot"`
}

type apiMember struct {
	Nick *string `json:"nick"`
}

type apiAttachment struct {
	Filename string `json:"filename"`
}

type apiMessageReference struct {
	MessageID string `json:"message_id"`
}

type apiMessage struct {
	ID               string               `json:"id"`
	ChannelID        string               `json:"channel_id"`
	Content          string               `json:"content"`
	Timestamp        string               `json:"timestamp"`
	Author           apiUser              `json:"author"`
	Member           *apiMember           `json:"member"`
	Attachments      []apiAttachment      `json:"attachments"`
	MessageReference *apiMessageReference `json:"message_reference"`
}

type apiRateLimit struct {
	Message    string  `json:"message"`
	RetryAfter float64 `json:"retry_after"`
	Global     bool    `json:"global"`
}

// GetBotIdentity retrieves the authenticated user and verifies it is a bot account.
func (c *Client) GetBotIdentity(ctx context.Context) (*BotIdentity, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/users/@me", nil)
	if err != nil {
		return nil, fmt.Errorf("create get identity request: %w", err)
	}

	resp, err := c.doWithRetry(req)
	if err != nil {
		return nil, fmt.Errorf("get bot identity: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("authentication failed: invalid Discord bot token (HTTP 401)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get bot identity returned unexpected HTTP status %d", resp.StatusCode)
	}

	var u apiUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("decode bot identity: %w", err)
	}

	if !u.Bot {
		return nil, fmt.Errorf("authenticated user %q (%s) is not a bot account", u.Username, u.ID)
	}

	return &BotIdentity{
		ID:       u.ID,
		Username: u.Username,
		IsBot:    u.Bot,
	}, nil
}

// ListGuildChannels lists all channels in a guild, filtered to supported types (text & announcement)
// and sorted deterministically by position, then ID.
func (c *Client) ListGuildChannels(ctx context.Context, guildID string) ([]Channel, error) {
	path := fmt.Sprintf("/guilds/%s/channels", url.PathEscape(guildID))
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("create list channels request: %w", err)
	}

	resp, err := c.doWithRetry(req)
	if err != nil {
		return nil, fmt.Errorf("list guild channels: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("authentication failed: invalid Discord bot token (HTTP 401)")
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("guild %s not found (HTTP 404)", guildID)
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("access to guild %s is forbidden (HTTP 403)", guildID)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list guild channels returned HTTP %d", resp.StatusCode)
	}

	var allChannels []Channel
	if err := json.NewDecoder(resp.Body).Decode(&allChannels); err != nil {
		return nil, fmt.Errorf("decode guild channels: %w", err)
	}

	// Filter only supported text and announcement channels
	var supported []Channel
	for _, ch := range allChannels {
		if ch.IsSupported() {
			supported = append(supported, ch)
		}
	}

	// Deterministic sorting: position ascending, then ID ascending
	sort.Slice(supported, func(i, j int) bool {
		if supported[i].Position != supported[j].Position {
			return supported[i].Position < supported[j].Position
		}
		return compareSnowflakes(supported[i].ID, supported[j].ID) < 0
	})

	return supported, nil
}

// ProbeChannelHistory performs a minimal limit=1 history probe to verify channel accessibility.
// Per SPEC.md, it never returns or logs message content.
func (c *Client) ProbeChannelHistory(ctx context.Context, channelID string) (*HistoryProbeResult, error) {
	path := fmt.Sprintf("/channels/%s/messages?limit=1", url.PathEscape(channelID))
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("create probe request: %w", err)
	}

	resp, err := c.doWithRetry(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	result := &HistoryProbeResult{
		StatusCode: resp.StatusCode,
	}

	switch resp.StatusCode {
	case http.StatusOK:
		result.Accessible = true
		var msgs []apiMessage
		if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
			return nil, fmt.Errorf("decode probe message: %w", err)
		}
		result.MessageCount = len(msgs)
		if len(msgs) > 0 && strings.TrimSpace(msgs[0].Content) != "" {
			result.HasContent = true
		}
		return result, nil

	case http.StatusForbidden:
		result.Error = fmt.Errorf("forbidden (HTTP 403): bot lacks View Channel or Read Message History permission on channel %s", channelID)
		return result, nil

	case http.StatusNotFound:
		result.Error = fmt.Errorf("channel %s not found (HTTP 404)", channelID)
		return result, nil

	default:
		result.Error = fmt.Errorf("channel probe returned HTTP %d", resp.StatusCode)
		return result, nil
	}
}

// FetchMessages retrieves an exact bounded message window (windowStart, windowEnd] from a channel.
// Messages are paginated using limit=100 and before=<oldest_message_id>, filtered, deduplicated,
// safety-capped, and returned in chronological oldest -> newest order.
func (c *Client) FetchMessages(
	ctx context.Context,
	channelID string,
	windowStart, windowEnd time.Time,
	maxMessages int,
) ([]NormalizedMessage, error) {
	if maxMessages <= 0 {
		return nil, errors.New("maxMessages must be positive")
	}
	if !windowEnd.After(windowStart) {
		return nil, fmt.Errorf("windowEnd (%v) must be after windowStart (%v)", windowEnd, windowStart)
	}

	var (
		accumulated   []NormalizedMessage
		seenIDs       = make(map[string]bool)
		beforeCursor  = ""
		pageCount     = 0
		totalExamined = 0
	)

	for {
		pageCount++
		query := url.Values{}
		query.Set("limit", "100")
		if beforeCursor != "" {
			query.Set("before", beforeCursor)
		}

		path := fmt.Sprintf("/channels/%s/messages?%s", url.PathEscape(channelID), query.Encode())
		req, err := c.newRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, fmt.Errorf("create fetch messages request: %w", err)
		}

		resp, err := c.doWithRetry(req)
		if err != nil {
			return nil, fmt.Errorf("fetch messages page %d: %w", pageCount, err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("fetch messages for channel %s returned HTTP %d", channelID, resp.StatusCode)
		}

		var rawMsgs []apiMessage
		dec := json.NewDecoder(resp.Body)
		if err := dec.Decode(&rawMsgs); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decode messages page: %w", err)
		}
		resp.Body.Close()

		if len(rawMsgs) == 0 {
			// Discord returned an empty page -> reached end of channel history
			break
		}

		// Pagination progress protection: ensure before cursor actually advances
		oldestMsg := rawMsgs[len(rawMsgs)-1]
		if oldestMsg.ID == beforeCursor {
			return nil, fmt.Errorf("pagination progress halted on channel %s: cursor %s did not advance", channelID, beforeCursor)
		}
		beforeCursor = oldestMsg.ID

		oldestParsedTime, err := time.Parse(time.RFC3339Nano, oldestMsg.Timestamp)
		if err != nil {
			// Fall back to standard RFC3339 if nano fails
			oldestParsedTime, err = time.Parse(time.RFC3339, oldestMsg.Timestamp)
			if err != nil {
				return nil, fmt.Errorf("malformed timestamp %q on message %s: %w", oldestMsg.Timestamp, oldestMsg.ID, err)
			}
		}

		// Examine all messages on this page
		// Per SPEC.md: max_messages_per_channel bounds REST traversal work and counts
		// EVERY message object fetched before time-window, deduplication, or bot filtering.
		for _, raw := range rawMsgs {
			totalExamined++
			if totalExamined > maxMessages {
				return nil, fmt.Errorf("channel %s exceeded max_messages_per_channel safety cap (%d messages examined)", channelID, maxMessages)
			}

			parsedTime, err := time.Parse(time.RFC3339Nano, raw.Timestamp)
			if err != nil {
				parsedTime, err = time.Parse(time.RFC3339, raw.Timestamp)
				if err != nil {
					return nil, fmt.Errorf("malformed timestamp %q on message %s: %w", raw.Timestamp, raw.ID, err)
				}
			}

			// Bounded window check: (windowStart, windowEnd]
			// Must be strictly after windowStart AND on-or-before windowEnd
			if parsedTime.After(windowStart) && !parsedTime.After(windowEnd) {
				if !seenIDs[raw.ID] {
					seenIDs[raw.ID] = true
					accumulated = append(accumulated, normalizeMessage(raw, parsedTime))
				}
			}
		}

		// Stop pagination if the oldest message on this page has crossed or reached the lower boundary
		if !oldestParsedTime.After(windowStart) {
			break
		}
	}

	// Sort accumulated messages chronologically oldest -> newest
	sort.Slice(accumulated, func(i, j int) bool {
		if !accumulated[i].Timestamp.Equal(accumulated[j].Timestamp) {
			return accumulated[i].Timestamp.Before(accumulated[j].Timestamp)
		}
		// Deterministic tie-breaker for identical timestamps: snowflake ID
		return compareSnowflakes(accumulated[i].ID, accumulated[j].ID) < 0
	})

	return accumulated, nil
}

// normalizeMessage converts a Discord API message to CordBrief's NormalizedMessage.
func normalizeMessage(raw apiMessage, ts time.Time) NormalizedMessage {
	// Display name resolution: member nickname -> author global name -> username
	authorName := raw.Author.Username
	if raw.Author.GlobalName != nil && strings.TrimSpace(*raw.Author.GlobalName) != "" {
		authorName = strings.TrimSpace(*raw.Author.GlobalName)
	}
	if raw.Member != nil && raw.Member.Nick != nil && strings.TrimSpace(*raw.Member.Nick) != "" {
		authorName = strings.TrimSpace(*raw.Member.Nick)
	}

	// Attachment filenames only
	var attachments []string
	for _, att := range raw.Attachments {
		if fn := strings.TrimSpace(att.Filename); fn != "" {
			attachments = append(attachments, fn)
		}
	}

	var replyToID string
	if raw.MessageReference != nil {
		replyToID = raw.MessageReference.MessageID
	}

	return NormalizedMessage{
		ID:              raw.ID,
		ChannelID:       raw.ChannelID,
		AuthorName:      authorName,
		AuthorIsBot:     raw.Author.Bot,
		Timestamp:       ts,
		Content:         raw.Content,
		ReplyToID:       replyToID,
		AttachmentNames: attachments,
	}
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	reqURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bot "+c.token)
	req.Header.Set("User-Agent", "CordBrief/0.1 (https://github.com/cordbrief)")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return req, nil
}

// doWithRetry executes an HTTP request with rate-limit respect and bounded exponential retries
// for transient network and 5xx errors.
func (c *Client) doWithRetry(req *http.Request) (*http.Response, error) {
	var (
		resp    *http.Response
		err     error
		backoff = InitialBackoff
	)

	for attempt := 0; attempt <= MaxRetries; attempt++ {
		// Wait if a previous response signaled client-level rate limiting
		if err := c.waitRateLimit(req.Context()); err != nil {
			return nil, err
		}

		resp, err = c.httpClient.Do(req)
		if err != nil {
			// Transient network error: retry if attempts remain
			if attempt < MaxRetries {
				if sleepErr := c.sleeper(req.Context(), backoff); sleepErr != nil {
					return nil, sleepErr
				}
				backoff *= 2
				continue
			}
			return nil, fmt.Errorf("network request failed after %d retries: %w", attempt, err)
		}

		// Handle 429 Too Many Requests
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter, parseErr := parseRetryAfter(resp)
			resp.Body.Close()

			if parseErr != nil {
				return nil, fmt.Errorf("received HTTP 429 rate limit but could not determine retry duration: %w", parseErr)
			}

			if attempt < MaxRetries {
				if sleepErr := c.sleeper(req.Context(), retryAfter); sleepErr != nil {
					return nil, sleepErr
				}
				continue
			}
			return nil, fmt.Errorf("rate limit retry attempts exhausted (%d attempts)", MaxRetries)
		}

		// Inspect successful response rate-limit headers
		c.updateRateLimit(resp)

		// Transient server error (500, 502, 503, 504): retry if attempts remain
		if resp.StatusCode >= 500 && resp.StatusCode <= 504 {
			if attempt < MaxRetries {
				resp.Body.Close()
				if sleepErr := c.sleeper(req.Context(), backoff); sleepErr != nil {
					return nil, sleepErr
				}
				backoff *= 2
				continue
			}
			// Exhausted retries for 5xx
			return resp, nil
		}

		// Non-retryable statuses (200, 400, 401, 403, 404, etc.)
		return resp, nil
	}

	return resp, err
}

// waitRateLimit sleeps until the current rate-limit reset deadline has passed.
func (c *Client) waitRateLimit(ctx context.Context) error {
	c.mu.Lock()
	reset := c.resetTime
	c.mu.Unlock()

	if now := time.Now(); reset.After(now) {
		return c.sleeper(ctx, reset.Sub(now))
	}
	return nil
}

// updateRateLimit updates the client's rate-limit reset tracking from response headers.
func (c *Client) updateRateLimit(resp *http.Response) {
	remaining := resp.Header.Get("X-RateLimit-Remaining")
	if remaining == "0" {
		resetAfterStr := resp.Header.Get("X-RateLimit-Reset-After")
		if resetAfterStr != "" {
			if secs, err := strconv.ParseFloat(resetAfterStr, 64); err == nil && secs > 0 {
				c.mu.Lock()
				c.resetTime = time.Now().Add(time.Duration(secs * float64(time.Second)))
				c.mu.Unlock()
			}
		}
	}
}

// parseRetryAfter extracts the rate-limit delay from Retry-After header or JSON body.
func parseRetryAfter(resp *http.Response) (time.Duration, error) {
	// 1. Try Retry-After header
	if h := resp.Header.Get("Retry-After"); h != "" {
		if secs, err := strconv.ParseFloat(h, 64); err == nil && secs > 0 {
			return time.Duration(secs * float64(time.Second)), nil
		}
	}

	// 2. Try JSON body retry_after field
	var rl apiRateLimit
	bodyBytes, err := io.ReadAll(resp.Body)
	if err == nil && len(bodyBytes) > 0 {
		if jErr := json.Unmarshal(bodyBytes, &rl); jErr == nil && rl.RetryAfter > 0 {
			return time.Duration(rl.RetryAfter * float64(time.Second)), nil
		}
	}

	return 0, errors.New("neither Retry-After header nor valid JSON retry_after body found")
}

// compareSnowflakes compares two Discord snowflake ID strings numerically.
func compareSnowflakes(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
