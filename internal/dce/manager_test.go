package dce

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pyed/CordBrief/internal/state"
)

func writeMockExportJSON(args []string) error {
	channelID := "1"
	var outPath string
	for i, arg := range args {
		if arg == "-c" && i+1 < len(args) {
			channelID = args[i+1]
		}
		if arg == "-o" && i+1 < len(args) {
			outPath = args[i+1]
		}
	}
	if outPath == "" {
		return errors.New("no -o found in args")
	}
	content := fmt.Sprintf(`{"guild":{"id":"1"},"channel":{"id":%q},"messages":[]}`, channelID)
	return os.WriteFile(outPath, []byte(content), 0600)
}

func TestManager_UnreadableStateReturnsError(t *testing.T) {
	dir := t.TempDir()
	// A directory where a JSON file belongs reliably fails to read on every OS.
	if err := os.MkdirAll(filepath.Join(dir, "dce", "updater.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if mgr, err := newUnspacedTestManager(t, dir, "bootstrap", "token", WithBootstrapVersion("2.48")); err == nil || mgr != nil {
		t.Fatalf("expected initialization error, got manager %v, error %v", mgr, err)
	}
}

func TestManager_CandidateSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	bootstrapDir := filepath.Join(tmpDir, "bootstrap")
	if err := os.MkdirAll(bootstrapDir, 0700); err != nil {
		t.Fatal(err)
	}
	bootstrapExe := filepath.Join(bootstrapDir, ExpectedExecutableName(runtime.GOOS))
	if err := os.WriteFile(bootstrapExe, []byte("bootstrap-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	dataDir := filepath.Join(tmpDir, "data")
	candDir := filepath.Join(dataDir, "dce", "versions", "2.49.0")
	if err := os.MkdirAll(candDir, 0700); err != nil {
		t.Fatal(err)
	}
	candExe := filepath.Join(candDir, ExpectedExecutableName(runtime.GOOS))
	if err := os.WriteFile(candExe, []byte("cand-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	executed := make([]string, 0)
	runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
		mu.Lock()
		executed = append(executed, name)
		mu.Unlock()
		return writeMockExportJSON(args)
	}

	mgr, err := newUnspacedTestManager(t, dataDir, bootstrapExe, "mock-token",
		WithCommandRunner(runner),
		WithBootstrapVersion("2.48.0"),
	)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	// Manually set candidate in state
	mgr.mu.Lock()
	mgr.state.CandidateVersion = "2.49.0"
	mgr.state.CandidatePath = candExe
	_ = SaveUpdaterState(mgr.statePath, mgr.state)
	mgr.mu.Unlock()

	if mgr.Status() != "2.48.0 · candidate 2.49.0 pending" {
		t.Fatalf("unexpected initial status: %q", mgr.Status())
	}

	// First export should use candidate and promote on success
	req := ExportRequest{ChannelID: "123456789"}
	res, err := mgr.Export(context.Background(), req)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil export result")
	}

	mu.Lock()
	if len(executed) != 1 || executed[0] != candExe {
		t.Fatalf("expected candidate %q to be executed once, got %v", candExe, executed)
	}
	mu.Unlock()

	// State must reflect promotion
	st := mgr.State()
	if st.ActiveVersion != "2.49.0" || st.ActivePath != candExe {
		t.Fatalf("expected promoted active version 2.49.0 and path %q, got %+v", candExe, st)
	}
	if st.CandidateVersion != "" || st.CandidatePath != "" {
		t.Fatalf("expected cleared candidate, got %+v", st)
	}
	if mgr.Status() != "2.49.0 · active" {
		t.Fatalf("expected status '2.49.0 · active', got %q", mgr.Status())
	}

	// Verify bootstrap binary was NOT deleted
	if _, err := os.Stat(bootstrapExe); err != nil {
		t.Fatalf("bootstrap binary must never be deleted: %v", err)
	}

	// Next export must use promoted 2.49.0 directly
	mu.Lock()
	executed = nil
	mu.Unlock()

	_, err = mgr.Export(context.Background(), req)
	if err != nil {
		t.Fatalf("second Export failed: %v", err)
	}
	mu.Lock()
	if len(executed) != 1 || executed[0] != candExe {
		t.Fatalf("expected promoted active %q to be executed, got %v", candExe, executed)
	}
	mu.Unlock()
}

func TestManager_CandidateFailureAndCancellation(t *testing.T) {
	t.Run("candidate failure triggers rollback and single retry with active", func(t *testing.T) {
		tmpDir := t.TempDir()
		bootstrapExe := filepath.Join(tmpDir, "bootstrap", ExpectedExecutableName(runtime.GOOS))
		_ = os.MkdirAll(filepath.Dir(bootstrapExe), 0700)
		_ = os.WriteFile(bootstrapExe, []byte("boot"), 0755)

		dataDir := filepath.Join(tmpDir, "data")
		candDir := filepath.Join(dataDir, "dce", "versions", "2.49.0")
		_ = os.MkdirAll(candDir, 0700)
		candExe := filepath.Join(candDir, ExpectedExecutableName(runtime.GOOS))
		_ = os.WriteFile(candExe, []byte("cand"), 0755)

		var mu sync.Mutex
		executed := make([]string, 0)
		runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
			mu.Lock()
			executed = append(executed, name)
			mu.Unlock()
			if name == candExe {
				return errors.New("candidate crashed")
			}
			return writeMockExportJSON(args)
		}

		mgr, err := newUnspacedTestManager(t, dataDir, bootstrapExe, "mock-token",
			WithCommandRunner(runner),
			WithBootstrapVersion("2.48.0"),
		)
		if err != nil {
			t.Fatal(err)
		}

		mgr.mu.Lock()
		mgr.state.CandidateVersion = "2.49.0"
		mgr.state.CandidatePath = candExe
		_ = SaveUpdaterState(mgr.statePath, mgr.state)
		mgr.mu.Unlock()

		req := ExportRequest{ChannelID: "123456789"}
		res, err := mgr.Export(context.Background(), req)
		if err != nil {
			t.Fatalf("expected successful fallback, got err: %v", err)
		}
		if res == nil {
			t.Fatal("expected non-nil result from active retry")
		}

		mu.Lock()
		if len(executed) != 2 || executed[0] != candExe || executed[1] != bootstrapExe {
			t.Fatalf("expected candidate failure followed by active retry, got %v", executed)
		}
		mu.Unlock()

		// State must record rejection
		st := mgr.State()
		if st.RejectedVersion != "2.49.0" {
			t.Fatalf("expected RejectedVersion 2.49.0, got %q", st.RejectedVersion)
		}
		if st.ActiveVersion != "2.48.0" || st.ActivePath != bootstrapExe {
			t.Fatalf("expected ActiveVersion 2.48.0, got %+v", st)
		}
		if st.CandidateVersion != "" {
			t.Fatalf("expected empty CandidateVersion, got %q", st.CandidateVersion)
		}
		if mgr.Status() != "2.48.0 · 2.49.0 rejected" {
			t.Fatalf("expected status '2.48.0 · 2.49.0 rejected', got %q", mgr.Status())
		}

		// Candidate directory should be cleaned up
		if _, err := os.Stat(candDir); !os.IsNotExist(err) {
			t.Fatalf("expected candidate dir %s to be removed, err: %v", candDir, err)
		}

		// Subsequent export directly uses active
		mu.Lock()
		executed = nil
		mu.Unlock()

		_, err = mgr.Export(context.Background(), req)
		if err != nil {
			t.Fatalf("export after rejection failed: %v", err)
		}
		mu.Lock()
		if len(executed) != 1 || executed[0] != bootstrapExe {
			t.Fatalf("expected only active %q, got %v", bootstrapExe, executed)
		}
		mu.Unlock()
	})

	t.Run("both candidate and active fail produces exactly two attempts", func(t *testing.T) {
		tmpDir := t.TempDir()
		bootstrapExe := filepath.Join(tmpDir, "bootstrap", ExpectedExecutableName(runtime.GOOS))
		_ = os.MkdirAll(filepath.Dir(bootstrapExe), 0700)
		_ = os.WriteFile(bootstrapExe, []byte("boot"), 0755)

		dataDir := filepath.Join(tmpDir, "data")
		candDir := filepath.Join(dataDir, "dce", "versions", "2.49.0")
		_ = os.MkdirAll(candDir, 0700)
		candExe := filepath.Join(candDir, ExpectedExecutableName(runtime.GOOS))
		_ = os.WriteFile(candExe, []byte("cand"), 0755)

		var mu sync.Mutex
		attempts := 0
		runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
			mu.Lock()
			attempts++
			mu.Unlock()
			return errors.New("exec error")
		}

		mgr, _ := newUnspacedTestManager(t, dataDir, bootstrapExe, "mock-token",
			WithCommandRunner(runner),
			WithBootstrapVersion("2.48.0"),
		)
		mgr.mu.Lock()
		mgr.state.CandidateVersion = "2.49.0"
		mgr.state.CandidatePath = candExe
		_ = SaveUpdaterState(mgr.statePath, mgr.state)
		mgr.mu.Unlock()

		req := ExportRequest{ChannelID: "123456789"}
		_, err := mgr.Export(context.Background(), req)
		if err == nil {
			t.Fatal("expected export failure, got nil")
		}

		mu.Lock()
		if attempts != 2 {
			t.Fatalf("expected exactly 2 attempts, got %d", attempts)
		}
		mu.Unlock()
	})

	t.Run("caller cancellation does not reject, promote, or retry", func(t *testing.T) {
		tmpDir := t.TempDir()
		bootstrapExe := filepath.Join(tmpDir, "bootstrap", ExpectedExecutableName(runtime.GOOS))
		_ = os.MkdirAll(filepath.Dir(bootstrapExe), 0700)
		_ = os.WriteFile(bootstrapExe, []byte("boot"), 0755)

		dataDir := filepath.Join(tmpDir, "data")
		candDir := filepath.Join(dataDir, "dce", "versions", "2.49.0")
		_ = os.MkdirAll(candDir, 0700)
		candExe := filepath.Join(candDir, ExpectedExecutableName(runtime.GOOS))
		_ = os.WriteFile(candExe, []byte("cand"), 0755)

		var mu sync.Mutex
		attempts := 0
		runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
			mu.Lock()
			attempts++
			mu.Unlock()
			return context.Canceled
		}

		mgr, _ := newUnspacedTestManager(t, dataDir, bootstrapExe, "mock-token",
			WithCommandRunner(runner),
			WithBootstrapVersion("2.48.0"),
		)
		mgr.mu.Lock()
		mgr.state.CandidateVersion = "2.49.0"
		mgr.state.CandidatePath = candExe
		_ = SaveUpdaterState(mgr.statePath, mgr.state)
		mgr.mu.Unlock()

		req := ExportRequest{ChannelID: "123456789"}
		_, err := mgr.Export(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("expected context canceled error, got %v", err)
		}

		mu.Lock()
		if attempts != 1 {
			t.Fatalf("expected exactly 1 attempt on cancel, got %d", attempts)
		}
		mu.Unlock()

		st := mgr.State()
		if st.CandidateVersion != "2.49.0" {
			t.Fatalf("candidate version must remain pending on cancellation, got %+v", st)
		}
		if st.RejectedVersion != "" {
			t.Fatalf("candidate must not be rejected on cancellation, got %+v", st)
		}
	})
}

