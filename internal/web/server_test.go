package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cordbrief/internal/config"
	"cordbrief/internal/llm"
)

func setupTestEnv(t *testing.T) (*Server, string, string) {
	t.Helper()
	tmpDir := t.TempDir()
	exchangeDir := filepath.Join(tmpDir, "exchange")
	eventsDir := filepath.Join(exchangeDir, "events")
	dataDir := filepath.Join(tmpDir, "data")
	_ = os.MkdirAll(eventsDir, 0755)
	_ = os.MkdirAll(dataDir, 0755)

	// Create sample catalog.json
	catalogJSON := `{
  "version": 1,
  "updated_at": "2026-09-04T00:00:00Z",
  "guilds": [
    {
      "id": "g1",
      "name": "Test Server",
      "channels": [
        {"id": "c1", "name": "general", "type": 0},
        {"id": "c2", "name": "chat", "type": 0}
      ]
    }
  ]
}`
	_ = os.WriteFile(filepath.Join(exchangeDir, "catalog.json"), []byte(catalogJSON), 0644)

	// Create initial watchlist.json
	wlJSON := `{"version": 1, "generation": 1, "channel_ids": ["c1"]}`
	_ = os.WriteFile(filepath.Join(exchangeDir, "watchlist.json"), []byte(wlJSON), 0644)

	// Create store
	store, err := config.NewStore(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}

	server, err := NewServer(ServerOptions{
		ExchangeDir: exchangeDir,
		DataDir:     dataDir,
		Store:       store,
	})
	if err != nil {
		t.Fatal(err)
	}

	return server, exchangeDir, dataDir
}

func TestServer_GetIndex(t *testing.T) {
	srv, _, _ := setupTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "CordBrief Setup Control Plane") {
		t.Errorf("missing header in response")
	}
	if !strings.Contains(body, "Test Server") || !strings.Contains(body, "#general") {
		t.Errorf("missing catalog server or channel in HTML: %s", body)
	}
	if !strings.Contains(body, `value="c1" checked`) {
		t.Errorf("expected channel c1 to be checked")
	}
}

func TestServer_PostWatchlist(t *testing.T) {
	srv, exchangeDir, _ := setupTestEnv(t)

	form := url.Values{}
	form.Add("channels", "c1")
	form.Add("channels", "c2")

	req := httptest.NewRequest(http.MethodPost, "/api/watchlist", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://127.0.0.1:8080")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 SeeOther redirect, got %d", rec.Code)
	}

	// Verify watchlist.json updated
	wlData, err := os.ReadFile(filepath.Join(exchangeDir, "watchlist.json"))
	if err != nil {
		t.Fatal(err)
	}

	var wl struct {
		Generation int      `json:"generation"`
		ChannelIDs []string `json:"channel_ids"`
	}
	_ = json.Unmarshal(wlData, &wl)

	if wl.Generation != 2 {
		t.Errorf("expected generation 2, got %d", wl.Generation)
	}
	if len(wl.ChannelIDs) != 2 {
		t.Errorf("expected 2 channels, got %d", len(wl.ChannelIDs))
	}
}

func TestServer_PostLLMSettings(t *testing.T) {
	srv, _, _ := setupTestEnv(t)

	form := url.Values{}
	form.Set("provider", "gemini")
	form.Set("gemini_model", "gemini-3.7-flash")
	form.Set("gemini_api_key", "my-new-secret-key")

	req := httptest.NewRequest(http.MethodPost, "/api/llm", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://127.0.0.1:8080")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", rec.Code)
	}

	if !srv.store.IsGeminiConfigured() {
		t.Fatal("expected Gemini to be configured after saving key")
	}
	if srv.store.GetGeminiKey() != "my-new-secret-key" {
		t.Errorf("unexpected secret stored: %s", srv.store.GetGeminiKey())
	}
}

