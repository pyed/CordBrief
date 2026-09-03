package discord

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestFakeDiscordServer_AuthAndRateLimit(t *testing.T) {
	server := NewFakeDiscordServer()
	defer server.Close()

	server.ExpectedToken = "test-token"

	// 1. Request without auth -> 401
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/channels/123", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 2. Request with correct auth -> 200
	req, _ = http.NewRequest(http.MethodGet, server.URL+"/channels/123", nil)
	req.Header.Set("Authorization", "Bot test-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 3. Rate limit injection -> 429
	server.RateLimitNext = true
	req, _ = http.NewRequest(http.MethodGet, server.URL+"/channels/123", nil)
	req.Header.Set("Authorization", "Bot test-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d", resp.StatusCode)
	}
	var rlResp struct {
		Message    string  `json:"message"`
		RetryAfter float64 `json:"retry_after"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rlResp); err != nil {
		t.Fatalf("failed decoding rate limit body: %v", err)
	}
	resp.Body.Close()
	if rlResp.RetryAfter <= 0 {
		t.Errorf("expected positive retry_after, got %f", rlResp.RetryAfter)
	}

	// 4. Next request should succeed again
	req, _ = http.NewRequest(http.MethodGet, server.URL+"/channels/123", nil)
	req.Header.Set("Authorization", "Bot test-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}
