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
)

// Default limits for response bodies.
const (
	// MaxResponseBytes caps successful JSON completions to 4 MiB.
	MaxResponseBytes = 4 * 1024 * 1024
	// MaxErrorBytes caps diagnostic error response bodies to 64 KiB.
	MaxErrorBytes = 64 * 1024
)

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

	model = strings.TrimSpace(model)
	if model == "" {
		return nil, errors.New("model cannot be empty")
	}

	if httpClient == nil {
		httpClient = &http.Client{}
	}

	return &Client{
		baseURL: baseURL,
		model:   model,
		apiKey:  strings.TrimSpace(apiKey),
		http:    httpClient,
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
		diagReader := io.LimitReader(resp.Body, MaxErrorBytes)
		diagBody, _ := io.ReadAll(diagReader)
		diagText := strings.TrimSpace(string(diagBody))
		if diagText != "" {
			return "", c.sanitizeError(fmt.Errorf("llm request failed with status %d: %s", resp.StatusCode, diagText))
		}
		return "", c.sanitizeError(fmt.Errorf("llm request failed with status %d", resp.StatusCode))
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
		return "", c.sanitizeError(fmt.Errorf("failed to parse llm response JSON: %w", err))
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

// sanitizeError strips the API key from any error message.
func (c *Client) sanitizeError(err error) error {
	if err == nil || c.apiKey == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), c.apiKey, "[REDACTED]")
	return errors.New(msg)
}
