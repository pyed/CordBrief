package job

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pyed/CordBrief/internal/brief"
	"github.com/pyed/CordBrief/internal/dce"
	"github.com/pyed/CordBrief/internal/llm"
	"github.com/pyed/CordBrief/internal/state"
)

func TestDeliverChannelLogsFailureOrDurableCommit(t *testing.T) {
	for _, outcome := range []string{"success", "delivery failure", "save failure"} {
		t.Run(outcome, func(t *testing.T) {
			store, _ := setupTestStore(t)
			st := state.NewEmptyState()
			original := state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindMessageID, Value: "1"}, LastError: "previous error"}
			st.Channels["123"] = original
			if err := store.SaveState(st); err != nil {
				t.Fatal(err)
			}

			var logs bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(previous)
			const secret = "test-api-key"
			calls := 0
			deliverer := &FakeDeliverer{deliverFunc: func(context.Context, string) error {
				calls++
				if strings.Contains(logs.String(), "successfully delivered brief") {
					t.Fatal("success logged before durable commit")
				}
				current, err := store.LoadState()
				if err != nil || current.Channels["123"].Cursor != original.Cursor {
					t.Fatal("cursor advanced before all parts succeeded", err)
				}
				if outcome == "delivery failure" && calls == 2 {
					return errors.New("send failed: " + secret)
				}
				return nil
			}}
			r, err := NewRunner(store, WithDeliverer(deliverer), WithLLMAPIKey(secret))
			if err != nil {
				t.Fatal(err)
			}
			result := &preparedChannel{channel: state.ChannelConfig{ID: "123", Name: "general"}, parts: []string{"part 1", "part 2"}, maxMessageID: "2"}
			if outcome == "save failure" {
				// Invalid cursor forces SaveState validation to fail on every platform.
				result.maxMessageID = "invalid"
			}
			r.deliverChannel(context.Background(), result)
			current, err := store.LoadState()
			if err != nil {
				t.Fatal(err)
			}
			failure := "[job] Telegram delivery failed for #general (123): send failed: [REDACTED]"
			success := "[job] successfully delivered brief and committed cursor 2 for #general (123)"
			if strings.Contains(logs.String(), secret) {
				t.Fatal("secret leaked in log")
			}
			switch outcome {
			case "success":
				if strings.Count(logs.String(), success) != 1 || current.Channels["123"].Cursor.Value != "2" || current.Channels["123"].LastError != "" || calls != 2 {
					t.Fatal("missing successful durable commit", logs.String(), current)
				}
			case "delivery failure":
				if strings.Count(logs.String(), failure) != 1 || current.Channels["123"].Cursor != original.Cursor || current.Channels["123"].LastError != "send failed: [REDACTED]" || calls != 2 {
					t.Fatal("failure log or state incorrect", logs.String(), current)
				}
			case "save failure":
				if current.Channels["123"] != original || calls != 3 || !strings.Contains(deliverer.GetMessages()[2], "Warning: saving state failed") {
					t.Fatal("save failure changed state or lost warning", current)
				}
			}
			if outcome != "success" && strings.Contains(logs.String(), "successfully delivered brief") {
				t.Fatal("false commit log", logs.String())
			}
			if outcome != "delivery failure" && strings.Contains(logs.String(), "Telegram delivery failed") {
				t.Fatal("false delivery failure log", logs.String())
			}
		})
	}
}

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

