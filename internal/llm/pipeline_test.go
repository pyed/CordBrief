package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cordbrief/internal/digest"
)

type mockLLMProvider struct {
	summarizeCalls []string
	generateCalls  []string
	chunkResponse  string
	reduceResponse *digest.Digest
}

func (m *mockLLMProvider) Summarize(ctx context.Context, sys, user string) (*digest.Digest, error) {
	m.summarizeCalls = append(m.summarizeCalls, user)
	if m.reduceResponse != nil {
		return m.reduceResponse, nil
	}
	return &digest.Digest{
		Title:    "Fast Path Digest",
		Overview: "Overview from fast path",
		Items: []digest.Item{
			{Kind: digest.KindFinding, Text: "Obs 1", SourceIDs: []string{"S000001"}},
		},
	}, nil
}

func (m *mockLLMProvider) GenerateText(ctx context.Context, sys, user string) (string, error) {
	m.generateCalls = append(m.generateCalls, user)
	if m.chunkResponse != "" {
		return m.chunkResponse, nil
	}
	// Default chunk response
	cs := ChunkSummary{
		Summary: "Chunk summary",
		Items: []digest.Item{
			{Kind: digest.KindFinding, Text: "Chunk Finding", SourceIDs: []string{"S000001"}},
		},
	}
	b, _ := json.Marshal(cs)
	return string(b), nil
}

func TestPipeline_SingleChunkFastPath(t *testing.T) {
	mock := &mockLLMProvider{}
	pipe := NewPipeline(mock, 10000, PromptConfig{})

	batch := &digest.Batch{
		IncludedMessages: []digest.SourceMessage{
			{SourceID: "S000001", Content: "Short message", Timestamp: time.Now().UTC()},
		},
	}

	d, err := pipe.GenerateDigest(context.Background(), batch)
	if err != nil {
		t.Fatalf("GenerateDigest failed: %v", err)
	}

	if len(mock.summarizeCalls) != 1 {
		t.Errorf("expected 1 summarize call on fast path, got %d", len(mock.summarizeCalls))
	}
	if len(mock.generateCalls) != 0 {
		t.Errorf("expected 0 generate calls on fast path, got %d", len(mock.generateCalls))
	}
	if d.Title != "Fast Path Digest" {
		t.Errorf("unexpected digest title: %s", d.Title)
	}
}

func TestPipeline_ChunkAndReduceWithSourceProvenance(t *testing.T) {
	mock := &mockLLMProvider{
		reduceResponse: &digest.Digest{
			Title:    "Reduced Digest",
			Overview: "Synthesized from chunks",
			Items: []digest.Item{
				{Kind: digest.KindFinding, Text: "Reduced finding", SourceIDs: []string{"S000001", "S000002"}},
			},
		},
	}

	// Set small MaxInputChars to force multi-chunking
	pipe := NewPipeline(mock, 450, PromptConfig{})

	batch := &digest.Batch{
		IncludedMessages: []digest.SourceMessage{
			{SourceID: "S000001", Content: "Discussion 1: measurements of grind speed", Timestamp: time.Now().UTC()},
			{SourceID: "S000002", Content: "Discussion 2: drawdown comparisons", Timestamp: time.Now().UTC()},
		},
	}

	d, err := pipe.GenerateDigest(context.Background(), batch)
	if err != nil {
		t.Fatalf("GenerateDigest failed: %v", err)
	}

	// Should have executed chunk generations and 1 reduce call
	if len(mock.generateCalls) < 2 {
		t.Errorf("expected at least 2 chunk calls, got %d", len(mock.generateCalls))
	}
	if len(mock.summarizeCalls) != 1 {
		t.Errorf("expected 1 reduce summarize call, got %d", len(mock.summarizeCalls))
	}

	// Verify source IDs survived
	if len(d.Items[0].SourceIDs) != 2 || d.Items[0].SourceIDs[0] != "S000001" {
		t.Errorf("source IDs did not survive reduce phase: %+v", d.Items[0].SourceIDs)
	}
}

func TestPipeline_OversizedIndividualMessageFailsLoudly(t *testing.T) {
	mock := &mockLLMProvider{}
	pipe := NewPipeline(mock, 100, PromptConfig{}) // tiny limit

	batch := &digest.Batch{
		IncludedMessages: []digest.SourceMessage{
			{SourceID: "S000001", Content: strings.Repeat("A", 200), Timestamp: time.Now().UTC()},
		},
	}

	_, err := pipe.GenerateDigest(context.Background(), batch)
	if err == nil || !strings.Contains(err.Error(), "exceeds configured max_input_chars") {
		t.Fatalf("expected oversized message error, got: %v", err)
	}
}

func TestCleanJSONResponse(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{`{"a": 1}`, `{"a": 1}`},
		{"```json\n{\"a\": 1}\n```", `{"a": 1}`},
		{"```\n{\"a\": 1}\n```", `{"a": 1}`},
	}

	for _, c := range cases {
		got := CleanJSONResponse(c.input)
		if got != c.expected {
			t.Errorf("CleanJSONResponse(%q) = %q, expected %q", c.input, got, c.expected)
		}
	}
}
