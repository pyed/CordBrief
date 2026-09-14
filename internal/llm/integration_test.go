package llm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveGeminiProof verifies our Go llm.Client against the official Gemini OpenAI-compatible endpoint.
// It is automatically skipped if LLM_API_KEY is not set.
func TestLiveGeminiProof(t *testing.T) {
	apiKey := os.Getenv("LLM_API_KEY")
	if apiKey == "" {
		t.Skip("skipping live Gemini proof: LLM_API_KEY not set in environment")
	}

	const baseURL = "https://generativelanguage.googleapis.com/v1beta/openai/"
	model := os.Getenv("LLM_MODEL")
	if model == "" {
		model = "gemini-3.5-flash-lite"
	}

	client, err := NewClient(baseURL, model, apiKey, nil)
	if err != nil {
		t.Fatalf("failed to create llm client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	messages := []Message{
		{Role: "system", Content: "You are a helpful assistant. Reply with one short sentence."},
		{Role: "user", Content: "Say hello and confirm you are operational."},
	}

	var resp string
	for attempt := 1; attempt <= 3; attempt++ {
		resp, err = client.Complete(ctx, messages)
		if err == nil {
			break
		}
		if strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), "429") {
			t.Logf("Attempt %d encountered transient error: %v. Retrying...", attempt, err)
			time.Sleep(2 * time.Second)
			continue
		}
		break
	}

	if err != nil {
		t.Fatalf("Gemini live call failed: %v", err)
	}

	t.Logf("Gemini live response: %s", strings.TrimSpace(resp))

	if strings.TrimSpace(resp) == "" {
		t.Fatal("expected non-empty response from Gemini")
	}

	if strings.Contains(resp, apiKey) {
		t.Fatal("CRITICAL: API key found in response content")
	}
}
