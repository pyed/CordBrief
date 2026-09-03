package digest

import (
	"fmt"
	"strings"
)

// JumpLink returns the canonical Discord web jump link for a trusted source message.
func JumpLink(m SourceMessage) string {
	return fmt.Sprintf("https://discord.com/channels/%s/%s/%s", m.GuildID, m.ChannelID, m.MessageID)
}

// RenderMarkdown formats the structured digest into human-readable Markdown for CLI inspection.
func RenderMarkdown(d *Digest, b *Batch) string {
	if d == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# CordBrief — %s\n\n", d.Title))
	sb.WriteString(fmt.Sprintf("%s\n\n", d.Overview))

	if len(d.Items) > 0 {
		sb.WriteString("## Key Insights\n\n")
		for _, item := range d.Items {
			kindLabel := strings.Title(item.Kind)
			sb.WriteString(fmt.Sprintf("• **[%s]** %s\n", kindLabel, item.Text))

			var links []string
			for _, sID := range item.SourceIDs {
				if b != nil {
					if sm, ok := b.SourceMap[sID]; ok {
						links = append(links, fmt.Sprintf("[%s](%s)", sID, JumpLink(sm)))
						continue
					}
				}
				links = append(links, sID)
			}
			if len(links) > 0 {
				sb.WriteString(fmt.Sprintf("  *Sources: %s*\n", strings.Join(links, ", ")))
			}
			sb.WriteString("\n")
		}
	}

	return strings.TrimRight(sb.String(), "\n") + "\n"
}