func makeMockDCEZip(t *testing.T, goos string) ([]byte, string) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	execName := ExpectedExecutableName(goos)
	w, err := zw.Create(execName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("mock-dce-binary")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zipBytes := buf.Bytes()
	sum := sha256.Sum256(zipBytes)
	return zipBytes, hex.EncodeToString(sum[:])
}

func TestManager_CheckForUpdates(t *testing.T) {
	t.Run("up to date release is no-op", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rel := GitHubRelease{
				TagName: "2.48.0",
			}
			_ = json.NewEncoder(w).Encode(rel)
		}))
		defer ts.Close()

		tmpDir := t.TempDir()
		rc := &ReleaseClient{
			HTTPClient: ts.Client(),
			Endpoint:   ts.URL,
			AllowHTTP:  true,
		}

		mgr, _ := newUnspacedTestManager(t, tmpDir, "/bootstrap/dce", "token",
			WithReleaseClient(rc),
			WithBootstrapVersion("2.48.0"),
		)

		updated, err := mgr.CheckForUpdates(context.Background())
		if err != nil {
			t.Fatalf("unexpected check error: %v", err)
		}
		if updated {
			t.Fatal("expected no update when release == active")
		}
	})

	t.Run("rejected release is not re-downloaded", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rel := GitHubRelease{
				TagName: "2.49.0",
			}
			_ = json.NewEncoder(w).Encode(rel)
		}))
		defer ts.Close()

		tmpDir := t.TempDir()
		rc := &ReleaseClient{
			HTTPClient: ts.Client(),
			Endpoint:   ts.URL,
			AllowHTTP:  true,
		}

		mgr, _ := newUnspacedTestManager(t, tmpDir, "/bootstrap/dce", "token",
			WithReleaseClient(rc),
			WithBootstrapVersion("2.48.0"),
		)
		mgr.mu.Lock()
		mgr.state.RejectedVersion = "2.49.0"
		mgr.mu.Unlock()

		updated, err := mgr.CheckForUpdates(context.Background())
		if err != nil {
			t.Fatalf("unexpected check error: %v", err)
		}
		if updated {
			t.Fatal("expected no update when release was previously rejected")
		}
	})

	t.Run("newer release is downloaded and staged as candidate", func(t *testing.T) {
		zipBytes, digestHex := makeMockDCEZip(t, runtime.GOOS)
		assetName, _ := ExpectedAssetPattern(runtime.GOOS, runtime.GOARCH)

		var ts *httptest.Server
		ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, ".zip") {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(zipBytes)
				return
			}
			rel := GitHubRelease{
				TagName: "2.50.0",
				Assets: []GitHubAsset{
					{
						Name:               assetName,
						BrowserDownloadURL: ts.URL + "/" + assetName,
						Size:               int64(len(zipBytes)),
						Digest:             "sha256:" + digestHex,
					},
				},
			}
			_ = json.NewEncoder(w).Encode(rel)
		}))
		defer ts.Close()

		tmpDir := t.TempDir()
		rc := &ReleaseClient{
			HTTPClient: ts.Client(),
			Endpoint:   ts.URL,
			AllowHTTP:  true,
		}

		mgr, _ := newUnspacedTestManager(t, tmpDir, "/bootstrap/dce", "token",
			WithReleaseClient(rc),
			WithBootstrapVersion("2.48.0"),
			WithPlatform(runtime.GOOS, runtime.GOARCH),
		)

		updated, err := mgr.CheckForUpdates(context.Background())
		if err != nil {
			t.Fatalf("unexpected check error: %v", err)
		}
		if !updated {
			t.Fatal("expected candidate to be installed")
		}

		st := mgr.State()
		if st.CandidateVersion != "2.50" {
			t.Fatalf("expected candidate 2.50, got %q", st.CandidateVersion)
		}
		if _, err := os.Stat(st.CandidatePath); err != nil {
			t.Fatalf("expected candidate executable to exist at %s: %v", st.CandidatePath, err)
		}
	})

	t.Run("weekly timing and next check calculation", func(t *testing.T) {
		tmpDir := t.TempDir()
		now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
		mgr, _ := newUnspacedTestManager(t, tmpDir, "/bootstrap/dce", "token",
			WithClock(func() time.Time { return now }),
		)

		// Never checked: due immediately
		if mgr.NextCheckDuration() != 0 {
			t.Fatalf("expected 0 duration when never checked, got %v", mgr.NextCheckDuration())
		}

		// Checked 2 days ago: due in 5 days
		mgr.mu.Lock()
		mgr.state.LastCheck = now.Add(-48 * time.Hour)
		mgr.mu.Unlock()

		expected := 5 * 24 * time.Hour
		if mgr.NextCheckDuration() != expected {
			t.Fatalf("expected %v, got %v", expected, mgr.NextCheckDuration())
		}

		// Checked 8 days ago: due immediately
		mgr.mu.Lock()
		mgr.state.LastCheck = now.Add(-8 * 24 * time.Hour)
		mgr.mu.Unlock()

		if mgr.NextCheckDuration() != 0 {
			t.Fatalf("expected 0 duration when checked >7 days ago, got %v", mgr.NextCheckDuration())
		}
	})
}

