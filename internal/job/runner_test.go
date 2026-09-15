package job

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyed/CordBrief/internal/brief"
	"github.com/pyed/CordBrief/internal/dce"
	"github.com/pyed/CordBrief/internal/llm"
	"github.com/pyed/CordBrief/internal/state"
)

// FakeDCE implements DCEExporter for unit tests.
type FakeDCE struct {
	mu           sync.Mutex
	configured   bool
	exports      []dce.ExportRequest
	exportFunc   func(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error)
	delayPerCall time.Duration
}

func (f *FakeDCE) IsConfigured() bool {
	return f.configured
}

func (f *FakeDCE) Export(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error) {
	f.mu.Lock()
	f.exports = append(f.exports, req)
	f.mu.Unlock()

	if f.delayPerCall > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.delayPerCall):
		}
	}

	if f.exportFunc != nil {
		return f.exportFunc(ctx, req)
	}
	return &dce.ExportResult{}, nil
}

// FakeDeliverer captures delivered Telegram messages.
type FakeDeliverer struct {
	mu          sync.Mutex
	messages    []string
	deliverFunc func(ctx context.Context, text string) error
}

func (f *FakeDeliverer) Deliver(ctx context.Context, text string) error {
	f.mu.Lock()
	f.messages = append(f.messages, text)
	f.mu.Unlock()

	if f.deliverFunc != nil {
		return f.deliverFunc(ctx, text)
	}
	return nil
}

func (f *FakeDeliverer) GetMessages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := make([]string, len(f.messages))
	copy(copied, f.messages)
	return copied
}

// FakeCompleter implements brief.Completer.
type FakeCompleter struct {
	mu           sync.Mutex
	calls        int
	completeFunc func(ctx context.Context, messages []llm.Message) (string, error)
}

func (f *FakeCompleter) Complete(ctx context.Context, messages []llm.Message) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()

	if f.completeFunc != nil {
		return f.completeFunc(ctx, messages)
	}
	return "• Factual summary item.", nil
}

func setupTestStore(t *testing.T) (*state.Store, string) {
	t.Helper()
	tmpDir := t.TempDir()
	store := state.NewStore(tmpDir)
	return store, tmpDir
}

