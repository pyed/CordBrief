package dce

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryMetadata(t *testing.T) {
	// Synthetic output follows the inspected installed 2.48 source format.
	// 500 entries intentionally represent a catalog much larger than a UI subset.
	var catalog strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&catalog, "%d | Category / channel-%d | extra\r\n", i, i)
	}
	for _, tc := range []struct {
		name, output string
		fail, bad    bool
	}{
		{"large", catalog.String(), false, false},
		{"empty", "", false, false},
		{"malformed", "123 | good\nnot a channel", false, true},
		{"duplicate", "123 | first\n123 | second", false, true},
		{"oversize", strings.Repeat("x", maxMetadataBytes+1), false, true},
		{"failure", "raw provider body secret", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FALLBACK_LLM_API_KEY", "fallback-secret")
			t.Setenv("LLM_API_KEY", "primary-secret")
			client := NewMockClient("active", "discord-secret", func(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
				if !reflect.DeepEqual(args, []string{"channels", "-g", "42", "--include-vc", "false", "--include-threads", "None"}) {
					t.Fatal(args)
				}
				if d, ok := ctx.Deadline(); !ok || time.Until(d) > 30*time.Second {
					t.Fatal("metadata is unbounded")
				}
				if strings.Contains(strings.Join(env, "\n"), "fallback-secret") || strings.Contains(strings.Join(env, "\n"), "primary-secret") {
					t.Fatal("LLM secret inherited")
				}
				io.WriteString(stdout, tc.output)
				if tc.fail {
					io.WriteString(stderr, tc.output)
					return errors.New(tc.output)
				}
				return nil
			})
			items, err := client.discover(context.Background(), "42")
			if (err != nil) != tc.bad {
				t.Fatal("unexpected parse result", err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("raw diagnostics exposed")
			}
			if tc.name == "large" && (len(items) != 500 || items[499].Name != "Category / channel-500 | extra") {
				t.Fatal("catalog truncated or permission inference applied")
			}
		})
	}
}

func TestDiscoveryNeverTouchesCandidateOrCooldown(t *testing.T) {
	for _, fail := range []bool{false, true} {
		calls := 0
		m, err := NewManager(t.TempDir(), "active", "token", WithBootstrapVersion("2.48"), WithCommandRunner(func(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
			calls++
			if name != "active" || !reflect.DeepEqual(args, []string{"guilds"}) {
				t.Fatal("candidate or export executed", name, args)
			}
			if fail {
				return errors.New("failure")
			}
			io.WriteString(stdout, "0 | Direct Messages\n42 | Server\n")
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		snapshot := m.State()
		snapshot.CandidatePath, snapshot.CandidateVersion = "candidate", "2.49"
		snapshot.LastExport = time.Now().UTC()
		if err := m.saveState(snapshot); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(m.statePath)
		items, err := m.Discover(context.Background(), "")
		if (err != nil) != fail || (!fail && (len(items) != 1 || items[0].ID != "42")) {
			t.Fatal("guild result", items, err)
		}
		after, _ := os.ReadFile(m.statePath)
		if string(before) != string(after) || snapshot != m.State() || calls != 1 {
			t.Fatal("discovery changed updater/cooldown state")
		}
		m.opMu.Lock()
		_, err = m.Discover(context.Background(), "")
		m.opMu.Unlock()
		if err == nil || calls != 1 {
			t.Fatal("busy manager executed metadata")
		}
		snapshot.ActivePath = ""
		m.saveState(snapshot)
		if _, err := m.Discover(context.Background(), ""); err == nil || calls != 1 {
			t.Fatal("candidate-only discovery must not execute")
		}
	}
}

func TestDiscoveryCancellationAndInvalidIDs(t *testing.T) {
	calls := 0
	c := NewMockClient("active", "token", func(ctx context.Context, _ string, _, _ []string, _, _ io.Writer) error { calls++; return ctx.Err() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.discover(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, id := range []string{"0", "-1", "1 --export", strings.Repeat("1", 21)} {
		if _, err := c.discover(context.Background(), id); err == nil {
			t.Fatal("invalid server accepted")
		}
	}
	if calls != 0 {
		t.Fatal("unexpected DCE call")
	}
}
