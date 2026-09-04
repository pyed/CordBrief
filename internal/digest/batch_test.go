package digest

import (
	"testing"
	"time"

	"cordbrief/internal/catalog"
	"cordbrief/internal/journal"
)

func TestBatch_DeterministicIDAndSourceIDs(t *testing.T) {
	t1 := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 3, 12, 1, 0, 0, time.UTC)

	replyTarget := "msg-1"
	records := []journal.Record{
		{
			Segment: 1,
			Offset:  0,
			Event: journal.Event{
				Version:   1,
				Event:     "message_create",
				MessageID: "msg-1",
				GuildID:   "g1",
				ChannelID: "ch-1",
				Timestamp: t1,
				Author:    journal.Author{ID: "u1", Name: "alice", DisplayName: "Alice", Bot: false},
				Content:   "First message",
			},
		},
		{
			Segment: 1,
			Offset:  200,
			Event: journal.Event{
				Version:          1,
				Event:            "message_create",
				MessageID:        "msg-2",
				GuildID:          "g1",
				ChannelID:        "ch-1",
				Timestamp:        t2,
				Author:           journal.Author{ID: "u2", Name: "bob", DisplayName: "Bob", Bot: false},
				Content:          "Replying to first message",
				ReplyToMessageID: &replyTarget,
			},
		},
	}

	start := journal.Cursor{Segment: 1, Offset: 0}
	end := journal.Cursor{Segment: 1, Offset: 400}
	wm := &journal.Watermark{MaxSegment: 1}

	b1, err := BuildBatch(records, start, end, wm, true)
	if err != nil {
		t.Fatalf("BuildBatch failed: %v", err)
	}

	b2, err := BuildBatch(records, start, end, wm, true)
	if err != nil {
		t.Fatalf("BuildBatch failed: %v", err)
	}

	if b1.BatchID != b2.BatchID {
		t.Errorf("expected deterministic batch IDs, got %s vs %s", b1.BatchID, b2.BatchID)
	}

	if len(b1.IncludedMessages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(b1.IncludedMessages))
	}

	if b1.IncludedMessages[0].SourceID != "S000001" {
		t.Errorf("expected S000001, got %s", b1.IncludedMessages[0].SourceID)
	}
	if b1.IncludedMessages[1].SourceID != "S000002" {
		t.Errorf("expected S000002, got %s", b1.IncludedMessages[1].SourceID)
	}

	// Verify reply linking
	if b1.IncludedMessages[1].ReplyToSourceID != "S000001" {
		t.Errorf("expected reply_to_source_id S000001, got %s", b1.IncludedMessages[1].ReplyToSourceID)
	}
}

func TestBatch_BotFiltering(t *testing.T) {
	t1 := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	records := []journal.Record{
		{
			Segment: 1,
			Offset:  0,
			Event: journal.Event{
				Version:   1,
				Event:     "message_create",
				MessageID: "msg-bot",
				GuildID:   "g1",
				ChannelID: "ch-1",
				Timestamp: t1,
				Author:    journal.Author{ID: "bot1", Name: "bot", Bot: true},
				Content:   "Automated ping",
			},
		},
		{
			Segment: 1,
			Offset:  200,
			Event: journal.Event{
				Version:   1,
				Event:     "message_create",
				MessageID: "msg-user",
				GuildID:   "g1",
				ChannelID: "ch-1",
				Timestamp: t1.Add(time.Second),
				Author:    journal.Author{ID: "u1", Name: "alice", Bot: false},
				Content:   "Human message",
			},
		},
	}

	start := journal.Cursor{Segment: 1, Offset: 0}
	end := journal.Cursor{Segment: 1, Offset: 400}

	// Filter bots = true
	bFiltered, err := BuildBatch(records, start, end, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(bFiltered.IncludedMessages) != 1 || bFiltered.IncludedMessages[0].MessageID != "msg-user" {
		t.Errorf("expected 1 human message, got %+v", bFiltered.IncludedMessages)
	}

	// Filter bots = false
	bAll, err := BuildBatch(records, start, end, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(bAll.IncludedMessages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(bAll.IncludedMessages))
	}
}

func TestBatch_CatalogGuildEnrichment(t *testing.T) {
	t1 := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	records := []journal.Record{
		{
			Segment: 1,
			Offset:  0,
			Event: journal.Event{
				Version:   1,
				Event:     "message_create",
				MessageID: "msg-no-guild",
				GuildID:   "", // Empty guild ID
				ChannelID: "ch-catalog",
				Timestamp: t1,
				Author:    journal.Author{ID: "u1", Name: "alice", Bot: false},
				Content:   "Recovered message with empty guild_id",
			},
		},
	}

	cat := &catalog.Catalog{
		Version: 1,
		Guilds: []catalog.Guild{
			{
				ID: "g-from-catalog",
				Channels: []catalog.Channel{
					{ID: "ch-catalog", Name: "test-channel"},
				},
			},
		},
	}

	start := journal.Cursor{Segment: 1, Offset: 0}
	end := journal.Cursor{Segment: 1, Offset: 100}

	// With catalog enrichment
	batch, err := BuildBatch(records, start, end, nil, false, cat)
	if err != nil {
		t.Fatalf("BuildBatch failed: %v", err)
	}

	if len(batch.IncludedMessages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(batch.IncludedMessages))
	}
	sm := batch.IncludedMessages[0]
	if sm.GuildID != "g-from-catalog" {
		t.Errorf("expected enriched guild_id 'g-from-catalog', got %q", sm.GuildID)
	}

	// Verify JumpLink
	link := JumpLink(sm)
	expectedLink := "https://discord.com/channels/g-from-catalog/ch-catalog/msg-no-guild"
	if link != expectedLink {
		t.Errorf("expected JumpLink %q, got %q", expectedLink, link)
	}

	// Without catalog (nil)
	batchNoCat, err := BuildBatch(records, start, end, nil, false)
	if err != nil {
		t.Fatalf("BuildBatch failed: %v", err)
	}
	if batchNoCat.IncludedMessages[0].GuildID != "" {
		t.Errorf("expected empty guild_id without catalog, got %q", batchNoCat.IncludedMessages[0].GuildID)
	}
	if JumpLink(batchNoCat.IncludedMessages[0]) != "" {
		t.Errorf("expected empty JumpLink when guild_id is empty, got %q", JumpLink(batchNoCat.IncludedMessages[0]))
	}
}

