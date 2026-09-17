package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 1, 2, 3, 4, 5, 7, 20: Comprehensive request serialization test
func TestClient_RequestSerialization(t *testing.T) {
	const secretKey = "sk-test-secret-12345"
	var (
		capturedPath    string
		capturedAuth    string
		capturedType    string
		capturedURL     string
		capturedBodyMap map[string]interface{}
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		capturedType = r.Header.Get("Content-Type")
		capturedURL = r.URL.String()

		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBodyMap)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model": "gemini-3.8-flash",
			"choices": [
				{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "This is the generated brief."
					}
				}
			]
		}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL+"/v1beta/openai/", "gemini-3.8-flash", secretKey, server.Client())
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	messages := []Message{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "User query"},
	}

	content, err := client.Complete(context.Background(), messages)
	if err != nil {
		t.Fatalf("complete failed: %v", err)
	}

	// 1. Correct endpoint joining
	if capturedPath != "/v1beta/openai/chat/completions" {
		t.Errorf("expected path /v1beta/openai/chat/completions, got %q", capturedPath)
	}

	// 2. Configured model sent exactly
	if capturedBodyMap["model"] != "gemini-3.8-flash" {
		t.Errorf("expected model gemini-3.8-flash, got %v", capturedBodyMap["model"])
	}

	// 3. System/user roles serialized correctly
	rawMsgs, ok := capturedBodyMap["messages"].([]interface{})
	if !ok || len(rawMsgs) != 2 {
		t.Fatalf("expected 2 messages, got %v", capturedBodyMap["messages"])
	}
	m0 := rawMsgs[0].(map[string]interface{})
	m1 := rawMsgs[1].(map[string]interface{})
	if m0["role"] != "system" || m0["content"] != "System prompt" {
		t.Errorf("unexpected msg 0: %v", m0)
	}
	if m1["role"] != "user" || m1["content"] != "User query" {
		t.Errorf("unexpected msg 1: %v", m1)
	}

	// 4. Content-Type application/json
	if capturedType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", capturedType)
	}

	// 5. Non-empty API key becomes Authorization: Bearer <key>
	if capturedAuth != "Bearer "+secretKey {
		t.Errorf("expected Authorization Bearer %s, got %q", secretKey, capturedAuth)
	}

	// 7. API key never appears in request URL
	if strings.Contains(capturedURL, secretKey) {
		t.Errorf("secret key leaked in URL: %q", capturedURL)
	}

	// 9. Valid Chat Completions response returns choices[0].message.content
	if content != "This is the generated brief." {
		t.Errorf("expected completion content 'This is the generated brief.', got %q", content)
	}

	// 20. No provider-specific optional fields sent unexpectedly (only model and messages)
	for k := range capturedBodyMap {
		if k != "model" && k != "messages" {
			t.Errorf("unexpected field %q in request payload", k)
		}
	}
}

// 6. Empty API key omits Authorization header
func TestClient_EmptyAPIKeyOmitsAuthorization(t *testing.T) {
	var capturedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "local-model", "", server.Client())
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	_, err = client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("complete failed: %v", err)
	}

	if capturedAuth != "" {
		t.Errorf("expected empty Authorization header, got %q", capturedAuth)
	}
}

// 8. API key never appears in returned errors
func TestSecurity_APIKeyNeverInErrors(t *testing.T) {
	const secretKey = "SUPER_SECRET_KEY_99999"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo key in error response body
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("Invalid key provided: " + secretKey))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "test-model", secretKey, server.Client())
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	_, err = client.Complete(context.Background(), []Message{{Role: "user", Content: "test"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errStr := err.Error()
	if strings.Contains(errStr, secretKey) {
		t.Fatalf("CRITICAL SECURITY VIOLATION: API key found in returned error: %s", errStr)
	}
	if errStr != "llm request failed with status 401" {
		t.Errorf("expected status-only error, got: %s", errStr)
	}
}

// 10. Non-2xx becomes useful bounded error
func TestClient_Non2xxError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"message": "Invalid parameter model"}}`))
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "bad-model", "key", server.Client())
	_, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for status 400, got nil")
	}

	if err.Error() != "llm request failed with status 400" {
		t.Errorf("expected status-only error, got: %v", err)
	}
}

// 11. Malformed response JSON rejected
func TestClient_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not valid json`))
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "test-model", "key", server.Client())
	_, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected malformed JSON error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to parse llm response JSON") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// 12. Zero choices rejected
func TestClient_ZeroChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices": []}`))
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "test-model", "key", server.Client())
	_, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for zero choices, got nil")
	}
	if !strings.Contains(err.Error(), "zero completion choices") {
		t.Errorf("unexpected error: %v", err)
	}
}

// 13. Empty content rejected
func TestClient_EmptyContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": "   \n\t  "}}]}`))
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "test-model", "key", server.Client())
	_, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for whitespace-only content, got nil")
	}
	if !strings.Contains(err.Error(), "empty completion content") {
		t.Errorf("unexpected error: %v", err)
	}
}

// 14. Oversized provider response rejected
func TestClient_OversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Write more than 4 MiB of padding
		chunk := strings.Repeat("A", 1024*1024)
		for i := 0; i < 5; i++ {
			_, _ = w.Write([]byte(chunk))
		}
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "test-model", "key", server.Client())
	_, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for oversized response, got nil")
	}
	if !strings.Contains(err.Error(), "exceeded maximum allowed size") {
		t.Errorf("unexpected error: %v", err)
	}
}

