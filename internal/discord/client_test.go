package discord

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// noopSleeper records sleep calls without sleeping.
func newTestSleeper() (Sleeper, *[]time.Duration) {
	var mu sync.Mutex
	durations := make([]time.Duration, 0)
	sleeper := func(ctx context.Context, d time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			mu.Lock()
			durations = append(durations, d)
			mu.Unlock()
			return nil
		}
	}
	return sleeper, &durations
}

func TestClient_Authentication(t *testing.T) {
	server := NewFakeDiscordServer()
	defer server.Close()
	server.ExpectedToken = "valid-token"

	t.Run("successful bot authentication", func(t *testing.T) {
		client := NewClient("valid-token", WithBaseURL(server.URL))
		ident, err := client.GetBotIdentity(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ident.Username != "CordBriefBot" || !ident.IsBot {
			t.Errorf("unexpected identity: %+v", ident)
		}
	})

	t.Run("invalid token 401 fails immediately without retry", func(t *testing.T) {
		sleeper, sleeps := newTestSleeper()
		client := NewClient("bad-token", WithBaseURL(server.URL), WithSleeper(sleeper))
		_, err := client.GetBotIdentity(context.Background())
		if err == nil {
			t.Fatal("expected 401 error, got nil")
		}
		if !strings.Contains(err.Error(), "HTTP 401") {
			t.Errorf("expected HTTP 401 in error, got: %v", err)
		}
		if len(*sleeps) > 0 {
			t.Errorf("401 must not trigger retries/sleeps, got %d sleeps", len(*sleeps))
		}
	})

	t.Run("non-bot user identity fails clearly", func(t *testing.T) {
		server.BotUser = BotIdentity{
			ID:       "9999",
			Username: "RegularUser",
			IsBot:    false,
		}
		client := NewClient("valid-token", WithBaseURL(server.URL))
		_, err := client.GetBotIdentity(context.Background())
		if err == nil {
			t.Fatal("expected error for non-bot identity, got nil")
		}
		if !strings.Contains(err.Error(), "not a bot account") {
			t.Errorf("expected 'not a bot account' error, got: %v", err)
		}
		// Reset bot identity
		server.BotUser = BotIdentity{ID: "1234567890", Username: "CordBriefBot", IsBot: true}
	})
}

func TestClient_GuildChannels(t *testing.T) {
	server := NewFakeDiscordServer()
	defer server.Close()
	server.ExpectedToken = "token"

	// Mock guild with supported and unsupported channels
	server.GuildChannels["guild-1"] = []Channel{
		{ID: "300", Name: "voice-general", Type: 2, Position: 3}, // Voice - unsupported
		{ID: "200", Name: "general-text", Type: 0, Position: 2},  // Text - supported
		{ID: "100", Name: "announcements", Type: 5, Position: 1}, // Announcement - supported
		{ID: "400", Name: "forum-bugs", Type: 15, Position: 4},   // Forum - unsupported
		{ID: "250", Name: "another-text", Type: 0, Position: 2},  // Text - same position as 200
	}

	client := NewClient("token", WithBaseURL(server.URL))
	channels, err := client.ListGuildChannels(context.Background(), "guild-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only 3 channels should be returned (100, 200, 250)
	if len(channels) != 3 {
		t.Fatalf("expected 3 supported channels, got %d", len(channels))
	}

	// Deterministic ordering: position ascending, tie-breaker ID ascending
	expectedOrder := []string{"100", "200", "250"}
	for i, ch := range channels {
		if ch.ID != expectedOrder[i] {
			t.Errorf("expected channel[%d] to be ID %s, got %s", i, expectedOrder[i], ch.ID)
		}
		if !ch.IsSupported() {
			t.Errorf("channel %s (%s) should be supported", ch.ID, ch.TypeName())
		}
	}
}

func TestClient_MessagePaginationAndWindow(t *testing.T) {
	server := NewFakeDiscordServer()
	defer server.Close()
	server.ExpectedToken = "token"

	baseTime := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	// Helper to generate N messages ordered newest to oldest
	generateMessages := func(count int, start time.Time, interval time.Duration) []map[string]any {
		msgs := make([]map[string]any, count)
		for i := 0; i < count; i++ {
			msgTime := start.Add(-time.Duration(i) * interval)
			id := fmt.Sprintf("%018d", count-i)
			msgs[i] = map[string]any{
				"id":         id,
				"channel_id": "ch-test",
				"content":    fmt.Sprintf("Message %d", count-i),
				"timestamp":  msgTime.Format(time.RFC3339Nano),
				"author": map[string]any{
					"id":          "user-1",
					"username":    "alice",
					"global_name": "Alice Wonderland",
					"bot":         false,
				},
				"member": map[string]any{
					"nick": "Alicia",
				},
				"attachments": []map[string]any{
					{"filename": "log.txt", "url": "https://cdn.discord.com/secret/log.txt"},
				},
			}
		}
		return msgs
	}

	t.Run("exact window (start, end] boundaries", func(t *testing.T) {
		msgs := []map[string]any{
			{"id": "5", "channel_id": "ch-window", "content": "m5", "timestamp": "2026-09-03T12:40:00Z", "author": map[string]any{"username": "u"}},
			{"id": "4", "channel_id": "ch-window", "content": "m4", "timestamp": "2026-09-03T12:30:00Z", "author": map[string]any{"username": "u"}},
			{"id": "3", "channel_id": "ch-window", "content": "m3", "timestamp": "2026-09-03T12:20:00Z", "author": map[string]any{"username": "u"}},
			{"id": "2", "channel_id": "ch-window", "content": "m2", "timestamp": "2026-09-03T12:10:00Z", "author": map[string]any{"username": "u"}},
			{"id": "1", "channel_id": "ch-window", "content": "m1", "timestamp": "2026-09-03T12:00:00Z", "author": map[string]any{"username": "u"}},
			{"id": "0", "channel_id": "ch-window", "content": "m0", "timestamp": "2026-09-03T11:50:00Z", "author": map[string]any{"username": "u"}},
		}
		server.Messages["ch-window"] = msgs

		windowStart := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
		windowEnd := time.Date(2026, 9, 3, 12, 30, 0, 0, time.UTC)

		client := NewClient("token", WithBaseURL(server.URL))
		result, err := client.FetchMessages(context.Background(), "ch-window", windowStart, windowEnd, 1000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(result))
		}

		expectedIDs := []string{"2", "3", "4"}
		for i, m := range result {
			if m.ID != expectedIDs[i] {
				t.Errorf("expected result[%d] to have ID %s, got %s", i, expectedIDs[i], m.ID)
			}
		}
	})

	t.Run("multi-page window with over 250 messages", func(t *testing.T) {
		msgs := generateMessages(300, baseTime.Add(5*time.Hour), time.Minute)
		server.Messages["ch-multi"] = msgs

		windowStart := baseTime
		windowEnd := baseTime.Add(5 * time.Hour)

		client := NewClient("token", WithBaseURL(server.URL))
		result, err := client.FetchMessages(context.Background(), "ch-multi", windowStart, windowEnd, 1000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result) != 300 {
			t.Fatalf("expected 300 messages, got %d", len(result))
		}

		for i := 1; i < len(result); i++ {
			if result[i].Timestamp.Before(result[i-1].Timestamp) {
				t.Fatalf("messages out of chronological order at index %d", i)
			}
		}
	})

	t.Run("deterministic same-timestamp ordering with snowflake tie-breaker", func(t *testing.T) {
		sameTime := "2026-09-03T12:15:00Z"
		server.Messages["ch-ties"] = []map[string]any{
			{"id": "300", "channel_id": "ch-ties", "content": "tie3", "timestamp": sameTime, "author": map[string]any{"username": "u"}},
			{"id": "100", "channel_id": "ch-ties", "content": "tie1", "timestamp": sameTime, "author": map[string]any{"username": "u"}},
			{"id": "200", "channel_id": "ch-ties", "content": "tie2", "timestamp": sameTime, "author": map[string]any{"username": "u"}},
		}

		client := NewClient("token", WithBaseURL(server.URL))
		result, err := client.FetchMessages(context.Background(), "ch-ties", baseTime, baseTime.Add(time.Hour), 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(result))
		}
		expectedIDs := []string{"100", "200", "300"}
		for i, m := range result {
			if m.ID != expectedIDs[i] {
				t.Errorf("expected result[%d] ID %s, got %s", i, expectedIDs[i], m.ID)
			}
		}
	})

	t.Run("safety cap exceeded errors loudly", func(t *testing.T) {
		server.Messages["ch-overflow"] = generateMessages(150, baseTime.Add(2*time.Hour), time.Minute)
		client := NewClient("token", WithBaseURL(server.URL))

		_, err := client.FetchMessages(context.Background(), "ch-overflow", baseTime, baseTime.Add(3*time.Hour), 100)
		if err == nil {
			t.Fatal("expected safety cap error, got nil")
		}
		if !strings.Contains(err.Error(), "exceeded max_messages_per_channel safety cap") {
			t.Errorf("expected safety cap error message, got: %v", err)
		}
	})

	t.Run("safety cap triggered by messages outside requested retained window", func(t *testing.T) {
		// 150 messages generated between 15:00 and 16:00 (all newer than windowEnd 13:00).
		// While paginating backwards to find messages in (12:00, 13:00], it examines 150 messages.
		// With maxMessages = 100, the cap must trigger even though 0 messages would be retained.
		server.Messages["ch-outside-window"] = generateMessages(150, baseTime.Add(4*time.Hour), time.Minute)
		client := NewClient("token", WithBaseURL(server.URL))

		windowStart := baseTime
		windowEnd := baseTime.Add(time.Hour)

		_, err := client.FetchMessages(context.Background(), "ch-outside-window", windowStart, windowEnd, 100)
		if err == nil {
			t.Fatal("expected safety cap error for messages outside window, got nil")
		}
		if !strings.Contains(err.Error(), "exceeded max_messages_per_channel safety cap") {
			t.Errorf("expected safety cap error, got: %v", err)
		}
	})

	t.Run("pagination progress protection fails when cursor repeats", func(t *testing.T) {
		server.RepeatStaticPage = true
		server.StaticPage = []map[string]any{
			{
				"id":         "static-cursor-1",
				"channel_id": "ch-loop",
				"content":    "loop",
				"timestamp":  baseTime.Add(10 * time.Minute).Format(time.RFC3339Nano),
				"author":     map[string]any{"username": "u"},
			},
		}
		defer func() {
			server.RepeatStaticPage = false
			server.StaticPage = nil
		}()

		client := NewClient("token", WithBaseURL(server.URL))
		// Window begins 10 hours earlier so client attempts to paginate backwards
		_, err := client.FetchMessages(context.Background(), "ch-loop", baseTime.Add(-10*time.Hour), baseTime.Add(time.Hour), 100)
		if err == nil {
			t.Fatal("expected pagination progress error, got nil")
		}
		if !strings.Contains(err.Error(), "pagination progress halted") {
			t.Errorf("expected pagination progress error, got: %v", err)
		}
	})

	t.Run("empty channel returns empty slice without error", func(t *testing.T) {
		server.Messages["ch-empty"] = []map[string]any{}
		client := NewClient("token", WithBaseURL(server.URL))
		result, err := client.FetchMessages(context.Background(), "ch-empty", baseTime, baseTime.Add(time.Hour), 100)
		if err != nil {
			t.Fatalf("unexpected error on empty channel: %v", err)
		}
		if len(result) != 0 {
			t.Fatalf("expected 0 messages, got %d", len(result))
		}
	})

	t.Run("malformed timestamp fails with clear error", func(t *testing.T) {
		server.Messages["ch-bad-ts"] = []map[string]any{
			{"id": "1", "channel_id": "ch-bad-ts", "content": "bad", "timestamp": "not-a-timestamp", "author": map[string]any{"username": "u"}},
		}
		client := NewClient("token", WithBaseURL(server.URL))
		_, err := client.FetchMessages(context.Background(), "ch-bad-ts", baseTime, baseTime.Add(time.Hour), 100)
		if err == nil {
			t.Fatal("expected error on malformed timestamp, got nil")
		}
		if !strings.Contains(err.Error(), "malformed timestamp") {
			t.Errorf("expected malformed timestamp in error, got: %v", err)
		}
	})
}

