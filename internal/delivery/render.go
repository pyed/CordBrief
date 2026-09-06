package delivery

import (
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"

	"cordbrief/internal/digest"
)

const (
	// MaxTelegramChunkRunes targets safe character count comfortably below Telegram's 4096 UTF-16 limit.
	MaxTelegramChunkRunes = 3800
	// TargetBodyRunes reserve space for multi-part banner headers.
	TargetBodyRunes = 3500
)

// BuildSourceDisplayMap builds a stable, digest-wide mapping from internal source IDs
// (e.g. "S000001", "S000002") to user-facing 1-based display numbers ("1", "2", "3"...).
// The mapping is strictly deterministic and digest-wide:
// - Unique source IDs cited across all items are collected and sorted in deterministic order.
// - Contiguous numbers 1..N are assigned to the sorted IDs.
// - If the same source ID is cited across multiple items, it receives the exact same number.
func BuildSourceDisplayMap(d *digest.Digest) map[string]string {
	if d == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var uniqueIDs []string
	for _, item := range d.Items {
		for _, sID := range item.SourceIDs {
			s := strings.TrimSpace(sID)
			if s == "" {
				continue
			}
			if _, exists := seen[s]; !exists {
				seen[s] = struct{}{}
				uniqueIDs = append(uniqueIDs, s)
			}
		}
	}
	sort.Strings(uniqueIDs)
	displayMap := make(map[string]string, len(uniqueIDs))
	for i, sID := range uniqueIDs {
		displayMap[sID] = strconv.Itoa(i + 1)
	}
	return displayMap
}

