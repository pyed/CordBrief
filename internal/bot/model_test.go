package bot

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyed/CordBrief/internal/brief"
	"github.com/pyed/CordBrief/internal/dce"
	"github.com/pyed/CordBrief/internal/job"
	"github.com/pyed/CordBrief/internal/llm"
	"github.com/pyed/CordBrief/internal/state"
)

type mockModelLister struct {
	callCount int32
	models    []llm.ModelInfo
	err       error
}

func (m *mockModelLister) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	atomic.AddInt32(&m.callCount, 1)
	if m.err != nil {
		return nil, m.err
	}
	return m.models, nil
}

func TestModelCache(t *testing.T) {
	cache := NewModelCache()

	// 1. Initial empty cache
	if _, _, ok := cache.Get("https://api.example.com"); ok {
		t.Fatal("expected cache miss on fresh cache")
	}

	// 2. Set models
	models := []string{"gemini-3.8-flash", "gemini-3.7-flash", "gemini-3.5-flash"}
	gen1 := cache.Set("https://api.example.com", models)
	if gen1 != 1 {
		t.Fatalf("expected generation 1, got %d", gen1)
	}

	// 3. Get with matching baseURL
	got, gen, ok := cache.Get("https://api.example.com")
	if !ok || gen != 1 || len(got) != 3 {
		t.Fatalf("unexpected cache hit: got=%v, gen=%d, ok=%v", got, gen, ok)
	}

	// 4. Get with different baseURL misses
	if _, _, ok := cache.Get("https://other.example.com"); ok {
		t.Fatal("expected cache miss for different baseURL")
	}

	// 5. Validation with matching generation and baseURL
	m, ok := cache.Validate("https://api.example.com", gen1, 1)
	if !ok || m != "gemini-3.7-flash" {
		t.Fatalf("validation failed: model=%s, ok=%v", m, ok)
	}

	// Validation with different baseURL fails
	if _, ok := cache.Validate("https://other.example.com", gen1, 1); ok {
		t.Fatal("expected validation failure for different baseURL")
	}

	// 6. Validation with stale generation fails
	gen2 := cache.Set("https://api.example.com", []string{"new-model"})
	if gen2 != 2 {
		t.Fatalf("expected generation 2, got %d", gen2)
	}
	if _, ok := cache.Validate("https://api.example.com", gen1, 1); ok {
		t.Fatal("expected validation failure for stale generation")
	}

	// 7. Validation out of bounds fails
	if _, ok := cache.Validate("https://api.example.com", gen2, 99); ok {
		t.Fatal("expected validation failure for out-of-bounds index")
	}
}

