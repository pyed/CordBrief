package job

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func TestFormatHeading(t *testing.T) {
	tests := []struct {
		server   string
		channel  string
		expected string
	}{
		{"LocalLLM", "general", "LocalLLM · #general"},
		{"", "general", "#general"},
		{"   ", "general", "#general"},
		{"LocalLLM", "", "LocalLLM · #unknown-channel"},
		{"", "", "#unknown-channel"},
		{"  My Server  ", "  dev-talk  ", "My Server · #dev-talk"},
	}

	for _, tc := range tests {
		got := FormatHeading(tc.server, tc.channel)
		if got != tc.expected {
			t.Errorf("FormatHeading(%q, %q) = %q; want %q", tc.server, tc.channel, got, tc.expected)
		}
	}
}

func TestFormatNoMessages(t *testing.T) {
	tests := []struct {
		server   string
		channel  string
		expected string
	}{
		{"LocalLLM", "general", "LocalLLM · #general\nNo new messages."},
		{"", "general", "#general\nNo new messages."},
	}

	for _, tc := range tests {
		got := FormatNoMessages(tc.server, tc.channel)
		if got != tc.expected {
			t.Errorf("FormatNoMessages(%q, %q) = %q; want %q", tc.server, tc.channel, got, tc.expected)
		}
	}
}

func TestSplitBrief_EmptyBody(t *testing.T) {
	// With server name
	parts := SplitBrief("LocalLLM", "general", 0, "", 3900)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	expected := "LocalLLM · #general\nNo new messages."
	if parts[0] != expected {
		t.Errorf("expected %q, got %q", expected, parts[0])
	}

	// Without server name (fallback)
	partsFallback := SplitBrief("", "general", 0, "", 3900)
	if len(partsFallback) != 1 {
		t.Fatalf("expected 1 part, got %d", len(partsFallback))
	}
	expectedFallback := "#general\nNo new messages."
	if partsFallback[0] != expectedFallback {
		t.Errorf("expected %q, got %q", expectedFallback, partsFallback[0])
	}
}

func TestSplitBrief_PreservesEveryRuneAndBoundsEveryPart(t *testing.T) {
	for _, body := range []string{
		"  " + strings.Repeat("alpha  βeta\r\n\n🚀 ", 500) + "\n ",
		strings.Repeat("🚀", 5000),
		strings.Repeat("x", 20000), // more than 99 parts at the small test limit
	} {
		parts := SplitBrief("Community", "test", 7, body, 120)
		var restored strings.Builder
		for i, part := range parts {
			if !utf8.ValidString(part) || len(utf16.Encode([]rune(part))) > 120 {
				t.Fatalf("invalid or oversized part %d", i+1)
			}
			header := fmt.Sprintf("Community · #test · %d/%d\n\n", i+1, len(parts))
			if i == 0 {
				header = fmt.Sprintf("Community · #test · 1/%d\n7 messages\n\n", len(parts))
			}
			if !strings.HasPrefix(part, header) {
				t.Fatalf("missing header at part %d", i+1)
			}
			restored.WriteString(strings.TrimPrefix(part, header))
		}
		if restored.String() != body {
			t.Fatal("split lost or duplicated text")
		}
	}
	for _, budget := range []int{2, 10, 120} {
		name := strings.Repeat("long-name", 50)
		parts := SplitBrief("", name, 1, "🚀 body", budget)
		if strings.Join(parts, "") != "#"+name+"\n1 message\n\n🚀 body" {
			t.Fatal("oversized header lost text")
		}
		for _, p := range parts {
			if len(utf16.Encode([]rune(p))) > budget {
				t.Fatal("oversized fallback part")
			}
		}
	}
}

func TestSplitBrief_SinglePartFits(t *testing.T) {
	body := "• Point 1: Database migration was approved.\n• Point 2: Staging rollout on Friday."
	// With server name
	parts := SplitBrief("LocalLLM", "eng", 42, body, 3900)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	if !strings.HasPrefix(parts[0], "LocalLLM · #eng\n42 messages\n\n") {
		t.Errorf("unexpected header: %s", parts[0])
	}
	if !strings.Contains(parts[0], body) {
		t.Errorf("expected body in message: %s", parts[0])
	}

	// Without server name
	partsFallback := SplitBrief("", "eng", 42, body, 3900)
	if len(partsFallback) != 1 {
		t.Fatalf("expected 1 part, got %d", len(partsFallback))
	}
	if !strings.HasPrefix(partsFallback[0], "#eng\n42 messages\n\n") {
		t.Errorf("unexpected header: %s", partsFallback[0])
	}
}

