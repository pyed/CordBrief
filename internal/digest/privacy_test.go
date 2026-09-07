package digest

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"cordbrief/internal/journal"
)

func TestArtifactPrivacyProperty(t *testing.T) {
	tmpDir := t.TempDir()

	// Construct realistic SourceMessages with sensitive content, tokens, authors, attachments
	sensitiveToken := "MTAyOTM4NDc1Ni41NjQ3Mzg=.sensitive_discord_token"
	sensitiveBody := "Confidential discussion about proprietary project and secret credentials"
	authorName := "SuperSecretUser#1234"
	promptExcerpt := "Please summarize the following top-secret conversation"

	sourceMap := map[string]SourceMessage{
		"S000001": {
			SourceID:      "S000001",
			GuildID:       "111222333444",
			ChannelID:     "555666777888",
			MessageID:     "999000111222",
			Timestamp:     time.Now().UTC(),
			AuthorName:    authorName,
			AuthorDisplay: "Alice Secret",
			Content:       sensitiveBody + " token: " + sensitiveToken,
			Attachments: []journal.Attachment{
				{ID: "123", Filename: "private_photo.png", ContentType: "image/png", Size: 1024},
			},
		},
		"S000002": {
			SourceID:      "S000002",
			GuildID:       "111222333444",
			ChannelID:     "555666777888",
			MessageID:     "999000111223",
			Timestamp:     time.Now().UTC(),
			AuthorName:    "BobSecret",
			AuthorDisplay: "Bob Internal",
			Content:       "Another message with internal session cookie: session=abcdef123456",
		},
	}

	d := &Digest{
		Title:    "Public Summary",
		Overview: "This is a clean public overview",
		Items: []Item{
			{
				Kind:      KindImportant,
				Text:      "Discussion regarding coffee extraction ratio.",
				SourceIDs: []string{"S000001", "S000002"},
			},
		},
	}

	refs := BuildSourceRefs(d, sourceMap)
	if len(refs) != 2 {
		t.Fatalf("expected 2 source refs, got %d", len(refs))
	}

	// Verify in-memory values
	ref1 := refs["S000001"]
	if ref1.GuildID != "111222333444" || ref1.ChannelID != "555666777888" || ref1.MessageID != "999000111222" {
		t.Fatalf("unexpected ref1 fields: %+v", ref1)
	}
	expectedLink := "https://discord.com/channels/111222333444/555666777888/999000111222"
	if ref1.JumpLink() != expectedLink {
		t.Fatalf("unexpected JumpLink: got %s, want %s", ref1.JumpLink(), expectedLink)
	}

	batchID := "1111111111111111111111111111111111111111111111111111111111111111"
	art := &Artifact{
		Version:              journal.CurrentSchemaVersion,
		BatchID:              batchID,
		CreatedAt:            time.Now().UTC(),
		CursorStart:          journal.Cursor{Version: 1, Segment: 1, Offset: 100},
		CursorEnd:            journal.Cursor{Version: 1, Segment: 1, Offset: 500},
		InputMessageCount:    2,
		IncludedMessageCount: 2,
		Provider:             "gemini",
		Model:                "gemini-3.7-flash",
		SourceRefs:           refs,
		Digest:               d,
	}

	if err := SaveArtifact(tmpDir, art); err != nil {
		t.Fatalf("SaveArtifact failed: %v", err)
	}

	artPath, err := ArtifactPath(tmpDir, batchID)
	if err != nil {
		t.Fatalf("ArtifactPath failed: %v", err)
	}

	rawBytes, err := os.ReadFile(artPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	// 1. Unmarshal into generic map and strictly inspect source_refs keys
	var rawJSON map[string]any
	if err := json.Unmarshal(rawBytes, &rawJSON); err != nil {
		t.Fatalf("json unmarshal failed: %v", err)
	}

	rawRefs, ok := rawJSON["source_refs"].(map[string]any)
	if !ok {
		t.Fatalf("expected source_refs in JSON, got: %v", rawJSON["source_refs"])
	}

	for sID, v := range rawRefs {
		fields, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("expected source_ref %s to be map, got: %T", sID, v)
		}
		// Must ONLY contain guild_id, channel_id, message_id
		for k := range fields {
			if k != "guild_id" && k != "channel_id" && k != "message_id" {
				t.Errorf("forbidden field %q found in source_refs[%s]", k, sID)
			}
		}
	}

	// 2. Scan entire raw serialized JSON file for forbidden content
	forbiddenStrings := []string{
		sensitiveToken,
		"Confidential discussion",
		"secret credentials",
		authorName,
		"Alice Secret",
		"BobSecret",
		"Bob Internal",
		"session=abcdef123456",
		"private_photo.png",
		promptExcerpt,
	}

	for _, forbidden := range forbiddenStrings {
		if strings.Contains(string(rawBytes), forbidden) {
			t.Errorf("PRIVACY VIOLATION: raw artifact JSON contains forbidden string %q", forbidden)
		}
	}
}