func TestRunner_CollectorChoosesCutoffForEachChannel(t *testing.T) {
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

	// The collector chooses the boundary after waiting for its actual DCE slot.
	if !fakeDCE.exports[0].Before.IsZero() || !fakeDCE.exports[1].Before.IsZero() {
		t.Fatal("runner pinned the cutoff before collection")
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
				return "", &llm.StatusError{StatusCode: http.StatusTooManyRequests, Message: "LLM rate limit reached"}
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
				return "", &url.Error{Op: "Post", URL: "https://api.openai.com/v1/chat/completions", Err: &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}}
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

type fakeNetError struct {
	msg string
}

func (e *fakeNetError) Error() string   { return e.msg }
func (e *fakeNetError) Timeout() bool   { return false }
func (e *fakeNetError) Temporary() bool { return false }

func TestRetryCompleter_TransientAndPermanent(t *testing.T) {
	transientCases := []struct {
		name string
		err  error
	}{
		{"HTTP 429 Too Many Requests", &llm.StatusError{StatusCode: 429, Message: "rate limit"}},
		{"HTTP 500 Internal Server Error", &llm.StatusError{StatusCode: 500, Message: "server error"}},
		{"HTTP 502 Bad Gateway", &llm.StatusError{StatusCode: 502, Message: "bad gateway"}},
		{"HTTP 503 Service Unavailable", &llm.StatusError{StatusCode: 503, Message: "overloaded"}},
		{"HTTP 504 Gateway Timeout", &llm.StatusError{StatusCode: 504, Message: "gateway timeout"}},
		{"net.OpError dial network failure", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNRESET}},
		{"net.Error implementation", &fakeNetError{msg: "network connection lost"}},
		{"url.Error transport failure", &url.Error{Op: "Post", URL: "http://localhost", Err: &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}}},
		{"HTTP client timeout while caller context alive", &url.Error{Op: "Post", URL: "http://localhost", Err: context.DeadlineExceeded}},
		{"io.ErrUnexpectedEOF connection cut", io.ErrUnexpectedEOF},
	}

	for _, tc := range transientCases {
		t.Run("transient_"+tc.name, func(t *testing.T) {
			attempts := 0
			fakeComp := &FakeCompleter{
				completeFunc: func(c context.Context, messages []llm.Message) (string, error) {
					attempts++
					if attempts == 1 {
						return "", tc.err
					}
					return "recovered", nil
				},
			}
			r := NewRetryCompleter(fakeComp)
			r.sleep = 5 * time.Millisecond

			res, err := r.Complete(context.Background(), []llm.Message{{Role: "user", Content: "hi"}})
			if err != nil {
				t.Fatalf("expected transient error to recover on retry: %v", err)
			}
			if res != "recovered" {
				t.Fatalf("unexpected response: %s", res)
			}
			if attempts != 2 {
				t.Fatalf("expected 2 attempts for transient error, got %d", attempts)
			}
		})
	}

	permanentCases := []struct {
		name string
		err  error
	}{
		{"plain error matching connection reset", errors.New("connection reset by peer")},
		{"plain error matching rate limit", errors.New("rate limit reached")},
		{"arbitrary local error", errors.New("arbitrary application failure")},
		{"JSON parse error", fmt.Errorf("failed to parse llm response JSON: invalid character")},
		{"validation zero choices", errors.New("llm response contained zero completion choices")},
		{"validation empty content", errors.New("llm response returned empty completion content")},
		{"validation oversized response", errors.New("llm response exceeded maximum allowed size (4 MiB)")},
		{"HTTP 400 Bad Request", &llm.StatusError{StatusCode: 400, Message: "invalid request"}},
		{"HTTP 401 Unauthorized", &llm.StatusError{StatusCode: 401, Message: "invalid api key"}},
		{"HTTP 403 Forbidden", &llm.StatusError{StatusCode: 403, Message: "not allowed"}},
		{"HTTP 404 Not Found (invalid model)", &llm.StatusError{StatusCode: 404, Message: "model not found"}},
		{"HTTP 422 Unprocessable Entity", &llm.StatusError{StatusCode: 422, Message: "unprocessable"}},
		{"raw context.Canceled", context.Canceled},
		{"raw context.DeadlineExceeded", context.DeadlineExceeded},
	}

	for _, tc := range permanentCases {
		t.Run("permanent_"+tc.name, func(t *testing.T) {
			attempts := 0
			fakeComp := &FakeCompleter{
				completeFunc: func(c context.Context, messages []llm.Message) (string, error) {
					attempts++
					return "", tc.err
				},
			}
			r := NewRetryCompleter(fakeComp)
			r.sleep = 5 * time.Millisecond

			_, err := r.Complete(context.Background(), []llm.Message{{Role: "user", Content: "hi"}})
			if err == nil {
				t.Fatalf("expected error for permanent status, got nil")
			}
			if attempts != 1 {
				t.Fatalf("expected exactly 1 attempt for permanent status %s (no retry), got %d", tc.name, attempts)
			}
		})
	}

	t.Run("caller cancellation does not retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		attempts := 0
		fakeComp := &FakeCompleter{
			completeFunc: func(c context.Context, messages []llm.Message) (string, error) {
				attempts++
				return "", c.Err()
			},
		}
		r := NewRetryCompleter(fakeComp)
		r.sleep = 5 * time.Millisecond

		_, err := r.Complete(ctx, []llm.Message{{Role: "user", Content: "hi"}})
		if err == nil {
			t.Fatal("expected error on canceled context, got nil")
		}
		if attempts != 1 {
			t.Fatalf("expected exactly 1 attempt on canceled context, got %d", attempts)
		}
	})

	t.Run("caller deadline expiration does not retry", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		attempts := 0
		fakeComp := &FakeCompleter{
			completeFunc: func(c context.Context, messages []llm.Message) (string, error) {
				attempts++
				return "", &url.Error{Op: "Post", URL: "http://localhost", Err: context.DeadlineExceeded}
			},
		}
		r := NewRetryCompleter(fakeComp)
		r.sleep = 5 * time.Millisecond

		_, err := r.Complete(ctx, []llm.Message{{Role: "user", Content: "hi"}})
		if err == nil {
			t.Fatal("expected error on expired context, got nil")
		}
		if attempts != 1 {
			t.Fatalf("expected exactly 1 attempt on expired context, got %d", attempts)
		}
	})
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
			return "", &url.Error{Op: "Post", URL: "http://localhost", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNRESET}}
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

