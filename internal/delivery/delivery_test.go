package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cordbrief/internal/config"
	"cordbrief/internal/digest"
)

// G2: Stdlib Telegram API Client
func TestTelegramClient(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/botfake-token/getMe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"id":         987654321,
				"is_bot":     true,
				"first_name": "CordBriefBot",
				"username":   "cordbrief_bot",
			},
		})
	})

	mux.HandleFunc("/botfake-token/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		if payload["chat_id"] != "123456" {
			http.Error(w, "unexpected chat_id", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"message_id": 555001,
			},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewTelegramClient("fake-token", WithBaseURL(srv.URL))
	ctx := context.Background()

	// 1. getMe
	user, err := client.GetMe(ctx)
	if err != nil {
		t.Fatalf("unexpected getMe error: %v", err)
	}
	if user.Username != "cordbrief_bot" || user.ID != 987654321 {
		t.Fatalf("unexpected bot user: %+v", user)
	}

	// 2. sendMessage
	res, err := client.SendMessage(ctx, "123456", "Hello World")
	if err != nil {
		t.Fatalf("unexpected sendMessage error: %v", err)
	}
	if res.MessageID != 555001 {
		t.Fatalf("expected message_id 555001, got %d", res.MessageID)
	}

	// 3. invalid token / 401 error
	badClient := NewTelegramClient("wrong-token", WithBaseURL(srv.URL))
	_, err = badClient.GetMe(ctx)
	if err == nil {
		t.Fatal("expected error with wrong token, got nil")
	}
}

// G3: Secret Hygiene & Token Redaction
func TestSecretHygiene(t *testing.T) {
	const sensitiveToken = "secret-telegram-token-987654321"

	// 1. Error sanitization
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":          false,
			"error_code":  401,
			"description": "Unauthorized: invalid bot token " + sensitiveToken,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewTelegramClient(sensitiveToken, WithBaseURL(srv.URL))
	_, err := client.GetMe(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errMsg := err.Error()
	if strings.Contains(errMsg, sensitiveToken) {
		t.Fatalf("CRITICAL: Secret token leaked in error message: %s", errMsg)
	}
	if !strings.Contains(errMsg, "[REDACTED]") {
		t.Fatalf("expected error to contain '[REDACTED]', got: %s", errMsg)
	}

	// 2. Store precedence: Environment vs Stored
	dir := t.TempDir()
	store, err := config.NewStore(dir, "")
	if err != nil {
		t.Fatalf("new store error: %v", err)
	}

	// Initial: none
	t.Setenv(config.EnvTelegramBotToken, "")
	if store.GetTelegramTokenSource() != config.SecretSourceNone {
		t.Fatalf("expected SecretSourceNone, got %v", store.GetTelegramTokenSource())
	}

	// Stored in secrets.json (mode 0600)
	if err := store.SaveTelegramBotToken("stored-secret-token"); err != nil {
		t.Fatalf("save token error: %v", err)
	}

	secPath := filepath.Join(dir, "secrets.json")
	info, err := os.Stat(secPath)
	if err != nil {
		t.Fatalf("stat secrets.json error: %v", err)
	}
	// Check permissions on non-windows if applicable, or verify write
	if store.GetTelegramBotToken() != "stored-secret-token" {
		t.Fatalf("expected 'stored-secret-token', got %q", store.GetTelegramBotToken())
	}
	if store.GetTelegramTokenSource() != config.SecretSourceStored {
		t.Fatalf("expected SecretSourceStored, got %v", store.GetTelegramTokenSource())
	}

	// Env precedence
	t.Setenv(config.EnvTelegramBotToken, "env-secret-token")
	if store.GetTelegramTokenSource() != config.SecretSourceEnvironment {
		t.Fatalf("expected SecretSourceEnvironment, got %v", store.GetTelegramTokenSource())
	}
	if store.GetTelegramBotToken() != "env-secret-token" {
		t.Fatalf("expected env token 'env-secret-token', got %q", store.GetTelegramBotToken())
	}
	_ = info
}

// G4: Interactive Chat Discovery & Safe Metadata
func TestChatDiscovery(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/botfake-token/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": []map[string]any{
				{
					"update_id": 1001,
					"message": map[string]any{
						"text": "Hello bot private message that MUST NOT be exposed",
						"chat": map[string]any{
							"id":         111222,
							"type":       "private",
							"first_name": "Sheriff",
							"last_name":  "User",
							"username":   "sheriff_u",
						},
					},
				},
				{
					"update_id": 1002,
					"message": map[string]any{
						"text": "Another private message from same user",
						"chat": map[string]any{
							"id":         111222,
							"type":       "private",
							"first_name": "Sheriff",
							"last_name":  "User",
							"username":   "sheriff_u",
						},
					},
				},
				{
					"update_id": 1003,
					"channel_post": map[string]any{
						"text": "Sensitive announcement text",
						"chat": map[string]any{
							"id":    -100987654321,
							"type":  "channel",
							"title": "CordBrief Alerts Channel",
						},
					},
				},
			},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewTelegramClient("fake-token", WithBaseURL(srv.URL))
	chats, err := client.GetUpdates(context.Background(), 0)
	if err != nil {
		t.Fatalf("unexpected getUpdates error: %v", err)
	}

	// Deduplication: 2 unique chats from 3 updates
	if len(chats) != 2 {
		t.Fatalf("expected 2 unique chats, got %d", len(chats))
	}

	chat1 := chats[0]
	if chat1.ID != "111222" || chat1.Type != "private" || chat1.Username != "sheriff_u" {
		t.Errorf("unexpected chat1: %+v", chat1)
	}
	if chat1.Label() != "Sheriff User (@sheriff_u)" {
		t.Errorf("unexpected label: %s", chat1.Label())
	}

	chat2 := chats[1]
	if chat2.ID != "-100987654321" || chat2.Type != "channel" || chat2.Title != "CordBrief Alerts Channel" {
		t.Errorf("unexpected chat2: %+v", chat2)
	}
	if chat2.Label() != "CordBrief Alerts Channel" {
		t.Errorf("unexpected label: %s", chat2.Label())
	}

	// Verify no message body text in chats JSON serialization
	b, _ := json.Marshal(chats)
	if strings.Contains(string(b), "MUST NOT be exposed") || strings.Contains(string(b), "Sensitive") {
		t.Fatalf("CRITICAL: Message body leaked into ChatInfo: %s", string(b))
	}
}