func TestRunner_SingleJobAtATime(t *testing.T) {
	store, _ := setupTestStore(t)
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "1", Name: "test"}}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	st := state.NewEmptyState()
	st.Channels["1"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindMessageID, Value: "1"}}
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	d := &FakeDCE{configured: true, exportFunc: func(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error) {
		close(entered)
		<-release
		return &dce.ExportResult{}, nil
	}}
	runner, err := NewRunner(store, WithDCEClient(d), WithDeliverer(&FakeDeliverer{}))
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	if !runner.Start(context.Background(), "") {
		t.Fatal("expected first Start to succeed")
	}
	<-entered
	if !runner.IsRunning() {
		t.Fatal("expected IsRunning to be true")
	}
	if runner.Start(context.Background(), "") {
		t.Fatal("expected second Start to fail while active")
	}
	if err := runner.Run(context.Background(), ""); err == nil {
		t.Fatal("concurrent Run was accepted")
	}
	waited := make(chan struct{})
	go func() { runner.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("Wait returned before delivery finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-waited
	if runner.IsRunning() {
		t.Fatal("expected IsRunning to be false after finish")
	}
	if len(d.exports) != 1 {
		t.Fatalf("got %d concurrent exports", len(d.exports))
	}
	if err := runner.Run(context.Background(), "missing"); err == nil {
		t.Fatal("expected unfollowed channel error")
	}
}

func TestRunner_FixedCutoffSharedAcrossChannels(t *testing.T) {
	store, _ := setupTestStore(t)
	fixedTime := time.Date(2026, 9, 14, 15, 0, 0, 0, time.UTC)

	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{
		{ID: "10001", Name: "alpha"},
		{ID: "10002", Name: "beta"},
	}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	st := state.NewEmptyState()
	st.Channels["10001"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T10:00:00Z"}}
	st.Channels["10002"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T11:00:00Z"}}
	if err := store.SaveState(st); err != nil {
		t.Fatalf("save state: %v", err)
	}

	fakeDCE := &FakeDCE{configured: true}
	fakeDel := &FakeDeliverer{}
	fakeComp := &FakeCompleter{}

	runner, _ := NewRunner(store,
		WithNow(func() time.Time { return fixedTime }),
		WithDCEClient(fakeDCE),
		WithDeliverer(fakeDel),
		WithCompleterFactory(func(b, m, k string) (brief.Completer, error) { return fakeComp, nil }),
	)

	if err := runner.Run(context.Background(), ""); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	fakeDCE.mu.Lock()
	defer fakeDCE.mu.Unlock()
	if len(fakeDCE.exports) != 2 {
		t.Fatalf("expected 2 exports, got %d", len(fakeDCE.exports))
	}

	// Sequential order
	if fakeDCE.exports[0].ChannelID != "10001" || fakeDCE.exports[1].ChannelID != "10002" {
		t.Fatalf("channels not processed in config order: %v, %v", fakeDCE.exports[0].ChannelID, fakeDCE.exports[1].ChannelID)
	}

	// Shared cutoff
	if !fakeDCE.exports[0].Before.Equal(fixedTime) || !fakeDCE.exports[1].Before.Equal(fixedTime) {
		t.Fatalf("cutoff mismatch: %v vs %v (expected %v)", fakeDCE.exports[0].Before, fakeDCE.exports[1].Before, fixedTime)
	}

	// Exact cursors preserved
	if fakeDCE.exports[0].After.Value != "2026-09-14T10:00:00Z" {
		t.Errorf("expected 10001 cursor 2026-09-14T10:00:00Z, got %s", fakeDCE.exports[0].After.Value)
	}
	if fakeDCE.exports[1].After.Value != "2026-09-14T11:00:00Z" {
		t.Errorf("expected 10002 cursor 2026-09-14T11:00:00Z, got %s", fakeDCE.exports[1].After.Value)
	}
}

func TestRunner_ZeroMessages_NoLLM_NoCursorChange(t *testing.T) {
	store, _ := setupTestStore(t)
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "10001", Name: "general"}}
	_ = store.SaveConfig(cfg)

	st := state.NewEmptyState()
	st.Channels["10001"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T12:00:00Z"}}
	_ = store.SaveState(st)

	fakeDCE := &FakeDCE{
		configured: true,
		exportFunc: func(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error) {
			return &dce.ExportResult{Messages: []dce.Message{}}, nil
		},
	}
	fakeDel := &FakeDeliverer{}
	fakeComp := &FakeCompleter{}

	runner, _ := NewRunner(store,
		WithDCEClient(fakeDCE),
		WithDeliverer(fakeDel),
		WithCompleterFactory(func(b, m, k string) (brief.Completer, error) { return fakeComp, nil }),
	)

	if err := runner.Run(context.Background(), ""); err != nil {
		t.Fatalf("run: %v", err)
	}

	if fakeComp.calls != 0 {
		t.Fatalf("expected 0 LLM calls for zero messages, got %d", fakeComp.calls)
	}

	msgs := fakeDel.GetMessages()
	if len(msgs) != 1 || msgs[0] != "#general\nNo new messages." {
		t.Fatalf("unexpected delivery: %v", msgs)
	}

	// Verify state cursor unchanged
	savedSt, _ := store.LoadState()
	if savedSt.Channels["10001"].Cursor.Kind != state.CursorKindTimestamp || savedSt.Channels["10001"].Cursor.Value != "2026-09-14T12:00:00Z" {
		t.Fatalf("cursor was modified on zero messages: %+v", savedSt.Channels["10001"].Cursor)
	}
}

func TestRunner_SuccessfulDelivery_TransitionsCursorToMessageID(t *testing.T) {
	store, _ := setupTestStore(t)
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "10001", Name: "general"}}
	_ = store.SaveConfig(cfg)

	st := state.NewEmptyState()
	st.Channels["10001"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T12:00:00Z"}}
	_ = store.SaveState(st)

	fakeDCE := &FakeDCE{
		configured: true,
		exportFunc: func(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error) {
			return &dce.ExportResult{
				Channel: dce.ChannelInfo{ID: "10001", Name: "general"},
				Messages: []dce.Message{
					{ID: "1001", Content: "Hello world", Author: dce.Author{Name: "Alice"}},
					{ID: "1002", Content: "Deployment complete", Author: dce.Author{Name: "Bob"}},
				},
				MaxMessageID: "1002",
			}, nil
		},
	}
	fakeDel := &FakeDeliverer{}
	fakeComp := &FakeCompleter{}

	nowFixed := time.Date(2026, 9, 14, 15, 30, 0, 0, time.UTC)
	runner, _ := NewRunner(store,
		WithNow(func() time.Time { return nowFixed }),
		WithDCEClient(fakeDCE),
		WithDeliverer(fakeDel),
		WithCompleterFactory(func(b, m, k string) (brief.Completer, error) { return fakeComp, nil }),
	)

	if err := runner.Run(context.Background(), ""); err != nil {
		t.Fatalf("run: %v", err)
	}

	msgs := fakeDel.GetMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 delivered message, got %d", len(msgs))
	}
	if !strings.HasPrefix(msgs[0], "#general\n2 messages\n\n") {
		t.Errorf("unexpected header: %s", msgs[0])
	}

	savedSt, _ := store.LoadState()
	chSt := savedSt.Channels["10001"]
	if chSt.Cursor.Kind != state.CursorKindMessageID {
		t.Fatalf("expected cursor kind 'message_id', got %q", chSt.Cursor.Kind)
	}
	if chSt.Cursor.Value != "1002" {
		t.Fatalf("expected cursor value '1002', got %q", chSt.Cursor.Value)
	}
	if chSt.LastSuccessAt != nowFixed.Format(time.RFC3339) {
		t.Fatalf("expected last success at %s, got %s", nowFixed.Format(time.RFC3339), chSt.LastSuccessAt)
	}
	if chSt.LastError != "" {
		t.Fatalf("expected last error cleared, got %q", chSt.LastError)
	}
	if err := runner.Run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if fakeDCE.exports[1].After != chSt.Cursor {
		t.Fatalf("second export used %+v, committed %+v", fakeDCE.exports[1].After, chSt.Cursor)
	}
}

