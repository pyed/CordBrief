package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cordbrief/internal/config"
	"cordbrief/internal/delivery"
	"cordbrief/internal/digest"
	"cordbrief/internal/journal"
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

	delSvc, err := delivery.NewService(delivery.ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
	})
	if err != nil {
		t.Fatal(err)
	}

	server, err := NewServer(ServerOptions{
		ExchangeDir:     exchangeDir,
		DataDir:         dataDir,
		Store:           store,
		DeliveryService: delSvc,
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
	if !strings.Contains(body, "CordBrief") || !strings.Contains(body, "Overview") {
		t.Errorf("missing header or page title in response")
	}
	if !strings.Contains(body, "Collector Daemon") || !strings.Contains(body, "Watched Channels") {
		t.Errorf("missing subsystem cards in Overview HTML: %s", body)
	}
	if !strings.Contains(body, `class="nav-item active"`) {
		t.Errorf("expected active nav item on overview")
	}
}

func TestServer_GetChannels(t *testing.T) {
	srv, _, _ := setupTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/channels", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Watched Channels") {
		t.Errorf("missing page title in response")
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

func TestSwitchToGeminiResetsEndpoint(t *testing.T) {
	srv, _, _ := setupTestEnv(t)
	cfg := srv.store.GetAppConfig()
	cfg.LLM.Provider = config.ProviderLocal
	cfg.LLM.BaseURL = "http://127.0.0.1:11434/v1"
	cfg.LLM.Model = "local-model"
	if err := srv.store.SaveAppConfig(cfg); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/llm", strings.NewReader("provider=gemini"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://127.0.0.1:8080")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || srv.store.GetAppConfig().LLM.BaseURL != config.DefaultGeminiBaseURL {
		t.Fatalf("Gemini retained local endpoint: status=%d config=%+v", rec.Code, srv.store.GetAppConfig().LLM)
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
	req := httptest.NewRequest(http.MethodGet, "/channels?flash=%3Cscript%3Ealert('flash-pwn')%3C/script%3E&error=%3Cscript%3Ealert('err-pwn')%3C/script%3E", nil)
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
	req := httptest.NewRequest(http.MethodGet, "/provider", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Not Configured") {
		t.Errorf("expected 'Not Configured' status")
	}

	// 2. Stored key
	_ = srv.store.SaveGeminiKey("my-stored-key")
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/provider", nil)
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Configured (stored in secrets.json)") {
		t.Errorf("expected 'Configured (stored in secrets.json)' status")
	}

	// 3. Environment override
	t.Setenv("GEMINI_API_KEY", "env-provided-key")
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/provider", nil)
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
	// Verify jump link reconstructed from journal with user-facing label 1 and internal ID in tooltip
	expectedJumpLink := "https://discord.com/channels/111222/333444/999888"
	if !strings.Contains(body, expectedJumpLink) {
		t.Errorf("expected jump link %s in detail view, got: %s", expectedJumpLink, body)
	}
	if !strings.Contains(body, ">1 ↗</a>") {
		t.Errorf("expected user-facing source label '1 ↗' in detail view, got: %s", body)
	}
	if !strings.Contains(body, "Internal ID: S000001") {
		t.Errorf("expected internal ID S000001 in tooltip, got: %s", body)
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
	nowStr := time.Now().UTC().Format(time.RFC3339)

	// 1. Ready status -> ✓ Up to date
	statusReady := `{"version":1,"updated_at":"` + nowStr + `","collector_state":"running","discord_authenticated":true,"watched_generation":1,"watched_channel_count":1,"active_segment":1,"recovery_state":"ready","recovery_pending_channels":0}`
	_ = os.WriteFile(statPath, []byte(statusReady), 0644)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "✓ Up to date") {
		t.Errorf("expected '✓ Up to date' in body, got: %s", rec.Body.String())
	}

	// 2. Recovering status -> Recovering (2 remaining)
	statusRec := `{"version":1,"updated_at":"` + nowStr + `","collector_state":"running","discord_authenticated":true,"watched_generation":1,"watched_channel_count":1,"active_segment":1,"recovery_state":"recovering","recovery_pending_channels":2}`
	_ = os.WriteFile(statPath, []byte(statusRec), 0644)

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Recovering (2 remaining)") {
		t.Errorf("expected 'Recovering (2 remaining)' in body, got: %s", rec.Body.String())
	}

	// 3. Error status -> Recovery Warning
	statusErr := `{"version":1,"updated_at":"` + nowStr + `","collector_state":"running","discord_authenticated":true,"watched_generation":1,"watched_channel_count":1,"active_segment":1,"recovery_state":"error","recovery_last_error":"REST 429"}`
	_ = os.WriteFile(statPath, []byte(statusErr), 0644)

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Recovery Warning") {
		t.Errorf("expected 'Recovery Warning' in body, got: %s", rec.Body.String())
	}
}