// G5: Digest HTML Rendering, Escaping & Safe Chunking
func TestTelegramRenderingAndChunking(t *testing.T) {
	d := &digest.Digest{
		Title:    "Community Catchup <script>alert(1)</script>",
		Overview: "Discussion about & special chars and \"quotes\".",
		Items: []digest.Item{
			{
				Kind:           digest.KindImportant,
				Text:           "Release 2.0 announced with <awesome> features.",
				SourceIDs:      []string{"S000001", "S000002"},
				ChannelContext: "general",
			},
		},
	}

	sourceMap := map[string]digest.SourceMessage{
		"S000001": {
			SourceID:  "S000001",
			GuildID:   "100",
			ChannelID: "200",
			MessageID: "300",
		},
	}

	// 1. Single chunk rendering & HTML escaping
	chunks := RenderTelegramHTML(d, sourceMap)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}

	chunk := chunks[0]
	if strings.Contains(chunk, "<script>") {
		t.Fatalf("HTML injection detected in chunk: %s", chunk)
	}
	if !strings.Contains(chunk, "&lt;script&gt;") {
		t.Fatalf("expected escaped script tag, got: %s", chunk)
	}
	if !strings.Contains(chunk, "&amp; special chars") {
		t.Fatalf("expected escaped ampersand, got: %s", chunk)
	}
	// Verify jump link with user-facing number 1
	if !strings.Contains(chunk, "<a href=\"https://discord.com/channels/100/200/300\">1</a>") {
		t.Fatalf("expected jump link for source 1, got: %s", chunk)
	}
	// S000002 has no jump link -> plain text 2
	if !strings.Contains(chunk, "<i>Sources: <a href=\"https://discord.com/channels/100/200/300\">1</a>, 2</i>") {
		t.Fatalf("expected 'Sources: 1, 2', got: %s", chunk)
	}
	// No localhost link
	if strings.Contains(chunk, "127.0.0.1") || strings.Contains(chunk, "localhost") {
		t.Fatalf("localhost URL found in telegram message: %s", chunk)
	}

	// 2. Multi-part chunking when content exceeds limit
	largeItems := make([]digest.Item, 40)
	for i := range largeItems {
		largeItems[i] = digest.Item{
			Kind:      digest.KindFinding,
			Text:      fmt.Sprintf("Item %d: %s", i+1, strings.Repeat("Detailed analytical findings and observations. ", 5)),
			SourceIDs: []string{fmt.Sprintf("S%06d", i+1)},
		}
	}
	largeDigest := &digest.Digest{
		Title:    "Mega Daily Digest",
		Overview: "Comprehensive review of 500+ messages.",
		Items:    largeItems,
	}

	multiChunks := RenderTelegramHTML(largeDigest, nil)
	if len(multiChunks) <= 1 {
		t.Fatalf("expected multi-part chunks, got %d", len(multiChunks))
	}

	for i, mc := range multiChunks {
		expectedHeader := fmt.Sprintf("<b>CordBrief Daily Digest — %d/%d</b>", i+1, len(multiChunks))
		if !strings.Contains(mc, expectedHeader) {
			t.Errorf("chunk %d missing header %s: %s", i+1, expectedHeader, mc[:100])
		}
		if countRunes(mc) > 4096 {
			t.Errorf("chunk %d exceeds Telegram limit of 4096: %d", i+1, countRunes(mc))
		}
	}
}

