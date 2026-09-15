package brief

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/pyed/CordBrief/internal/llm"
)

// DefaultChunkBudget is the conservative character budget per LLM request (~80k characters, ~20k tokens).
// Rationale: Well below modern context windows (e.g. Gemini 1M+, OpenAI 128k) and local models (32k+ context),
// ensuring fast execution, low memory footprint, and broad compatibility while accommodating active days in 1 call.
const DefaultChunkBudget = 80000

// MaxReductionPasses is the safety bound for hierarchical compression passes, preventing infinite reduction loops.
const MaxReductionPasses = 5

// Option configures Engine instances.
type Option func(*Engine)

// WithPrompt configures the operator briefing prompt instructions.
func WithPrompt(prompt string) Option {
	return func(e *Engine) {
		e.prompt = prompt
	}
}

// Engine turns channel messages into a concise executive brief.
type Engine struct {
	completer   Completer
	ChunkBudget int
	prompt      string
}

// NewEngine creates a briefing Engine backed by the given Completer.
func NewEngine(completer Completer, opts ...Option) *Engine {
	e := &Engine{
		completer:   completer,
		ChunkBudget: DefaultChunkBudget,
		prompt:      DefaultCustomizablePrompt,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Summarize transforms a channel's messages into an executive brief.
// If messages is empty, returns an empty string without making any LLM calls.
func (e *Engine) Summarize(ctx context.Context, ch Channel, messages []Message) (string, error) {
	if len(messages) == 0 {
		return "", nil
	}
	if e.completer == nil {
		return "", errors.New("briefing engine completer is nil")
	}

	budget := e.ChunkBudget
	if budget <= 0 {
		budget = DefaultChunkBudget
	}

	// 1. Partition messages into character-bounded chunks without splitting message blocks
	chunks := e.chunkMessages(messages, budget)

	// 2. Single chunk path: direct synthesis
	if len(chunks) == 1 {
		transcript := chunks[0]
		userPrompt := BuildUserPrompt(ch, transcript)
		req := []llm.Message{
			{Role: "system", Content: BuildBriefSystemPrompt(e.prompt)},
			{Role: "user", Content: userPrompt},
		}
		return e.completer.Complete(ctx, req)
	}

	// 3. Multi-chunk hierarchical path
	// Stage 1: Summarize each chunk into chronological factual notes
	notes := make([]string, len(chunks))
	chunkSysPrompt := BuildChunkNotesSystemPrompt(e.prompt)
	for i, chunk := range chunks {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		userPrompt := BuildChunkUserPrompt(ch, i, len(chunks), chunk)
		req := []llm.Message{
			{Role: "system", Content: chunkSysPrompt},
			{Role: "user", Content: userPrompt},
		}
		note, err := e.completer.Complete(ctx, req)
		if err != nil {
			return "", fmt.Errorf("failed to summarize chunk %d of %d: %w", i+1, len(chunks), err)
		}
		notes[i] = fmt.Sprintf("--- Section %d of %d ---\n%s", i+1, len(chunks), strings.TrimSpace(note))
	}

	// Stage 2: Reduction loop if combined notes exceed character budget
	combinedNotes := strings.Join(notes, "\n\n")
	passes := 0
	for len(combinedNotes) > budget {
		passes++
		if passes > MaxReductionPasses {
			return "", fmt.Errorf("hierarchical reduction failed: exceeded maximum reduction passes (%d) without fitting budget", MaxReductionPasses)
		}

		// Batch notes into groups fitting within budget
		batchedNotes := e.batchTextBlocks(notes, budget)
		reducedNotes := make([]string, len(batchedNotes))
		for i, batch := range batchedNotes {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}

			userPrompt := fmt.Sprintf("Channel: #%s (Consolidation Pass %d, Group %d of %d)\n\n%s\n\nConsolidate and compress these notes preserving all key facts.",
				ch.Name, passes, i+1, len(batchedNotes), batch)
			req := []llm.Message{
				{Role: "system", Content: chunkSysPrompt},
				{Role: "user", Content: userPrompt},
			}
			reduced, err := e.completer.Complete(ctx, req)
			if err != nil {
				return "", fmt.Errorf("reduction pass %d group %d failed: %w", passes, i+1, err)
			}
			reducedNotes[i] = strings.TrimSpace(reduced)
		}

		notes = reducedNotes
		combinedNotes = strings.Join(notes, "\n\n")
	}

	// Stage 3: Final synthesis
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	synthesisUserPrompt := BuildSynthesisUserPrompt(ch, combinedNotes)
	synthesisReq := []llm.Message{
		{Role: "system", Content: BuildSynthesisSystemPrompt(e.prompt)},
		{Role: "user", Content: synthesisUserPrompt},
	}

	return e.completer.Complete(ctx, synthesisReq)
}

// chunkMessages packs complete rendered message blocks into chunks up to target budget.
// An individual message block is never split across chunks unless it alone exceeds budget.
func (e *Engine) chunkMessages(messages []Message, budget int) []string {
	blocks := make([]string, len(messages))
	for i, m := range messages {
		blocks[i] = RenderMessage(m)
	}
	return e.batchTextBlocks(blocks, budget)
}

// batchTextBlocks keeps whole blocks together when possible and splits oversized blocks without losing text.
func (e *Engine) batchTextBlocks(blocks []string, budget int) []string {
	budget = max(budget, utf8.UTFMax) // A budget must fit at least one UTF-8 rune.
	var batches []string
	var current strings.Builder

	for _, block := range blocks {
		if len(block) > budget && current.Len() > 0 {
			batches = append(batches, current.String())
			current.Reset()
		}
		for len(block) > budget {
			end := budget
			for !utf8.RuneStart(block[end]) {
				end--
			}
			batches = append(batches, block[:end])
			block = block[end:]
		}
		extraLen := len(block)
		if current.Len() > 0 {
			extraLen += 2
		}

		if current.Len()+extraLen > budget && current.Len() > 0 {
			batches = append(batches, current.String())
			current.Reset()
			current.WriteString(block)
		} else {
			if current.Len() > 0 {
				current.WriteString("\n\n")
			}
			current.WriteString(block)
		}
	}

	if current.Len() > 0 {
		batches = append(batches, current.String())
	}

	return batches
}