func TestModelBrowser(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	lister := &mockModelLister{
		models: []llm.ModelInfo{
			{ProviderID: "models/gemini-3.8-flash", ModelID: "gemini-3.8-flash"},
			{ProviderID: "models/gemini-3.7-flash", ModelID: "gemini-3.7-flash"},
			{ProviderID: "models/gemini-3.5-flash", ModelID: "gemini-3.5-flash"},
		},
	}
	b.modelLister = lister

	// 1. /model with no args opens browser
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/model"))
	if count := atomic.LoadInt32(&lister.callCount); count != 1 {
		t.Fatalf("expected 1 ListModels call, got %d", count)
	}

	lastMsg := sender.lastSent()
	if lastMsg == nil || !strings.Contains(lastMsg.Text, "Current: gemini-3.8-flash") {
		t.Fatalf("unexpected message: %+v", lastMsg)
	}
	if !strings.Contains(lastMsg.Text, "Models reported by provider:") {
		t.Errorf("missing provider disclaimer: %s", lastMsg.Text)
	}
	if !strings.Contains(lastMsg.Text, "(Note: Some provider models may not support chat summaries.)") {
		t.Errorf("missing disclaimer note: %s", lastMsg.Text)
	}

	kb := getInlineKeyboard(lastMsg)
	// Check that current model is marked with checkmark
	foundCurrentMarked, foundOther := false, false
	var select37Cb string
	for _, row := range kb {
		for _, btn := range row {
			if btn.Text == "✓ gemini-3.8-flash" {
				foundCurrentMarked = true
			}
			if btn.Text == "gemini-3.7-flash" {
				foundOther = true
				select37Cb = btn.CallbackData
			}
		}
	}
	if !foundCurrentMarked {
		t.Errorf("current model was not marked with checkmark in keyboard: %+v", kb)
	}
	if !foundOther || select37Cb == "" {
		t.Fatalf("other model button not found in keyboard: %+v", kb)
	}

	// 2. Second /model uses cache and does NOT call provider again
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/model"))
	if count := atomic.LoadInt32(&lister.callCount); count != 1 {
		t.Fatalf("expected call count to remain 1, got %d", count)
	}

	// 3. Select model gemini-3.7-flash
	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", select37Cb))
	lastEdited := sender.lastEdited()
	if lastEdited == nil || !strings.Contains(lastEdited.Text, "LLM model updated to:\ngemini-3.7-flash") {
		t.Fatalf("unexpected edit message: %+v", lastEdited)
	}

	// Verify persisted to config
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load config failed: %v", err)
	}
	if cfg.LLM.Model != "gemini-3.7-flash" {
		t.Fatalf("expected config model gemini-3.7-flash, got: %s", cfg.LLM.Model)
	}

	// Verify /status agrees
	sender.sent = nil
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/status"))
	statusMsg := sender.lastSent()
	if statusMsg == nil || !strings.Contains(statusMsg.Text, "LLM: gemini-3.7-flash") {
		t.Fatalf("status did not reflect updated model: %+v", statusMsg)
	}

	// 4. Selecting current model again informs user without error
	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", select37Cb))
	if !strings.Contains(sender.lastEdited().Text, "Model is already set to gemini-3.7-flash") {
		t.Fatalf("expected already set message: %+v", sender.lastEdited())
	}

	// 5. Stale generation callback is rejected
	staleCb := "m:s:999:0"
	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", staleCb))
	if !strings.Contains(sender.lastEdited().Text, "Model list changed. Open /model again.") {
		t.Fatalf("expected stale generation rejection: %+v", sender.lastEdited())
	}

	// 5b. Callback when store BaseURL has changed is rejected
	cfgBefore, _ := store.LoadConfig()
	cfgModified := *cfgBefore
	cfgModified.LLM.BaseURL = "https://other.example.com/v1"
	_ = store.SaveConfig(&cfgModified)
	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", select37Cb))
	if !strings.Contains(sender.lastEdited().Text, "Model list changed. Open /model again.") {
		t.Fatalf("expected baseURL mismatch rejection: %+v", sender.lastEdited())
	}
	_ = store.SaveConfig(cfgBefore)

	// 6. Refresh (m:ref) invokes provider once more
	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", "m:ref"))
	if count := atomic.LoadInt32(&lister.callCount); count != 2 {
		t.Fatalf("expected 2 ListModels calls after refresh, got %d", count)
	}

	// 7. Failed refresh preserves previous cache
	lister.err = errors.New("network error")
	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", "m:ref"))
	if !strings.Contains(sender.lastEdited().Text, "Failed to discover models: network error") {
		t.Fatalf("expected error message on failed refresh: %+v", sender.lastEdited())
	}
	// Cache should still hold models
	cachedModels, _, ok := b.modelCache.Get(cfg.LLM.BaseURL)
	if !ok || len(cachedModels) != 3 {
		t.Fatalf("expected previous cache to be preserved, got ok=%v, len=%d", ok, len(cachedModels))
	}
}

func TestModelActiveBriefGuard(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	lister := &mockModelLister{
		models: []llm.ModelInfo{
			{ProviderID: "gemini-3.8-flash", ModelID: "gemini-3.8-flash"},
			{ProviderID: "gemini-3.5-flash", ModelID: "gemini-3.5-flash"},
		},
	}
	b.modelLister = lister

	// Prime cache first
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/model"))

	// Setup runner with blocking exporter to simulate active brief
	cfg, _ := store.LoadConfig()
	cfg.Channels = append(cfg.Channels, state.ChannelConfig{ID: "111222", Name: "ch"})
	_ = store.SaveConfig(cfg)
	st, _ := store.LoadState()
	st.Channels["111222"] = state.ChannelState{
		Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-15T00:00:00Z"},
	}
	_ = store.SaveState(st)

	blockCh := make(chan struct{})
	blocker := &fakeBlocker{blockCh: blockCh}
	r, err := job.NewRunner(store, job.WithDCEClient(blocker), job.WithDeliverer(&fakeDeliverer{}))
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}
	b.runner = r

	if !r.Start(ctx, "") {
		t.Fatal("failed to start runner")
	}
	for i := 0; i < 50; i++ {
		if r.IsRunning() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !r.IsRunning() {
		t.Fatal("runner not in running state")
	}

	// 1. Direct /model mutation while brief is active is blocked
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/model gemini-3.5-flash"))
	lastMsg := sender.lastSent()
	if lastMsg == nil || !strings.Contains(lastMsg.Text, "A brief is currently running. Change the model after it finishes.") {
		t.Fatalf("expected active brief block on direct setter: %+v", lastMsg)
	}

	// 2. Interactive selection callback while brief is active is blocked
	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", "m:s:1:1"))
	lastEdited := sender.lastEdited()
	if lastEdited == nil || !strings.Contains(lastEdited.Text, "A brief is currently running. Change the model after it finishes.") {
		t.Fatalf("expected active brief block on callback selection: %+v", lastEdited)
	}

	// 3. Refresh callback while brief is active is blocked
	b.HandleUpdate(ctx, nil, makeCallback(12345, "private", "m:ref"))
	lastEdited2 := sender.lastEdited()
	if lastEdited2 == nil || !strings.Contains(lastEdited2.Text, "A brief is currently running.") {
		t.Fatalf("expected active brief block on refresh: %+v", lastEdited2)
	}

	// 4. Cached browsing (/model with no args) still displays menu!
	sender.sent = nil
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/model"))
	if sender.lastSent() == nil || !strings.Contains(sender.lastSent().Text, "LLM Model") {
		t.Fatalf("expected cached model browser to display even while brief active: %+v", sender.lastSent())
	}

	close(blockCh)
	r.Wait()
}

