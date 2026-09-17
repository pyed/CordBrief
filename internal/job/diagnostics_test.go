package job

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/pyed/CordBrief/internal/brief"
	"github.com/pyed/CordBrief/internal/dce"
	"github.com/pyed/CordBrief/internal/llm"
	"github.com/pyed/CordBrief/internal/state"
)

type diagnosticTransport func(*http.Request) (*http.Response, error)

func (f diagnosticTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestRunnerSafeDiagnostics(t *testing.T) {
	for _, profile := range []string{"primary", "fallback"} {
		for _, failure := range []string{"400", "401", "429", "503", "timeout", "transport", "parser"} {
			t.Run(profile+"/"+failure, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					store, _ := setupTestStore(t)
					cfg := state.DefaultConfig()
					cfg.Channels = []state.ChannelConfig{{ID: "1", Name: "one"}}
					cfg.LLM = state.LLMConfig{BaseURL: "https://primary.example/v1", Model: "primary"}
					cfg.Fallback = &state.FallbackConfig{Enabled: profile == "fallback", LLMConfig: state.LLMConfig{BaseURL: "https://fallback.example/v1", Model: "fallback"}}
					if err := store.SaveConfig(cfg); err != nil {
						t.Fatal(err)
					}
					st := state.NewEmptyState()
					original := state.Cursor{Kind: state.CursorKindMessageID, Value: "1"}
					st.Channels["1"] = state.ChannelState{Cursor: original}
					if err := store.SaveState(st); err != nil {
						t.Fatal(err)
					}
					exporter := &FakeDCE{configured: true, exportFunc: func(context.Context, dce.ExportRequest) (*dce.ExportResult, error) {
						return &dce.ExportResult{Messages: []dce.Message{{ID: "2", Content: "DISCORD_MESSAGE_CONTENT"}}, MaxMessageID: "2"}, nil
					}}
					attempts := map[string]int{}
					factory := func(base, model, key string) (brief.Completer, error) {
						client := &http.Client{Transport: diagnosticTransport(func(req *http.Request) (*http.Response, error) {
							attempts[model]++
							if req.Header.Get("Authorization") != "Bearer "+model+"-key" {
								t.Fatal("wrong credential")
							}
							status := http.StatusUnauthorized
							if model == profile {
								switch failure {
								case "400":
									status = 400
								case "401":
									status = 401
								case "429":
									status = 429
								case "503":
									status = 503
								case "parser":
									status = 200
								case "timeout":
									return nil, &url.Error{Op: "Post", URL: base, Err: fmt.Errorf("timeout primary-key fallback-key: %w", context.DeadlineExceeded)}
								case "transport":
									return nil, errors.New("connection refused primary-key fallback-key")
								}
							}
							return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("RAW_PROVIDER_BODY primary-key fallback-key DISCORD_MESSAGE_CONTENT")), Header: make(http.Header)}, nil
						})}
						return llm.NewClient(base, model, key, client)
					}
					r, err := NewRunner(store, WithDCEClient(exporter), WithDeliverer(&FakeDeliverer{}), WithCompleterFactory(factory), WithLLMAPIKey("primary-key"), WithFallbackAPIKey("fallback-key"))
					if err != nil {
						t.Fatal(err)
					}
					var logs bytes.Buffer
					old := log.Writer()
					log.SetOutput(&logs)
					defer log.SetOutput(old)
					if err := r.Run(context.Background(), ""); err != nil {
						t.Fatal(err)
					}
					saved, err := store.LoadState()
					if err != nil {
						t.Fatal(err)
					}
					diagnostic := saved.Channels["1"].LastError
					want, count := "status "+failure, 1
					switch failure {
					case "429", "503":
						count = 2
					case "timeout":
						want, count = "timeout", 2
					case "transport":
						want, count = "connection refused", 2
					case "parser":
						want = "failed to parse llm response JSON"
					}
					if !strings.Contains(diagnostic, want) || !strings.Contains(logs.String(), "summarization failed for #one (1): "+diagnostic) {
						t.Fatal("safe classification missing from log or LastError", diagnostic, logs.String())
					}
					if failure == "timeout" || failure == "transport" {
						if !strings.Contains(diagnostic, "llm transport error") || strings.Count(diagnostic, "[REDACTED]") != 2 {
							t.Fatal("transport classification or key redaction missing", diagnostic)
						}
					}
					for _, secret := range []string{"primary-key", "fallback-key", "RAW_PROVIDER_BODY", "DISCORD_MESSAGE_CONTENT"} {
						if strings.Contains(logs.String(), secret) || strings.Contains(diagnostic, secret) {
							t.Fatal("private data in diagnostics")
						}
					}
					if saved.Channels["1"].Cursor != original || len(exporter.exports) != 1 || attempts[profile] != count {
						t.Fatal("cursor, export or retry invariant changed")
					}
					if profile == "fallback" && attempts["primary"] != 1 {
						t.Fatal("wrong primary attempts")
					}
					if profile == "primary" && attempts["fallback"] != 0 {
						t.Fatal("disabled fallback used")
					}
				})
			})
		}
	}
}

func TestRunnerSafeDiagnosticsDCE(t *testing.T) {
	store, _ := setupTestStore(t)
	cfg := state.DefaultConfig()
	cfg.Channels = []state.ChannelConfig{{ID: "1", Name: "one"}}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	st := state.NewEmptyState()
	original := state.Cursor{Kind: state.CursorKindMessageID, Value: "1"}
	st.Channels["1"] = state.ChannelState{Cursor: original}
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	calls := 0
	exporter := dce.NewMockClient("never-executed", "discord-key", func(_ context.Context, _ string, _, _ []string, _, stderr io.Writer) error {
		calls++
		io.WriteString(stderr, "access denied discord-key primary-key fallback-key")
		return errors.New("exit status 1")
	})
	r, _ := NewRunner(store, WithDCEClient(exporter), WithDeliverer(&FakeDeliverer{}), WithLLMAPIKey("primary-key"), WithFallbackAPIKey("fallback-key"))
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	if err := r.Run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	saved, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := saved.Channels["1"].LastError
	if !strings.Contains(diagnostic, "access denied") || !strings.Contains(diagnostic, "exit status 1") || strings.Count(diagnostic, "[REDACTED]") != 3 || !strings.Contains(logs.String(), "DCE export failed for #one (1): "+diagnostic) {
		t.Fatal("DCE diagnostic not retained safely", diagnostic)
	}
	for _, secret := range []string{"discord-key", "primary-key", "fallback-key"} {
		if strings.Contains(logs.String(), secret) || strings.Contains(diagnostic, secret) {
			t.Fatal("credential in DCE diagnostic")
		}
	}
	if calls != 1 || saved.Channels["1"].Cursor != original {
		t.Fatal("DCE failure changed cursor or repeated export")
	}
}