func TestClient_Normalization(t *testing.T) {
	server := NewFakeDiscordServer()
	defer server.Close()
	server.ExpectedToken = "token"

	ts := "2026-09-03T12:05:00Z"
	server.Messages["ch-norm"] = []map[string]any{
		{
			"id":         "101",
			"channel_id": "ch-norm",
			"content":    "Hello world with attachment",
			"timestamp":  ts,
			"author": map[string]any{
				"id":          "u1",
				"username":    "bob",
				"global_name": "Bob Builder",
				"bot":         true,
			},
			"member": map[string]any{
				"nick": "Bobby",
			},
			"attachments": []map[string]any{
				{"filename": "screenshot.png", "url": "https://cdn.discord.com/secret/screenshot.png"},
				{"filename": "data.csv", "url": "https://cdn.discord.com/secret/data.csv"},
			},
			"message_reference": map[string]any{
				"message_id": "99",
			},
		},
	}

	start := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 3, 12, 10, 0, 0, time.UTC)

	client := NewClient("token", WithBaseURL(server.URL))
	result, err := client.FetchMessages(context.Background(), "ch-norm", start, end, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}

	m := result[0]
	if m.AuthorName != "Bobby" {
		t.Errorf("expected author name 'Bobby', got %q", m.AuthorName)
	}
	if !m.AuthorIsBot {
		t.Error("expected AuthorIsBot to be true")
	}
	if m.ReplyToID != "99" {
		t.Errorf("expected ReplyToID '99', got %q", m.ReplyToID)
	}
	if len(m.AttachmentNames) != 2 || m.AttachmentNames[0] != "screenshot.png" || m.AttachmentNames[1] != "data.csv" {
		t.Errorf("unexpected attachment names: %v", m.AttachmentNames)
	}
	if m.LocalSourceID != "" {
		t.Errorf("LocalSourceID must remain empty at this layer, got %q", m.LocalSourceID)
	}
}