func TestManager_PinnedCheckRefreshesLastCheckWithoutRequest(t *testing.T) {
	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer ts.Close()

	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	mgr, err := newUnspacedTestManager(t, t.TempDir(), "/bootstrap/dce", "token",
		WithReleaseClient(&ReleaseClient{Endpoint: ts.URL + "/latest", HTTPClient: ts.Client(), AllowHTTP: true}),
		WithBootstrapVersion("2.48.0"),
		WithClock(func() time.Time { return now }),
		WithCommandRunner(func(context.Context, string, []string, []string, io.Writer, io.Writer) error {
			return errors.New("probe disabled")
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	mgr.mu.Lock()
	mgr.state.PinnedVersion = "2.48.0"
	mgr.state.LastCheck = now.Add(-8 * 24 * time.Hour)
	mgr.mu.Unlock()
	if mgr.NextCheckDuration() != 0 {
		t.Fatal("pinned updater should initially be due")
	}

	updated, err := mgr.CheckForUpdates(context.Background())
	if err != nil || updated {
		t.Fatalf("pinned active check should be a local no-op, updated=%v err=%v", updated, err)
	}
	if requests != 0 {
		t.Fatalf("pinned active check made %d GitHub requests", requests)
	}
	if got := mgr.NextCheckDuration(); got != 7*24*time.Hour {
		t.Fatalf("pinned check did not advance schedule: got %v", got)
	}
}

func newUnspacedTestManager(t *testing.T, dir, path, token string, opts ...ManagerOption) (*Manager, error) {
	t.Helper()
	cfg := state.DefaultConfig()
	cfg.DCECooldownSeconds = 0
	if err := state.NewStore(dir).SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return NewManager(dir, path, token, opts...)
}

func TestExportCooldownPersistsAcrossRestartAndRollback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		var starts []time.Time
		runner := func(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
			starts = append(starts, time.Now())
			for i, arg := range args {
				if arg == "--before" && args[i+1] != time.Now().UTC().Format("2006-01-02T15:04:05Z") {
					t.Fatal("stale cutoff")
				}
			}
			if name == "candidate" {
				return errors.New("candidate crashed")
			}
			return writeMockExportJSON(args)
		}
		newManager := func() *Manager {
			m, err := NewManager(dir, "active", "fake", WithBootstrapVersion("2.48"), WithCommandRunner(runner))
			if err != nil {
				t.Fatal(err)
			}
			return m
		}
		m := newManager()
		req := ExportRequest{ChannelID: "123"}
		if _, err := m.Export(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		m = newManager()
		next := m.State()
		next.CandidatePath, next.CandidateVersion = "candidate", "2.49"
		if err := m.saveState(next); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Export(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if len(starts) != 3 || starts[1].Sub(starts[0]) != 15*time.Minute || starts[2].Sub(starts[1]) != 15*time.Minute {
			t.Fatalf("unspaced restart/candidate/fallback: %v", starts)
		}
		if m.State().RejectedVersion != "2.49" || m.State().ActivePath != "active" {
			t.Fatal(m.State())
		}
		cfg := state.DefaultConfig()
		cfg.DCECooldownSeconds = 0
		if err := state.NewStore(dir).SaveConfig(cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Export(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if !starts[3].Equal(starts[2]) {
			t.Fatal("zero cooldown still waits")
		}
	})
}

func TestExportCooldownCancellationKeepsCandidatePending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		m, err := NewManager(t.TempDir(), "active", "fake", WithBootstrapVersion("2.48"),
			WithCommandRunner(func(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
				calls++
				return writeMockExportJSON(args)
			}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Export(context.Background(), ExportRequest{ChannelID: "123"}); err != nil {
			t.Fatal(err)
		}
		next := m.State()
		next.CandidateVersion, next.CandidatePath = "2.49", "candidate"
		if err := m.saveState(next); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, err := m.Export(ctx, ExportRequest{ChannelID: "123"}); done <- err }()
		synctest.Wait()
		at := time.Now()
		cancel()
		if err := <-done; err == nil {
			t.Fatal("cancellation ignored")
		}
		if !time.Now().Equal(at) || calls != 1 || m.State() != next {
			t.Fatal("wait ran DCE, changed candidate, or delayed cancellation")
		}
	})
}

func TestManager_CandidateMalformedRollback(t *testing.T) {
	tmpDir := t.TempDir()
	bootstrapExe := filepath.Join(tmpDir, "bootstrap", ExpectedExecutableName(runtime.GOOS))
	_ = os.MkdirAll(filepath.Dir(bootstrapExe), 0700)
	_ = os.WriteFile(bootstrapExe, []byte("boot"), 0755)

	dataDir := filepath.Join(tmpDir, "data")
	candDir := filepath.Join(dataDir, "dce", "versions", "2.49.0")
	_ = os.MkdirAll(candDir, 0700)
	candExe := filepath.Join(candDir, ExpectedExecutableName(runtime.GOOS))
	_ = os.WriteFile(candExe, []byte("cand"), 0755)

	var mu sync.Mutex
	executed := make([]string, 0)
	runner := func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
		mu.Lock()
		executed = append(executed, name)
		mu.Unlock()
		if name == candExe {
			for i, arg := range args {
				if arg == "-o" && i+1 < len(args) {
					return os.WriteFile(args[i+1], []byte(`{"guild":{"id":"1"},"channel":{"id":"100"}}`), 0600)
				}
			}
			return errors.New("no -o flag")
		}
		return writeMockExportJSON(args)
	}

	mgr, err := newUnspacedTestManager(t, dataDir, bootstrapExe, "mock-token",
		WithCommandRunner(runner),
		WithBootstrapVersion("2.48.0"),
	)
	if err != nil {
		t.Fatal(err)
	}

	mgr.mu.Lock()
	mgr.state.CandidateVersion = "2.49.0"
	mgr.state.CandidatePath = candExe
	_ = SaveUpdaterState(mgr.statePath, mgr.state)
	mgr.mu.Unlock()

	req := ExportRequest{ChannelID: "100"}
	res, err := mgr.Export(context.Background(), req)
	if err != nil {
		t.Fatalf("expected successful active retry after candidate malformed output, got err: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil result from active retry")
	}

	mu.Lock()
	if len(executed) != 2 || executed[0] != candExe || executed[1] != bootstrapExe {
		t.Fatalf("expected candidate execution followed by active retry, got %v", executed)
	}
	mu.Unlock()

	st := mgr.State()
	if st.RejectedVersion != "2.49.0" {
		t.Fatalf("expected RejectedVersion 2.49.0, got %q", st.RejectedVersion)
	}
	if st.ActiveVersion != "2.48.0" || st.ActivePath != bootstrapExe {
		t.Fatalf("expected ActiveVersion 2.48.0, got %+v", st)
	}
	if st.CandidateVersion != "" {
		t.Fatalf("expected empty CandidateVersion, got %q", st.CandidateVersion)
	}
	if mgr.Status() != "2.48.0 · 2.49.0 rejected" {
		t.Fatalf("unexpected status: %s", mgr.Status())
	}
}