func TestDirectModelCommand(t *testing.T) {
	b, sender, store, _ := setupTestBot(t)
	ctx := context.Background()

	// 1. Valid direct model set
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/model gemini-3.7-flash"))
	lastMsg := sender.lastSent()
	if lastMsg == nil || !strings.Contains(lastMsg.Text, "LLM model updated to: gemini-3.7-flash") {
		t.Fatalf("unexpected message: %+v", lastMsg)
	}

	cfg, _ := store.LoadConfig()
	if cfg.LLM.Model != "gemini-3.7-flash" {
		t.Fatalf("expected config model gemini-3.7-flash, got: %s", cfg.LLM.Model)
	}

	// 2. Rejects invalid inputs (too long > 128 chars)
	longModel := strings.Repeat("a", 129)
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/model "+longModel))
	if !strings.Contains(sender.lastSent().Text, "Invalid model identifier") {
		t.Fatalf("expected rejection of too-long model: %+v", sender.lastSent())
	}
}

func TestRuntimeModelSwitch(t *testing.T) {
	b, _, store, _ := setupTestBot(t)
	ctx := context.Background()

	// Seed followed channel and cursor
	cfg, _ := store.LoadConfig()
	cfg.Channels = append(cfg.Channels, state.ChannelConfig{ID: "555111", Name: "general"})
	_ = store.SaveConfig(cfg)
	st, _ := store.LoadState()
	st.Channels["555111"] = state.ChannelState{
		Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-15T00:00:00Z"},
	}
	_ = store.SaveState(st)

	var lastInstantiatedModel string
	completerFactory := func(baseURL, model, apiKey string) (brief.Completer, error) {
		lastInstantiatedModel = model
		return &fakeCompleter{}, nil
	}

	deliverer := &fakeDeliverer{}
	exporter := &fakeExporter{}

	runner, err := job.NewRunner(store,
		job.WithCompleterFactory(completerFactory),
		job.WithDeliverer(deliverer),
		job.WithDCEClient(exporter),
	)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}
	b.runner = runner

	// 1. First brief run uses default model gemini-3.8-flash
	if !runner.Start(ctx, "") {
		t.Fatal("failed to start first job")
	}
	runner.Wait()

	if lastInstantiatedModel != "gemini-3.8-flash" {
		t.Fatalf("expected job 1 to use gemini-3.8-flash, got %q", lastInstantiatedModel)
	}

	// 2. Operator switches model at runtime via /model without restart
	b.HandleUpdate(ctx, nil, makeMsg(12345, "private", "/model gemini-3.5-flash"))

	// 3. Next brief run automatically uses gemini-3.5-flash!
	if !runner.Start(ctx, "") {
		t.Fatal("failed to start second job")
	}
	runner.Wait()

	if lastInstantiatedModel != "gemini-3.5-flash" {
		t.Fatalf("expected job 2 to use gemini-3.5-flash without restart, got %q", lastInstantiatedModel)
	}
}

// Test helpers
type fakeBlocker struct {
	blockCh chan struct{}
}

func (f *fakeBlocker) Export(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error) {
	<-f.blockCh
	return &dce.ExportResult{}, nil
}

func (f *fakeBlocker) IsConfigured() bool {
	return true
}

type fakeDeliverer struct{}

func (f *fakeDeliverer) Deliver(ctx context.Context, text string) error {
	return nil
}

type fakeExporter struct{}

func (f *fakeExporter) Export(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error) {
	return &dce.ExportResult{
		Guild:        dce.GuildInfo{ID: "g1", Name: "Guild"},
		Channel:      dce.ChannelInfo{ID: req.ChannelID, Name: "ch"},
		Messages:     []dce.Message{{ID: "100", Content: "hello"}},
		MaxMessageID: "100",
	}, nil
}

func (f *fakeExporter) IsConfigured() bool {
	return true
}

type fakeCompleter struct{}

func (f *fakeCompleter) Complete(ctx context.Context, messages []llm.Message) (string, error) {
	return "Summary of messages", nil
}
