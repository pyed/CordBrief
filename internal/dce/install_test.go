package dce

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Fixtures exercise only HTTP and filesystem paths. The process hook fails if called.
func installTestManager(t *testing.T) (*Manager, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	zipBytes, digest := makeMockDCEZip(t, runtime.GOOS)
	assetName, _ := ExpectedAssetPattern(runtime.GOOS, runtime.GOARCH)
	requests, downloads := &atomic.Int32{}, &atomic.Int32{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/asset.zip" {
			downloads.Add(1)
			w.Write(zipBytes)
			return
		}
		tag := "2.50.0"
		if strings.HasPrefix(r.URL.Path, "/tags/") {
			tag = strings.TrimPrefix(r.URL.Path, "/tags/")
		}
		if tag == "2.40.0" {
			http.NotFound(w, r)
			return
		}
		rel := GitHubRelease{TagName: tag, Assets: []GitHubAsset{{Name: assetName, BrowserDownloadURL: server.URL + "/asset.zip", Digest: "sha256:" + digest}}}
		if tag == "2.41.0" {
			rel.Assets[0].Digest = ""
		}
		if tag == "2.42.0" {
			rel.Assets[0].Digest = "sha256:" + strings.Repeat("0", 64)
		}
		if tag == "2.43.0" {
			rel.Assets = nil
		}
		if tag == "2.44.0" {
			rel.TagName = "2.45.0"
		}
		json.NewEncoder(w).Encode(rel)
	}))
	t.Cleanup(server.Close)
	mgr, err := NewManager(t.TempDir(), "", "test-token",
		WithReleaseClient(&ReleaseClient{Endpoint: server.URL + "/latest", HTTPClient: server.Client(), AllowHTTP: true}),
		WithCommandRunner(func(context.Context, string, []string, []string, io.Writer, io.Writer) error {
			t.Error("installer executed DCE")
			return errors.New("execution forbidden")
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return mgr, requests, downloads
}

func TestEnsureFirstInstallAndOfflineRestart(t *testing.T) {
	mgr, requests, downloads := installTestManager(t)
	ctx := context.Background()
	if err := mgr.Ensure(ctx, ""); err != nil {
		t.Fatal(err)
	}
	st := mgr.State()
	if !mgr.IsConfigured() || st.ActivePath != "" || st.CandidateVersion != "2.50" || downloads.Load() != 1 {
		t.Fatal("first install was not staged on probation")
	}
	if fi, err := os.Stat(st.CandidatePath); err != nil || !fi.Mode().IsRegular() {
		t.Fatal("candidate executable missing")
	}
	// An interrupted first run must keep the untested candidate after restarting.
	restarted, err := NewManager(mgr.dataDir, "", "test-token", WithReleaseClient(mgr.releaseClient))
	if err != nil {
		t.Fatal(err)
	}
	before := requests.Load()
	if err := restarted.Ensure(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != before || restarted.State().ActivePath != "" {
		t.Fatal("restart fetched again or promoted an untested candidate")
	}
	// Exercise state transitions directly, without a DCE export.
	if err := mgr.promoteCandidate(st.CandidateVersion, st.CandidatePath); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Ensure(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != before || mgr.State().ActivePath != st.CandidatePath {
		t.Fatal("active install was not reused")
	}
	if err := os.Remove(st.CandidatePath); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Ensure(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 2 || mgr.State().ActivePath != "" {
		t.Fatal("missing executable was not reinstalled on probation")
	}
}

func TestEnsurePinDowngradeAndUnpin(t *testing.T) {
	mgr, requests, downloads := installTestManager(t)
	ctx := context.Background()
	if err := mgr.Ensure(ctx, ""); err != nil {
		t.Fatal(err)
	}
	st := mgr.State()
	if err := mgr.promoteCandidate(st.CandidateVersion, st.CandidatePath); err != nil {
		t.Fatal(err)
	}
	active := mgr.State().ActivePath
	if err := mgr.Ensure(ctx, "2.48.0"); err != nil {
		t.Fatal(err)
	}
	st = mgr.State()
	if st.PinnedVersion != "2.48.0" || st.CandidateVersion != "2.48" || st.ActivePath != active {
		t.Fatal("explicit downgrade did not retain known-good active")
	}
	before := requests.Load()
	if _, err := mgr.CheckForUpdates(ctx); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != before || downloads.Load() != 2 {
		t.Fatal("pin was not honored")
	}
	// Returning to latest before probation must discard the pending downgrade.
	if err := mgr.Ensure(ctx, "latest"); err != nil {
		t.Fatal(err)
	}
	if mgr.State().PinnedVersion != "" || mgr.State().CandidatePath != "" || mgr.State().ActivePath != active {
		t.Fatal("unpin retained a stale downgrade")
	}
	if _, err := os.Stat(st.CandidatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("obsolete candidate was not cleaned")
	}
	if err := mgr.Ensure(ctx, "2.48.0"); err != nil {
		t.Fatal(err)
	}
	st = mgr.State()
	if err := mgr.promoteCandidate(st.CandidateVersion, st.CandidatePath); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewManager(mgr.dataDir, "", "test-token", WithReleaseClient(mgr.releaseClient))
	if err != nil {
		t.Fatal(err)
	}
	before = requests.Load()
	if err := restarted.Ensure(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.CheckForUpdates(ctx); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != before || restarted.State().PinnedVersion != "2.48.0" {
		t.Fatal("pin did not survive restart")
	}
	if err := restarted.Ensure(ctx, "latest"); err != nil {
		t.Fatal(err)
	}
	if restarted.State().CandidateVersion != "2.50" || restarted.State().PinnedVersion != "" {
		t.Fatal("latest did not resume updates")
	}
}

func TestEnsureFailurePreservesActiveAndCandidate(t *testing.T) {
	mgr, _, _ := installTestManager(t)
	ctx := context.Background()
	if err := mgr.Ensure(ctx, ""); err != nil {
		t.Fatal(err)
	}
	st := mgr.State()
	if err := mgr.promoteCandidate(st.CandidateVersion, st.CandidatePath); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Ensure(ctx, "2.48.0"); err != nil {
		t.Fatal(err)
	}
	st = mgr.State()
	for _, tag := range []string{"../bad", "2.40.0", "2.41.0", "2.42.0", "2.43.0", "2.44.0"} {
		if err := mgr.Ensure(ctx, tag); err == nil {
			t.Fatalf("accepted invalid/missing/unverified release %s", tag)
		}
		next := mgr.State()
		if next.ActivePath != st.ActivePath || next.CandidatePath != st.CandidatePath || next.PinnedVersion != st.PinnedVersion {
			t.Fatal("failed install changed selected versions")
		}
		for _, path := range []string{st.ActivePath, st.CandidatePath} {
			if _, err := os.Stat(path); err != nil {
				t.Fatal("failed install removed a usable binary")
			}
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := mgr.Ensure(cancelled, "2.47.0"); err == nil {
		t.Fatal("cancelled install succeeded")
	}
	if mgr.State().CandidatePath != st.CandidatePath {
		t.Fatal("cancellation changed candidate")
	}
}

func TestCandidateStateFailureKeepsFiles(t *testing.T) {
	mgr, _, _ := installTestManager(t)
	if err := mgr.Ensure(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	st := mgr.State()
	// Force an atomic save failure without relying on platform permission semantics.
	bad := filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(bad, 0700); err != nil {
		t.Fatal(err)
	}
	mgr.statePath = bad
	if err := mgr.promoteCandidate(st.CandidateVersion, st.CandidatePath); err == nil {
		t.Fatal("promotion ignored persistence failure")
	}
	if err := mgr.rejectCandidate(st.CandidateVersion, st.CandidatePath); err == nil {
		t.Fatal("rejection ignored persistence failure")
	}
	if mgr.State() != st {
		t.Fatal("failed save changed in-memory selection")
	}
	if _, err := os.Stat(st.CandidatePath); err != nil {
		t.Fatal("failed save deleted candidate")
	}
}

func TestFirstCandidateRejectionNeedsAnotherVersion(t *testing.T) {
	mgr, _, downloads := installTestManager(t)
	ctx := context.Background()
	if err := mgr.Ensure(ctx, ""); err != nil {
		t.Fatal(err)
	}
	st := mgr.State()
	if err := mgr.rejectCandidate(st.CandidateVersion, st.CandidatePath); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Ensure(ctx, ""); err == nil {
		t.Fatal("rejected first install was reused")
	}
	if downloads.Load() != 1 {
		t.Fatal("rejected version was downloaded again")
	}
	if err := mgr.Ensure(ctx, "2.48.0"); err != nil {
		t.Fatal(err)
	}
	if mgr.State().CandidateVersion != "2.48" {
		t.Fatal("alternate version did not recover")
	}
}

func TestUnpinRejectedLatestRetainsRollback(t *testing.T) {
	mgr, _, _ := installTestManager(t)
	ctx := context.Background()
	if err := mgr.Ensure(ctx, "2.48.0"); err != nil {
		t.Fatal(err)
	}
	st := mgr.State()
	if err := mgr.promoteCandidate(st.CandidateVersion, st.CandidatePath); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Ensure(ctx, "latest"); err != nil {
		t.Fatal(err)
	}
	st = mgr.State()
	if err := mgr.rejectCandidate(st.CandidateVersion, st.CandidatePath); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Ensure(ctx, "2.48.0"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Ensure(ctx, "latest"); err != nil {
		t.Fatal(err)
	}
	if mgr.State().PinnedVersion != "" || mgr.State().ActiveVersion != "2.48" || mgr.State().RejectedVersion != "2.50" {
		t.Fatal("returning to automatic updates lost rollback or retained pin")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type stalledBody struct {
	ctx    context.Context
	prefix bool
	closed *int
}

func (b *stalledBody) Read(p []byte) (int, error) {
	if !b.prefix {
		b.prefix = true
		return copy(p, "partial"), nil
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b *stalledBody) Close() error { *b.closed++; return nil }

func TestMetadataDeadlineIsSeparateFromAssetDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := NewReleaseClient()
		if client.HTTPClient.Timeout != 0 {
			t.Fatal("shared whole-response timeout")
		}
		client.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
		start := time.Now()
		_, err := client.FetchRelease(context.Background(), "")
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 30*time.Second {
			t.Fatalf("metadata deadline: %v %v", err, time.Since(start))
		}
	})
}

func TestSlowAssetSucceedsAndParentDeadlineWins(t *testing.T) {
	for _, shortParent := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			payload := "complete"
			client := NewReleaseClient()
			calls := 0
			client.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				select {
				case <-r.Context().Done():
					return nil, r.Context().Err()
				case <-time.After(150 * time.Second):
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
				}
			})
			limit := PreparationTimeout
			if shortParent {
				limit = time.Minute
			}
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			start := time.Now()
			err := client.DownloadAndVerify(ctx, GitHubAsset{BrowserDownloadURL: "https://example.invalid/a", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(payload)))}, filepath.Join(t.TempDir(), "asset.zip"))
			if calls != 1 {
				t.Fatal("unexpected retry", calls)
			}
			if shortParent {
				if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != time.Minute {
					t.Fatal("parent deadline ignored", err)
				}
			} else if err != nil || time.Since(start) != 150*time.Second {
				t.Fatal("slow transfer rejected", err)
			}
		})
	}
}

func TestAssetStallRetriesFreshAndRemainsBounded(t *testing.T) {
	for _, recover := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			payload := []byte("verified complete archive")
			asset := GitHubAsset{BrowserDownloadURL: "https://example.invalid/asset", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(payload))}
			target := filepath.Join(t.TempDir(), "archive.zip")
			if err := os.WriteFile(target, []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
			calls, closed := 0, 0
			client := NewReleaseClient()
			client.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				old, err := os.ReadFile(target)
				if err != nil || string(old) != "existing" {
					t.Fatal("partial target exposed")
				}
				deadline, ok := r.Context().Deadline()
				if !ok || deadline.Sub(time.Now()) != 3*time.Minute {
					t.Fatal("wrong asset deadline")
				}
				var body io.ReadCloser = &stalledBody{ctx: r.Context(), closed: &closed}
				if recover && calls == 2 {
					body = io.NopCloser(bytes.NewReader(payload))
				}
				return &http.Response{StatusCode: 200, Body: body, Header: make(http.Header)}, nil
			})
			start := time.Now()
			err := client.DownloadAndVerify(context.Background(), asset, target)
			got, _ := os.ReadFile(target)
			if recover {
				if err != nil || calls != 2 || closed != 1 || !bytes.Equal(got, payload) || time.Since(start) != 3*time.Minute+2*time.Second {
					t.Fatalf("recovery: %v calls=%d closed=%d elapsed=%v", err, calls, closed, time.Since(start))
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "3 attempts") || calls != 3 || closed != 3 || string(got) != "existing" || time.Since(start) != 9*time.Minute+6*time.Second {
					t.Fatalf("exhaustion: %v calls=%d closed=%d elapsed=%v", err, calls, closed, time.Since(start))
				}
			}
			if _, err := os.Stat(target + ".download.tmp"); !os.IsNotExist(err) {
				t.Fatal("partial file left behind")
			}
		})
	}
}