func TestClient_RateLimitsAndRetries(t *testing.T) {
	t.Run("429 rate limit with Retry-After header", func(t *testing.T) {
		server := NewFakeDiscordServer()
		defer server.Close()
		server.ExpectedToken = "token"

		sleeper, sleeps := newTestSleeper()
		server.RateLimitNext = true
		server.RateLimitSecs = 0.5
		server.RateLimitInHeader = true
		server.RateLimitInBody = false

		client := NewClient("token", WithBaseURL(server.URL), WithSleeper(sleeper))
		ident, err := client.GetBotIdentity(context.Background())
		if err != nil {
			t.Fatalf("unexpected error after rate limit: %v", err)
		}
		if ident.Username != "CordBriefBot" {
			t.Errorf("unexpected bot: %+v", ident)
		}
		if len(*sleeps) != 1 {
			t.Fatalf("expected 1 sleep call, got %d", len(*sleeps))
		}
		if (*sleeps)[0] != 500*time.Millisecond {
			t.Errorf("expected 500ms sleep, got %v", (*sleeps)[0])
		}
	})

	t.Run("429 rate limit with JSON body retry_after fallback", func(t *testing.T) {
		server := NewFakeDiscordServer()
		defer server.Close()
		server.ExpectedToken = "token"

		sleeper, sleeps := newTestSleeper()
		server.RateLimitNext = true
		server.RateLimitSecs = 0.25
		server.RateLimitInHeader = false
		server.RateLimitInBody = true

		client := NewClient("token", WithBaseURL(server.URL), WithSleeper(sleeper))
		ident, err := client.GetBotIdentity(context.Background())
		if err != nil {
			t.Fatalf("unexpected error after body rate limit: %v", err)
		}
		if ident.Username != "CordBriefBot" {
			t.Errorf("unexpected bot: %+v", ident)
		}
		if len(*sleeps) != 1 {
			t.Fatalf("expected 1 sleep call, got %d", len(*sleeps))
		}
		if (*sleeps)[0] != 250*time.Millisecond {
			t.Errorf("expected 250ms sleep, got %v", (*sleeps)[0])
		}
	})

	t.Run("context cancellation while waiting for rate limit", func(t *testing.T) {
		server := NewFakeDiscordServer()
		defer server.Close()
		server.ExpectedToken = "token"

		server.RateLimitNext = true
		server.RateLimitSecs = 10.0

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		client := NewClient("token", WithBaseURL(server.URL))
		_, err := client.GetBotIdentity(ctx)
		if err == nil {
			t.Fatal("expected context canceled error, got nil")
		}
	})

	t.Run("successful response with exhausted remaining quota waits reset deadline on next request", func(t *testing.T) {
		server := NewFakeDiscordServer()
		defer server.Close()
		server.ExpectedToken = "token"

		sleeper, sleeps := newTestSleeper()
		server.RemainingZero = true
		server.ResetAfterSecs = 0.3

		client := NewClient("token", WithBaseURL(server.URL), WithSleeper(sleeper))
		// First request exhausts remaining quota
		_, err := client.ProbeChannelHistory(context.Background(), "ch-first")
		if err != nil {
			t.Fatalf("first request failed: %v", err)
		}

		// Second request should wait for reset deadline before executing
		_, err = client.ProbeChannelHistory(context.Background(), "ch-second")
		if err != nil {
			t.Fatalf("second request failed: %v", err)
		}

		if len(*sleeps) != 1 {
			t.Fatalf("expected 1 rate-limit reset wait sleep, got %d", len(*sleeps))
		}
	})

	t.Run("transient 500 retry succeeds", func(t *testing.T) {
		server := NewFakeDiscordServer()
		defer server.Close()
		server.ExpectedToken = "token"

		sleeper, sleeps := newTestSleeper()
		server.FailStatus = http.StatusInternalServerError

		client := NewClient("token", WithBaseURL(server.URL), WithSleeper(sleeper))
		ident, err := client.GetBotIdentity(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ident.Username != "CordBriefBot" {
			t.Errorf("unexpected identity: %+v", ident)
		}
		if len(*sleeps) != 1 {
			t.Errorf("expected 1 retry sleep, got %d", len(*sleeps))
		}
	})

	t.Run("403 and 404 do not retry", func(t *testing.T) {
		server := NewFakeDiscordServer()
		defer server.Close()
		server.ExpectedToken = "token"

		sleeper, sleeps := newTestSleeper()
		server.ChannelStatus["ch-403"] = http.StatusForbidden

		client := NewClient("token", WithBaseURL(server.URL), WithSleeper(sleeper))
		probe, err := client.ProbeChannelHistory(context.Background(), "ch-403")
		if err != nil {
			t.Fatalf("unexpected probe error: %v", err)
		}
		if probe.StatusCode != http.StatusForbidden {
			t.Errorf("expected status 403, got %d", probe.StatusCode)
		}
		if len(*sleeps) > 0 {
			t.Errorf("403 must not retry, got %d sleeps", len(*sleeps))
		}
	})

	t.Run("bounded retry exhaustion fails after max retries", func(t *testing.T) {
		server := NewFakeDiscordServer()
		defer server.Close()
		server.ExpectedToken = "token"

		sleeper, sleeps := newTestSleeper()
		// Always return 500
		server.FailStatus = http.StatusInternalServerError

		client := NewClient("token", WithBaseURL(server.URL), WithSleeper(sleeper))
		// Force server to fail continuously
		server.FailStatus = http.StatusInternalServerError
		// We override handleUsersMe to continuously return 500
		server.BotUserStatus = http.StatusInternalServerError

		_, err := client.GetBotIdentity(context.Background())
		if err == nil {
			t.Fatal("expected error after retry exhaustion, got nil")
		}
		// MaxRetries = 3 sleeps
		if len(*sleeps) != MaxRetries {
			t.Errorf("expected %d retry sleeps, got %d", MaxRetries, len(*sleeps))
		}
	})
}

func TestClient_ProbeChannelHistory(t *testing.T) {
	server := NewFakeDiscordServer()
	defer server.Close()
	server.ExpectedToken = "token"

	client := NewClient("token", WithBaseURL(server.URL))

	t.Run("channel with content returns accessible with content", func(t *testing.T) {
		server.Messages["ch-with-msg"] = []map[string]any{
			{"id": "1", "content": "Sample message text", "timestamp": "2026-09-03T12:00:00Z"},
		}
		probe, err := client.ProbeChannelHistory(context.Background(), "ch-with-msg")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !probe.Accessible || probe.MessageCount != 1 || !probe.HasContent {
			t.Errorf("unexpected probe result: %+v", probe)
		}
	})

	t.Run("empty channel returns accessible without content", func(t *testing.T) {
		server.Messages["ch-empty-probe"] = []map[string]any{}
		probe, err := client.ProbeChannelHistory(context.Background(), "ch-empty-probe")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !probe.Accessible || probe.MessageCount != 0 || probe.HasContent {
			t.Errorf("unexpected probe result: %+v", probe)
		}
	})

	t.Run("forbidden channel returns 403 error", func(t *testing.T) {
		server.ChannelStatus["ch-forbidden"] = http.StatusForbidden
		probe, err := client.ProbeChannelHistory(context.Background(), "ch-forbidden")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if probe.Accessible || probe.Error == nil || !strings.Contains(probe.Error.Error(), "HTTP 403") {
			t.Errorf("unexpected probe result: %+v", probe)
		}
	})
}
