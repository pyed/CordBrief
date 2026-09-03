package app

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Redaction replacements
const (
	RedactedSecret  = "[REDACTED_SECRET]"
	RedactedContent = "[REDACTED_CONTENT]"
	RedactedAuth    = "[REDACTED_AUTH]"
)

// SafeReplaceAttr intercepts and redacts sensitive keys and values from slog records.
// Per SPEC.md:
// - Discord tokens, LLM keys, authorization headers, raw Discord messages,
//   and complete LLM prompts must NEVER be logged.
func SafeReplaceAttr(groups []string, a slog.Attr) slog.Attr {
	key := strings.ToLower(a.Key)

	// 1. Redact secrets by key name
	if strings.Contains(key, "token") ||
		strings.Contains(key, "secret") ||
		strings.Contains(key, "authorization") ||
		strings.Contains(key, "api_key") ||
		strings.Contains(key, "apikey") ||
		strings.Contains(key, "password") ||
		key == "auth" {
		return slog.String(a.Key, RedactedSecret)
	}

	// 2. Redact raw message content or prompts
	if strings.Contains(key, "raw_message") ||
		strings.Contains(key, "message_content") ||
		key == "content" ||
		strings.Contains(key, "prompt") {
		return slog.String(a.Key, RedactedContent)
	}

	// 3. Inspect string values for leaked auth headers or tokens
	if a.Value.Kind() == slog.KindString {
		str := a.Value.String()
		if strings.HasPrefix(str, "Bot ") || strings.HasPrefix(str, "Bearer ") {
			return slog.String(a.Key, RedactedAuth)
		}
	}

	return a
}

// NewLogger creates a new slog.Logger configured with the safe redaction policy.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	if w == nil {
		w = os.Stderr
	}

	opts := &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: SafeReplaceAttr,
	}

	handler := slog.NewTextHandler(w, opts)
	return slog.New(handler)
}