func TestRunner_DCEFailure_LeavesCursorUnchanged_IsolatesOtherChannels(t *testing.T) {
	store, _ := setupTestStore(t)
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{
		{ID: "90001", Name: "broken"},
		{ID: "90002", Name: "healthy"},
	}
	_ = store.SaveConfig(cfg)

	st := state.NewEmptyState()
	st.Channels["90001"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T01:00:00Z"}}
	st.Channels["90002"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T02:00:00Z"}}
	_ = store.SaveState(st)

	fakeDCE := &FakeDCE{
		configured: true,
		exportFunc: func(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error) {
			if req.ChannelID == "90001" {
				return nil, errors.New("DCE process exited status 1")
			}
			return &dce.ExportResult{
				Channel: dce.ChannelInfo{ID: "90002", Name: "healthy"},
				Messages: []dce.Message{
					{ID: "5001", Content: "Healthy update", Author: dce.Author{Name: "Carol"}},
				},
				MaxMessageID: "5001",
			}, nil
		},
	}
	fakeDel := &FakeDeliverer{}
	fakeComp := &FakeCompleter{}

	runner, _ := NewRunner(store,
		WithDCEClient(fakeDCE),
		WithDeliverer(fakeDel),
		WithCompleterFactory(func(b, m, k string) (brief.Completer, error) { return fakeComp, nil }),
	)

	_ = runner.Run(context.Background(), "")

	savedSt, _ := store.LoadState()
	// Channel 1 failed: cursor untouched, last_error set
	ch1 := savedSt.Channels["90001"]
	if ch1.Cursor.Value != "2026-09-14T01:00:00Z" {
		t.Errorf("failed channel cursor was modified: %s", ch1.Cursor.Value)
	}
	if !strings.Contains(ch1.LastError, "DCE process exited status 1") {
		t.Errorf("expected last_error to record failure, got %s", ch1.LastError)
	}

	// Channel 2 succeeded independently
	ch2 := savedSt.Channels["90002"]
	if ch2.Cursor.Kind != state.CursorKindMessageID || ch2.Cursor.Value != "5001" {
		t.Errorf("healthy channel cursor did not advance: %+v", ch2.Cursor)
	}

	msgs := fakeDel.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 delivered notices, got %d: %v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0], "Brief failed during Discord collection") {
		t.Errorf("expected failure notice for broken channel, got: %s", msgs[0])
	}
	if !strings.Contains(msgs[1], "#healthy\n1 message\n\n") {
		t.Errorf("expected brief for healthy channel, got: %s", msgs[1])
	}
}