func TestBriefTimingAndFreshness(t *testing.T) {
	for _, mode := range []string{"manual", "scheduled", "overrun"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store, dir := setupTestStore(t)
				cfg := state.DefaultConfig()
				st := state.NewEmptyState()
				for i := 1; i <= 4; i++ {
					id := fmt.Sprint(i)
					cfg.Channels = append(cfg.Channels, state.ChannelConfig{ID: id, Name: id})
					st.Channels[id] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindMessageID, Value: "1"}}
				}
				if err := store.SaveConfig(cfg); err != nil {
					t.Fatal(err)
				}
				if err := store.SaveState(st); err != nil {
					t.Fatal(err)
				}
				start := time.Now()
				work := 2 * time.Minute
				if mode == "overrun" {
					work = 20 * time.Minute
				}
				var exports, summaries, deliveries []time.Time
				var afters []string
				nextBrief := false
				unchanged := func() {
					if mode == "manual" || nextBrief {
						return
					}
					current, err := store.LoadState()
					if err != nil {
						t.Fatal(err)
					}
					for _, ch := range current.Channels {
						if ch.Cursor.Value != "1" {
							t.Fatal("cursor advanced during preparation")
						}
					}
				}
				manager, err := dce.NewManager(dir, "fake-dce", "fake-token", dce.WithBootstrapVersion("2.48"),
					dce.WithCommandRunner(func(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
						unchanged()
						values := map[string]string{}
						for i := 0; i+1 < len(args); i++ {
							values[args[i]] = args[i+1]
						}
						exports = append(exports, time.Now())
						afters = append(afters, values["--after"])
						cutoff, err := time.Parse(time.RFC3339, values["--before"])
						if err != nil || !cutoff.Equal(time.Now()) {
							t.Fatalf("actual export cutoff: %v %v", cutoff, err)
						}
						messages := []map[string]any{}
						for _, message := range []struct {
							id string
							at time.Time
						}{{"10", start.Add(-time.Minute)}, {"11", start.Add(5 * time.Minute)}} {
							if message.id == "11" && values["-c"] != "1" {
								continue
							}
							if dce.CompareSnowflake(message.id, values["--after"]) > 0 && message.at.Before(cutoff) {
								messages = append(messages, map[string]any{"id": message.id, "timestamp": message.at, "content": "message " + message.id})
							}
						}
						data, _ := json.Marshal(map[string]any{"channel": map[string]string{"id": values["-c"]}, "messages": messages})
						return os.WriteFile(values["-o"], data, 0600)
					}))
				if err != nil {
					t.Fatal(err)
				}
				completer := &FakeCompleter{completeFunc: func(context.Context, []llm.Message) (string, error) {
					unchanged()
					summaries = append(summaries, time.Now())
					time.Sleep(work)
					return "Summary", nil
				}}
				deliverer := &FakeDeliverer{deliverFunc: func(context.Context, string) error {
					if len(deliveries) == 0 {
						unchanged()
					}
					deliveries = append(deliveries, time.Now())
					return nil
				}}
				r, err := NewRunner(store, WithDCEClient(manager), WithDeliverer(deliverer), WithCompleterFactory(func(string, string, string) (brief.Completer, error) { return completer, nil }))
				if err != nil {
					t.Fatal(err)
				}
				if mode == "manual" {
					if err := r.Run(context.Background(), ""); err != nil {
						t.Fatal(err)
					}
				} else {
					if !r.StartScheduled(context.Background(), start.Add(time.Hour)) {
						t.Fatal("not started")
					}
					r.Wait()
				}
				if len(exports) != 4 || len(summaries) != 4 || len(deliveries) != 4 {
					t.Fatalf("exports=%v summaries=%v deliveries=%v", exports, summaries, deliveries)
				}
				for i := range 4 {
					want := start.Add(time.Duration(i) * max(15*time.Minute, work))
					if !exports[i].Equal(want) || !summaries[i].Equal(want) {
						t.Fatal("LLM work did not use cooldown window", exports, summaries)
					}
					delivery := want.Add(work)
					if mode != "manual" {
						delivery = start.Add(max(time.Hour, 4*work))
					}
					if !deliveries[i].Equal(delivery) {
						t.Fatalf("delivery %d: %v want %v", i, deliveries[i], delivery)
					}
				}
				current, _ := store.LoadState()
				if current.Channels["1"].Cursor.Value != "10" {
					t.Fatal("early channel consumed a later arrival")
				}
				nextBrief = true
				if err := r.Run(context.Background(), "1"); err != nil {
					t.Fatal(err)
				}
				current, _ = store.LoadState()
				if len(exports) != 5 || afters[4] != "10" || current.Channels["1"].Cursor.Value != "11" {
					t.Fatal("later arrival was lost or channel re-exported at delivery")
				}
			})
		})
	}
}

