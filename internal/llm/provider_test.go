package llm

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"cordbrief/internal/config"
)

func TestProvider_EndpointResolution(t *testing.T) {
	// 1. Gemini official endpoint
	pGemini := NewOpenAICompatibleProvider(config.DefaultGeminiBaseURL, config.DefaultGeminiModel, "test-key", 8192, 30*time.Second)
	if pGemini.BaseURL != "https://generativelanguage.googleapis.com/v1beta/openai" {
		t.Errorf("expected trimmed base URL, got: %s", pGemini.BaseURL)
	}

	// 2. Local Docker-style endpoint
	pLocal := NewOpenAICompatibleProvider("http://host.docker.internal:8081/v1", "custom-model", "", 8192, 30*time.Second)
	if pLocal.BaseURL != "http://host.docker.internal:8081/v1" {
		t.Errorf("expected local base URL, got: %s", pLocal.BaseURL)
	}
}

func TestProvider_ChatCompletionsRoundtrip(t *testing.T) {
	fake := NewFakeLLMServer()
	defer fake.Close()

	fake.ExpectedKey = "secret-gemini-key"
	fake.ExpectedModel = "gemini-3.7-flash"
	fake.ResponseContent = `{"title":"Test Title","overview":"Test Overview","items":[{"kind":"finding","text":"Observed fact","source_ids":["S000001"]}]}`

	p := NewOpenAICompatibleProvider(fake.URL, "gemini-3.7-flash", "secret-gemini-key", 2048, 5*time.Second)
	digestRes, err := p.Summarize(context.Background(), "system prompt", "user prompt")
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}

	if digestRes.Title != "Test Title" {
		t.Errorf("expected title 'Test Title', got: %s", digestRes.Title)
	}
	if len(digestRes.Items) != 1 || digestRes.Items[0].Kind != "finding" {
		t.Errorf("unexpected items: %+v", digestRes.Items)
	}
}

func TestProvider_SecretNeverLeakedInErrors(t *testing.T) {
	fake := NewFakeLLMServer()
	defer fake.Close()

	// Simulate 500 error
	fake.FailStatus = http.StatusInternalServerError

	secretKey := "super-confidential-api-key-12345"
	p := NewOpenAICompatibleProvider(fake.URL, "model", secretKey, 1024, 2*time.Second)

	_, err := p.Summarize(context.Background(), "sys", "user")
	if err == nil {
		t.Fatal("expected error from failed endpoint")
	}

	if strings.Contains(err.Error(), secretKey) {
		t.Fatalf("SECURITY VIOLATION: secret API key leaked in error string: %s", err.Error())
	}
}

func TestProvider_TimeoutContext(t *testing.T) {
	fake := NewFakeLLMServer()
	defer fake.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond)

	p := NewOpenAICompatibleProvider(fake.URL, "model", "", 1024, 5*time.Second)
	_, err := p.Summarize(ctx, "sys", "user")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}