// RenderTelegramHTML formats a structured digest artifact into one or more Unicode-safe HTML chunks.
// All user and LLM content is strictly escaped with html.EscapeString to prevent HTML injection.
// Source references are presented as clickable 1-based numbers ("1, 2") backed by stable digest-wide mapping.
func RenderTelegramHTML(d *digest.Digest, sourceMap map[string]digest.SourceMessage) []string {
	if d == nil {
		return nil
	}

	displayMap := BuildSourceDisplayMap(d)

	escapedTitle := html.EscapeString(strings.TrimSpace(d.Title))
	escapedOverview := html.EscapeString(strings.TrimSpace(d.Overview))

	// 1. Build discrete semantic blocks
	var blocks []string

	// Block 0: Title and Executive Overview
	var headerSB strings.Builder
	if escapedTitle != "" {
		headerSB.WriteString(fmt.Sprintf("<b>%s</b>\n\n", escapedTitle))
	}
	if escapedOverview != "" {
		headerSB.WriteString(escapedOverview)
	}
	if headerSB.Len() > 0 {
		blocks = append(blocks, headerSB.String())
	}

	// Blocks 1..N: Individual insight items
	for _, item := range d.Items {
		var itemSB strings.Builder
		kindLabel := titleCase(item.Kind)
		escapedText := html.EscapeString(strings.TrimSpace(item.Text))
		itemSB.WriteString(fmt.Sprintf("• <b>[%s]</b> %s", kindLabel, escapedText))

		// Render jump links if available with stable user-facing numbers (1, 2)
		type sourceRender struct {
			displayNum int
			html       string
		}
		var renderedSources []sourceRender
		for _, sID := range item.SourceIDs {
			sIDTrim := strings.TrimSpace(sID)
			if sIDTrim == "" {
				continue
			}
			displayLabel := sIDTrim
			displayNum := 0
			if mapped, ok := displayMap[sIDTrim]; ok {
				displayLabel = mapped
				if n, err := strconv.Atoi(mapped); err == nil {
					displayNum = n
				}
			}
			escapedLabel := html.EscapeString(displayLabel)
			var jumpURL string
			if sourceMap != nil {
				if sm, ok := sourceMap[sIDTrim]; ok {
					jumpURL = digest.JumpLink(sm)
				}
			}

			var rendered string
			if jumpURL != "" {
				rendered = fmt.Sprintf("<a href=\"%s\">%s</a>", html.EscapeString(jumpURL), escapedLabel)
			} else {
				rendered = escapedLabel
			}
			renderedSources = append(renderedSources, sourceRender{
				displayNum: displayNum,
				html:       rendered,
			})
		}

		// Sort item sources by display number for stable, clean order (1, 2 rather than 2, 1)
		sort.SliceStable(renderedSources, func(i, j int) bool {
			return renderedSources[i].displayNum < renderedSources[j].displayNum
		})

		var linkParts []string
		for _, rs := range renderedSources {
			linkParts = append(linkParts, rs.html)
		}

		if len(linkParts) > 0 {
			itemSB.WriteString(fmt.Sprintf("\n  <i>Sources: %s</i>", strings.Join(linkParts, ", ")))
		}

		blocks = append(blocks, itemSB.String())
	}

	if len(blocks) == 0 {
		return nil
	}

	// 2. Check if all blocks fit comfortably in a single message
	singleCandidate := strings.Join(blocks, "\n\n")
	if countRunes(singleCandidate) <= MaxTelegramChunkRunes {
		return []string{singleCandidate}
	}

	// 3. Multi-part chunking: group semantic blocks into chunks <= TargetBodyRunes
	var rawChunks []string
	var currentChunk strings.Builder

	for _, block := range blocks {
		// If a single block exceeds TargetBodyRunes by itself, slice it at line/sentence boundaries
		if countRunes(block) > TargetBodyRunes {
			if currentChunk.Len() > 0 {
				rawChunks = append(rawChunks, currentChunk.String())
				currentChunk.Reset()
			}
			subParts := splitLargeBlock(block, TargetBodyRunes)
			for _, sp := range subParts {
				rawChunks = append(rawChunks, sp)
			}
			continue
		}

		potentialLen := countRunes(currentChunk.String())
		if currentChunk.Len() > 0 {
			potentialLen += countRunes("\n\n")
		}
		potentialLen += countRunes(block)

		if potentialLen > TargetBodyRunes && currentChunk.Len() > 0 {
			rawChunks = append(rawChunks, currentChunk.String())
			currentChunk.Reset()
		}

		if currentChunk.Len() > 0 {
			currentChunk.WriteString("\n\n")
		}
		currentChunk.WriteString(block)
	}

	if currentChunk.Len() > 0 {
		rawChunks = append(rawChunks, currentChunk.String())
	}

	// 4. Prefix with clear part numbering: "CordBrief Daily Digest — 1/N"
	totalParts := len(rawChunks)
	if totalParts <= 1 {
		return rawChunks
	}

	finalChunks := make([]string, totalParts)
	for i, chunk := range rawChunks {
		banner := fmt.Sprintf("<b>CordBrief Daily Digest — %d/%d</b>\n\n", i+1, totalParts)
		finalChunks[i] = banner + chunk
	}

	return finalChunks
}

func countRunes(s string) int {
	return len([]rune(s))
}

// splitLargeBlock splits an unusually oversized block along line or sentence boundaries.
func splitLargeBlock(block string, maxRunes int) []string {
	var parts []string
	lines := strings.Split(block, "\n")
	var current strings.Builder

	for _, line := range lines {
		if countRunes(current.String())+countRunes(line)+1 > maxRunes && current.Len() > 0 {
			parts = append(parts, current.String())
			current.Reset()
		}
		if countRunes(line) > maxRunes {
			// Sub-split by sentence or hard runes
			runes := []rune(line)
			for len(runes) > maxRunes {
				parts = append(parts, string(runes[:maxRunes]))
				runes = runes[maxRunes:]
			}
			if len(runes) > 0 {
				if current.Len() > 0 {
					current.WriteString("\n")
				}
				current.WriteString(string(runes))
			}
			continue
		}
		if current.Len() > 0 {
			current.WriteString("\n")
		}
		current.WriteString(line)
	}

	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}

// titleCase capitalizes the first ASCII letter of s while lowercase-ing the rest.
func titleCase(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	r := []rune(strings.ToLower(s))
	if len(r) > 0 && r[0] >= 'a' && r[0] <= 'z' {
		r[0] = r[0] - 'a' + 'A'
	}
	return string(r)
}