func TestServer_PostLLMTest(t *testing.T) {
	srv, _, _ := setupTestEnv(t)
	fake := llm.NewFakeLLMServer()
	defer fake.Close()

	fake.ResponseContent = "PONG"

	form := url.Values{}
	form.Set("provider", "local")
	form.Set("local_base_url", fake.URL)
	form.Set("local_model", "test-model")

	req := httptest.NewRequest(http.MethodPost, "/api/llm/test", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://127.0.0.1:8080")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var res struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if !res.OK || !strings.Contains(res.Message, "PONG") {
		t.Errorf("unexpected test result: %+v", res)
	}
}

func TestServer_PostDigestPreview(t *testing.T) {
	srv, exchangeDir, _ := setupTestEnv(t)
	fake := llm.NewFakeLLMServer()
	defer fake.Close()

	fake.ResponseContent = `{"title":"UI Preview Test","overview":"Overview","items":[{"kind":"finding","text":"Some finding","source_ids":["S000001"]}]}`

	// Configure local provider targeting fake server
	cfg := srv.store.GetAppConfig()
	cfg.LLM.Provider = "local"
	cfg.LLM.BaseURL = fake.URL
	cfg.LLM.Model = "mock-model"
	_ = srv.store.SaveAppConfig(cfg)

	// Write 1 journal message
	eventsDir := filepath.Join(exchangeDir, "events")
	seg1 := filepath.Join(eventsDir, "0000000000000001.ndjson")
	eventLine := []byte(`{"version":1,"event":"message_create","message_id":"111","guild_id":"g1","channel_id":"c1","timestamp":"2026-09-04T00:00:00Z","captured_at":"2026-09-04T00:00:00Z","author":{"id":"u1","name":"alice","display_name":"Alice","bot":false},"content":"Test message for preview"}` + "\n")
	_ = os.WriteFile(seg1, eventLine, 0644)

	req := httptest.NewRequest(http.MethodPost, "/api/digest/preview", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://127.0.0.1:8080")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var res struct {
		OK               bool   `json:"ok"`
		Empty            bool   `json:"empty"`
		BatchID          string `json:"batch_id"`
		RenderedMarkdown string `json:"rendered_markdown"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)

	if !res.OK || res.Empty {
		t.Fatalf("expected valid non-empty preview, got %+v", res)
	}
	if !strings.Contains(res.RenderedMarkdown, "UI Preview Test") {
		t.Errorf("rendered markdown missing title: %s", res.RenderedMarkdown)
	}

	// Verify cursor was NOT advanced
	ackFile := filepath.Join(exchangeDir, "core-ack.json")
	if _, err := os.Stat(ackFile); !os.IsNotExist(err) {
		t.Fatal("expected core-ack.json to NOT exist after preview")
	}
}

func TestServer_CSRFRejection(t *testing.T) {
	srv, _, _ := setupTestEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/api/watchlist", strings.NewReader("channels=c1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://evil-attacker.com")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for cross-origin request, got %d", rec.Code)
	}
}

func TestServer_MethodNotAllowed(t *testing.T) {
	srv, _, _ := setupTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/watchlist", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 Method Not Allowed, got %d", rec.Code)
	}
}

func TestServer_XSSSanitization(t *testing.T) {
	srv, exchangeDir, _ := setupTestEnv(t)

	// Inject adversarial payloads in catalog.json
	adversarialCatalog := `{
  "version": 1,
  "updated_at": "2026-09-04T00:00:00Z",
  "guilds": [
    {
      "id": "g-xss",
      "name": "<script>alert('guild-pwn')</script>",
      "channels": [
        {"id": "c-xss-1", "name": "<img src=x onerror=alert('img-xss')>", "type": 0},
        {"id": "c-xss-2", "name": "\"><script>alert('attr-break')</script>", "type": 0}
      ]
    }
  ]
}`
	_ = os.WriteFile(filepath.Join(exchangeDir, "catalog.json"), []byte(adversarialCatalog), 0644)

	// Send request with adversarial flash messages
	req := httptest.NewRequest(http.MethodGet, "/?flash=%3Cscript%3Ealert('flash-pwn')%3C/script%3E&error=%3Cscript%3Ealert('err-pwn')%3C/script%3E", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	body := rec.Body.String()

	// 1. Verify NO unescaped malicious scripts exist
	dangerousStrings := []string{
		"<script>alert('guild-pwn')</script>",
		"<img src=x onerror=alert('img-xss')>",
		"\"><script>alert('attr-break')</script>",
		"<script>alert('flash-pwn')</script>",
		"<script>alert('err-pwn')</script>",
	}
	for _, s := range dangerousStrings {
		if strings.Contains(body, s) {
			t.Fatalf("XSS VULNERABILITY DETECTED: body contains raw unescaped HTML: %s", s)
		}
	}

	// 2. Verify escaped entities are present
	if !strings.Contains(body, "&lt;script&gt;alert(&#39;guild-pwn&#39;)&lt;/script&gt;") &&
		!strings.Contains(body, "&lt;script&gt;alert('guild-pwn')&lt;/script&gt;") {
		t.Errorf("expected escaped guild name in HTML: %s", body)
	}
	if !strings.Contains(body, "&lt;img src=x onerror=alert(&#39;img-xss&#39;)&gt;") &&
		!strings.Contains(body, "&lt;img src=x onerror=alert('img-xss')&gt;") {
		t.Errorf("expected escaped channel name in HTML: %s", body)
	}

	// 3. Verify security headers are set
	if ct := rec.Header().Get("X-Content-Type-Options"); ct != "nosniff" {
		t.Errorf("expected X-Content-Type-Options: nosniff, got %s", ct)
	}
	if xfo := rec.Header().Get("X-Frame-Options"); xfo != "DENY" {
		t.Errorf("expected X-Frame-Options: DENY, got %s", xfo)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("expected CSP header, got %s", csp)
	}
}

func TestServer_SecretSourceDisplayAndRejection(t *testing.T) {
	srv, _, _ := setupTestEnv(t)

	// 1. None initially
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Not Configured") {
		t.Errorf("expected 'Not Configured' status")
	}

	// 2. Stored key
	_ = srv.store.SaveGeminiKey("my-stored-key")
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Configured (stored in secrets.json)") {
		t.Errorf("expected 'Configured (stored in secrets.json)' status")
	}

	// 3. Environment override
	t.Setenv("GEMINI_API_KEY", "env-provided-key")
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	srv.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "Configured (via environment)") {
		t.Errorf("expected 'Configured (via environment)' status")
	}
	if !strings.Contains(body, "Managed by environment variable (GEMINI_API_KEY)") || !strings.Contains(body, "disabled") {
		t.Errorf("expected input to be disabled with environment explanation")
	}
	// Verify secret values are NEVER in HTML
	if strings.Contains(body, "env-provided-key") || strings.Contains(body, "my-stored-key") {
		t.Fatalf("SECURITY VIOLATION: secret key leaked in rendered HTML")
	}

	// 4. Attempting to override via UI when environment key is active must be rejected
	form := url.Values{}
	form.Set("provider", "gemini")
	form.Set("gemini_api_key", "attempted-ui-override")
	req = httptest.NewRequest(http.MethodPost, "/api/llm", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://127.0.0.1:8080")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", rec.Code)
	}
	location := rec.Header().Get("Location")
	if !strings.Contains(location, "managed+by+environment") {
		t.Errorf("expected rejection redirect for environment-managed key: %s", location)
	}
}

func TestServer_ContentTypeValidation(t *testing.T) {
	srv, _, _ := setupTestEnv(t)

	// POST without application/x-www-form-urlencoded or multipart/form-data
	req := httptest.NewRequest(http.MethodPost, "/api/watchlist", strings.NewReader("channels=c1"))
	req.Header.Set("Content-Type", "text/plain")
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://127.0.0.1:8080")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415 Unsupported Media Type, got %d", rec.Code)
	}
}

func TestServer_Inbox_EmptyAndPopulated(t *testing.T) {
	srv, _, dataDir := setupTestEnv(t)
	digestsDir := filepath.Join(dataDir, "digests")
	_ = os.MkdirAll(digestsDir, 0755)

	// 1. Empty Inbox
	req := httptest.NewRequest(http.MethodGet, "/inbox", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /inbox, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Inbox is empty") {
		t.Errorf("expected empty inbox message, got: %s", body)
	}
	if !strings.Contains(body, "0 Digests") {
		t.Errorf("expected 0 Digests badge, got: %s", body)
	}

	// 2. Populate with 2 valid digests and 1 corrupt file
	id1 := strings.Repeat("a", 64)
	id2 := strings.Repeat("b", 64)

	art1 := `{
  "version": 1,
  "batch_id": "` + id1 + `",
  "created_at": "2026-09-04T01:00:00Z",
  "cursor_start": {"segment": 1, "offset": 0},
  "cursor_end": {"segment": 1, "offset": 100},
  "input_message_count": 5,
  "included_message_count": 5,
  "provider": "gemini",
  "model": "gemini-3.7-flash",
  "trigger": {"type": "manual"},
  "digest": {
    "title": "Manual Test Digest",
    "overview": "Overview of manual digest",
    "items": [{"kind": "finding", "text": "Finding 1", "source_ids": ["S000001"]}]
  }
}`

	art2 := `{
  "version": 1,
  "batch_id": "` + id2 + `",
  "created_at": "2026-09-04T02:00:00Z",
  "cursor_start": {"segment": 1, "offset": 100},
  "cursor_end": {"segment": 1, "offset": 200},
  "input_message_count": 8,
  "included_message_count": 6,
  "provider": "gemini",
  "model": "gemini-3.7-flash",
  "trigger": {"type": "scheduled", "slot_id": "Asia/Riyadh/2026-09-04/02:00"},
  "digest": {
    "title": "Scheduled Test Digest",
    "overview": "Overview of scheduled digest",
    "items": [{"kind": "important", "text": "Important item", "source_ids": ["S000002"]}]
  }
}`

	_ = os.WriteFile(filepath.Join(digestsDir, id1+".json"), []byte(art1), 0644)
	_ = os.WriteFile(filepath.Join(digestsDir, id2+".json"), []byte(art2), 0644)
	_ = os.WriteFile(filepath.Join(digestsDir, strings.Repeat("c", 64)+".json"), []byte("{corrupt json"), 0644)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/inbox", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	body = rec.Body.String()

	if !strings.Contains(body, "2 Digests") {
		t.Errorf("expected 2 Digests badge, got: %s", body)
	}
	if !strings.Contains(body, "Manual Test Digest") || !strings.Contains(body, "Scheduled Test Digest") {
		t.Errorf("expected both digest titles in inbox list")
	}
	if !strings.Contains(body, "Scheduled") || !strings.Contains(body, "Manual") {
		t.Errorf("expected trigger badges in inbox list")
	}
	if !strings.Contains(body, "1 corrupted digest artifact(s) were isolated") {
		t.Errorf("expected corrupt alert in inbox list, got: %s", body)
	}
}

func TestServer_DigestDetail_ValidAndNotFound(t *testing.T) {
	srv, exchangeDir, dataDir := setupTestEnv(t)
	digestsDir := filepath.Join(dataDir, "digests")
	_ = os.MkdirAll(digestsDir, 0755)

	validID := strings.Repeat("d", 64)
	art := `{
  "version": 1,
  "batch_id": "` + validID + `",
  "created_at": "2026-09-04T02:00:00Z",
  "cursor_start": {"segment": 1, "offset": 0},
  "cursor_end": {"segment": 1, "offset": 230},
  "input_message_count": 1,
  "included_message_count": 1,
  "provider": "gemini",
  "model": "gemini-3.7-flash",
  "trigger": {"type": "scheduled", "slot_id": "Asia/Riyadh/2026-09-04/02:00"},
  "digest": {
    "title": "Deep Dive Digest",
    "overview": "Detailed overview of discussions",
    "items": [
      {
        "kind": "finding",
        "text": "Core stability proven",
        "source_ids": ["S000001"],
        "channel_context": "general"
      }
    ]
  }
}`
	_ = os.WriteFile(filepath.Join(digestsDir, validID+".json"), []byte(art), 0644)

	// Write journal event to test Discord jump link reconstruction
	eventsDir := filepath.Join(exchangeDir, "events")
	seg1 := filepath.Join(eventsDir, "0000000000000001.ndjson")
	eventLine := []byte(`{"version":1,"event":"message_create","message_id":"999888","guild_id":"111222","channel_id":"333444","timestamp":"2026-09-04T01:50:00Z","captured_at":"2026-09-04T01:50:00Z","author":{"id":"u1","name":"alice","display_name":"Alice","bot":false},"content":"Verified stability"}` + "\n")
	_ = os.WriteFile(seg1, eventLine, 0644)

	// 1. Valid detail request
	req := httptest.NewRequest(http.MethodGet, "/digests/"+validID, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Deep Dive Digest") {
		t.Errorf("missing title in detail view")
	}
	if !strings.Contains(body, "Detailed overview of discussions") {
		t.Errorf("missing overview in detail view")
	}
	if !strings.Contains(body, "Core stability proven") {
		t.Errorf("missing item text in detail view")
	}
	if !strings.Contains(body, "Scheduled (Asia/Riyadh/2026-09-04/02:00)") {
		t.Errorf("missing scheduled badge with slot_id in detail view")
	}
	if !strings.Contains(body, "#general") {
		t.Errorf("missing channel context tag in detail view")
	}
	// Verify jump link reconstructed from journal
	expectedJumpLink := "https://discord.com/channels/111222/333444/999888"
	if !strings.Contains(body, expectedJumpLink) {
		t.Errorf("expected jump link %s in detail view, got: %s", expectedJumpLink, body)
	}

	// 2. Non-existent 64-hex ID returns 404
	missingID := strings.Repeat("0", 64)
	req = httptest.NewRequest(http.MethodGet, "/digests/"+missingID, nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing ID, got %d", rec.Code)
	}

	// 3. Malformed ID returns 404
	badIDs := []string{"1234", validID + ";rm", "not-a-valid-hex-id", strings.Repeat("A", 64)}
	for _, badID := range badIDs {
		req = httptest.NewRequest(http.MethodGet, "/digests/"+badID, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404 for bad ID %q, got %d", badID, rec.Code)
		}
	}
}

func TestServer_ScheduleSettings(t *testing.T) {
	srv, _, _ := setupTestEnv(t)

	// 1. Valid schedule update
	form := url.Values{}
	form.Set("enabled", "true")
	form.Set("time", "14:30")
	form.Set("timezone", "Asia/Riyadh")

	req := httptest.NewRequest(http.MethodPost, "/api/schedule", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://127.0.0.1:8080")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "flash=Schedule+settings+saved") {
		t.Errorf("expected success flash, got: %s", loc)
	}

	cfg := srv.store.GetScheduleConfig()
	if !cfg.Enabled || cfg.Time != "14:30" || cfg.Timezone != "Asia/Riyadh" {
		t.Errorf("saved config mismatch: %+v", cfg)
	}

	// 2. Invalid time
	form.Set("time", "25:00")
	req = httptest.NewRequest(http.MethodPost, "/api/schedule", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://127.0.0.1:8080")

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", rec.Code)
	}
	loc = rec.Header().Get("Location")
	if !strings.Contains(loc, "error=Invalid+schedule+settings") {
		t.Errorf("expected error redirect for invalid time, got: %s", loc)
	}

	// 3. Invalid timezone
	form.Set("time", "14:30")
	form.Set("timezone", "Fantasy/Land")
	req = httptest.NewRequest(http.MethodPost, "/api/schedule", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://127.0.0.1:8080")

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", rec.Code)
	}
	loc = rec.Header().Get("Location")
	if !strings.Contains(loc, "error=Invalid+schedule+settings") {
		t.Errorf("expected error redirect for invalid timezone, got: %s", loc)
	}
}

func TestServer_InboxAndDetail_XSSSanitization(t *testing.T) {
	srv, _, dataDir := setupTestEnv(t)
	digestsDir := filepath.Join(dataDir, "digests")
	_ = os.MkdirAll(digestsDir, 0755)

	xssID := strings.Repeat("9", 64)
	maliciousArt := `{
  "version": 1,
  "batch_id": "` + xssID + `",
  "created_at": "2026-09-04T00:00:00Z",
  "cursor_start": {"segment": 1, "offset": 0},
  "cursor_end": {"segment": 1, "offset": 100},
  "input_message_count": 1,
  "included_message_count": 1,
  "provider": "gemini",
  "model": "gemini-3.7-flash",
  "trigger": {"type": "scheduled", "slot_id": "<script>alert('slot-xss')</script>"},
  "digest": {
    "title": "<script>alert('title-xss')</script>",
    "overview": "<img src=x onerror=alert('overview-xss')>",
    "items": [
      {
        "kind": "finding",
        "text": "<b onmouseover=alert('item-xss')>clickme</b>",
        "source_ids": ["<script>s1</script>"],
        "channel_context": "\"><script>alert('channel-xss')</script>"
      }
    ]
  }
}`
	_ = os.WriteFile(filepath.Join(digestsDir, xssID+".json"), []byte(maliciousArt), 0644)

	// Check /inbox
	req := httptest.NewRequest(http.MethodGet, "/inbox", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	inboxBody := rec.Body.String()
	dangerousInboxStrings := []string{
		"<script>alert('title-xss')</script>",
		"<img src=x onerror=alert('overview-xss')>",
	}
	for _, s := range dangerousInboxStrings {
		if strings.Contains(inboxBody, s) {
			t.Fatalf("XSS VULNERABILITY in /inbox: found unescaped %s", s)
		}
	}

	// Check /digests/{id}
	req = httptest.NewRequest(http.MethodGet, "/digests/"+xssID, nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	detailBody := rec.Body.String()
	dangerousDetailStrings := []string{
		"<script>alert('slot-xss')</script>",
		"<script>alert('title-xss')</script>",
		"<img src=x onerror=alert('overview-xss')>",
		"<b onmouseover=alert('item-xss')>clickme</b>",
		"<script>s1</script>",
		"\"><script>alert('channel-xss')</script>",
	}
	for _, s := range dangerousDetailStrings {
		if strings.Contains(detailBody, s) {
			t.Fatalf("XSS VULNERABILITY in /digests/{id}: found unescaped %s", s)
		}
	}

	// Ensure escaped representations exist
	if !strings.Contains(detailBody, "&lt;script&gt;alert(&#39;title-xss&#39;)&lt;/script&gt;") &&
		!strings.Contains(detailBody, "&lt;script&gt;alert('title-xss')&lt;/script&gt;") {
		t.Errorf("expected escaped title in detail view")
	}
}

func TestFormatDisplayTime(t *testing.T) {
	utcTime := time.Date(2026, time.September, 4, 0, 17, 7, 0, time.UTC)

	cases := []struct {
		tz       string
		expected string
	}{
		{"Asia/Riyadh", "Sep 04, 2026 03:17 — Asia/Riyadh"},
		{"America/New_York", "Sep 03, 2026 20:17 — America/New_York"},
		{"UTC", "Sep 04, 2026 00:17 — UTC"},
		{"", "Sep 04, 2026 00:17 — UTC"},
		{"Invalid/Timezone", "Sep 04, 2026 00:17 — UTC"},
	}

	for _, tc := range cases {
		got := FormatDisplayTime(utcTime, tc.tz)
		if got != tc.expected {
			t.Errorf("FormatDisplayTime(..., %q) = %q, want %q", tc.tz, got, tc.expected)
		}
	}
}

func TestServer_TimezoneDisplayRendering(t *testing.T) {
	srv, _, dataDir := setupTestEnv(t)
	digestsDir := filepath.Join(dataDir, "digests")
	_ = os.MkdirAll(digestsDir, 0755)

	// Configure schedule with Asia/Riyadh, disabled
	cfg := srv.store.GetAppConfig()
	cfg.Schedule.Enabled = false
	cfg.Schedule.Timezone = "Asia/Riyadh"
	cfg.Schedule.Time = "03:17"
	if err := srv.store.SaveAppConfig(cfg); err != nil {
		t.Fatalf("failed saving config: %v", err)
	}

	batchID := strings.Repeat("d", 64)
	art := `{
  "version": 1,
  "batch_id": "` + batchID + `",
  "created_at": "2026-09-04T00:17:00Z",
  "cursor_start": {"segment": 1, "offset": 0},
  "cursor_end": {"segment": 1, "offset": 100},
  "input_message_count": 4,
  "included_message_count": 4,
  "provider": "gemini",
  "model": "gemini-3.7-flash",
  "trigger": {"type": "scheduled", "slot_id": "Asia/Riyadh/2026-09-04/03:17"},
  "digest": {
    "title": "Timezone Display Test",
    "overview": "Overview for timezone test",
    "items": [{"kind": "finding", "text": "Item 1", "source_ids": ["S000001"]}]
  }
}`
	if err := os.WriteFile(filepath.Join(digestsDir, batchID+".json"), []byte(art), 0644); err != nil {
		t.Fatalf("failed writing test artifact: %v", err)
	}

	// 1. Verify /inbox renders Asia/Riyadh
	req := httptest.NewRequest(http.MethodGet, "/inbox", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	body := rec.Body.String()
	expectedTimeStr := "Sep 04, 2026 03:17 — Asia/Riyadh"
	if !strings.Contains(body, expectedTimeStr) {
		t.Errorf("inbox view does not contain expected timezone formatted time %q, body:\n%s", expectedTimeStr, body)
	}

	// 2. Verify /digests/{id} renders Asia/Riyadh
	req = httptest.NewRequest(http.MethodGet, "/digests/"+batchID, nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	detailBody := rec.Body.String()
	if !strings.Contains(detailBody, "Generated on "+expectedTimeStr) {
		t.Errorf("detail view does not contain %q, body:\n%s", "Generated on "+expectedTimeStr, detailBody)
	}

	// 3. Switch timezone to America/New_York (-04:00 EDT)
	cfg.Schedule.Timezone = "America/New_York"
	if err := srv.store.SaveAppConfig(cfg); err != nil {
		t.Fatalf("failed updating config: %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, "/inbox", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	bodyNY := rec.Body.String()
	expectedNY := "Sep 03, 2026 20:17 — America/New_York"
	if !strings.Contains(bodyNY, expectedNY) {
		t.Errorf("inbox view does not contain NY formatted time %q, body:\n%s", expectedNY, bodyNY)
	}
}

func TestServer_ContinuityStatusTile(t *testing.T) {
	srv, exchangeDir, _ := setupTestEnv(t)
	statPath := filepath.Join(exchangeDir, "collector-status.json")

	// 1. Ready status -> ✓ Up to date
	statusReady := `{"version":1,"updated_at":"2026-09-04T00:00:00Z","collector_state":"running","discord_authenticated":true,"watched_generation":1,"watched_channel_count":1,"active_segment":1,"recovery_state":"ready","recovery_pending_channels":0}`
	_ = os.WriteFile(statPath, []byte(statusReady), 0644)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "✓ Up to date") {
		t.Errorf("expected '✓ Up to date' in body, got: %s", rec.Body.String())
	}

	// 2. Recovering status -> Recovering (2 remaining)
	statusRec := `{"version":1,"updated_at":"2026-09-04T00:00:00Z","collector_state":"running","discord_authenticated":true,"watched_generation":1,"watched_channel_count":1,"active_segment":1,"recovery_state":"recovering","recovery_pending_channels":2}`
	_ = os.WriteFile(statPath, []byte(statusRec), 0644)

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Recovering (2 remaining)") {
		t.Errorf("expected 'Recovering (2 remaining)' in body, got: %s", rec.Body.String())
	}

	// 3. Error status -> Recovery Warning
	statusErr := `{"version":1,"updated_at":"2026-09-04T00:00:00Z","collector_state":"running","discord_authenticated":true,"watched_generation":1,"watched_channel_count":1,"active_segment":1,"recovery_state":"error","recovery_last_error":"REST 429"}`
	_ = os.WriteFile(statPath, []byte(statusErr), 0644)

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Recovery Warning") {
		t.Errorf("expected 'Recovery Warning' in body, got: %s", rec.Body.String())
	}
}
