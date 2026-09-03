package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cordbrief/internal/digest"
)

// Provider abstracts the language model summarizer.
type Provider interface {
	Summarize(ctx context.Context, systemPrompt, userPrompt string) (*digest.Digest, error)
	GenerateText(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// OpenAICompatibleProvider communicates with OpenAI-compatible chat completion endpoints.
type OpenAICompatibleProvider struct {
	BaseURL         string
	Model           string
	APIKey          string
	MaxOutputTokens int
	Timeout         time.Duration
	HTTPClient      *http.Client
}

// NewOpenAICompatibleProvider creates a configured provider with sensible defaults.
func NewOpenAICompatibleProvider(baseURL, model, apiKey string, maxTokens int, timeout time.Duration) *OpenAICompatibleProvider {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	if maxTokens <= 0 {
		maxTokens = 2048
	}
	return &OpenAICompatibleProvider{
		BaseURL:         strings.TrimRight(baseURL, "/"),
		Model:           model,
		APIKey:          apiKey,
		MaxOutputTokens: maxTokens,
		Timeout:         timeout,
		HTTPClient:      &http.Client{Timeout: timeout},
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature"`
}

type chatResponse struct {
	ID      string `json:"id"`
	Error   *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
	Choices []struct {
		Index        int         `json:"index"`
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
}

// GenerateText sends a chat completion request and returns the raw assistant response content.
func (p *OpenAICompatibleProvider) GenerateText(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if p.BaseURL == "" {
		return "", errors.New("llm base_url is not configured")
	}

	endpoint := strings.TrimRight(p.BaseURL, "/")
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}

	reqPayload := chatRequest{
		Model: p.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens:   p.MaxOutputTokens,
		Temperature: 0.2,
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}

	// Bounded retry for transient 5xx / connection drops only (max 1 retry)
	var respContent string
	var lastErr error

	for attempt := 0; attempt <= 1; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(1 * time.Second):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return "", fmt.Errorf("create chat request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		if p.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.APIKey)
		}

		client := p.HTTPClient
		if client == nil {
			client = http.DefaultClient
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("llm request failed: %w", err)
			continue
		}

		// Read response body with safe upper bound (4MB)
		lr := io.LimitReader(resp.Body, 4*1024*1024)
		respBytes, readErr := io.ReadAll(lr)
		_ = resp.Body.Close()

		if readErr != nil {
			lastErr = fmt.Errorf("read llm response: %w", readErr)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			errMsg := fmt.Sprintf("llm endpoint returned HTTP %d", resp.StatusCode)
			var errResp chatResponse
			if err := json.Unmarshal(respBytes, &errResp); err == nil && errResp.Error != nil && errResp.Error.Message != "" {
				errMsg = fmt.Sprintf("llm endpoint error (%d): %s", resp.StatusCode, errResp.Error.Message)
			}
			lastErr = errors.New(errMsg)

			// Only retry transient 5xx errors
			if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout {
				continue
			}
			return "", lastErr
		}

		var chatResp chatResponse
		if err := json.Unmarshal(respBytes, &chatResp); err != nil {
			return "", fmt.Errorf("unmarshal chat response: %w (raw response: %s)", err, string(respBytes))
		}

		if len(chatResp.Choices) == 0 {
			return "", errors.New("llm returned 0 choices")
		}

		respContent = chatResp.Choices[0].Message.Content
		if strings.TrimSpace(respContent) == "" {
			return "", errors.New("llm returned empty content")
		}

		return respContent, nil
	}

	return "", lastErr
}

// CleanJSONResponse extracts JSON from model output, stripping markdown fences if present.
func CleanJSONResponse(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		// Strip opening fence
		idx := strings.Index(s, "\n")
		if idx != -1 {
			s = s[idx+1:]
		}
		// Strip trailing fence
		if endIdx := strings.LastIndex(s, "```"); endIdx != -1 {
			s = s[:endIdx]
		}
	}
	return strings.TrimSpace(s)
}

// Summarize requests a structured digest from the model with a single controlled repair attempt on syntax errors.
func (p *OpenAICompatibleProvider) Summarize(ctx context.Context, systemPrompt, userPrompt string) (*digest.Digest, error) {
	raw, err := p.GenerateText(ctx, systemPrompt, userPrompt)
	if err != nil {
		return nil, err
	}

	cleaned := CleanJSONResponse(raw)
	var d digest.Digest
	if err := json.Unmarshal([]byte(cleaned), &d); err == nil {
		return &d, nil
	} else {
		// Single controlled repair attempt
		repairUserPrompt := BuildRepairPrompt(cleaned, err)
		repairedRaw, repairErr := p.GenerateText(ctx, systemPrompt, repairUserPrompt)
		if repairErr != nil {
			return nil, fmt.Errorf("initial parse error (%v); repair request failed: %w", err, repairErr)
		}

		cleanedRepair := CleanJSONResponse(repairedRaw)
		var repairedDigest digest.Digest
		if unmarshalErr := json.Unmarshal([]byte(cleanedRepair), &repairedDigest); unmarshalErr != nil {
			return nil, fmt.Errorf("malformed JSON from model after repair attempt: %w (output: %s)", unmarshalErr, cleanedRepair)
		}
		return &repairedDigest, nil
	}
}