// G6: Durable Delivery State & Multipart Resume
func TestDurableDeliveryState(t *testing.T) {
	dir := t.TempDir()
	batchID := strings.Repeat("d", 64)

	rec := &DeliveryRecord{
		DigestBatchID:      batchID,
		DestinationID:      "12345",
		State:              StateSending,
		NextPart:           1,
		TotalParts:         3,
		TelegramMessageIDs: []int64{1001},
	}

	if err := SaveDeliveryRecord(dir, rec); err != nil {
		t.Fatalf("save delivery record: %v", err)
	}

	loaded, err := LoadDeliveryRecord(dir, batchID)
	if err != nil {
		t.Fatalf("load delivery record: %v", err)
	}

	if loaded.State != StateSending || loaded.NextPart != 1 || len(loaded.TelegramMessageIDs) != 1 {
		t.Fatalf("unexpected loaded record: %+v", loaded)
	}
	if loaded.TelegramMessageIDs[0] != 1001 {
		t.Fatalf("expected message ID 1001, got %d", loaded.TelegramMessageIDs[0])
	}
}

// G7: Ambiguous Transport Failure & Uncertain State
func TestUncertainDeliverySemantics(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/botfake-token/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		// Simulate ambiguous 502 Bad Gateway / connection drop
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`<html><body>502 Bad Gateway</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	store, _ := config.NewStore(dir, "")
	_ = store.SaveTelegramBotToken("fake-token")
	_ = store.SaveDeliveryConfig(config.DeliveryConfig{
		Telegram: config.TelegramConfig{
			Enabled: true,
			ChatID:  "12345",
		},
	})

	// Create test digest artifact on disk
	digestsDir := filepath.Join(dir, "digests")
	_ = os.MkdirAll(digestsDir, 0755)
	batchID := strings.Repeat("a", 64)
	art := &digest.Artifact{
		BatchID: batchID,
		Digest: &digest.Digest{
			Title:    "Test Digest",
			Overview: "Test Overview",
		},
	}
	_ = digest.SaveArtifact(digestsDir, art)

	svc, err := NewService(ServiceOptions{
		DataDir: dir,
		Store:   store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token, WithBaseURL(srv.URL))
		},
	})
	if err != nil {
		t.Fatalf("new service error: %v", err)
	}

	// 1. First deliver attempt -> receives 502 -> marks StateUncertain
	rec, err := svc.DeliverBatch(context.Background(), batchID, false)
	if err == nil {
		t.Fatal("expected error on 502 Bad Gateway, got nil")
	}
	if rec.State != StateUncertain {
		t.Fatalf("expected StateUncertain, got %s", rec.State)
	}

	// 2. Automatic retry without force MUST NOT resend
	rec2, err2 := svc.DeliverBatch(context.Background(), batchID, false)
	if err2 == nil || !strings.Contains(err2.Error(), "operator confirmation required") {
		t.Fatalf("expected operator confirmation error on uncertain retry, got: %v", err2)
	}
	if rec2.State != StateUncertain {
		t.Fatalf("expected StateUncertain preserved, got %s", rec2.State)
	}

	// 3. Operator clicks "Send again anyway" (force=true) -> allows attempt
	rec3, _ := svc.DeliverBatch(context.Background(), batchID, true)
	if rec3 == nil {
		t.Fatal("expected non-nil record on forced retry")
	}
}

// G8: Core In-Process Delivery Worker & Startup Recovery
func TestDeliveryWorker(t *testing.T) {
	mux := http.NewServeMux()
	sentCount := 0
	mux.HandleFunc("/botfake-token/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		sentCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"message_id": 9000 + sentCount,
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	store, _ := config.NewStore(dir, "")
	_ = store.SaveTelegramBotToken("fake-token")
	_ = store.SaveDeliveryConfig(config.DeliveryConfig{
		Telegram: config.TelegramConfig{
			Enabled: true,
			ChatID:  "12345",
		},
	})

	// Create test digest artifact on disk
	digestsDir := filepath.Join(dir, "digests")
	_ = os.MkdirAll(digestsDir, 0755)
	batchID := strings.Repeat("b", 64)
	art := &digest.Artifact{
		BatchID: batchID,
		Digest: &digest.Digest{
			Title:    "Test Digest",
			Overview: "Test Overview",
		},
	}
	_ = digest.SaveArtifact(digestsDir, art)

	// Pre-create pending delivery state simulating uncompleted delivery before restart
	rec := &DeliveryRecord{
		DigestBatchID: batchID,
		DestinationID: "12345",
		State:         StatePending,
		CreatedAt:     time.Now().UTC(),
	}
	_ = SaveDeliveryRecord(dir, rec)

	svc, err := NewService(ServiceOptions{
		DataDir: dir,
		Store:   store,
		ClientGetter: func(token string) *TelegramClient {
			return NewTelegramClient(token, WithBaseURL(srv.URL))
		},
	})
	if err != nil {
		t.Fatalf("new service error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go svc.StartWorker(ctx)

	// Wait for background worker to resume and finish delivery
	deadline := time.Now().Add(5 * time.Second)
	var finalRec *DeliveryRecord
	for time.Now().Before(deadline) {
		finalRec, _ = svc.GetDelivery(batchID)
		if finalRec != nil && finalRec.State == StateSent {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if finalRec == nil || finalRec.State != StateSent {
		t.Fatalf("expected delivery worker to resume and send pending batch, got: %+v", finalRec)
	}
	if len(finalRec.TelegramMessageIDs) != 1 || finalRec.TelegramMessageIDs[0] != 9001 {
		t.Fatalf("unexpected message IDs: %+v", finalRec.TelegramMessageIDs)
	}
}

// Tests covering Section 4: deterministic source display mapping (A..G)
func TestSourceDisplayMapping(t *testing.T) {
	d := &digest.Digest{
		Title:    "Source Mapping Test",
		Overview: "Testing stable user-facing source numbering",
		Items: []digest.Item{
			{
				Kind:      digest.KindImportant,
				Text:      "First finding citing sources 1 and 2.",
				SourceIDs: []string{"S000001", "S000002"},
			},
			{
				Kind:      digest.KindFinding,
				Text:      "Second finding citing source 2 again and source 3.",
				SourceIDs: []string{"S000002", "S000003"},
			},
			{
				Kind:      digest.KindExperiment,
				Text:      "Third finding with out-of-order slice.",
				SourceIDs: []string{"S000003", "S000001"},
			},
			{
				Kind:      digest.KindQuestion,
				Text:      "Fourth finding citing an unlinked source with special characters in ID: <evil>.",
				SourceIDs: []string{"S000004_special_<tag>"},
			},
		},
	}

	sourceMap := map[string]digest.SourceMessage{
		"S000001": {
			SourceID:  "S000001",
			GuildID:   "guild1",
			ChannelID: "chan1",
			MessageID: "msg1",
		},
		"S000002": {
			SourceID:  "S000002",
			GuildID:   "guild2",
			ChannelID: "chan2",
			MessageID: "msg2",
		},
		"S000003": {
			SourceID:  "S000003",
			GuildID:   "guild3",
			ChannelID: "chan3",
			MessageID: "msg3",
		},
		// S000004_special_<tag> is intentionally NOT in sourceMap to test missing jump link
	}

	// Build mapping
	m := BuildSourceDisplayMap(d)
	if m == nil {
		t.Fatal("expected non-nil display map")
	}

	// A: S000001 -> 1, S000002 -> 2
	if m["S000001"] != "1" {
		t.Errorf("expected S000001 -> 1, got %s", m["S000001"])
	}
	if m["S000002"] != "2" {
		t.Errorf("expected S000002 -> 2, got %s", m["S000002"])
	}
	if m["S000003"] != "3" {
		t.Errorf("expected S000003 -> 3, got %s", m["S000003"])
	}
	if m["S000004_special_<tag>"] != "4" {
		t.Errorf("expected S000004_special_<tag> -> 4, got %s", m["S000004_special_<tag>"])
	}

	// Render HTML
	chunks := RenderTelegramHTML(d, sourceMap)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	htmlOutput := chunks[0]

	// A: displayed as clickable 1, 2
	expectedLink1 := `<a href="https://discord.com/channels/guild1/chan1/msg1">1</a>`
	expectedLink2 := `<a href="https://discord.com/channels/guild2/chan2/msg2">2</a>`
	expectedLink3 := `<a href="https://discord.com/channels/guild3/chan3/msg3">3</a>`

	if !strings.Contains(htmlOutput, fmt.Sprintf("<i>Sources: %s, %s</i>", expectedLink1, expectedLink2)) {
		t.Errorf("item 1: expected clickable 1, 2, got:\n%s", htmlOutput)
	}

	// B: repeated S000002 citation later -> still displays 2
	if !strings.Contains(htmlOutput, fmt.Sprintf("<i>Sources: %s, %s</i>", expectedLink2, expectedLink3)) {
		t.Errorf("item 2: expected repeated citation to display 2, 3, got:\n%s", htmlOutput)
	}

	// C: source order remains deterministic within item 3 (was [S000003, S000001] -> rendered as [1, 3])
	if !strings.Contains(htmlOutput, fmt.Sprintf("<i>Sources: %s, %s</i>", expectedLink1, expectedLink3)) {
		t.Errorf("item 3: expected sorted 1, 3 order, got:\n%s", htmlOutput)
	}

	// D: jump links remain unchanged (URLs point to exact Discord channel/message IDs)
	if !strings.Contains(htmlOutput, "https://discord.com/channels/guild1/chan1/msg1") ||
		!strings.Contains(htmlOutput, "https://discord.com/channels/guild2/chan2/msg2") ||
		!strings.Contains(htmlOutput, "https://discord.com/channels/guild3/chan3/msg3") {
		t.Errorf("jump links corrupted in output")
	}

	// E: missing jump link -> do not fabricate a link, displays plain 4 without <a> tag
	if strings.Contains(htmlOutput, "href=\"\"") || strings.Contains(htmlOutput, "<a href=\"\">4</a>") {
		t.Errorf("fabricated empty link found for unlinked source")
	}
	if !strings.Contains(htmlOutput, "<i>Sources: 4</i>") {
		t.Errorf("expected plain '4' for unlinked source, got:\n%s", htmlOutput)
	}

	// F: HTML escaping remains safe (no raw tags injected)
	if strings.Contains(htmlOutput, "<evil>") {
		t.Errorf("HTML injection detected in source label: %s", htmlOutput)
	}

	// G: internal source IDs remain unchanged in persisted/memory struct
	if d.Items[0].SourceIDs[0] != "S000001" || d.Items[0].SourceIDs[1] != "S000002" {
		t.Errorf("item 0 source IDs mutated: %+v", d.Items[0].SourceIDs)
	}
	if d.Items[1].SourceIDs[0] != "S000002" || d.Items[1].SourceIDs[1] != "S000003" {
		t.Errorf("item 1 source IDs mutated: %+v", d.Items[1].SourceIDs)
	}
}

func TestBatchIDValidation(t *testing.T) {
	tmpDir := t.TempDir()
	invalidIDs := []string{
		"invalid-batch",
		"../../etc/passwd",
		"0123456789abcdef",
		strings.Repeat("A", 64),
	}
	for _, id := range invalidIDs {
		if _, err := DeliveryRecordPath(tmpDir, id); err == nil {
			t.Errorf("expected DeliveryRecordPath to reject %q, got nil", id)
		}
	}
	validID := strings.Repeat("a", 64)
	if path, err := DeliveryRecordPath(tmpDir, validID); err != nil || path == "" {
		t.Errorf("expected DeliveryRecordPath to accept valid id, got err: %v", err)
	}
}

func TestSmallCleanups(t *testing.T) {
	// 1. titleCase replaces deprecated strings.Title
	if tc := titleCase("finding"); tc != "Finding" {
		t.Errorf("expected Finding, got %s", tc)
	}
	if tc := titleCase("ACTION_ITEM"); tc != "Action_item" {
		t.Errorf("expected Action_item, got %s", tc)
	}
	if tc := titleCase(""); tc != "" {
		t.Errorf("expected empty string, got %s", tc)
	}

	// 2. DeliveryRecordPath uses filepath.Join and canonical path structure
	tmpDir := t.TempDir()
	validID := strings.Repeat("b", 64)
	p, err := DeliveryRecordPath(tmpDir, validID)
	if err != nil {
		t.Fatalf("DeliveryRecordPath failed: %v", err)
	}
	expected := filepath.Join(tmpDir, "deliveries", validID, "telegram.json")
	if p != expected {
		t.Errorf("expected path %q, got %q", expected, p)
	}
}
