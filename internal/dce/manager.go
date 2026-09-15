package dce

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Manager coordinates DiscordChatExporter execution, automatic updates, candidate probation,
// and automatic rollback. It implements job.DCEExporter.
type Manager struct {
	dataDir          string
	bootstrapPath    string
	bootstrapVersion string
	token            string
	releaseClient    *ReleaseClient
	runner           CommandRunner
	goos             string
	goarch           string
	now              func() time.Time

	statePath string
	state     *UpdaterState
	mu        sync.Mutex
}

// ManagerOption configures a Manager instance.
type ManagerOption func(*Manager)

// WithReleaseClient sets a custom ReleaseClient.
func WithReleaseClient(rc *ReleaseClient) ManagerOption {
	return func(m *Manager) {
		m.releaseClient = rc
	}
}

// WithCommandRunner sets a custom CommandRunner for executing DCE binaries.
func WithCommandRunner(runner CommandRunner) ManagerOption {
	return func(m *Manager) {
		m.runner = runner
	}
}

// WithBootstrapVersion sets an explicit bootstrap version string.
func WithBootstrapVersion(ver string) ManagerOption {
	return func(m *Manager) {
		m.bootstrapVersion = ver
	}
}

// WithPlatform sets an explicit OS/Arch platform for asset matching.
func WithPlatform(goos, goarch string) ManagerOption {
	return func(m *Manager) {
		m.goos = goos
		m.goarch = goarch
	}
}

// WithClock sets a custom clock function for the Manager.
func WithClock(now func() time.Time) ManagerOption {
	return func(m *Manager) {
		m.now = now
	}
}

// NewManager creates a Manager instance, loading or initializing updater state.
func NewManager(dataDir, bootstrapPath, token string, opts ...ManagerOption) (*Manager, error) {
	m := &Manager{
		dataDir:       dataDir,
		bootstrapPath: strings.TrimSpace(bootstrapPath),
		token:         strings.TrimSpace(token),
		goos:          runtime.GOOS,
		goarch:        runtime.GOARCH,
		now:           time.Now,
	}

	for _, opt := range opts {
		opt(m)
	}

	if m.releaseClient == nil {
		m.releaseClient = NewReleaseClient()
	}
	if m.runner == nil {
		m.runner = defaultCommandRunner
	}

	// Determine bootstrap version if not explicitly set
	if m.bootstrapVersion == "" && m.bootstrapPath != "" {
		c := NewMockClient(m.bootstrapPath, m.token, m.runner)
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		if v, err := c.Version(ctx); err == nil && v != "" {
			if pv, perr := ParseVersion(v); perr == nil {
				m.bootstrapVersion = pv.String()
			}
		}
		cancel()
	}
	if m.bootstrapVersion == "" {
		m.bootstrapVersion = "2.48.0"
	}

	dceDir := filepath.Join(dataDir, "dce")
	m.statePath = filepath.Join(dceDir, "updater.json")

	st, err := LoadUpdaterState(m.statePath, m.bootstrapPath, m.bootstrapVersion)
	if err != nil && st == nil {
		return nil, err
	}
	m.state = st

	// If newly created or active was empty, save initial state
	if m.state.ActivePath == "" && m.bootstrapPath != "" {
		m.state.ActivePath = m.bootstrapPath
		m.state.ActiveVersion = m.bootstrapVersion
		_ = SaveUpdaterState(m.statePath, m.state)
	}

	return m, nil
}

// IsConfigured returns true if the manager has credentials and an executable path.
func (m *Manager) IsConfigured() bool {
	if m == nil || m.token == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state != nil && (m.state.ActivePath != "" || m.bootstrapPath != "")
}

// State returns a snapshot copy of the current updater state.
func (m *Manager) State() UpdaterState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == nil {
		return UpdaterState{}
	}
	return *m.state
}

