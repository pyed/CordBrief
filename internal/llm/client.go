package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MaxResponseBytes caps successful JSON completions to 4 MiB.
const MaxResponseBytes = 4 * 1024 * 1024

// DefaultRequestTimeout is the timeout applied to the default HTTP client.
// It bounds stalled requests while providing ample time for completions.
const DefaultRequestTimeout = 60 * time.Second

// StatusError represents an HTTP error response from an LLM provider.
type StatusError struct {
	StatusCode int
	Message    string
}

func (e *StatusError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("llm request failed with status %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("llm request failed with status %d", e.StatusCode)
}

// Message represents a single chat role and textual content.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client is a generic standard-library client for OpenAI-compatible Chat Completions APIs.
type Client struct {
	baseURL string
	model   string
	apiKey  string
	http    *http.Client
}

// NewClient constructs an OpenAI-compatible LLM client.
// baseURL must be a valid http or https URL.
// model must be non-empty.
// apiKey may be empty for unauthenticated local servers (e.g., llama.cpp, vLLM).
func NewClient(baseURL, model, apiKey string, httpClient *http.Client) (*Client, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, errors.New("base URL cannot be empty")
	}

	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid base URL %q: malformed URL", baseURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("invalid base URL %q: unsupported scheme %q (must be http or https)", baseURL, u.Scheme)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("base URL must not contain credentials, query or fragment")
	}

	model = strings.TrimSpace(model)
	if model == "" {
		return nil, errors.New("model cannot be empty")
	}

	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: DefaultRequestTimeout,
		}
	}
	// Never forward either profile's credentials through redirects, even to
	// another port or path on the same host. Do not mutate a supplied client.
	isolatedHTTP := *httpClient
	isolatedHTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return &Client{
		baseURL: baseURL,
		model:   model,
		apiKey:  strings.TrimSpace(apiKey),
		http:    &isolatedHTTP,
	}, nil
}

// Model returns the configured model identifier.
func (c *Client) Model() string {
	return c.model
}

// BaseURL returns the configured base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// HasAPIKey reports whether the client has an API key configured.
func (c *Client) HasAPIKey() bool {
	return c.apiKey != ""
}

// Complete sends a non-streaming Chat Completions request and returns choices[0].message.content.
// It strictly obeys the provided context for timeouts and cancellation.
func (c *Client) Complete(ctx context.Context, messages []Message) (string, error) {
	if len(messages) == 0 {
		return "", errors.New("cannot complete empty message list")
	}

	endpoint := strings.TrimRight(c.baseURL, "/") + "/chat/completions"

	reqPayload := struct {
		Model    string    `json:"model"`
		Messages []Message `json:"messages"`
	}{
		Model:    c.model,
		Messages: messages,
	}

	reqBody, err := json.Marshal(reqPayload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal chat completion request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("failed to create http request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", c.sanitizeError(fmt.Errorf("llm transport error: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", &StatusError{StatusCode: resp.StatusCode}
	}

	// Read response bounded by MaxResponseBytes
	limitedReader := io.LimitReader(resp.Body, MaxResponseBytes+1)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return "", c.sanitizeError(fmt.Errorf("failed to read llm response body: %w", err))
	}
	if int64(len(body)) > MaxResponseBytes {
		return "", errors.New("llm response exceeded maximum allowed size (4 MiB)")
	}

	var respPayload struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(body, &respPayload); err != nil {
		return "", errors.New("failed to parse llm response JSON")
	}

	if len(respPayload.Choices) == 0 {
		return "", errors.New("llm response contained zero completion choices")
	}

	content := respPayload.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" {
		return "", errors.New("llm response returned empty completion content")
	}

	return content, nil
}

type sanitizedError struct {
	err error
	msg string
}

func (s *sanitizedError) Error() string { return s.msg }
func (s *sanitizedError) Unwrap() error { return s.err }

// sanitizeError strips the API key from any error message while preserving StatusError and error wrapping.
func (c *Client) sanitizeError(err error) error {
	if err == nil {
		return nil
	}
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		sanitizedMsg := statusErr.Message
		if c.apiKey != "" && strings.Contains(sanitizedMsg, c.apiKey) {
			sanitizedMsg = strings.ReplaceAll(sanitizedMsg, c.apiKey, "[REDACTED]")
		}
		return &StatusError{
			StatusCode: statusErr.StatusCode,
			Message:    sanitizedMsg,
		}
	}
	if c.apiKey == "" || !strings.Contains(err.Error(), c.apiKey) {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), c.apiKey, "[REDACTED]")
	return &sanitizedError{
		err: err,
		msg: msg,
	}
}

// isGeminiEndpoint reports whether baseURL points to the Google Gemini OpenAI-compatible API.
func isGeminiEndpoint(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "generativelanguage.googleapis.com")
}

// ModelInfo holds identity metadata for an available LLM model.
type ModelInfo struct {
	ProviderID string `json:"provider_id"`
	ModelID    string `json:"model_id"`
}

// ListModels queries the provider's OpenAI-compatible /models endpoint.
// Custom URL paths are preserved, API keys are kept out of query strings,
// and sensitive keys are redacted from all returned errors.
// Model IDs are treated as opaque, except for the verified Google Gemini endpoint
// where the "models/" prefix is stripped to match CordBrief configuration.
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if c == nil || c.baseURL == "" {
		return nil, errors.New("llm client is not initialized")
	}

	endpoint := strings.TrimRight(c.baseURL, "/") + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.sanitizeError(fmt.Errorf("llm transport error: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{StatusCode: resp.StatusCode}
	}

	limitedReader := io.LimitReader(resp.Body, MaxResponseBytes+1)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, c.sanitizeError(fmt.Errorf("failed to read llm response body: %w", err))
	}
	if int64(len(body)) > MaxResponseBytes {
		return nil, errors.New("llm response exceeded maximum allowed size (4 MiB)")
	}

	var respPayload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &respPayload); err != nil {
		return nil, errors.New("failed to parse llm models JSON")
	}

	var models []ModelInfo
	seen := make(map[string]bool)
	for _, item := range respPayload.Data {
		rawID := strings.TrimSpace(item.ID)
		if rawID == "" {
			continue
		}
		modelID := rawID
		if isGeminiEndpoint(c.baseURL) {
			modelID = strings.TrimPrefix(rawID, "models/")
		}
		if modelID == "" {
			continue
		}
		if seen[modelID] {
			continue
		}
		seen[modelID] = true
		models = append(models, ModelInfo{
			ProviderID: rawID,
			ModelID:    modelID,
		})
	}

	return models, nil
}