func TestScheduledCancellationAndPartialDeliveryKeepCursor(t *testing.T) {
	for _, cancelWhileWaiting := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			store, _ := setupTestStore(t)
			cfg := state.DefaultConfig()
			cfg.Channels = []state.ChannelConfig{{ID: "1", Name: "one"}}
			if err := store.SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}
			st := state.NewEmptyState()
			st.Channels["1"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindMessageID, Value: "1"}}
			if err := store.SaveState(st); err != nil {
				t.Fatal(err)
			}
			exporter := &FakeDCE{configured: true, exportFunc: func(context.Context, dce.ExportRequest) (*dce.ExportResult, error) {
				return &dce.ExportResult{Messages: []dce.Message{{ID: "2", Content: "update"}}, MaxMessageID: "2"}, nil
			}}
			comp := &FakeCompleter{completeFunc: func(context.Context, []llm.Message) (string, error) { return strings.Repeat("summary ", 1000), nil }}
			calls := 0
			del := &FakeDeliverer{deliverFunc: func(context.Context, string) error {
				calls++
				current, _ := store.LoadState()
				if current.Channels["1"].Cursor.Value != "1" {
					t.Fatal("early commit")
				}
				if calls == 2 {
					return fmt.Errorf("send failed")
				}
				return nil
			}}
			r, _ := NewRunner(store, WithDCEClient(exporter), WithDeliverer(del), WithCompleterFactory(func(string, string, string) (brief.Completer, error) { return comp, nil }))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if !r.StartScheduled(ctx, time.Now().Add(time.Hour)) {
				t.Fatal("not started")
			}
			if cancelWhileWaiting {
				synctest.Wait()
				cancel()
			}
			r.Wait()
			current, _ := store.LoadState()
			if current.Channels["1"].Cursor.Value != "1" {
				t.Fatal("cursor consumed undelivered brief")
			}
			if (cancelWhileWaiting && calls != 0) || (!cancelWhileWaiting && calls != 2) {
				t.Fatal("unexpected sends", calls)
			}
		})
	}
}