// Status returns a human-readable status string for Telegram /status.
func (m *Manager) Status() string {
	if !m.IsConfigured() {
		return "not configured"
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	activeVer := m.state.ActiveVersion
	if activeVer == "" {
		activeVer = m.bootstrapVersion
	}
	if activeVer == "" {
		activeVer = "unknown"
	}

	if m.state.CandidateVersion != "" {
		return fmt.Sprintf("%s · candidate %s pending", activeVer, m.state.CandidateVersion)
	}
	if m.state.RejectedVersion != "" {
		return fmt.Sprintf("%s · %s rejected", activeVer, m.state.RejectedVersion)
	}
	return fmt.Sprintf("%s · up to date", activeVer)
}

// NextCheckDuration returns the duration until the next weekly check is due.
func (m *Manager) NextCheckDuration() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == nil || m.state.LastCheck.IsZero() {
		return 0
	}
	elapsed := m.now().Sub(m.state.LastCheck)
	interval := 7 * 24 * time.Hour
	if elapsed >= interval {
		return 0
	}
	return interval - elapsed
}

// Export executes a bounded export using DiscordChatExporter.
// If a candidate version is pending, it executes the candidate on probation.
// On candidate success: candidate is promoted to active.
// On candidate failure: candidate is rejected and rollback retries once with active known-good.
// On caller cancellation: candidate remains pending without promotion or rejection.
func (m *Manager) Export(ctx context.Context, req ExportRequest) (*ExportResult, error) {
	if !m.IsConfigured() {
		return nil, errors.New("dce is not configured")
	}

	m.mu.Lock()
	activePath := m.state.ActivePath
	candPath := m.state.CandidatePath
	candVer := m.state.CandidateVersion
	m.mu.Unlock()

	// If candidate exists, execute candidate on probation
	if candPath != "" {
		candClient := NewMockClient(candPath, m.token, m.runner)
		res, err := candClient.Export(ctx, req)
		if err == nil {
			// Candidate succeeded! Promote candidate.
			m.promoteCandidate(candVer, candPath)
			return res, nil
		}

		// If caller canceled or timed out, do not reject, do not promote, do not retry
		if isCancellation(ctx, err) {
			return nil, err
		}

		// Candidate failed under normal execution. Reject candidate and rollback.
		m.rejectCandidate(candVer, candPath)

		// Retry requested operation once with active known-good if context alive
		if ctx.Err() == nil && activePath != "" {
			activeClient := NewMockClient(activePath, m.token, m.runner)
			return activeClient.Export(ctx, req)
		}
		return nil, err
	}

	// No candidate: execute known-good active
	if activePath == "" {
		return nil, errors.New("no active dce executable configured")
	}
	activeClient := NewMockClient(activePath, m.token, m.runner)
	return activeClient.Export(ctx, req)
}

func (m *Manager) promoteCandidate(candVer, candPath string) {
	m.mu.Lock()
	oldActivePath := m.state.ActivePath
	m.state.ActiveVersion = candVer
	m.state.ActivePath = candPath
	m.state.CandidateVersion = ""
	m.state.CandidatePath = ""
	_ = SaveUpdaterState(m.statePath, m.state)
	m.mu.Unlock()

	// Clean up obsolete updater-managed previous version
	versionsDir := filepath.Join(m.dataDir, "dce", "versions")
	if oldActivePath != "" && isManagedPath(versionsDir, oldActivePath) {
		if filepath.Clean(oldActivePath) != filepath.Clean(m.bootstrapPath) {
			oldDir := filepath.Dir(oldActivePath)
			_ = os.RemoveAll(oldDir)
		}
	}
}

func (m *Manager) rejectCandidate(candVer, candPath string) {
	m.mu.Lock()
	m.state.RejectedVersion = candVer
	m.state.CandidateVersion = ""
	m.state.CandidatePath = ""
	_ = SaveUpdaterState(m.statePath, m.state)
	m.mu.Unlock()

	// Clean up rejected candidate files
	versionsDir := filepath.Join(m.dataDir, "dce", "versions")
	if candPath != "" && isManagedPath(versionsDir, candPath) {
		if filepath.Clean(candPath) != filepath.Clean(m.bootstrapPath) {
			candDir := filepath.Dir(candPath)
			_ = os.RemoveAll(candDir)
		}
	}
}

