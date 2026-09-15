package llm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pyed/CordBrief/internal/llm"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestListModels(t *testing.T) {
	t.Run("generic provider preserves opaque model IDs without rewriting", func(t *testing.T) {
		var capturedPath string
		var capturedAuth string

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedPath = r.URL.Path
			capturedAuth = r.Header.Get("Authorization")

			payload := map[string]any{
				"object": "list",
				"data": []map[string]any{
					{"id": "models/custom-model", "owned_by": "custom"},
					{"id": "meta-llama/Llama-3-8b", "owned_by": "meta"},
					{"id": "models/custom-model", "owned_by": "custom"}, // duplicate
					{"id": "   "}, // whitespace
					{"id": "gpt-4o", "owned_by": "openai"},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(payload)
		}))
		defer ts.Close()

		baseURL := ts.URL + "/v1"
		client, err := llm.NewClient(baseURL, "default-model", "test-key-12345", ts.Client())
		if err != nil {
			t.Fatalf("failed to create client: %v", err)
		}

		models, err := client.ListModels(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if capturedPath != "/v1/models" {
			t.Errorf("expected path /v1/models, got %q", capturedPath)
		}
		if capturedAuth != "Bearer test-key-12345" {
			t.Errorf("expected Bearer auth, got %q", capturedAuth)
		}

		if len(models) != 3 {
			t.Fatalf("expected 3 models, got %d: %+v", len(models), models)
		}
		// Exact opaque ID preservation:
		if models[0].ProviderID != "models/custom-model" || models[0].ModelID != "models/custom-model" {
			t.Errorf("model 0 mismatch: %+v", models[0])
		}
		if models[1].ProviderID != "meta-llama/Llama-3-8b" || models[1].ModelID != "meta-llama/Llama-3-8b" {
			t.Errorf("model 1 mismatch: %+v", models[1])
		}
		if models[2].ProviderID != "gpt-4o" || models[2].ModelID != "gpt-4o" {
			t.Errorf("model 2 mismatch: %+v", models[2])
		}
	})

	t.Run("gemini provider normalizes models prefix and deduplicates", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			payload := map[string]any{
				"object": "list",
				"data": []map[string]any{
					{"id": "models/gemini-3.8-flash", "owned_by": "google"},
					{"id": "models/gemini-3.5-flash", "owned_by": "google"},
					{"id": "gemini-3.8-flash", "owned_by": "google"}, // duplicate after normalization
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(payload)
		}))
		defer ts.Close()

		tsURL, _ := url.Parse(ts.URL)
		mockTransport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme = tsURL.Scheme
			r.URL.Host = tsURL.Host
			return ts.Client().Transport.RoundTrip(r)
		})
		httpClient := &http.Client{Transport: mockTransport}

		client, err := llm.NewClient("https://generativelanguage.googleapis.com/v1beta/openai", "default", "key", httpClient)
		if err != nil {
			t.Fatalf("failed to create client: %v", err)
		}

		models, err := client.ListModels(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(models) != 2 {
			t.Fatalf("expected 2 deduplicated models, got %d: %+v", len(models), models)
		}
		if models[0].ProviderID != "models/gemini-3.8-flash" || models[0].ModelID != "gemini-3.8-flash" {
			t.Errorf("model 0 mismatch: %+v", models[0])
		}
		if models[1].ProviderID != "models/gemini-3.5-flash" || models[1].ModelID != "gemini-3.5-flash" {
			t.Errorf("model 1 mismatch: %+v", models[1])
		}
	})

	t.Run("omits Authorization header when api key is empty", func(t *testing.T) {
		var hasAuth bool
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, hasAuth = r.Header["Authorization"]
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		}))
		defer ts.Close()

		client, err := llm.NewClient(ts.URL, "model", "", ts.Client())
		if err != nil {
			t.Fatalf("failed to create client: %v", err)
		}

		_, err = client.ListModels(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if hasAuth {
			t.Error("expected no Authorization header when apiKey is empty")
		}
	})

	t.Run("redacts api key from transport and provider errors", func(t *testing.T) {
		apiKey := "super-secret-api-key-999"
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintf(w, `{"error": "invalid key: %s"}`, apiKey)
		}))
		defer ts.Close()

		client, err := llm.NewClient(ts.URL, "model", apiKey, ts.Client())
		if err != nil {
			t.Fatalf("failed to create client: %v", err)
		}

		_, err = client.ListModels(context.Background())
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		if strings.Contains(err.Error(), apiKey) {
			t.Fatalf("sensitive api key leaked in error: %s", err.Error())
		}
		if !strings.Contains(err.Error(), "[REDACTED]") {
			t.Fatalf("expected [REDACTED] in error, got: %s", err.Error())
		}
	})

	t.Run("context cancellation aborts request", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(100 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		client, _ := llm.NewClient(ts.URL, "model", "key", ts.Client())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := client.ListModels(ctx)
		if err == nil {
			t.Fatal("expected cancellation error, got nil")
		}
	})

	t.Run("malformed json returns descriptive error", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("not valid json"))
		}))
		defer ts.Close()

		client, _ := llm.NewClient(ts.URL, "model", "key", ts.Client())
		_, err := client.ListModels(context.Background())
		if err == nil {
			t.Fatal("expected error on malformed json, got nil")
		}
		if !strings.Contains(err.Error(), "parse llm models JSON") {
			t.Errorf("unexpected error message: %v", err)
		}
	})
}
