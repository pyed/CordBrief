package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
)

// FakeLLMServer provides an httptest.Server simulating OpenAI-compatible chat completion endpoints.
type FakeLLMServer struct {
	Server          *httptest.Server
	URL             string
	mu              sync.Mutex
	Requests        []map[string]any
	ExpectedKey     string
	ExpectedModel   string
	ResponseContent string
	FailStatus      int
}

// NewFakeLLMServer creates and starts a new FakeLLMServer.
func NewFakeLLMServer() *FakeLLMServer {
	fls := &FakeLLMServer{
		ResponseContent: `{"overview":"Test digest overview","items":[{"category":"announcements","text":"All systems normal","source_ids":["m0001"]}],"worth_opening":[{"text":"Check roadmap thread","source_id":"m0002"}]}`,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", fls.handleChatCompletions)
	mux.HandleFunc("/chat/completions", fls.handleChatCompletions)

	fls.Server = httptest.NewServer(mux)
	fls.URL = fls.Server.URL
	return fls
}

// Close shuts down the fake LLM server.
func (fls *FakeLLMServer) Close() {
	fls.Server.Close()
}

func (fls *FakeLLMServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	fls.mu.Lock()
	defer fls.mu.Unlock()

	// Verify authorization if key expected
	if fls.ExpectedKey != "" {
		expectedAuth := "Bearer " + fls.ExpectedKey
		if r.Header.Get("Authorization") != expectedAuth {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": "Incorrect API key provided",
					"type":    "invalid_request_error",
				},
			})
			return
		}
	}

	var reqBody map[string]any
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err == nil {
		fls.Requests = append(fls.Requests, reqBody)
	}

	if fls.FailStatus > 0 {
		status := fls.FailStatus
		fls.FailStatus = 0
		w.WriteHeader(status)
		return
	}

	resp := map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"created": 1677652288,
		"model":   fls.ExpectedModel,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": fls.ResponseContent,
				},
				"finish_reason": "stop",
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
