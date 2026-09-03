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