func TestRunner_SummarizeFailure_LeavesCursorUnchanged(t *testing.T) {
	store, _ := setupTestStore(t)
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "10001", Name: "general"}, {ID: "10002", Name: "healthy"}}
	_ = store.SaveConfig(cfg)

	st := state.NewEmptyState()
	st.Channels["10001"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T05:00:00Z"}}
	st.Channels["10002"] = st.Channels["10001"]
	_ = store.SaveState(st)

	fakeDCE := &FakeDCE{
		configured: true,
		exportFunc: func(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error) {
			return &dce.ExportResult{
				Channel: dce.ChannelInfo{ID: "10001", Name: "general"},
				Messages: []dce.Message{
					{ID: "2001", Content: "Message 1", Author: dce.Author{Name: "Alice"}},
				},
				MaxMessageID: "2001",
			}, nil
		},
	}
	fakeDel := &FakeDeliverer{}
	attempts := 0
	fakeComp := &FakeCompleter{
		completeFunc: func(ctx context.Context, messages []llm.Message) (string, error) {
			attempts++
			if attempts <= 2 {
				return "", errors.New("LLM rate limit reached")
			}
			return "Healthy channel summary", nil
		},
	}

	runner, _ := NewRunner(store,
		WithDCEClient(fakeDCE),
		WithDeliverer(fakeDel),
		WithCompleterFactory(func(b, m, k string) (brief.Completer, error) { return fakeComp, nil }),
	)

	_ = runner.Run(context.Background(), "")

	savedSt, _ := store.LoadState()
	ch1 := savedSt.Channels["10001"]
	if ch1.Cursor.Value != "2026-09-14T05:00:00Z" {
		t.Fatalf("cursor was advanced despite LLM failure: %s", ch1.Cursor.Value)
	}
	if !strings.Contains(ch1.LastError, "LLM rate limit reached") {
		t.Fatalf("expected last_error recorded, got: %s", ch1.LastError)
	}

	msgs := fakeDel.GetMessages()
	if len(msgs) != 2 || !strings.Contains(msgs[0], "Brief failed during summarization") {
		t.Fatalf("expected summarization failure notice, got: %v", msgs)
	}
	if attempts != 3 || len(fakeDCE.exports) != 2 {
		t.Fatal("retry repeated export or did not exhaust exactly two attempts")
	}
	if savedSt.Channels["10002"].Cursor != (state.Cursor{Kind: state.CursorKindMessageID, Value: "2001"}) {
		t.Fatal("LLM failure prevented next channel completing")
	}
}

func TestRunner_TransientLLMFailure_RetriedOnce(t *testing.T) {
	attempt := 0
	fakeComp := &FakeCompleter{
		completeFunc: func(ctx context.Context, messages []llm.Message) (string, error) {
			attempt++
			if attempt == 1 {
				return "", errors.New("transient network drop")
			}
			return "• Succeeded on retry attempt.", nil
		},
	}

	retryComp := NewRetryCompleter(fakeComp)
	retryComp.sleep = 10 * time.Millisecond // fast test sleep

	res, err := retryComp.Complete(context.Background(), []llm.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("expected retry to succeed, got: %v", err)
	}
	if res != "• Succeeded on retry attempt." {
		t.Fatalf("unexpected result: %s", res)
	}
	if attempt != 2 {
		t.Fatalf("expected exactly 2 attempts, got %d", attempt)
	}
}

func TestRunner_ContextCancellation_NotRetried(t *testing.T) {
	attempt := 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled upfront

	fakeComp := &FakeCompleter{
		completeFunc: func(c context.Context, messages []llm.Message) (string, error) {
			attempt++
			return "", context.Canceled
		},
	}

	retryComp := NewRetryCompleter(fakeComp)
	_, err := retryComp.Complete(ctx, []llm.Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error on canceled context")
	}
	if attempt != 1 {
		t.Fatalf("expected 1 attempt without retry on cancellation, got %d", attempt)
	}
}