func TestDownloadCancellationStopsBodyAndRetryWait(t *testing.T) {
	for _, duringBody := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			calls, closed := 0, 0
			client := NewReleaseClient()
			client.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if !duringBody {
					return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
				}
				return &http.Response{StatusCode: 200, Body: &stalledBody{ctx: r.Context(), closed: &closed}, Header: make(http.Header)}, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			target := filepath.Join(t.TempDir(), "asset.zip")
			done := make(chan error, 1)
			go func() {
				done <- client.DownloadAndVerify(ctx, GitHubAsset{BrowserDownloadURL: "https://example.invalid/a", Digest: "sha256:" + strings.Repeat("0", 64)}, target)
			}()
			synctest.Wait()
			at := time.Now()
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if calls != 1 || !time.Now().Equal(at) || (duringBody && closed != 1) {
				t.Fatal("cancel delayed/retried", calls, closed)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("canceled target exposed")
			}
			if _, err := os.Stat(target + ".download.tmp"); !os.IsNotExist(err) {
				t.Fatal("partial left behind")
			}
		})
	}
}

func TestFailedTransferNeverBecomesCandidate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := NewReleaseClient()
		payload, digest := makeMockDCEZip(t, "linux")
		calls, closed := 0, 0
		client.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var body io.ReadCloser
			if strings.HasSuffix(r.URL.Path, "/latest") {
				data, _ := json.Marshal(GitHubRelease{TagName: "2.50", Assets: []GitHubAsset{{Name: "DiscordChatExporter.Cli.linux-x64.zip", BrowserDownloadURL: "https://example.invalid/asset", Digest: "sha256:" + digest}}})
				body = io.NopCloser(bytes.NewReader(data))
			} else {
				calls++
				body = &stalledBody{ctx: r.Context(), closed: &closed}
			}
			return &http.Response{StatusCode: 200, Body: body, Header: make(http.Header)}, nil
		})
		m, err := NewManager(t.TempDir(), "", "fake", WithPlatform("linux", "amd64"), WithReleaseClient(client), WithCommandRunner(func(context.Context, string, []string, []string, io.Writer, io.Writer) error {
			t.Fatal("installer executed DCE")
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), PreparationTimeout)
		defer cancel()
		if err := m.Ensure(ctx, ""); err == nil || calls != 3 || m.IsConfigured() {
			t.Fatalf("failed install: %v calls=%d state=%+v", err, calls, m.State())
		}
		if entries, _ := os.ReadDir(filepath.Join(m.dataDir, "dce", "versions")); len(entries) != 0 {
			t.Fatal("partial candidate files")
		}
		// Positive control: the identical fixture becomes a candidate when complete.
		transport := client.HTTPClient.Transport
		client.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/latest") {
				return transport.RoundTrip(r)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(payload)), Header: make(http.Header)}, nil
		})
		if err := m.Ensure(ctx, ""); err != nil || m.State().CandidatePath == "" || m.State().ActivePath != "" {
			t.Fatalf("verified fixture not staged: %v", err)
		}
	})
}