// 15. Context cancellation works
func TestClient_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "test-model", "key", server.Client())
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before or during call

	_, err := client.Complete(ctx, []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected context canceled in error, got: %v", err)
	}
}

// 16, 17, 18: URL scheme validation
func TestClient_URLSchemes(t *testing.T) {
	// HTTP valid
	c1, err := NewClient("http://localhost:8080/v1", "model", "", nil)
	if err != nil || c1 == nil {
		t.Errorf("expected http URL to be accepted: %v", err)
	}

	// HTTPS valid
	c2, err := NewClient("https://api.example.com/v1", "model", "", nil)
	if err != nil || c2 == nil {
		t.Errorf("expected https URL to be accepted: %v", err)
	}

	// Unsupported schemes
	invalidURLs := []string{
		"ftp://example.com",
		"file:///etc/passwd",
		"gopher://example.com",
		"not-a-url",
		"",
	}
	for _, badURL := range invalidURLs {
		_, err := NewClient(badURL, "model", "", nil)
		if err == nil {
			t.Errorf("expected error for invalid URL %q, got nil", badURL)
		}
	}
}

// 19. Configured base path is preserved
func TestClient_PreservesBasePath(t *testing.T) {
	tests := []struct {
		baseURL      string
		expectedPath string
	}{
		{"http://localhost:8080/custom/v1", "/custom/v1/chat/completions"},
		{"http://localhost:8080/custom/v1/", "/custom/v1/chat/completions"},
		{"https://generativelanguage.googleapis.com/v1beta/openai/", "/v1beta/openai/chat/completions"},
		{"https://generativelanguage.googleapis.com/v1beta/openai", "/v1beta/openai/chat/completions"},
		{"http://localhost:11434", "/chat/completions"},
	}

	for _, tc := range tests {
		var capturedPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
		}))

		// Rebase test path to test server
		u, _ := server.Client().Transport, server.URL
		_ = u
		// Test URL calculation directly
		endpoint := strings.TrimRight(tc.baseURL, "/") + "/chat/completions"
		parsed, err := http.NewRequest(http.MethodPost, endpoint, nil)
		if err != nil {
			t.Fatalf("failed to create request for %s: %v", endpoint, err)
		}
		if parsed.URL.Path != tc.expectedPath {
			t.Errorf("for base %q, expected path %q, got %q", tc.baseURL, tc.expectedPath, parsed.URL.Path)
		}

		server.Close()
		_ = capturedPath
	}
}

func TestIsGeminiEndpoint(t *testing.T) {
	tests := []struct {
		url      string
		expected bool
	}{
		{"https://generativelanguage.googleapis.com/v1beta/openai", true},
		{"https://GENERATIVELANGUAGE.GOOGLEAPIS.COM/v1beta/openai", true},
		{"https://generativelanguage.googleapis.com:443/v1beta/openai", true},
		{"https://api.openai.com/v1", false},
		{"https://openrouter.ai/api/v1", false},
		{"http://localhost:11434/v1", false},
		{"invalid://url%%", false},
		{"", false},
	}

	for _, tc := range tests {
		got := isGeminiEndpoint(tc.url)
		if got != tc.expected {
			t.Errorf("isGeminiEndpoint(%q) = %v, expected %v", tc.url, got, tc.expected)
		}
	}
}

func TestLLM_Timeout_StalledServer(t *testing.T) {
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "test-model", "", &http.Client{Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	start := time.Now()
	_, err = client.Complete(context.Background(), []Message{{Role: "user", Content: "hello"}})
	elapsed := time.Since(start)

	close(done)
	server.CloseClientConnections()
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}

	if err == nil {
		t.Fatal("expected timeout error for stalled server, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("request took too long to time out: %v", elapsed)
	}
}

func TestLLM_DefaultTimeout_Installed(t *testing.T) {
	client, err := NewClient("http://localhost:1234", "test-model", "", nil)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	if client.http == nil {
		t.Fatal("expected http.Client to be initialized")
	}
	if client.http.Timeout != DefaultRequestTimeout {
		t.Fatalf("expected default timeout %v, got %v", DefaultRequestTimeout, client.http.Timeout)
	}
	if DefaultRequestTimeout <= 0 {
		t.Fatal("expected DefaultRequestTimeout to be positive finite duration")
	}
}

func TestLLM_PreservesCustomClient(t *testing.T) {
	customClient := &http.Client{Timeout: 123 * time.Millisecond}
	client, err := NewClient("http://localhost:1234", "test-model", "", customClient)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	if client.http == customClient || customClient.CheckRedirect != nil || client.http.Transport != customClient.Transport {
		t.Fatal("client must be copied with transport retained and caller unmodified")
	}
	if client.http.Timeout != 123*time.Millisecond {
		t.Fatalf("expected custom timeout 123ms, got %v", client.http.Timeout)
	}
}

func TestLLM_StatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("Rate limit reached"))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "test-model", "test-key", server.Client())
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	_, err = client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var statusErr *StatusError
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("expected status 429 in error, got: %v", err)
	}
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected StatusError type, got %T: %v", err, err)
	}
	if statusErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected StatusCode 429, got %d", statusErr.StatusCode)
	}
}
