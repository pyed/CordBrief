package job

import (
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// DefaultMaxTelegramRunes also bounds UTF-16 units, keeping non-BMP emoji safely within the limit.
const DefaultMaxTelegramRunes = 3900

// FormatHeading returns the standard Telegram brief heading for a server and channel.
// If serverName is non-empty, it formats as "<serverName> · #<channelName>".
// If serverName is empty, it formats as "#<channelName>".
func FormatHeading(serverName, channelName string) string {
	s := strings.TrimSpace(serverName)
	c := strings.TrimSpace(channelName)
	if c == "" {
		c = "unknown-channel"
	}
	if s != "" {
		return fmt.Sprintf("%s · #%s", s, c)
	}
	return fmt.Sprintf("#%s", c)
}

// FormatNoMessages returns the standard notice when a channel has zero new messages.
func FormatNoMessages(serverName, channelName string) string {
	return fmt.Sprintf("%s\nNo new messages.", FormatHeading(serverName, channelName))
}

// SplitBrief formats a channel brief into one or more Telegram messages strictly bounded by maxRunes.
// It never truncates text, never splits UTF-8 runes incorrectly, and prefers paragraph/line boundaries.
func SplitBrief(serverName, channelName string, messageCount int, body string, maxRunes int) []string {
	if maxRunes < 2 {
		maxRunes = DefaultMaxTelegramRunes
	}

	heading := FormatHeading(serverName, channelName)
	if strings.TrimSpace(body) == "" {
		return sliceText(FormatNoMessages(serverName, channelName), maxRunes, maxRunes)
	}

	countLabel := fmt.Sprintf("%d message", messageCount)
	if messageCount != 1 {
		countLabel = fmt.Sprintf("%d messages", messageCount)
	}

	// 1. Single part check
	singleHeader := fmt.Sprintf("%s\n%s\n\n", heading, countLabel)
	singleMsg := singleHeader + body
	if telegramUnits(singleMsg) <= maxRunes {
		return []string{singleMsg}
	}

	// 2. Multi-part splitting
	// A part contains at least one rune, so len(body) bounds the part-number width.
	upperBound := len(body)
	estPart1HeaderLen := telegramUnits(fmt.Sprintf("%s · %d/%d\n%s\n\n", heading, upperBound, upperBound, countLabel))
	estSubHeaderLen := telegramUnits(fmt.Sprintf("%s · %d/%d\n\n", heading, upperBound, upperBound))
	if maxRunes-estPart1HeaderLen < 2 {
		return sliceText(singleMsg, maxRunes, maxRunes)
	}

	rawChunks := sliceText(body, maxRunes-estPart1HeaderLen, maxRunes-estSubHeaderLen)
	totalParts := len(rawChunks)

	// In the rare event slicing produced 1 chunk, treat as single
	if totalParts <= 1 {
		return []string{singleMsg}
	}

	result := make([]string, totalParts)
	for i, chunk := range rawChunks {
		var header string
		if i == 0 {
			header = fmt.Sprintf("%s · %d/%d\n%s\n\n", heading, i+1, totalParts, countLabel)
		} else {
			header = fmt.Sprintf("%s · %d/%d\n\n", heading, i+1, totalParts)
		}
		result[i] = header + chunk
	}

	return result
}

// SplitText breaks text into contiguous parts strictly respecting maxRunes UTF-16 units.
// It never loses characters, preserves exact content and order, and breaks on natural paragraph/line boundaries.
func SplitText(text string, maxRunes int) []string {
	if maxRunes < 2 {
		maxRunes = DefaultMaxTelegramRunes
	}
	return sliceText(text, maxRunes, maxRunes)
}

// sliceText breaks text into contiguous chunks respecting the given rune budgets.
func sliceText(text string, firstBudget, subBudget int) []string {
	var chunks []string
	remaining := text
	isFirst := true

	for len(remaining) > 0 {
		budget := subBudget
		if isFirst {
			budget = firstBudget
			isFirst = false
		}

		remRunes := []rune(remaining)
		end, units := 0, 0
		for end < len(remRunes) && units+utf16.RuneLen(remRunes[end]) <= budget {
			units += utf16.RuneLen(remRunes[end])
			end++
		}
		if end == len(remRunes) {
			chunks = append(chunks, remaining)
			break
		}

		splitIdx := findSplitIndex(remRunes[:end])
		chunks = append(chunks, string(remRunes[:splitIdx]))
		remaining = string(remRunes[splitIdx:])
	}

	return chunks
}

func telegramUnits(text string) int {
	units := 0
	for _, r := range text {
		units += utf16.RuneLen(r)
	}
	return units
}

// findSplitIndex finds the best break point within the candidate rune slice.
// Precedence: paragraph boundary ("\n\n"), line boundary ("\n"), word boundary (" "), or hard cut.
func findSplitIndex(runes []rune) int {
	text := string(runes)
	budget := len(runes)

	// 1. Look for paragraph break "\n\n" in upper half
	if idx := strings.LastIndex(text, "\n\n"); idx != -1 {
		rIdx := utf8.RuneCountInString(text[:idx])
		if rIdx >= budget/2 {
			return rIdx + 2 // include "\n\n" in current chunk or split right after
		}
	}

	// 2. Look for single line break "\n" in upper half
	if idx := strings.LastIndex(text, "\n"); idx != -1 {
		rIdx := utf8.RuneCountInString(text[:idx])
		if rIdx >= budget/2 {
			return rIdx + 1
		}
	}

	// 3. Look for space " " in upper half
	if idx := strings.LastIndex(text, " "); idx != -1 {
		rIdx := utf8.RuneCountInString(text[:idx])
		if rIdx >= budget/2 {
			return rIdx + 1
		}
	}

	// 4. Hard fallback to max allowed runes
	return budget
}