func TestRunner_MultiPartDelivery_PartialFailure_LeavesCursorUnchanged(t *testing.T) {
	for _, failedPart := range []int{1, 2} {
		t.Run(fmt.Sprintf("part_%d", failedPart), func(t *testing.T) {
			store, _ := setupTestStore(t)
			cfg := state.DefaultConfig()
			cfg.Channels = []state.ChannelConfig{{ID: "10001", Name: "general"}, {ID: "10002", Name: "healthy"}}
			_ = store.SaveConfig(cfg)

			st := state.NewEmptyState()
			st.Channels["10001"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T07:00:00Z"}}
			st.Channels["10002"] = st.Channels["10001"]
			_ = store.SaveState(st)

			// Produce a body long enough to require multi-part delivery
			longBody := strings.Repeat("Key point alpha. ", 150) + "\n\n" + strings.Repeat("Key point beta. ", 150)
			fakeDCE := &FakeDCE{
				configured: true,
				exportFunc: func(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error) {
					return &dce.ExportResult{
						Channel: dce.ChannelInfo{ID: "10001", Name: "general"},
						Messages: []dce.Message{
							{ID: "7001", Content: "A", Author: dce.Author{Name: "Alice"}},
							{ID: "7002", Content: "B", Author: dce.Author{Name: "Bob"}},
						},
						MaxMessageID: "7002",
					}, nil
				},
			}

			deliveryAttempt := 0
			fakeDel := &FakeDeliverer{
				deliverFunc: func(ctx context.Context, text string) error {
					current, err := store.LoadState()
					if err != nil {
						t.Fatal(err)
					}
					if current.Channels["10001"].Cursor != st.Channels["10001"].Cursor {
						t.Fatal("cursor moved before all sends returned success")
					}
					if strings.HasPrefix(text, "#healthy") {
						if current.Channels["10002"].Cursor != st.Channels["10002"].Cursor {
							t.Fatal("healthy cursor moved before final send succeeded")
						}
						return nil
					}
					deliveryAttempt++
					if deliveryAttempt == failedPart {
						return errors.New("telegram network socket timeout")
					}
					return nil
				},
			}

			fakeComp := &FakeCompleter{
				completeFunc: func(ctx context.Context, messages []llm.Message) (string, error) {
					return longBody, nil
				},
			}

			runner, _ := NewRunner(store,
				WithDCEClient(fakeDCE),
				WithDeliverer(fakeDel),
				WithCompleterFactory(func(b, m, k string) (brief.Completer, error) { return fakeComp, nil }),
			)

			_ = runner.Run(context.Background(), "")

			savedSt, _ := store.LoadState()
			ch1 := savedSt.Channels["10001"]
			// Invariant: Part 2 failed -> cursor MUST NOT advance!
			if ch1.Cursor.Value != "2026-09-14T07:00:00Z" {
				t.Fatalf("CRITICAL INVARIANT VIOLATION: cursor advanced after partial delivery failure! cursor: %+v", ch1.Cursor)
			}
			if !strings.Contains(ch1.LastError, "telegram network socket timeout") {
				t.Fatalf("expected last_error recorded, got %s", ch1.LastError)
			}
			if deliveryAttempt != failedPart {
				t.Fatalf("attempted %d sends after failure at %d", deliveryAttempt, failedPart)
			}
			if savedSt.Channels["10002"].Cursor != (state.Cursor{Kind: state.CursorKindMessageID, Value: "7002"}) {
				t.Fatal("Telegram failure prevented subsequent multipart channel completing")
			}
		})
	}
}

func TestRetry_DeadlineAndCancellationDuringDelay(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		comp := &FakeCompleter{completeFunc: func(context.Context, []llm.Message) (string, error) {
			calls++
			if deadline {
				return "", context.DeadlineExceeded
			}
			go func() { time.Sleep(10 * time.Millisecond); cancel() }()
			return "", errors.New("transient error")
		}}
		_, err := NewRetryCompleter(comp).Complete(ctx, []llm.Message{{Role: "user", Content: "hi"}})
		cancel()
		if err == nil || calls != 1 {
			t.Fatalf("retried after cancellation/deadline: calls=%d error=%v", calls, err)
		}
	}
}

