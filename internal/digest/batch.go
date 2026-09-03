package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"cordbrief/internal/journal"
)

// BuildBatch constructs a deterministic DigestBatch from raw journal records.
func BuildBatch(records []journal.Record, startCur, endCur journal.Cursor, wm *journal.Watermark, ignoreBots bool) (*Batch, error) {
	if wm == nil {
		wm = &journal.Watermark{}
	}

	// Sort records deterministically: chronological first, then segment/offset tie-break
	sortedRecords := make([]journal.Record, len(records))
	copy(sortedRecords, records)
	sort.SliceStable(sortedRecords, func(i, j int) bool {
		ti, tj := sortedRecords[i].Event.Timestamp, sortedRecords[j].Event.Timestamp
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		if sortedRecords[i].Segment != sortedRecords[j].Segment {
			return sortedRecords[i].Segment < sortedRecords[j].Segment
		}
		return sortedRecords[i].Offset < sortedRecords[j].Offset
	})

	// Pre-pass: map discord message IDs to local source IDs
	msgIDToSourceID := make(map[string]string)
	var included []SourceMessage
	sourceMap := make(map[string]SourceMessage)

	for _, rec := range sortedRecords {
		ev := rec.Event
		if ev.Event != "message_create" {
			continue
		}
		if ignoreBots && ev.Author.Bot {
			continue
		}

		sourceID := fmt.Sprintf("S%06d", len(included)+1)
		msgIDToSourceID[ev.MessageID] = sourceID

		var replyLocalID, replyExtID string
		if ev.ReplyToMessageID != nil && strings.TrimSpace(*ev.ReplyToMessageID) != "" {
			targetMsgID := strings.TrimSpace(*ev.ReplyToMessageID)
			if local, found := msgIDToSourceID[targetMsgID]; found {
				replyLocalID = local
			} else {
				replyExtID = targetMsgID
			}
		}

		sm := SourceMessage{
			SourceID:        sourceID,
			GuildID:         ev.GuildID,
			ChannelID:       ev.ChannelID,
			MessageID:       ev.MessageID,
			Timestamp:       ev.Timestamp,
			AuthorName:      ev.Author.Name,
			AuthorDisplay:   ev.Author.DisplayName,
			AuthorBot:       ev.Author.Bot,
			Content:         ev.Content,
			ReplyToSourceID: replyLocalID,
			ExternalReplyID: replyExtID,
			Attachments:     ev.Attachments,
		}

		included = append(included, sm)
		sourceMap[sourceID] = sm
	}

	batchID := computeDeterministicBatchID(journal.CurrentSchemaVersion, startCur, endCur, included)

	return &Batch{
		Version:             journal.CurrentSchemaVersion,
		BatchID:             batchID,
		StartCursor:         startCur,
		EndCursor:           endCur,
		Watermark:           *wm,
		TotalJournalRecords: len(records),
		IncludedMessages:    included,
		SourceMap:           sourceMap,
	}, nil
}

func computeDeterministicBatchID(version int, start, end journal.Cursor, msgs []SourceMessage) string {
	h := sha256.New()
	fmt.Fprintf(h, "v:%d|start:%d:%d|end:%d:%d|count:%d\n",
		version, start.Segment, start.Offset, end.Segment, end.Offset, len(msgs))

	for _, m := range msgs {
		fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s\n",
			m.SourceID, m.GuildID, m.ChannelID, m.MessageID, m.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z"), m.Content)
	}

	return hex.EncodeToString(h.Sum(nil))
}
