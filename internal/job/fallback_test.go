package job

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pyed/CordBrief/internal/brief"
	"github.com/pyed/CordBrief/internal/dce"
	"github.com/pyed/CordBrief/internal/llm"
	"github.com/pyed/CordBrief/internal/state"
)

func TestFallbackTransitionAndRetry(t *testing.T) {
	t.Run("primary success never initializes fallback", func(t *testing.T) {
		f := &fallbackCompleter{primary: &FakeCompleter{}, createFallback: func() (brief.Completer, error) { t.Fatal("fallback touched"); return nil, nil }}
		if _, err := f.Complete(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		primary := &FakeCompleter{completeFunc: func(context.Context, []llm.Message) (string, error) { return "", &llm.StatusError{StatusCode: 503} }}
		fallback := &FakeCompleter{}
		created := 0
		f := &fallbackCompleter{primary: NewRetryCompleter(primary), createFallback: func() (brief.Completer, error) { created++; return NewRetryCompleter(fallback), nil }}
		for range 3 {
			if _, err := f.Complete(context.Background(), []llm.Message{{Content: "same collected input"}}); err != nil {
				t.Fatal(err)
			}
		}
		if primary.calls != 2 || fallback.calls != 3 || created != 1 || !f.active {
			t.Fatal("retry or sticky transition wrong", primary.calls, fallback.calls)
		}
	})
}

func TestFallbackFailedChannelDoesNotCorruptNext(t *testing.T) {
	store, _ := setupTestStore(t)
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "1"}, {ID: "2"}, {ID: "3"}}
	cfg.Fallback = &state.FallbackConfig{Enabled: true, LLMConfig: state.LLMConfig{BaseURL: "https://fallback.example/v1", Model: "fallback"}}
	st := state.NewEmptyState()
	for _, ch := range cfg.Channels {
		st.Channels[ch.ID] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindMessageID, Value: "1"}}
	}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	p := &FakeCompleter{completeFunc: func(context.Context, []llm.Message) (string, error) { return "", errors.New("permanent failure") }}
	calls := 0
	f := &FakeCompleter{completeFunc: func(context.Context, []llm.Message) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("first channel fails")
		}
		return "summary", nil
	}}
	exporter := &FakeDCE{configured: true, exportFunc: func(context.Context, dce.ExportRequest) (*dce.ExportResult, error) {
		return &dce.ExportResult{Messages: []dce.Message{{ID: "2", Content: "message"}}, MaxMessageID: "2"}, nil
	}}
	r, _ := NewRunner(store, WithDCEClient(exporter), WithDeliverer(&FakeDeliverer{}), WithCompleterFactory(func(base, model, key string) (brief.Completer, error) {
		if model == "fallback" {
			return f, nil
		}
		return p, nil
	}))
	if err := r.Run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	after, _ := store.LoadState()
	if after.Channels["1"].Cursor.Value != "1" || after.Channels["2"].Cursor.Value != "2" || after.Channels["3"].Cursor.Value != "2" || len(exporter.exports) != 3 || p.calls != 2 || f.calls != 3 {
		t.Fatal("channel isolation or successful takeover broken", after)
	}
}

func TestFallbackCancellation(t *testing.T) {
	for _, mode := range []string{"pre-canceled", "caller", "deadline", "raw cancellation", "raw deadline", "retry delay", "transport timeout", "HTTP", "network", "parser", "ordinary"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "deadline" {
					ctx, cancel = context.WithTimeout(context.Background(), time.Second)
					defer cancel()
				}
				if mode == "pre-canceled" {
					cancel()
				}
				primary := &FakeCompleter{completeFunc: func(context.Context, []llm.Message) (string, error) {
					switch mode {
					case "caller":
						cancel()
					case "deadline":
						time.Sleep(2 * time.Second)
					case "raw cancellation":
						return "", context.Canceled
					case "raw deadline":
						return "", context.DeadlineExceeded
					case "transport timeout":
						return "", &url.Error{Op: "Post", URL: "http://example", Err: context.DeadlineExceeded}
					case "HTTP":
						return "", &llm.StatusError{StatusCode: 503}
					case "network":
						return "", io.ErrUnexpectedEOF
					case "parser":
						return "", errors.New("failed to parse llm response JSON")
					case "retry delay":
						go func() { time.Sleep(time.Second); cancel() }()
						return "", &llm.StatusError{StatusCode: 503}
					}
					return "", errors.New("primary failed")
				}}
				fallback := &FakeCompleter{}
				created := 0
				f := &fallbackCompleter{primary: NewRetryCompleter(primary), createFallback: func() (brief.Completer, error) { created++; return fallback, nil }}
				_, err := f.Complete(ctx, nil)
				wantPrimary := 1
				if mode == "pre-canceled" {
					wantPrimary = 0
				}
				if mode == "transport timeout" || mode == "HTTP" || mode == "network" {
					wantPrimary = 2
				}
				if primary.calls != wantPrimary {
					t.Fatal("primary retry count changed", primary.calls, wantPrimary)
				}
				if ctx.Err() != nil {
					if err != ctx.Err() || created != 0 || fallback.calls != 0 {
						t.Fatal("caller cancellation was not returned without fallback", err)
					}
				} else if err != nil || created != 1 || fallback.calls != 1 || !f.active {
					t.Fatal("live caller did not permit fallback", err)
				}
			})
		})
	}
}

