package llm

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestFakeLLMServer(t *testing.T) {
	server := NewFakeLLMServer()
	defer server.Close()

	server.ExpectedKey = "test-llm-key"
	server.ExpectedModel = "llama3"

	// 1. Missing auth -> 401
	payload := []byte(`{"model":"llama3","messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(payload))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 2. Correct auth -> 200 with chat completion response
	req, _ = http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-llm-key")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed decoding response: %v", err)
	}
	resp.Body.Close()

	if len(result.Choices) == 0 || result.Choices[0].Message.Content == "" {
		t.Fatal("expected non-empty message content in completion response")
	}

	// 3. Simulated failure
	server.FailStatus = http.StatusInternalServerError
	req, _ = http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-llm-key")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}