// CheckForUpdates contacts the official GitHub latest stable release endpoint.
// Returns (true, nil) if a newer version was discovered and installed as candidate.
func (m *Manager) CheckForUpdates(ctx context.Context) (bool, error) {
	m.mu.Lock()
	m.state.LastCheck = m.now().UTC()
	_ = SaveUpdaterState(m.statePath, m.state)
	activeVerStr := m.state.ActiveVersion
	candVerStr := m.state.CandidateVersion
	candPathStr := m.state.CandidatePath
	rejVerStr := m.state.RejectedVersion
	m.mu.Unlock()

	rel, err := m.releaseClient.FetchLatestRelease(ctx)
	if err != nil {
		return false, fmt.Errorf("fetch latest release: %w", err)
	}

	relVer, err := ParseVersion(rel.TagName)
	if err != nil {
		return false, fmt.Errorf("parse release tag %q: %w", rel.TagName, err)
	}

	activeVer, err := ParseVersion(activeVerStr)
	if err != nil {
		activeVer, _ = ParseVersion(m.bootstrapVersion)
	}

	// No downgrade from active version
	if CompareVersions(relVer, activeVer) <= 0 {
		return false, nil
	}

	// If latest release equals already-rejected version, do nothing
	if rejVerStr != "" {
		if rejVer, err := ParseVersion(rejVerStr); err == nil && CompareVersions(relVer, rejVer) == 0 {
			return false, nil
		}
	}

	// If latest release equals existing candidate, do nothing
	if candVerStr != "" {
		if candVer, err := ParseVersion(candVerStr); err == nil {
			if CompareVersions(relVer, candVer) == 0 {
				return false, nil
			}
			if CompareVersions(relVer, candVer) < 0 {
				return false, nil
			}
			// Newer version than current candidate: clean up older untested candidate
			versionsDir := filepath.Join(m.dataDir, "dce", "versions")
			if isManagedPath(versionsDir, candPathStr) {
				_ = os.RemoveAll(filepath.Dir(candPathStr))
			}
		}
	}

	// Find platform asset
	asset, err := m.releaseClient.FindAsset(rel, m.goos, m.goarch)
	if err != nil {
		return false, fmt.Errorf("find asset for %s/%s: %w", m.goos, m.goarch, err)
	}

	// Download to temp file
	tmpFile, err := os.CreateTemp("", "cordbrief-dce-*.zip")
	if err != nil {
		return false, fmt.Errorf("create temp zip file: %w", err)
	}
	tmpZipPath := tmpFile.Name()
	_ = tmpFile.Close()
	defer os.Remove(tmpZipPath)

	if err := m.releaseClient.DownloadAndVerify(ctx, *asset, tmpZipPath); err != nil {
		return false, fmt.Errorf("download and verify asset: %w", err)
	}

	// Extract into target directory
	targetDir := filepath.Join(m.dataDir, "dce", "versions", relVer.String())
	if err := ExtractReleaseZip(tmpZipPath, targetDir, m.goos); err != nil {
		_ = os.RemoveAll(targetDir)
		return false, fmt.Errorf("extract release zip: %w", err)
	}
	exePath := filepath.Join(targetDir, ExpectedExecutableName(m.goos))

	// Update state
	m.mu.Lock()
	m.state.CandidateVersion = relVer.String()
	m.state.CandidatePath = exePath
	err = SaveUpdaterState(m.statePath, m.state)
	m.mu.Unlock()

	if err != nil {
		_ = os.RemoveAll(targetDir)
		return false, fmt.Errorf("save state after install: %w", err)
	}

	return true, nil
}

// Start launches the background weekly update checker.
func (m *Manager) Start(ctx context.Context) {
	if !m.IsConfigured() {
		return
	}
	go func() {
		for {
			wait := m.NextCheckDuration()
			if wait > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}

			checkCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			_, _ = m.CheckForUpdates(checkCtx)
			cancel()

			select {
			case <-ctx.Done():
				return
			case <-time.After(7 * 24 * time.Hour):
			}
		}
	}()
}

// isManagedPath returns true if path is strictly inside the managed versions directory.
func isManagedPath(versionsDir, path string) bool {
	cleanDir := filepath.Clean(versionsDir)
	cleanPath := filepath.Clean(path)
	rel, err := filepath.Rel(cleanDir, cleanPath)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && rel != "."
}

// isCancellation returns true if the error or context represents cancellation/timeout.
func isCancellation(ctx context.Context, err error) bool {
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "context canceled") || strings.Contains(msg, "context deadline exceeded") {
			return true
		}
	}
	return false
}