func TestFallbackDisabledReturnsPrimaryError(t *testing.T) {
	for _, primaryErr := range []error{context.Canceled, context.DeadlineExceeded, errors.New("ordinary failure")} {
		f := &fallbackCompleter{primary: &FakeCompleter{completeFunc: func(context.Context, []llm.Message) (string, error) { return "", primaryErr }}}
		if _, err := f.Complete(context.Background(), nil); err != primaryErr {
			t.Fatal("disabled fallback changed primary error", err)
		}
	}
}

func TestFallbackBothProvidersBoundedAndNotStickyOnFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fail := func(context.Context, []llm.Message) (string, error) { return "", &llm.StatusError{StatusCode: 429} }
		p, f := &FakeCompleter{completeFunc: fail}, &FakeCompleter{completeFunc: fail}
		c := &fallbackCompleter{primary: NewRetryCompleter(p), createFallback: func() (brief.Completer, error) { return NewRetryCompleter(f), nil }}
		for range 2 {
			if _, err := c.Complete(context.Background(), nil); err == nil {
				t.Fatal("expected failure")
			}
		}
		if p.calls != 4 || f.calls != 4 || c.active {
			t.Fatal("retry bound or failed takeover changed")
		}
	})
}

func TestFallbackRunnerIsolationAndNextJob(t *testing.T) {
	for _, mode := range []string{"primary success", "fallback success", "both fail", "disabled", "local fallback", "scheduled"} {
		t.Run(mode, func(t *testing.T) {
			store, _ := setupTestStore(t)
			var primaryBodies, fallbackBodies [][]byte
			fallbackKey := "fallback-key"
			if mode == "local fallback" {
				fallbackKey = ""
			}
			fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				want := ""
				if fallbackKey != "" {
					want = "Bearer " + fallbackKey
				}
				if r.Header.Get("Authorization") != want || r.URL.Path != "/fallback/chat/completions" {
					t.Error("fallback credential or endpoint mismatch")
				}
				body, _ := io.ReadAll(r.Body)
				fallbackBodies = append(fallbackBodies, body)
				if mode == "both fail" {
					w.WriteHeader(400)
					io.WriteString(w, "RAW_PROVIDER_BODY Discord-message-content fallback-key")
					return
				}
				io.WriteString(w, `{"choices":[{"message":{"content":"Fallback summary"}}]}`)
			}))
			defer fallbackServer.Close()
			primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer primary-key" || r.URL.Path != "/primary/chat/completions" {
					t.Error("primary credential or endpoint mismatch")
				}
				body, _ := io.ReadAll(r.Body)
				primaryBodies = append(primaryBodies, body)
				if mode != "primary success" {
					w.WriteHeader(400)
					io.WriteString(w, "RAW_PROVIDER_BODY Discord-message-content primary-key")
					return
				}
				io.WriteString(w, `{"choices":[{"message":{"content":"Primary summary"}}]}`)
			}))
			defer primaryServer.Close()
			cfg := state.DefaultConfig()
			cfg.LLM = state.LLMConfig{BaseURL: primaryServer.URL + "/primary", Model: "same-model"}
			cfg.Fallback = &state.FallbackConfig{Enabled: mode != "disabled", LLMConfig: state.LLMConfig{BaseURL: fallbackServer.URL + "/fallback", Model: "same-model"}}
			cfg.Channels = []state.ChannelConfig{{ID: "1", Name: "one"}, {ID: "2", Name: "two"}, {ID: "3", Name: "empty"}}
			st := state.NewEmptyState()
			for _, ch := range cfg.Channels {
				st.Channels[ch.ID] = state.ChannelState{Cursor: state.Cursor{Kind: state.CursorKindMessageID, Value: "10"}}
			}
			if err := store.SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveState(st); err != nil {
				t.Fatal(err)
			}
			exporter := &FakeDCE{configured: true, exportFunc: func(_ context.Context, req dce.ExportRequest) (*dce.ExportResult, error) {
				if req.ChannelID == "3" {
					return &dce.ExportResult{}, nil
				}
				id := "11"
				if req.After.Value == "11" {
					id = "12"
				}
				return &dce.ExportResult{Channel: dce.ChannelInfo{ID: req.ChannelID}, Messages: []dce.Message{{ID: id, Content: "Discord-message-content"}}, MaxMessageID: id}, nil
			}}
			deliverer := &FakeDeliverer{}
			runner, _ := NewRunner(store, WithDCEClient(exporter), WithDeliverer(deliverer), WithLLMAPIKey("primary-key"), WithFallbackAPIKey(fallbackKey))
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			for run := 1; run <= 2; run++ {
				if mode == "scheduled" {
					if !runner.StartScheduled(context.Background(), time.Now().Add(-time.Second)) {
						t.Fatal("not started")
					}
					runner.Wait()
				} else if err := runner.Run(context.Background(), ""); err != nil {
					t.Fatal(err)
				}
				saved, err := store.LoadState()
				if err != nil {
					t.Fatal(err)
				}
				want := "11"
				if run == 2 {
					want = "12"
				}
				if mode == "both fail" || mode == "disabled" {
					want = "10"
				}
				if saved.Channels["1"].Cursor.Value != want || saved.Channels["2"].Cursor.Value != want || saved.Channels["3"].Cursor.Value != "10" {
					t.Fatal("cursor invariant violated", saved)
				}
				if len(exporter.exports) != run*3 {
					t.Fatal("failover re-exported")
				}
				pCalls, fCalls := run, run*2
				if mode == "primary success" || mode == "disabled" {
					pCalls, fCalls = run*2, 0
				}
				if mode == "both fail" {
					pCalls = run * 2
				}
				if len(primaryBodies) != pCalls || len(fallbackBodies) != fCalls {
					t.Fatal("wrong per-job provider transition", len(primaryBodies), len(fallbackBodies))
				}
			}
			if len(fallbackBodies) > 0 && !bytes.Equal(primaryBodies[0], fallbackBodies[0]) {
				t.Fatal("failover changed already-collected request")
			}
			for _, private := range []string{"RAW_PROVIDER_BODY", "Discord-message-content", "primary-key", "fallback-key"} {
				if strings.Contains(logs.String(), private) {
					t.Fatal("private data logged")
				}
			}
		})
	}
}

func TestFallbackDuringHierarchicalWork(t *testing.T) {
	var seen [][]llm.Message
	p := &FakeCompleter{completeFunc: func(_ context.Context, messages []llm.Message) (string, error) {
		seen = append(seen, messages)
		if len(seen) == 2 {
			return "", errors.New("primary down")
		}
		return "notes", nil
	}}
	f := &FakeCompleter{completeFunc: func(_ context.Context, messages []llm.Message) (string, error) {
		if len(seen) == 2 {
			if !reflect.DeepEqual(messages, seen[1]) {
				t.Fatal("failed chunk was not reused")
			}
			seen = append(seen, nil)
		}
		return "short notes", nil
	}}
	c := &fallbackCompleter{primary: NewRetryCompleter(p), createFallback: func() (brief.Completer, error) { return NewRetryCompleter(f), nil }}
	e := brief.NewEngine(c)
	e.ChunkBudget = 300
	_, err := e.Summarize(context.Background(), brief.Channel{ID: "1"}, []brief.Message{{ID: "1", Content: strings.Repeat("content ", 130)}})
	if err != nil || p.calls != 2 || f.calls < 3 {
		t.Fatal("hierarchical failover failed", err, p.calls, f.calls)
	}
}
