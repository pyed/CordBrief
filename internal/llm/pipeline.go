package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"cordbrief/internal/digest"
)

// Pipeline coordinates digest generation with single-chunk fast path and chunk/reduce fallback.
type Pipeline struct {
	Provider      Provider
	MaxInputChars int
	Config        PromptConfig
}

// NewPipeline creates a new pipeline instance.
func NewPipeline(p Provider, maxChars int, cfg PromptConfig) *Pipeline {
	if maxChars <= 0 {
		maxChars = 30000
	}
	return &Pipeline{
		Provider:      p,
		MaxInputChars: maxChars,
		Config:        cfg,
	}
}

// ChunkSummary represents intermediate structured observations extracted from a single chunk.
type ChunkSummary struct {
	Summary string        `json:"summary"`
	Items   []digest.Item `json:"items"`
}

// GenerateDigest generates a structured digest using the single-chunk fast path or chunk/reduce pipeline.
func (pipe *Pipeline) GenerateDigest(ctx context.Context, batch *digest.Batch) (*digest.Digest, error) {
	if batch == nil {
		return nil, fmt.Errorf("batch cannot be nil")
	}

	if len(batch.IncludedMessages) == 0 {
		return &digest.Digest{
			Title:    "No New Activity",
			Overview: "No relevant messages were observed in this digest period.",
			Items:    nil,
		}, nil
	}

	// Format entire batch to check if it fits the single-chunk fast path
	fullPayload, err := BuildUserPayload(batch.IncludedMessages)
	if err != nil {
		return nil, fmt.Errorf("build full user payload: %w", err)
	}

	if len(fullPayload) <= pipe.MaxInputChars {
		// Single-chunk fast path: no reduce call needed
		systemPrompt := BuildSystemPrompt(pipe.Config)
		d, err := pipe.Provider.Summarize(ctx, systemPrompt, fullPayload)
		if err != nil {
			return nil, fmt.Errorf("single-chunk summarization failed: %w", err)
		}
		return d, nil
	}

	// Multi-chunk path: partition messages deterministically
	chunks, err := pipe.partitionMessages(batch.IncludedMessages)
	if err != nil {
		return nil, fmt.Errorf("partition messages for chunking: %w", err)
	}

	var allIntermediateItems []digest.Item
	chunkSystemPrompt := BuildChunkSystemPrompt(pipe.Config)

	for i, chunkMsgs := range chunks {
		chunkPayload, err := BuildUserPayload(chunkMsgs)
		if err != nil {
			return nil, fmt.Errorf("build chunk [%d] payload: %w", i+1, err)
		}

		rawChunkResp, err := pipe.Provider.GenerateText(ctx, chunkSystemPrompt, chunkPayload)
		if err != nil {
			return nil, fmt.Errorf("summarize chunk [%d/%d]: %w", i+1, len(chunks), err)
		}

		cleanedResp := CleanJSONResponse(rawChunkResp)
		var cs ChunkSummary
		if err := json.Unmarshal([]byte(cleanedResp), &cs); err != nil {
			// Single repair attempt for chunk
			repairPrompt := BuildRepairPrompt(cleanedResp, err)
			repairedRaw, repErr := pipe.Provider.GenerateText(ctx, chunkSystemPrompt, repairPrompt)
			if repErr != nil {
				return nil, fmt.Errorf("chunk [%d] repair failed: %w (orig error: %v)", i+1, repErr, err)
			}
			cleanedRepair := CleanJSONResponse(repairedRaw)
			if unmarshalErr := json.Unmarshal([]byte(cleanedRepair), &cs); unmarshalErr != nil {
				return nil, fmt.Errorf("chunk [%d] malformed output after repair: %w", i+1, unmarshalErr)
			}
		}

		allIntermediateItems = append(allIntermediateItems, cs.Items...)
	}

	// Reduce phase: synthesize all intermediate items into the final Digest
	reducePayloadBytes, err := json.MarshalIndent(map[string]any{
		"extracted_observations": allIntermediateItems,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal reduce payload: %w", err)
	}

	reduceSystemPrompt := BuildReduceSystemPrompt(pipe.Config)
	reduceUserPrompt := fmt.Sprintf("<INTERMEDIATE_CHUNK_OBSERVATIONS>\n%s\n</INTERMEDIATE_CHUNK_OBSERVATIONS>\n\nSynthesize these observations into the final unified JSON digest. Ensure all items strictly cite original source IDs.", string(reducePayloadBytes))

	finalDigest, err := pipe.Provider.Summarize(ctx, reduceSystemPrompt, reduceUserPrompt)
	if err != nil {
		return nil, fmt.Errorf("reduce phase failed: %w", err)
	}

	return finalDigest, nil
}

// partitionMessages splits messages into chunks that fit within MaxInputChars without splitting individual messages.
func (pipe *Pipeline) partitionMessages(msgs []digest.SourceMessage) ([][]digest.SourceMessage, error) {
	var chunks [][]digest.SourceMessage
	var currentChunk []digest.SourceMessage

	for _, m := range msgs {
		// Verify individual message does not exceed entire budget
		singlePayload, err := BuildUserPayload([]digest.SourceMessage{m})
		if err != nil {
			return nil, err
		}
		if len(singlePayload) > pipe.MaxInputChars {
			return nil, fmt.Errorf("message %s (%s) length (%d chars) exceeds configured max_input_chars (%d)",
				m.SourceID, m.MessageID, len(singlePayload), pipe.MaxInputChars)
		}

		testChunk := append(currentChunk, m)
		testPayload, err := BuildUserPayload(testChunk)
		if err != nil {
			return nil, err
		}

		if len(testPayload) > pipe.MaxInputChars && len(currentChunk) > 0 {
			chunks = append(chunks, currentChunk)
			currentChunk = []digest.SourceMessage{m}
		} else {
			currentChunk = testChunk
		}
	}

	if len(currentChunk) > 0 {
		chunks = append(chunks, currentChunk)
	}

	return chunks, nil
}
