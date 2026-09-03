package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestSafeLogger_Redaction(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelDebug)

	// Log with various sensitive attributes
	logger.Info("sample message",
		slog.String("operation", "fetch_channel"),
		slog.String("channel_id", "123456"),
		slog.Int("message_count", 42),
		slog.String("bot_token", "secret-discord-token-xyz"),
		slog.String("api_key", "sk-secret-llm-key"),
		slog.String("authorization", "Bot secret-discord-token-xyz"),
		slog.String("header_val", "Bearer my-secret-token"),
		slog.String("raw_message", "Hello everyone in secret channel"),
		slog.String("content", "Sensitive user conversation"),
		slog.String("system_prompt", "You are an assistant..."),
	)

	out := buf.String()

	// Verify safe fields ARE present
	if !strings.Contains(out, "operation=fetch_channel") {
		t.Errorf("expected operation to be present, got: %s", out)
	}
	if !strings.Contains(out, "channel_id=123456") {
		t.Errorf("expected channel_id to be present, got: %s", out)
	}
	if !strings.Contains(out, "message_count=42") {
		t.Errorf("expected message_count to be present, got: %s", out)
	}

	// Verify secrets and raw content ARE REDACTED
	if strings.Contains(out, "secret-discord-token-xyz") {
		t.Errorf("discord token was leaked in log: %s", out)
	}
	if strings.Contains(out, "sk-secret-llm-key") {
		t.Errorf("api key was leaked in log: %s", out)
	}
	if strings.Contains(out, "my-secret-token") {
		t.Errorf("auth header value was leaked in log: %s", out)
	}
	if strings.Contains(out, "Hello everyone in secret channel") {
		t.Errorf("raw_message was leaked in log: %s", out)
	}
	if strings.Contains(out, "Sensitive user conversation") {
		t.Errorf("content was leaked in log: %s", out)
	}
	if strings.Contains(out, "You are an assistant...") {
		t.Errorf("prompt was leaked in log: %s", out)
	}

	// Verify redaction tags are present
	if !strings.Contains(out, RedactedSecret) {
		t.Errorf("expected %s in output", RedactedSecret)
	}
	if !strings.Contains(out, RedactedContent) {
		t.Errorf("expected %s in output", RedactedContent)
	}
	if !strings.Contains(out, RedactedAuth) {
		t.Errorf("expected %s in output", RedactedAuth)
	}
}
