package dce

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultGitHubLatestReleaseURL = "https://api.github.com/repos/Tyrrrz/DiscordChatExporter/releases/latest"
	DefaultUserAgent              = "CordBrief/3.0"
	MaxReleaseJSONBytes           = 1024 * 1024       // 1 MB
	MaxDownloadBytes              = 150 * 1024 * 1024 // 150 MB
)

// GitHubRelease represents the subset of GitHub release metadata needed by the updater.
type GitHubRelease struct {
	TagName    string        `json:"tag_name"`
	Assets     []GitHubAsset `json:"assets"`
	Prerelease bool          `json:"prerelease"`
	Draft      bool          `json:"draft"`
}

// GitHubAsset represents a single downloadable artifact published with a release.
type GitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
}

// ReleaseClient fetches release metadata and downloads assets.
type ReleaseClient struct {
	HTTPClient *http.Client
	Endpoint   string
	UserAgent  string
	AllowHTTP  bool // strictly for tests with httptest.Server
}

// NewReleaseClient constructs a standard ReleaseClient.
func NewReleaseClient() *ReleaseClient {
	return &ReleaseClient{
		HTTPClient: &http.Client{Timeout: 2 * time.Minute},
		Endpoint:   DefaultGitHubLatestReleaseURL,
		UserAgent:  DefaultUserAgent,
	}
}

// ExpectedAssetPattern returns the standard asset filename for the given OS and architecture.
func ExpectedAssetPattern(goos, goarch string) (string, error) {
	switch goos {
	case "windows":
		switch goarch {
		case "amd64":
			return "DiscordChatExporter.Cli.win-x64.zip", nil
		case "arm64":
			return "DiscordChatExporter.Cli.win-arm64.zip", nil
		}
	case "linux":
		switch goarch {
		case "amd64":
			return "DiscordChatExporter.Cli.linux-x64.zip", nil
		case "arm64":
			return "DiscordChatExporter.Cli.linux-arm64.zip", nil
		}
	case "darwin":
		switch goarch {
		case "amd64":
			return "DiscordChatExporter.Cli.osx-x64.zip", nil
		case "arm64":
			return "DiscordChatExporter.Cli.osx-arm64.zip", nil
		}
	}
	return "", fmt.Errorf("unsupported platform: %s/%s", goos, goarch)
}

// FetchLatestRelease queries the GitHub latest stable release endpoint.
func (c *ReleaseClient) FetchLatestRelease(ctx context.Context) (*GitHubRelease, error) {
	return c.FetchRelease(ctx, "")
}

// FetchRelease uses the official latest endpoint or an exact official release tag.
func (c *ReleaseClient) FetchRelease(ctx context.Context, tag string) (*GitHubRelease, error) {
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = DefaultGitHubLatestReleaseURL
	}
	if tag != "" {
		if _, err := ParseVersion(tag); err != nil {
			return nil, errors.New("DCE version must be an official numeric release tag")
		}
		endpoint = strings.TrimSuffix(endpoint, "/latest") + "/tags/" + url.PathEscape(tag)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create release request: %w", err)
	}

	ua := c.UserAgent
	if ua == "" {
		ua = DefaultUserAgent
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/vnd.github+json")

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code from release API: %d", resp.StatusCode)
	}

	limitedBody := io.LimitReader(resp.Body, MaxReleaseJSONBytes)
	var rel GitHubRelease
	if err := json.NewDecoder(limitedBody).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release json: %w", err)
	}

	if strings.TrimSpace(rel.TagName) == "" {
		return nil, errors.New("release response missing tag_name")
	}
	if rel.Draft || (tag == "" && rel.Prerelease) {
		return nil, errors.New("release is not a published stable release")
	}
	if tag != "" && rel.TagName != tag {
		return nil, errors.New("release response does not match the requested tag")
	}

	return &rel, nil
}

// FindAsset finds the matching asset for the specified GOOS and GOARCH in the release.
func (c *ReleaseClient) FindAsset(rel *GitHubRelease, goos, goarch string) (*GitHubAsset, error) {
	if rel == nil {
		return nil, errors.New("cannot find asset in nil release")
	}

	expectedName, err := ExpectedAssetPattern(goos, goarch)
	if err != nil {
		return nil, err
	}

	for _, a := range rel.Assets {
		if a.Name == expectedName {
			return &a, nil
		}
	}

	return nil, fmt.Errorf("release %s has no asset matching %s for %s/%s", rel.TagName, expectedName, goos, goarch)
}

// DownloadAndVerify streams the asset into targetPath and validates its SHA-256 digest.
func (c *ReleaseClient) DownloadAndVerify(ctx context.Context, asset GitHubAsset, targetPath string) error {
	rawURL := strings.TrimSpace(asset.BrowserDownloadURL)
	if rawURL == "" {
		return errors.New("download url cannot be empty")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid download url: %w", err)
	}
	if !c.AllowHTTP && u.Scheme != "https" {
		return fmt.Errorf("insecure download scheme %q; HTTPS required", u.Scheme)
	}

	// Validate digest syntax
	digestStr := strings.TrimSpace(asset.Digest)
	if !strings.HasPrefix(digestStr, "sha256:") {
		return fmt.Errorf("missing or unsupported asset digest %q (expected sha256:<hex>)", digestStr)
	}
	expectedHex := strings.TrimPrefix(digestStr, "sha256:")
	expectedBytes, err := hex.DecodeString(expectedHex)
	if err != nil || len(expectedBytes) != 32 {
		return fmt.Errorf("invalid sha256 hex in asset digest: %q", expectedHex)
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0700); err != nil {
		return fmt.Errorf("create target dir: %w", err)
	}

	tmpPath := targetPath + ".download.tmp"
	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create temp download file: %w", err)
	}

	cleanedUp := false
	defer func() {
		if !cleanedUp {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}
	ua := c.UserAgent
	if ua == "" {
		ua = DefaultUserAgent
	}
	req.Header.Set("User-Agent", ua)

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected download status code: %d", resp.StatusCode)
	}

	hasher := sha256.New()
	multiWriter := io.MultiWriter(tmpFile, hasher)

	// Stream while enforcing size bounds
	limitedReader := io.LimitReader(resp.Body, MaxDownloadBytes+1)
	written, err := io.Copy(multiWriter, limitedReader)
	if err != nil {
		return fmt.Errorf("streaming download failed: %w", err)
	}

	if written > MaxDownloadBytes {
		return fmt.Errorf("download exceeded maximum allowed size (%d bytes)", MaxDownloadBytes)
	}

	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	// Verify SHA-256 digest
	actualBytes := hasher.Sum(nil)
	if subtle.ConstantTimeCompare(actualBytes, expectedBytes) != 1 {
		return fmt.Errorf("digest mismatch: expected sha256:%s, got sha256:%x", expectedHex, actualBytes)
	}

	// Atomically replace targetPath with verified temp file
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("rename verified download: %w", err)
	}
	cleanedUp = true

	return nil
}