func TestRunner_SaveStateFailure_AfterDelivery_LeavesDurableCursorUnchanged(t *testing.T) {
	store, _ := setupTestStore(t)
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "10001", Name: "general"}, {ID: "10002", Name: "healthy"}}
	_ = store.SaveConfig(cfg)

	st := state.NewEmptyState()
	st.Channels["10001"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T08:00:00Z"}}
	st.Channels["10002"] = st.Channels["10001"]
	_ = store.SaveState(st)

	fakeDCE := &FakeDCE{
		configured: true,
		exportFunc: func(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error) {
			return &dce.ExportResult{
				Channel: dce.ChannelInfo{ID: "10001", Name: "general"},
				Messages: []dce.Message{
					{ID: "8001", Content: "Ready", Author: dce.Author{Name: "Alice"}},
				},
				MaxMessageID: "8001",
			}, nil
		},
	}

	before, err := os.ReadFile(store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	dir := store.DataDir()
	backup := dir + "-saved"
	blocked := false
	restore := func() {
		if blocked {
			if err := os.Remove(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(backup, dir); err != nil {
				t.Fatal(err)
			}
			blocked = false
		}
	}
	defer restore()
	fakeDel := &FakeDeliverer{deliverFunc: func(ctx context.Context, text string) error {
		if strings.HasPrefix(text, "#general\n1 message") {
			// A file at the data-directory path forces an actual I/O failure in SaveState.
			if err := os.Rename(dir, backup); err != nil {
				t.Fatal(err)
			}
			blocked = true
			if err := os.WriteFile(dir, []byte("block writes"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if strings.Contains(text, "may duplicate") {
			restore()
			after, err := os.ReadFile(store.StatePath())
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("failed commit changed durable state")
			}
		}
		return nil
	}}
	fakeComp := &FakeCompleter{}

	runner, _ := NewRunner(store,
		WithDCEClient(fakeDCE),
		WithDeliverer(fakeDel),
		WithCompleterFactory(func(b, m, k string) (brief.Completer, error) { return fakeComp, nil }),
	)

	_ = runner.Run(context.Background(), "")

	after, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if after.Channels["10001"].Cursor != st.Channels["10001"].Cursor {
		t.Fatal("failed commit consumed first channel")
	}
	if after.Channels["10002"].Cursor != (state.Cursor{Kind: state.CursorKindMessageID, Value: "8001"}) {
		t.Fatal("persistence failure prevented next channel completing")
	}
	msgs := fakeDel.GetMessages()
	if len(msgs) != 3 || !strings.Contains(msgs[0], "#general\n1 message") || !strings.Contains(msgs[1], "may duplicate") {
		t.Fatalf("expected delivered brief followed by duplicate warning: %v", msgs)
	}
}

func TestRunner_SingleChannelRun_ValidAndUnfollowed(t *testing.T) {
	store, _ := setupTestStore(t)
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{
		{ID: "10001", Name: "general"},
		{ID: "10002", Name: "dev"},
	}
	_ = store.SaveConfig(cfg)

	st := state.NewEmptyState()
	st.Channels["10001"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T08:00:00Z"}}
	st.Channels["10002"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T08:00:00Z"}}
	_ = store.SaveState(st)

	fakeDCE := &FakeDCE{configured: true}
	fakeDel := &FakeDeliverer{}
	fakeComp := &FakeCompleter{}

	runner, _ := NewRunner(store,
		WithDCEClient(fakeDCE),
		WithDeliverer(fakeDel),
		WithCompleterFactory(func(b, m, k string) (brief.Completer, error) { return fakeComp, nil }),
	)

	// Unfollowed channel rejected
	err := runner.Run(context.Background(), "99999")
	if err == nil {
		t.Fatal("expected error for unfollowed channel ID")
	}

	// Followed channel runs alone
	if err := runner.Run(context.Background(), "10002"); err != nil {
		t.Fatalf("expected single channel run to succeed, got: %v", err)
	}

	fakeDCE.mu.Lock()
	defer fakeDCE.mu.Unlock()
	if len(fakeDCE.exports) != 1 || fakeDCE.exports[0].ChannelID != "10002" {
		t.Fatalf("expected only channel 10002 to be exported, got: %v", fakeDCE.exports)
	}
}

func TestRunner_SanitizesAPIKeyInErrors(t *testing.T) {
	store, _ := setupTestStore(t)
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "10001", Name: "general"}}
	_ = store.SaveConfig(cfg)

	const secretKey = "super-secret-gemini-key-12345"
	fakeDel := &FakeDeliverer{}
	runner, _ := NewRunner(store,
		WithDeliverer(fakeDel),
		WithLLMAPIKey(secretKey),
		WithCompleterFactory(func(b, m, k string) (brief.Completer, error) {
			return nil, fmt.Errorf("authentication error: failed with key %s", secretKey)
		}),
		WithDCEClient(&FakeDCE{configured: true}),
	)

	_ = runner.Run(context.Background(), "")

	msgs := fakeDel.GetMessages()
	for _, m := range msgs {
		if strings.Contains(m, secretKey) {
			t.Fatalf("CRITICAL SECURITY LEAK: secret key found in user message: %s", m)
		}
		if !strings.Contains(m, "[REDACTED]") {
			t.Errorf("expected [REDACTED] in error message: %s", m)
		}
	}
}

func TestRunner_ServerHeadingFormatting(t *testing.T) {
	store, _ := setupTestStore(t)
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{
		{ID: "1001", Name: "configured-name"},
		{ID: "1002", Name: "no-guild-channel"},
		{ID: "1003", Name: "empty-zero-msgs"},
	}
	_ = store.SaveConfig(cfg)

	st := state.NewEmptyState()
	for _, ch := range cfg.Channels {
		st.Channels[ch.ID] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindTimestamp, Value: "2026-09-14T08:00:00Z"}}
	}
	_ = store.SaveState(st)

	fakeDCE := &FakeDCE{
		configured: true,
		exportFunc: func(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error) {
			switch req.ChannelID {
			case "1001":
				return &dce.ExportResult{
					Guild:   dce.GuildInfo{Name: "LocalLLM"},
					Channel: dce.ChannelInfo{ID: "1001", Name: "dce-actual-name"},
					Messages: []dce.Message{
						{ID: "9001", Content: "Hello world", Author: dce.Author{Name: "Alice"}},
					},
					MaxMessageID: "9001",
				}, nil
			case "1002":
				return &dce.ExportResult{
					Guild:   dce.GuildInfo{Name: ""}, // empty server name
					Channel: dce.ChannelInfo{ID: "1002", Name: "no-guild-channel"},
					Messages: []dce.Message{
						{ID: "9002", Content: "Fallback server name", Author: dce.Author{Name: "Bob"}},
					},
					MaxMessageID: "9002",
				}, nil
			case "1003":
				return &dce.ExportResult{
					Guild:    dce.GuildInfo{Name: "LocalLLM"},
					Channel:  dce.ChannelInfo{ID: "1003", Name: "empty-zero-msgs"},
					Messages: []dce.Message{},
				}, nil
			}
			return &dce.ExportResult{}, nil
		},
	}

	fakeDel := &FakeDeliverer{}
	fakeComp := &FakeCompleter{
		completeFunc: func(ctx context.Context, messages []llm.Message) (string, error) {
			return "• Summary content.", nil
		},
	}

	runner, _ := NewRunner(store,
		WithDCEClient(fakeDCE),
		WithDeliverer(fakeDel),
		WithCompleterFactory(func(b, m, k string) (brief.Completer, error) { return fakeComp, nil }),
	)

	if err := runner.Run(context.Background(), ""); err != nil {
		t.Fatalf("expected run to succeed, got %v", err)
	}

	msgs := fakeDel.GetMessages()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 delivered messages, got %d: %v", len(msgs), msgs)
	}

	// 1001: Server + preferred DCE channel name
	expected1 := "LocalLLM · #dce-actual-name\n1 message\n\n• Summary content."
	if msgs[0] != expected1 {
		t.Errorf("channel 1001:\ngot:  %q\nwant: %q", msgs[0], expected1)
	}

	// 1002: Empty server name fallback
	expected2 := "#no-guild-channel\n1 message\n\n• Summary content."
	if msgs[1] != expected2 {
		t.Errorf("channel 1002:\ngot:  %q\nwant: %q", msgs[1], expected2)
	}

	// 1003: Zero messages with server name
	expected3 := "LocalLLM · #empty-zero-msgs\nNo new messages."
	if msgs[2] != expected3 {
		t.Errorf("channel 1003:\ngot:  %q\nwant: %q", msgs[2], expected3)
	}
}