func TestSplitBrief_SingularPluralMessageCount(t *testing.T) {
	part1 := SplitBrief("LocalLLM", "test", 1, "Only one message", 3900)[0]
	if !strings.Contains(part1, "1 message\n") {
		t.Errorf("expected '1 message', got: %s", part1)
	}

	part2 := SplitBrief("LocalLLM", "test", 5, "Five messages", 3900)[0]
	if !strings.Contains(part2, "5 messages\n") {
		t.Errorf("expected '5 messages', got: %s", part2)
	}
}

func TestSplitBrief_MultiPartParagraphBreak(t *testing.T) {
	p1 := strings.Repeat("Alpha beta gamma delta. ", 10)
	p2 := strings.Repeat("Epsilon zeta eta theta. ", 10)
	body := p1 + "\n\n" + p2

	// Set small budget that forces 2 parts
	maxRunes := 220
	parts := SplitBrief("LocalLLM", "dev", 15, body, maxRunes)
	if len(parts) < 2 {
		t.Fatalf("expected >= 2 parts, got %d", len(parts))
	}

	for i, part := range parts {
		count := utf8.RuneCountInString(part)
		if count > maxRunes {
			t.Errorf("part %d exceeded maxRunes: got %d, max %d", i+1, count, maxRunes)
		}
	}

	// Part 1 should have 1/N header with server name
	if !strings.HasPrefix(parts[0], "LocalLLM · #dev · 1/") {
		t.Errorf("expected part 1 header, got: %s", parts[0])
	}
	if !strings.Contains(parts[0], "15 messages") {
		t.Errorf("expected message count in part 1: %s", parts[0])
	}

	// Part 2 should have 2/N header without repeating message count
	if !strings.HasPrefix(parts[1], "LocalLLM · #dev · 2/") {
		t.Errorf("expected part 2 header, got: %s", parts[1])
	}
	if strings.Contains(parts[1], "15 messages") {
		t.Errorf("part 2 should not repeat message count: %s", parts[1])
	}
}

func TestSplitBrief_MultiPartLineBreak(t *testing.T) {
	line1 := strings.Repeat("First line information. ", 8)
	line2 := strings.Repeat("Second line information. ", 8)
	body := line1 + "\n" + line2

	maxRunes := 180
	parts := SplitBrief("LocalLLM", "prod", 10, body, maxRunes)
	if len(parts) < 2 {
		t.Fatalf("expected >= 2 parts, got %d", len(parts))
	}

	for i, part := range parts {
		count := utf8.RuneCountInString(part)
		if count > maxRunes {
			t.Errorf("part %d exceeded maxRunes: got %d, max %d", i+1, count, maxRunes)
		}
	}
}

func TestSplitBrief_UnicodeEmojiIntegrity(t *testing.T) {
	// Japanese text, complex emojis (multi-byte runes like 🚀, 👨‍👩‍👧‍👦, 🔴)
	complexText := "🚀 Deployment status: 完了しました！ 🔴 Incident resolved. " + strings.Repeat("日本語テストと絵文字🎉✨ ", 30)
	maxRunes := 140

	parts := SplitBrief("LocalLLM", "releases", 3, complexText, maxRunes)
	if len(parts) < 2 {
		t.Fatalf("expected >= 2 parts, got %d", len(parts))
	}

	for i, part := range parts {
		if !utf8.ValidString(part) {
			t.Errorf("part %d contains invalid UTF-8!", i)
		}
		count := utf8.RuneCountInString(part)
		if count > maxRunes {
			t.Errorf("part %d exceeded maxRunes %d: got %d", i, maxRunes, count)
		}
	}
}

func TestSplitBrief_UnbrokenLongWord(t *testing.T) {
	// Huge unbroken word with no spaces or newlines
	hugeWord := strings.Repeat("X", 500)
	maxRunes := 150

	parts := SplitBrief("", "hardcut", 1, hugeWord, maxRunes)
	if len(parts) < 4 {
		t.Fatalf("expected >= 4 parts, got %d", len(parts))
	}

	for i, part := range parts {
		count := utf8.RuneCountInString(part)
		if count > maxRunes {
			t.Errorf("part %d exceeded maxRunes: got %d, max %d", i, count, maxRunes)
		}
	}
}
