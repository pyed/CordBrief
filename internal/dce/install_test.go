package dce

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
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