func TestServer_StaleCollectorStatus(t *testing.T) {
	srv, exchangeDir, _ := setupTestEnv(t)
	statPath := filepath.Join(exchangeDir, "collector-status.json")

	// Timestamp is 2 minutes old -> stale
	staleTime := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339)
	statusStale := `{"version":1,"updated_at":"` + staleTime + `","collector_state":"running","discord_authenticated":true,"watched_generation":1,"watched_channel_count":1,"active_segment":1,"recovery_state":"ready","recovery_pending_channels":0}`
	_ = os.WriteFile(statPath, []byte(statusStale), 0644)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "Collector Offline") {
		t.Errorf("expected 'Collector Offline' in body for stale status, got: %s", body)
	}
	if strings.Contains(body, "✓ Up to date") {
		t.Errorf("stale collector status should NOT claim '✓ Up to date'")
	}
}

func TestServer_CollectorCommand(t *testing.T) {
	srv, exchangeDir, _ := setupTestEnv(t)

	// 1. Missing CSRF header fails
	req := httptest.NewRequest(http.MethodPost, "/api/collector/command", strings.NewReader("command=enter_reauth"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.com")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for bad CSRF, got %d", rec.Code)
	}

	// 2. Invalid command fails
	req = httptest.NewRequest(http.MethodPost, "/api/collector/command", strings.NewReader("command=invalid_cmd"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for invalid command, got %d", rec.Code)
	}

	// 3. Valid enter_reauth command writes collector-command.json
	req = httptest.NewRequest(http.MethodPost, "/api/collector/command", strings.NewReader("command=enter_reauth"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", rec.Code)
	}

	cmdFile := filepath.Join(exchangeDir, "collector-command.json")
	data, err := os.ReadFile(cmdFile)
	if err != nil {
		t.Fatalf("failed to read written command file: %v", err)
	}

	var cmd journal.CollectorCommand
	if err := json.Unmarshal(data, &cmd); err != nil {
		t.Fatalf("failed to unmarshal command JSON: %v", err)
	}
	if cmd.Command != "enter_reauth" {
		t.Errorf("expected command 'enter_reauth', got %q", cmd.Command)
	}
	if cmd.Version != 1 {
		t.Errorf("expected version 1, got %d", cmd.Version)
	}
	if !strings.HasPrefix(cmd.RequestID, "req-") {
		t.Errorf("expected request_id prefix 'req-', got %q", cmd.RequestID)
	}

	// 4. Second command while first is still pending returns 409 Conflict
	req = httptest.NewRequest(http.MethodPost, "/api/collector/command", strings.NewReader("command=enter_reauth"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 Conflict for duplicate pending command, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Command already pending") {
		t.Errorf("expected 'Command already pending' in body, got: %s", rec.Body.String())
	}
}

func TestWebDeliveryHandlers(t *testing.T) {
	tmpDir := t.TempDir()
	exchangeDir := filepath.Join(tmpDir, "exchange")
	eventsDir := filepath.Join(exchangeDir, "events")
	dataDir := filepath.Join(tmpDir, "data")
	digestsDir := filepath.Join(dataDir, "digests")
	_ = os.MkdirAll(eventsDir, 0755)
	_ = os.MkdirAll(digestsDir, 0755)

	// Mock Telegram Bot API Server
	var sendCount int
	mockTG := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "getMe") {
			if strings.Contains(r.URL.Path, "invalid-token") {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"ok": false, "error_code": 401, "description": "Unauthorized"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok": true, "result": {"id": 12345678, "is_bot": true, "first_name": "CordBriefBot", "username": "cordbrief_bot"}}`))
			return
		}
		if strings.Contains(r.URL.Path, "getUpdates") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok": true, "result": [
				{"update_id": 101, "message": {"message_id": 1, "chat": {"id": -100987654321, "type": "supergroup", "title": "Dev Team"}, "text": "sensitive message text"}},
				{"update_id": 102, "message": {"message_id": 2, "chat": {"id": 11223344, "type": "private", "first_name": "Alice", "username": "alice_dev"}, "text": "secret user chat"}}
			]}`))
			return
		}
		if strings.Contains(r.URL.Path, "sendMessage") {
			sendCount++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"ok": true, "result": {"message_id": %d}}`, 500+sendCount)))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockTG.Close()

	store, err := config.NewStore(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}

	delSvc, err := delivery.NewService(delivery.ServiceOptions{
		DataDir:     dataDir,
		ExchangeDir: exchangeDir,
		Store:       store,
		ClientGetter: func(token string) *delivery.TelegramClient {
			return delivery.NewTelegramClient(token, delivery.WithBaseURL(mockTG.URL))
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv, err := NewServer(ServerOptions{
		ExchangeDir:     exchangeDir,
		DataDir:         dataDir,
		Store:           store,
		DeliveryService: delSvc,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. CSRF rejection on state-changing Telegram endpoints
	t.Run("CSRF rejection", func(t *testing.T) {
		endpoints := []string{
			"/api/telegram/settings",
			"/api/telegram/test",
			"/api/telegram/chats",
			"/api/telegram/send-test",
			"/api/digests/" + strings.Repeat("f", 64) + "/deliver",
		}
		for _, ep := range endpoints {
			req := httptest.NewRequest(http.MethodPost, ep, strings.NewReader("token=123"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", "http://evil-origin.com")
			req.Host = "127.0.0.1:28741"
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("expected 403 Forbidden for endpoint %s with bad CSRF, got %d", ep, rec.Code)
			}
		}
	})

	// 2. POST /api/telegram/test: when unconfigured and empty token submitted -> returns 400
	t.Run("POST telegram test unconfigured error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/telegram/test", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://127.0.0.1:28741")
		req.Host = "127.0.0.1:28741"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "telegram bot token is not configured") {
			t.Errorf("expected error message to say token is not configured, got: %s", rec.Body.String())
		}
	})

	// 3. POST /api/telegram/test: with invalid token -> rejected and NOT saved to secrets
	t.Run("POST telegram test invalid token not saved", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/telegram/test", strings.NewReader("token=invalid-token"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://127.0.0.1:28741")
		req.Host = "127.0.0.1:28741"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d", rec.Code)
		}
		if token := store.GetTelegramBotToken(); token != "" {
			t.Errorf("expected secrets token to remain empty on test failure, got %q", token)
		}
	})

	// 4. POST /api/telegram/test: multipart form parsing and token auto-saved to secrets.json
	t.Run("POST telegram test multipart and auto-persist", func(t *testing.T) {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		_ = writer.WriteField("token", "multipart-secret-token")
		_ = writer.Close()

		req := httptest.NewRequest(http.MethodPost, "/api/telegram/test", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("Origin", "http://127.0.0.1:28741")
		req.Host = "127.0.0.1:28741"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d (body: %s)", rec.Code, rec.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed decoding json: %v", err)
		}
		if resp["ok"] != true || resp["configured"] != true {
			t.Errorf("expected ok=true and configured=true, got %v", resp)
		}
		if resp["username"] != "cordbrief_bot" {
			t.Errorf("expected username cordbrief_bot, got %v", resp["username"])
		}

		// Verify token was persisted to secrets.json!
		if token := store.GetTelegramBotToken(); token != "multipart-secret-token" {
			t.Errorf("expected token to be auto-persisted to secrets.json, got %q", token)
		}
	})

	// 5. POST /api/telegram/test: testing invalid token keeps previously saved token intact
	t.Run("POST telegram test preserves existing token on failure", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/telegram/test", strings.NewReader("token=invalid-token"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://127.0.0.1:28741")
		req.Host = "127.0.0.1:28741"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d", rec.Code)
		}
		if token := store.GetTelegramBotToken(); token != "multipart-secret-token" {
			t.Errorf("expected secrets token to remain 'multipart-secret-token', got %q", token)
		}
	})

	// 6. POST /api/telegram/chats: calls getUpdates using stored token immediately without re-entering
	t.Run("POST telegram chats safe discovery using stored token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/telegram/chats", nil)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://127.0.0.1:28741")
		req.Host = "127.0.0.1:28741"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}

		body := rec.Body.String()
		if strings.Contains(body, "sensitive message text") || strings.Contains(body, "secret user chat") {
			t.Fatalf("CRITICAL: Discovered chats leaked message text bodies: %s", body)
		}
		if !strings.Contains(body, "-100987654321") || !strings.Contains(body, "Dev Team") {
			t.Errorf("missing safe chat metadata in response: %s", body)
		}
	})

	// 7. Environment variable precedence: env token is authoritative and never overwritten
	t.Run("Environment variable token precedence", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "env-authoritative-token")
		envStore, err := config.NewStore(dataDir, "")
		if err != nil {
			t.Fatal(err)
		}
		envDelSvc, err := delivery.NewService(delivery.ServiceOptions{
			DataDir:     dataDir,
			ExchangeDir: exchangeDir,
			Store:       envStore,
			ClientGetter: func(token string) *delivery.TelegramClient {
				return delivery.NewTelegramClient(token, delivery.WithBaseURL(mockTG.URL))
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		envSrv, err := NewServer(ServerOptions{
			ExchangeDir:     exchangeDir,
			DataDir:         dataDir,
			Store:           envStore,
			DeliveryService: envDelSvc,
		})
		if err != nil {
			t.Fatal(err)
		}

		req := httptest.NewRequest(http.MethodPost, "/api/telegram/test", strings.NewReader("token=attempted-override-token"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://127.0.0.1:28741")
		req.Host = "127.0.0.1:28741"
		rec := httptest.NewRecorder()
		envSrv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}
		// Confirm secrets.json was NOT overwritten with attempted-override-token
		if store.GetTelegramBotToken() == "attempted-override-token" {
			t.Errorf("CRITICAL: secrets.json was overwritten despite environment variable precedence!")
		}
	})

	// 8. POST /api/telegram/settings: validation rejects enabled=true when destination chat_id is empty
	t.Run("POST telegram settings validation empty destination", func(t *testing.T) {
		form := url.Values{}
		form.Set("enabled", "true")
		form.Set("chat_id", "") // empty destination

		req := httptest.NewRequest(http.MethodPost, "/api/telegram/settings", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://127.0.0.1:28741")
		req.Host = "127.0.0.1:28741"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303 redirect on validation failure, got %d", rec.Code)
		}
		loc := rec.Header().Get("Location")
		if !strings.Contains(loc, "Select+a+Telegram+destination+before+enabling+daily+delivery") {
			t.Errorf("expected destination required error in redirect, got %q", loc)
		}

		// Verify existing token was NOT destroyed by validation failure
		if token := store.GetTelegramBotToken(); token != "multipart-secret-token" {
			t.Errorf("expected existing token 'multipart-secret-token' preserved, got %q", token)
		}

		// Verify Telegram was NOT enabled
		cfg := store.GetDeliveryConfig()
		if cfg.Telegram.Enabled {
			t.Errorf("Telegram delivery must NOT be enabled with empty destination")
		}
	})

	// 9. Requirement 6A: Send Test Ping uses submitted form chat_id when none persisted
	t.Run("POST telegram send-test uses submitted chat_id without prior save", func(t *testing.T) {
		// Verify no chat_id is currently persisted in store
		cfg := store.GetDeliveryConfig()
		if cfg.Telegram.ChatID != "" {
			t.Fatalf("precondition failed: expected empty persisted chat_id, got %q", cfg.Telegram.ChatID)
		}

		form := url.Values{}
		form.Set("chat_id", "-100987654321") // newly discovered chat submitted by user

		req := httptest.NewRequest(http.MethodPost, "/api/telegram/send-test", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://127.0.0.1:28741")
		req.Host = "127.0.0.1:28741"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d (body: %s)", rec.Code, rec.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed decoding json: %v", err)
		}
		if resp["ok"] != true {
			t.Errorf("expected ok=true, got %v", resp["ok"])
		}
		if resp["message"] != "Test message sent successfully." {
			t.Errorf("expected clean success message, got %v", resp["message"])
		}
	})

	// 10. Requirement 6B & 6E: Save valid destination then Send Test Ping with blank form falls back to persisted
	t.Run("POST telegram save valid destination and fallback test ping", func(t *testing.T) {
		// Save valid destination
		form := url.Values{}
		form.Set("enabled", "true")
		form.Set("token", "my-secret-tg-token")
		form.Set("chat_id", "-100987654321")
		form.Set("chat_label", "Engineering Digest")

		saveReq := httptest.NewRequest(http.MethodPost, "/api/telegram/settings", strings.NewReader(form.Encode()))
		saveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		saveReq.Header.Set("Origin", "http://127.0.0.1:28741")
		saveReq.Host = "127.0.0.1:28741"
		saveRec := httptest.NewRecorder()
		srv.ServeHTTP(saveRec, saveReq)

		if saveRec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303 redirect, got %d", saveRec.Code)
		}

		cfg := store.GetDeliveryConfig()
		if !cfg.Telegram.Enabled || cfg.Telegram.ChatID != "-100987654321" {
			t.Fatalf("expected saved enabled and chat_id, got %+v", cfg.Telegram)
		}

		// Send Test Ping with blank form chat_id -> falls back to persisted
		pingReq := httptest.NewRequest(http.MethodPost, "/api/telegram/send-test", strings.NewReader("chat_id="))
		pingReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		pingReq.Header.Set("Origin", "http://127.0.0.1:28741")
		pingReq.Host = "127.0.0.1:28741"
		pingRec := httptest.NewRecorder()
		srv.ServeHTTP(pingRec, pingReq)

		if pingRec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK using persisted chat_id, got %d (body: %s)", pingRec.Code, pingRec.Body.String())
		}
	})

	// 11. Requirement 6C: Form chat_id overrides persisted chat_id for test
	t.Run("POST telegram send-test form chat_id overrides persisted", func(t *testing.T) {
		form := url.Values{}
		form.Set("chat_id", "-100888888888") // temporary override for test

		req := httptest.NewRequest(http.MethodPost, "/api/telegram/send-test", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://127.0.0.1:28741")
		req.Host = "127.0.0.1:28741"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d (body: %s)", rec.Code, rec.Body.String())
		}

		// Persisted config remains untouched
		cfg := store.GetDeliveryConfig()
		if cfg.Telegram.ChatID != "-100987654321" {
			t.Errorf("persisted chat_id should not change when test overrides it, got %q", cfg.Telegram.ChatID)
		}
	})

	// 12. POST /api/digests/{batch-id}/deliver: delivers existing artifact and verifies UI views
	t.Run("POST digest delivery and UI views", func(t *testing.T) {
		batchID := strings.Repeat("f", 64)
		art := &digest.Artifact{
			Version: journal.CurrentSchemaVersion,
			BatchID: batchID,
			Digest: &digest.Digest{
				Title:    "Sprint Alpha Summary",
				Overview: "Everything shipped successfully.",
				Items: []digest.Item{
					{Kind: "important", Text: "System online"},
				},
			},
			CreatedAt: time.Now().UTC(),
		}
		if err := digest.SaveArtifact(digestsDir, art); err != nil {
			t.Fatalf("failed saving test artifact: %v", err)
		}

		// Initial deliver POST
		req := httptest.NewRequest(http.MethodPost, "/api/digests/"+batchID+"/deliver", nil)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://127.0.0.1:28741")
		req.Host = "127.0.0.1:28741"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303 redirect, got %d", rec.Code)
		}

		// Verify delivery record persisted as StateSent
		recState, err := delSvc.GetDelivery(batchID)
		if err != nil || recState == nil {
			t.Fatalf("expected delivery record for batch, got: %v", err)
		}
		if recState.State != delivery.StateSent {
			t.Errorf("expected StateSent, got %s", recState.State)
		}

		// Verify Telegram UI has Telegram settings card
		idxReq := httptest.NewRequest(http.MethodGet, "/telegram", nil)
		idxRec := httptest.NewRecorder()
		srv.ServeHTTP(idxRec, idxReq)
		idxHTML := idxRec.Body.String()
		if !strings.Contains(idxHTML, "Telegram Delivery Configuration") {
			t.Errorf("Telegram UI missing Telegram Delivery Configuration card")
		}
		if !strings.Contains(idxHTML, "Discover Chats") {
			t.Errorf("Telegram UI missing Discover Chats button")
		}

		// Verify Inbox UI has Telegram delivery badge
		inboxReq := httptest.NewRequest(http.MethodGet, "/inbox", nil)
		inboxRec := httptest.NewRecorder()
		srv.ServeHTTP(inboxRec, inboxReq)
		inboxHTML := inboxRec.Body.String()
		if !strings.Contains(inboxHTML, "✈ Telegram: Sent") {
			t.Errorf("Inbox UI missing Telegram Sent badge: %s", inboxHTML)
		}

		// Verify Detail UI has Telegram Delivery card and Sent status
		detailReq := httptest.NewRequest(http.MethodGet, "/digests/"+batchID, nil)
		detailRec := httptest.NewRecorder()
		srv.ServeHTTP(detailRec, detailReq)
		detailHTML := detailRec.Body.String()
		if !strings.Contains(detailHTML, "Telegram Delivery") || !strings.Contains(detailHTML, "All parts delivered to Telegram") {
			t.Errorf("Detail UI missing Telegram delivery status: %s", detailHTML)
		}

		// 13. Verify secret hygiene: raw secret tokens are NEVER rendered into HTML
		if strings.Contains(idxHTML, "my-secret-tg-token") || strings.Contains(idxHTML, "multipart-secret-token") {
			t.Fatalf("CRITICAL: Secret Telegram token leaked into Index HTML!")
		}
		if strings.Contains(inboxHTML, "my-secret-tg-token") || strings.Contains(detailHTML, "my-secret-tg-token") {
			t.Fatalf("CRITICAL: Secret Telegram token leaked into Inbox/Detail HTML!")
		}
	})

	// 14. Requirements 7 & 8: Channels UI defense-in-depth filters hidden Discord channel sentinels
	t.Run("Channels UI filters hidden channel sentinels from catalog", func(t *testing.T) {
		catData := `{
  "version": 1,
  "updated_at": "2026-09-06T00:00:00Z",
  "guilds": [
    {
      "id": "g_test",
      "name": "Test Server",
      "channels": [
        {"id": "ch_legit_1", "name": "general", "type": 0},
        {"id": "ch_legit_2", "name": "📡  Hidden Signal  📡", "type": 0},
        {"id": "ch_invis_1", "name": "___hidden___", "type": 0},
        {"id": "ch_invis_2", "name": "__hidden__", "type": 0}
      ]
    }
  ]
}`
		if err := os.WriteFile(filepath.Join(exchangeDir, "catalog.json"), []byte(catData), 0644); err != nil {
			t.Fatal(err)
		}

		idxReq := httptest.NewRequest(http.MethodGet, "/channels", nil)
		idxRec := httptest.NewRecorder()
		srv.ServeHTTP(idxRec, idxReq)

		if idxRec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", idxRec.Code)
		}

		body := idxRec.Body.String()
		// Legitimate channels must be rendered
		if !strings.Contains(body, "#general") {
			t.Errorf("expected #general rendered in channel picker")
		}
		if !strings.Contains(body, "#📡  Hidden Signal  📡") {
			t.Errorf("expected legitimate channel '#📡  Hidden Signal  📡' rendered in channel picker")
		}
		// Inaccessible sentinels must NOT be rendered
		if strings.Contains(body, "___hidden___") {
			t.Errorf("CRITICAL: Sentinel '___hidden___' was rendered in channel picker: %s", body)
		}
		if strings.Contains(body, "__hidden__") {
			t.Errorf("CRITICAL: Sentinel '__hidden__' was rendered in channel picker: %s", body)
		}
	})
}

