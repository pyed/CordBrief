package brief

import (
	"fmt"
	"strings"
)

// RenderMessage renders a single Discord message into a clean, compact text block.
// No unnecessary metadata (avatars, discriminators, reactions, guilds) is included.
func RenderMessage(m Message) string {
	var sb strings.Builder

	// Header: [15:04 UTC] Author:
	timeStr := m.Timestamp.UTC().Format("15:04 UTC")
	author := strings.TrimSpace(m.Author)
	if author == "" {
		author = "Unknown"
	}

	content := strings.TrimSpace(m.Content)
	if content != "" {
		sb.WriteString(fmt.Sprintf("[%s] %s: %s", timeStr, author, content))
	} else {
		sb.WriteString(fmt.Sprintf("[%s] %s", timeStr, author))
	}

	// Reply reference
	if m.ReplyToID != "" {
		sb.WriteString(fmt.Sprintf("\n  reply-to: %s", m.ReplyToID))
	}

	// Attachments
	for _, att := range m.Attachments {
		name := strings.TrimSpace(att.FileName)
		u := strings.TrimSpace(att.URL)
		if name != "" && u != "" {
			sb.WriteString(fmt.Sprintf("\n  attachment: %s <%s>", name, u))
		} else if u != "" {
			sb.WriteString(fmt.Sprintf("\n  attachment: <%s>", u))
		} else if name != "" {
			sb.WriteString(fmt.Sprintf("\n  attachment: %s", name))
		}
	}

	// Embeds (only non-empty title/URL/description)
	for _, emb := range m.Embeds {
		title := strings.TrimSpace(emb.Title)
		u := strings.TrimSpace(emb.URL)
		desc := strings.TrimSpace(emb.Description)

		if title == "" && u == "" && desc == "" {
			continue
		}

		sb.WriteString("\n  link: ")
		parts := make([]string, 0, 3)
		if title != "" {
			parts = append(parts, title)
		}
		if u != "" {
			parts = append(parts, fmt.Sprintf("<%s>", u))
		}
		if desc != "" {
			parts = append(parts, desc)
		}
		sb.WriteString(strings.Join(parts, " - "))
	}

	return sb.String()
}

// RenderTranscript combines rendered message blocks separated by newlines.
func RenderTranscript(messages []Message) string {
	if len(messages) == 0 {
		return ""
	}
	blocks := make([]string, len(messages))
	for i, m := range messages {
		blocks[i] = RenderMessage(m)
	}
	return strings.Join(blocks, "\n\n")
}
