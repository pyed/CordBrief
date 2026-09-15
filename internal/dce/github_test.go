package dce_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyed/CordBrief/internal/dce"
)

func TestGitHub(t *testing.T) {
	ctx := context.Background()

	t.Run("fetch latest release success", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("expected GET, got %s", r.Method)
			}
			if r.Header.Get("User-Agent") != "CordBrief/3.0" {
				t.Errorf("unexpected User-Agent: %s", r.Header.Get("User-Agent"))
			}
			if r.Header.Get("Accept") != "application/vnd.github+json" {
				t.Errorf("unexpected Accept: %s", r.Header.Get("Accept"))
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
  "tag_name": "2.49.0",
  "assets": [
    {
      "name": "DiscordChatExporter.Cli.win-x64.zip",
      "browser_download_url": "https://example.com/win-x64.zip",
      "size": 12345,
      "digest": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    }
  ]
}`))
		}))
		defer ts.Close()

		client := dce.NewReleaseClient()
		client.Endpoint = ts.URL
		client.HTTPClient = ts.Client()

		rel, err := client.FetchLatestRelease(ctx)
		if err != nil {
			t.Fatalf("fetch failed: %v", err)
		}
		if rel.TagName != "2.49.0" {
			t.Fatalf("expected tag 2.49.0, got %s", rel.TagName)
		}
		if len(rel.Assets) != 1 || rel.Assets[0].Name != "DiscordChatExporter.Cli.win-x64.zip" {
			t.Fatalf("unexpected assets: %+v", rel.Assets)
		}
	})

	t.Run("fetch latest release handles errors", func(t *testing.T) {
		// HTTP 404
		ts404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer ts404.Close()

		c404 := dce.NewReleaseClient()
		c404.Endpoint = ts404.URL
		c404.HTTPClient = ts404.Client()
		if _, err := c404.FetchLatestRelease(ctx); err == nil {
			t.Fatal("expected error on 404 status, got nil")
		}

		// Malformed JSON
		tsBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not-json"))
		}))
		defer tsBadJSON.Close()

		cBad := dce.NewReleaseClient()
		cBad.Endpoint = tsBadJSON.URL
		cBad.HTTPClient = tsBadJSON.Client()
		if _, err := cBad.FetchLatestRelease(ctx); err == nil {
			t.Fatal("expected error on bad JSON, got nil")
		}

		// Empty tag name
		tsEmptyTag := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"tag_name": ""}`))
		}))
		defer tsEmptyTag.Close()

		cEmpty := dce.NewReleaseClient()
		cEmpty.Endpoint = tsEmptyTag.URL
		cEmpty.HTTPClient = tsEmptyTag.Client()
		if _, err := cEmpty.FetchLatestRelease(ctx); err == nil {
			t.Fatal("expected error on empty tag_name, got nil")
		}
	})

	t.Run("platform asset matching", func(t *testing.T) {
		tests := []struct {
			goos, goarch string
			expected     string
			shouldErr    bool
		}{
			{"windows", "amd64", "DiscordChatExporter.Cli.win-x64.zip", false},
			{"windows", "arm64", "DiscordChatExporter.Cli.win-arm64.zip", false},
			{"linux", "amd64", "DiscordChatExporter.Cli.linux-x64.zip", false},
			{"linux", "arm64", "DiscordChatExporter.Cli.linux-arm64.zip", false},
			{"darwin", "amd64", "DiscordChatExporter.Cli.osx-x64.zip", false},
			{"darwin", "arm64", "DiscordChatExporter.Cli.osx-arm64.zip", false},
			{"openbsd", "amd64", "", true},
			{"windows", "386", "", true},
		}

		for _, tc := range tests {
			got, err := dce.ExpectedAssetPattern(tc.goos, tc.goarch)
			if tc.shouldErr {
				if err == nil {
					t.Errorf("ExpectedAssetPattern(%q, %q) expected error, got %q", tc.goos, tc.goarch, got)
				}
			} else {
				if err != nil {
					t.Fatalf("ExpectedAssetPattern(%q, %q) failed: %v", tc.goos, tc.goarch, err)
				}
				if got != tc.expected {
					t.Errorf("ExpectedAssetPattern(%q, %q) = %q, want %q", tc.goos, tc.goarch, got, tc.expected)
				}
			}
		}
	})

	t.Run("download and verify with SHA-256", func(t *testing.T) {
		payload := []byte("Simulated DiscordChatExporter zip payload data")
		h := sha256.Sum256(payload)
		correctDigest := fmt.Sprintf("sha256:%x", h)

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
		}))
		defer ts.Close()

		tempDir := t.TempDir()
		destPath := filepath.Join(tempDir, "archive.zip")

		client := dce.NewReleaseClient()
		client.AllowHTTP = true // allow httptest
		client.HTTPClient = ts.Client()

		// 1. Success with correct digest
		asset := dce.GitHubAsset{
			Name:               "DiscordChatExporter.Cli.win-x64.zip",
			BrowserDownloadURL: ts.URL + "/download.zip",
			Size:               int64(len(payload)),
			Digest:             correctDigest,
		}

		if err := client.DownloadAndVerify(ctx, asset, destPath); err != nil {
			t.Fatalf("download failed: %v", err)
		}

		downloadedBytes, err := os.ReadFile(destPath)
		if err != nil {
			t.Fatalf("failed to read downloaded file: %v", err)
		}
		if string(downloadedBytes) != string(payload) {
			t.Fatalf("downloaded payload mismatch")
		}

		// 2. Digest mismatch rejected
		badDigestAsset := asset
		badDigestAsset.Digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		badDest := filepath.Join(tempDir, "bad.zip")

		if err := client.DownloadAndVerify(ctx, badDigestAsset, badDest); err == nil {
			t.Fatal("expected error on digest mismatch, got nil")
		} else if !strings.Contains(err.Error(), "digest mismatch") {
			t.Fatalf("unexpected error message: %v", err)
		}
		if _, err := os.Stat(badDest); !os.IsNotExist(err) {
			t.Fatal("expected failed download target to not exist")
		}

		// 3. Missing/invalid digest rejected
		noDigestAsset := asset
		noDigestAsset.Digest = ""
		if err := client.DownloadAndVerify(ctx, noDigestAsset, badDest); err == nil {
			t.Fatal("expected error on missing digest, got nil")
		}

		// 4. Insecure HTTP rejected when AllowHTTP is false
		secureClient := dce.NewReleaseClient()
		secureClient.AllowHTTP = false
		if err := secureClient.DownloadAndVerify(ctx, asset, badDest); err == nil {
			t.Fatal("expected error on insecure HTTP URL, got nil")
		} else if !strings.Contains(err.Error(), "insecure download scheme") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}