func TestServer_NavigationArchitecture(t *testing.T) {
	srv, _, _ := setupTestEnv(t)

	cases := []struct {
		path         string
		expectedNav  string
		expectedText string
	}{
		{"/", "overview", "Overview"},
		{"/overview", "overview", "Overview"},
		{"/inbox", "inbox", "Inbox"},
		{"/channels", "channels", "Watched Channels"},
		{"/schedule", "schedule", "Schedule"},
		{"/provider", "provider", "LLM Synthesis Provider"},
		{"/telegram", "telegram", "Telegram Delivery Configuration"},
		{"/system", "system", "System Status"},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 OK for %s, got %d", tc.path, rec.Code)
			}

			body := rec.Body.String()

			// 1. Sidebar shell present
			if !strings.Contains(body, `<aside class="sidebar">`) {
				t.Errorf("missing sidebar shell on %s", tc.path)
			}

			// 2. Active nav link highlighted
			expectedActiveLink := fmt.Sprintf(`href="%s" class="nav-item active"`, tc.path)
			if tc.path == "/overview" {
				expectedActiveLink = `href="/" class="nav-item active"`
			}
			if !strings.Contains(body, expectedActiveLink) {
				t.Errorf("missing active nav link on %s: expected %s in HTML", tc.path, expectedActiveLink)
			}

			// 3. Expected page content present
			if !strings.Contains(body, tc.expectedText) {
				t.Errorf("missing expected page text %q on %s", tc.expectedText, tc.path)
			}
		})
	}
}