func TestRunner_Mutate_ExcludesJobStart(t *testing.T) {
	store, _ := setupTestStore(t)
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "100", Name: "general"}}
	_ = store.SaveConfig(cfg)
	st := state.NewEmptyState()
	st.Channels["100"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindMessageID, Value: "10"}}
	_ = store.SaveState(st)

	exporter := &FakeDCE{
		configured: true,
		exportFunc: func(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error) {
			return &dce.ExportResult{
				Channel:  dce.ChannelInfo{ID: req.ChannelID},
				Messages: []dce.Message{},
			}, nil
		},
	}
	var delivered []string
	var delMu sync.Mutex
	del := &FakeDeliverer{
		deliverFunc: func(ctx context.Context, msg string) error {
			delMu.Lock()
			delivered = append(delivered, msg)
			delMu.Unlock()
			return nil
		},
	}

	r, err := NewRunner(store, WithDCEClient(exporter), WithDeliverer(del))
	if err != nil {
		t.Fatal(err)
	}

	mutateStarted := make(chan struct{})
	continueMutate := make(chan struct{})
	mutateDone := make(chan struct{})

	go func() {
		defer close(mutateDone)
		err := r.Mutate(func() error {
			close(mutateStarted)
			<-continueMutate
			cfg, _ := store.LoadConfig()
			cfg.Channels = append(cfg.Channels, state.ChannelConfig{ID: "200", Name: "random"})
			_ = store.SaveConfig(cfg)
			st, _ := store.LoadState()
			st.Channels["200"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindMessageID, Value: "20"}}
			_ = store.SaveState(st)
			return nil
		})
		if err != nil {
			t.Errorf("mutation failed: %v", err)
		}
	}()

	<-mutateStarted

	jobStarted := make(chan bool)
	go func() {
		ok := r.Start(context.Background(), "")
		jobStarted <- ok
	}()

	select {
	case <-jobStarted:
		t.Fatal("job started while mutation was still in progress!")
	case <-time.After(50 * time.Millisecond):
	}

	close(continueMutate)
	<-mutateDone

	ok := <-jobStarted
	if !ok {
		t.Fatal("job failed to start after mutation completed")
	}

	r.Wait()

	delMu.Lock()
	defer delMu.Unlock()
	if len(delivered) != 2 {
		t.Fatalf("expected 2 channel deliveries, got %d: %v", len(delivered), delivered)
	}
}

func TestRunner_Mutate_RejectedWhenRunning(t *testing.T) {
	store, _ := setupTestStore(t)
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "100", Name: "general"}}
	_ = store.SaveConfig(cfg)
	st := state.NewEmptyState()
	st.Channels["100"] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindMessageID, Value: "10"}}
	_ = store.SaveState(st)

	jobHold := make(chan struct{})
	exporter := &FakeDCE{
		configured: true,
		exportFunc: func(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error) {
			<-jobHold
			return &dce.ExportResult{
				Channel: dce.ChannelInfo{ID: req.ChannelID},
			}, nil
		},
	}
	del := &FakeDeliverer{}

	r, err := NewRunner(store, WithDCEClient(exporter), WithDeliverer(del))
	if err != nil {
		t.Fatal(err)
	}

	if !r.Start(context.Background(), "") {
		t.Fatal("failed to start runner")
	}

	called := false
	err = r.Mutate(func() error {
		called = true
		return nil
	})

	if !errors.Is(err, ErrBriefRunning) {
		t.Fatalf("expected ErrBriefRunning, got %v", err)
	}
	if called {
		t.Fatal("mutation function was called while brief was running")
	}

	close(jobHold)
	r.Wait()
}

func TestRunner_OversizedExport_LeavesCursorUnchanged(t *testing.T) {
	store, _ := setupTestStore(t)
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "100", Name: "general"}}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	st := state.NewEmptyState()
	originalCursor := state.Cursor{Kind: state.CursorKindMessageID, Value: "500"}
	st.Channels["100"] = state.ChannelState{Cursor: originalCursor}
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}

	d := &FakeDCE{
		configured: true,
		exportFunc: func(ctx context.Context, req dce.ExportRequest) (*dce.ExportResult, error) {
			return nil, fmt.Errorf("%w: 60000000 bytes exceeds limit of 52428800 bytes", dce.ErrExportTooLarge)
		},
	}
	deliverer := &FakeDeliverer{}

	runner, err := NewRunner(store, WithDCEClient(d), WithDeliverer(deliverer))
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	err = runner.Run(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected runner error: %v", err)
	}

	loadedSt, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	chState := loadedSt.Channels["100"]
	if chState.Cursor != originalCursor {
		t.Fatalf("expected cursor %v to remain unchanged, got %v", originalCursor, chState.Cursor)
	}
	if !strings.Contains(chState.LastError, "dce export exceeded maximum allowed size") {
		t.Fatalf("expected LastError to record size error, got: %s", chState.LastError)
	}

	msgs := deliverer.GetMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 delivery notice, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0], "Brief failed during Discord collection") || !strings.Contains(msgs[0], "Nothing was consumed") {
		t.Fatalf("unexpected delivery notice: %s", msgs[0])
	}
}