func TestServer_SystemPageSecretHygiene(t *testing.T) {
	srv, _, _ := setupTestEnv(t)

	envKey := "super-secret-env-gemini-key-12345"
	storedKey := "super-secret-stored-gemini-key-67890"
	tgToken := "987654321:super-secret-tg-token-abcdef"

	t.Setenv("GEMINI_API_KEY", envKey)
	_ = srv.store.SaveGeminiKey(storedKey)
	_ = srv.store.SaveTelegramBotToken(tgToken)

	req := httptest.NewRequest(http.MethodGet, "/system", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /system, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Verify system diagnostic cards
	if !strings.Contains(body, "Collector Daemon") {
		t.Errorf("missing Collector Daemon card in /system")
	}
	if !strings.Contains(body, "Journal &amp; Storage Telemetry") && !strings.Contains(body, "Journal & Storage Telemetry") {
		t.Errorf("missing Journal & Storage Telemetry card in /system")
	}
	if !strings.Contains(body, "Core Runtime Service") {
		t.Errorf("missing Core Runtime Service card in /system")
	}
	if !strings.Contains(body, "Security &amp; Secret Hygiene") && !strings.Contains(body, "Security & Secret Hygiene") {
		t.Errorf("missing Security card in /system")
	}

	// Verify ZERO secret leaks
	if strings.Contains(body, envKey) {
		t.Fatalf("SECURITY VIOLATION: Environment Gemini key leaked in /system HTML!")
	}
	if strings.Contains(body, storedKey) {
		t.Fatalf("SECURITY VIOLATION: Stored Gemini key leaked in /system HTML!")
	}
	if strings.Contains(body, tgToken) {
		t.Fatalf("SECURITY VIOLATION: Telegram bot token leaked in /system HTML!")
	}
	if strings.Contains(body, "super-secret") {
		t.Fatalf("SECURITY VIOLATION: Secret fragment leaked in /system HTML!")
	}
}

func TestServer_DedicatedSecretStatusAndResolution(t *testing.T) {
	// A, B, C: Gemini Provider secret status on /provider
	t.Run("Gemini secret status states", func(t *testing.T) {
		srv, _, _ := setupTestEnv(t)

		// C. Gemini absent -> /provider says "Not Configured"
		req := httptest.NewRequest(http.MethodGet, "/provider", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if !strings.Contains(rec.Body.String(), "Not Configured") {
			t.Errorf("expected 'Not Configured' when Gemini key is absent")
		}

		// B. Gemini stored key present -> /provider says "Configured (stored in secrets.json)"
		_ = srv.store.SaveGeminiKey("stored-gemini-secret-999")
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/provider", nil)
		srv.ServeHTTP(rec, req)
		body := rec.Body.String()
		if !strings.Contains(body, "Configured (stored in secrets.json)") {
			t.Errorf("expected 'Configured (stored in secrets.json)' when key is stored")
		}

		// A. Gemini env key present -> /provider says "Configured (via environment)"
		t.Setenv("GEMINI_API_KEY", "env-gemini-secret-888")
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/provider", nil)
		srv.ServeHTTP(rec, req)
		body = rec.Body.String()
		if !strings.Contains(body, "Configured (via environment)") {
			t.Errorf("expected 'Configured (via environment)' when env key is set")
		}
		if !strings.Contains(body, "disabled") {
			t.Errorf("expected input to be disabled when env key is set")
		}

		// I. No rendered page contains raw secret values
		if strings.Contains(body, "stored-gemini-secret-999") || strings.Contains(body, "env-gemini-secret-888") {
			t.Fatalf("CRITICAL: raw Gemini secret leaked in /provider HTML")
		}
	})

	// D, E, F: Telegram secret status on /telegram
	t.Run("Telegram secret status states", func(t *testing.T) {
		srv, _, _ := setupTestEnv(t)

		// F. Telegram absent -> /telegram says "NOT CONFIGURED"
		req := httptest.NewRequest(http.MethodGet, "/telegram", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if !strings.Contains(rec.Body.String(), "NOT CONFIGURED") {
			t.Errorf("expected 'NOT CONFIGURED' when Telegram token is absent")
		}

		// D. Telegram stored key present -> /telegram says "CONFIGURED (STORED IN SECRETS.JSON)"
		_ = srv.store.SaveTelegramBotToken("111111:stored-token-secret")
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/telegram", nil)
		srv.ServeHTTP(rec, req)
		body := rec.Body.String()
		if !strings.Contains(body, "CONFIGURED (STORED IN SECRETS.JSON)") {
			t.Errorf("expected 'CONFIGURED (STORED IN SECRETS.JSON)' when token is stored in secrets.json, got: %s", body)
		}

		// E. Telegram env token present -> /telegram says "CONFIGURED (FROM ENVIRONMENT)"
		t.Setenv("TELEGRAM_BOT_TOKEN", "222222:env-token-secret")
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/telegram", nil)
		srv.ServeHTTP(rec, req)
		body = rec.Body.String()
		if !strings.Contains(body, "CONFIGURED (FROM ENVIRONMENT)") {
			t.Errorf("expected 'CONFIGURED (FROM ENVIRONMENT)' when token is in env, got: %s", body)
		}

		// I. No rendered page contains raw secret values
		if strings.Contains(body, "stored-token-secret") || strings.Contains(body, "env-token-secret") {
			t.Fatalf("CRITICAL: raw Telegram token leaked in /telegram HTML")
		}
	})

	// G: Test Connection uses resolved Gemini secret source
	t.Run("Test Connection uses resolved Gemini secret source", func(t *testing.T) {
		srv, _, _ := setupTestEnv(t)

		// Absent key -> returns error
		form := url.Values{}
		form.Set("provider", "gemini")
		req := httptest.NewRequest(http.MethodPost, "/api/llm/test", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Origin", "http://127.0.0.1:8080")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if !strings.Contains(rec.Body.String(), "GEMINI_API_KEY is not configured") {
			t.Errorf("expected error when key is absent, got: %s", rec.Body.String())
		}

		// Stored key -> store.GetGeminiKey() is used
		_ = srv.store.SaveGeminiKey("stored-key-active")
		if srv.store.GetGeminiKey() != "stored-key-active" {
			t.Errorf("expected store.GetGeminiKey() to return stored key")
		}

		// Env key takes precedence over stored key
		t.Setenv("GEMINI_API_KEY", "env-key-precedence")
		if srv.store.GetGeminiKey() != "env-key-precedence" {
			t.Errorf("expected store.GetGeminiKey() to return env key over stored key")
		}
	})

	// H: Telegram Test Ping uses resolved Telegram token source
	t.Run("Telegram Test Ping uses resolved Telegram token source", func(t *testing.T) {
		srv, _, _ := setupTestEnv(t)

		// Absent token -> returns error
		form := url.Values{}
		form.Set("chat_id", "123456")
		req := httptest.NewRequest(http.MethodPost, "/api/telegram/send-test", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Origin", "http://127.0.0.1:8080")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if !strings.Contains(rec.Body.String(), "telegram bot token is not configured") {
			t.Errorf("expected error when token is absent, got: %s", rec.Body.String())
		}

		// Stored token -> store.GetTelegramBotToken() is used
		_ = srv.store.SaveTelegramBotToken("stored-bot-token-active")
		if srv.store.GetTelegramBotToken() != "stored-bot-token-active" {
			t.Errorf("expected store.GetTelegramBotToken() to return stored token")
		}

		// Env token takes precedence over stored token
		t.Setenv("TELEGRAM_BOT_TOKEN", "env-bot-token-precedence")
		if srv.store.GetTelegramBotToken() != "env-bot-token-precedence" {
			t.Errorf("expected store.GetTelegramBotToken() to return env token over stored token")
		}
	})
}

func TestBatchIDValidation(t *testing.T) {
	srv, _, _ := setupTestEnv(t)

	invalidIDs := []string{
		"invalid-id",
		"0123456789abcdef",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdefg", // non-hex
		strings.Repeat("A", 64), // uppercase not allowed
		"etc_passwd",
	}

	for _, id := range invalidIDs {
		// GET /digests/<id> -> 404
		req := httptest.NewRequest(http.MethodGet, "/digests/"+id, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /digests/%s: expected 404, got %d", id, rec.Code)
		}

		// POST /api/digests/<id> -> 400 Bad Request
		postReq := httptest.NewRequest(http.MethodPost, "/api/digests/"+id, nil)
		postReq.Host = "127.0.0.1:8080"
		postReq.Header.Set("Origin", "http://127.0.0.1:8080")
		postRec := httptest.NewRecorder()
		srv.ServeHTTP(postRec, postReq)
		if postRec.Code != http.StatusBadRequest {
			t.Errorf("POST /api/digests/%s: expected 400, got %d", id, postRec.Code)
		}
	}

	// Traversal attempt -> blocked (cannot return 200 OK)
	traversalReq := httptest.NewRequest(http.MethodGet, "/digests/../../etc/passwd", nil)
	traversalRec := httptest.NewRecorder()
	srv.ServeHTTP(traversalRec, traversalReq)
	if traversalRec.Code == http.StatusOK {
		t.Errorf("expected traversal attempt to be blocked, got 200 OK")
	}

	// Valid 64-char hex batch ID
	validID := strings.Repeat("a", 64)
	req := httptest.NewRequest(http.MethodGet, "/digests/"+validID, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	// Not found on disk, but passed validation (404, not 400)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for valid nonexistent batch, got %d", rec.Code)
	}
}

func TestCalculateBacklog(t *testing.T) {
	srv, exchangeDir, _ := setupTestEnv(t)
	eventsDir := filepath.Join(exchangeDir, "events")

	// Segment 1: 400 bytes
	seg1 := filepath.Join(eventsDir, "0000000000000001.ndjson")
	_ = os.WriteFile(seg1, bytes.Repeat([]byte("a"), 400), 0644)

	// Segment 2: 600 bytes
	seg2 := filepath.Join(eventsDir, "0000000000000002.ndjson")
	_ = os.WriteFile(seg2, bytes.Repeat([]byte("b"), 600), 0644)

	// Cursor at segment 1, offset 100
	ackPath := filepath.Join(exchangeDir, "core-ack.json")
	cur := &journal.Cursor{
		Version: 1,
		Segment: 1,
		Offset:  100,
	}
	_ = journal.SaveCursor(ackPath, cur)

	// 1. Overview page: unconsumed should be (400-100) + 600 = 900 bytes
	req := httptest.NewRequest(http.MethodGet, "/overview", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("overview failed: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "900") {
		t.Errorf("expected overview to display 900 bytes backlog: %s", body)
	}

	// 2. System page: unconsumed 900, total 1000
	sysReq := httptest.NewRequest(http.MethodGet, "/system", nil)
	sysRec := httptest.NewRecorder()
	srv.ServeHTTP(sysRec, sysReq)
	if sysRec.Code != http.StatusOK {
		t.Fatalf("system failed: %d", sysRec.Code)
	}
	sysBody := sysRec.Body.String()
	if !strings.Contains(sysBody, "900") {
		t.Errorf("expected system to display 900 unconsumed bytes: %s", sysBody)
	}
	if !strings.Contains(sysBody, "1000") && !strings.Contains(sysBody, "1,000") && !strings.Contains(sysBody, "1.0 KB") {
		t.Errorf("expected system to display 1000 bytes total size: %s", sysBody)
	}
}

func TestWebRequestLimitsAndTimeouts(t *testing.T) {
	srv, _, _ := setupTestEnv(t)

	// 1. Send body > 128KB (e.g. 150KB)
	largeBody := strings.Repeat("channels=c1&", 15000)
	req := httptest.NewRequest(http.MethodPost, "/api/watchlist", strings.NewReader(largeBody))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://127.0.0.1:8080")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 RequestEntityTooLarge for oversized body, got %d", rec.Code)
	}

	// 2. Normal-sized body succeeds
	normalBody := "channels=c1"
	reqNormal := httptest.NewRequest(http.MethodPost, "/api/watchlist", strings.NewReader(normalBody))
	reqNormal.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqNormal.Host = "127.0.0.1:8080"
	reqNormal.Header.Set("Origin", "http://127.0.0.1:8080")

	recNormal := httptest.NewRecorder()
	srv.ServeHTTP(recNormal, reqNormal)

	if recNormal.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 SeeOther for normal body, got %d", recNormal.Code)
	}
}

func TestCSRFProtectionAndThreatModel(t *testing.T) {
	srv, _, _ := setupTestEnv(t)

	body := "channels=c1"

	// Case 1: Sec-Fetch-Site: cross-site -> 403
	{
		req := httptest.NewRequest(http.MethodPost, "/api/watchlist", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Origin", "http://127.0.0.1:8080")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 on Sec-Fetch-Site: cross-site, got %d", rec.Code)
		}
	}

	// Case 2: Origin: null -> 403
	{
		req := httptest.NewRequest(http.MethodPost, "/api/watchlist", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "null")
		req.Host = "127.0.0.1:8080"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 on Origin: null, got %d", rec.Code)
		}
	}

	// Case 3: Origin mismatch -> 403
	{
		req := httptest.NewRequest(http.MethodPost, "/api/watchlist", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://attacker.com")
		req.Host = "127.0.0.1:8080"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 on mismatched Origin, got %d", rec.Code)
		}
	}

	// Case 4: Origin matches Host -> 303 (Allowed)
	{
		req := httptest.NewRequest(http.MethodPost, "/api/watchlist", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://127.0.0.1:8080")
		req.Host = "127.0.0.1:8080"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("expected 303 on matching Origin, got %d", rec.Code)
		}
	}

	// Case 5: No Origin, Referer mismatch -> 403
	{
		req := httptest.NewRequest(http.MethodPost, "/api/watchlist", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Referer", "http://attacker.com/evil")
		req.Host = "127.0.0.1:8080"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 on mismatched Referer, got %d", rec.Code)
		}
	}

	// Case 6: No Origin, Referer matches Host -> 303 (Allowed)
	{
		req := httptest.NewRequest(http.MethodPost, "/api/watchlist", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Referer", "http://127.0.0.1:8080/channels")
		req.Host = "127.0.0.1:8080"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("expected 303 on matching Referer, got %d", rec.Code)
		}
	}

	// Case 7: No Origin, No Referer, No browser Sec headers (local CLI / curl) -> 303 (Allowed)
	{
		req := httptest.NewRequest(http.MethodPost, "/api/watchlist", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = "127.0.0.1:8080"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("expected 303 for local CLI without Origin/Referer, got %d", rec.Code)
		}
	}
}
