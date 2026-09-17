package dce

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pyed/CordBrief/internal/state"
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

	statePath      string
	state          *UpdaterState
	maxExportBytes int64
	mu             sync.Mutex
	opMu           sync.Mutex // Serialize install/export so candidate files cannot change during use.
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

	dceDir := filepath.Join(dataDir, "dce")
	m.statePath = filepath.Join(dceDir, "updater.json")

	st, err := LoadUpdaterState(m.statePath, m.bootstrapPath, m.bootstrapVersion)
	if err != nil && st == nil {
		return nil, err
	}
	m.state = st

	return m, nil
}

// IsConfigured returns true if the manager has credentials and an executable path.
func (m *Manager) IsConfigured() bool {
	if m == nil || m.token == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state != nil && (m.state.ActivePath != "" || m.state.CandidatePath != "")
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

	status := fmt.Sprintf("%s · active", activeVer)
	if m.state.CandidateVersion != "" {
		status = fmt.Sprintf("%s · candidate %s pending", activeVer, m.state.CandidateVersion)
	} else if m.state.RejectedVersion != "" {
		status = fmt.Sprintf("%s · %s rejected", activeVer, m.state.RejectedVersion)
	}
	if m.state.PinnedVersion != "" {
		status += " · pinned to " + m.state.PinnedVersion
	}
	return status
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
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if !m.IsConfigured() {
		return nil, errors.New("dce is not configured")
	}

	m.mu.Lock()
	activePath := m.state.ActivePath
	candPath := m.state.CandidatePath
	candVer := m.state.CandidateVersion
	m.mu.Unlock()

	// Gate each actual process attempt, including a candidate rollback. The existing
	// operation lock serializes this path with updates; no separate queue is needed.
	attempted := false
	run := func(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
		if err := m.reserveExport(ctx); err != nil {
			return err
		}
		if req.Before.IsZero() {
			args = append(args, "--before", m.now().UTC().Format("2006-01-02T15:04:05Z"))
		}
		attempted = true
		return m.runner(ctx, name, args, env, stdout, stderr)
	}

	// If candidate exists, execute candidate on probation
	if candPath != "" {
		candClient := NewMockClient(candPath, m.token, run)
		candClient.maxExportBytes = m.maxExportBytes
		res, err := candClient.Export(ctx, req)
		if err == nil {
			// Candidate succeeded! Promote candidate.
			if err := m.promoteCandidate(candVer, candPath); err != nil {
				return nil, err
			}
			return res, nil
		}

		// If caller canceled or timed out, do not reject, do not promote, do not retry
		if !attempted || isCancellation(ctx, err) {
			return nil, err
		}

		// Candidate failed under normal execution. Reject candidate and rollback.
		if err := m.rejectCandidate(candVer, candPath); err != nil {
			return nil, err
		}

		// Retry requested operation once with active known-good if context alive
		if ctx.Err() == nil && activePath != "" {
			activeClient := NewMockClient(activePath, m.token, run)
			activeClient.maxExportBytes = m.maxExportBytes
			return activeClient.Export(ctx, req)
		}
		return nil, err
	}

	// No candidate: execute known-good active
	if activePath == "" {
		return nil, errors.New("no active dce executable configured")
	}
	activeClient := NewMockClient(activePath, m.token, run)
	activeClient.maxExportBytes = m.maxExportBytes
	return activeClient.Export(ctx, req)
}

// reserveExport persists the start boundary before invoking DCE, so even failed
// attempts and application restarts retain spacing. It never touches cursors.
func (m *Manager) reserveExport(ctx context.Context) error {
	cfg, err := state.NewStore(m.dataDir).LoadConfig()
	if err != nil {
		return err
	}
	next := m.State()
	if !next.LastExport.IsZero() && cfg.DCECooldown() > 0 {
		if err := waitContext(ctx, next.LastExport.Add(cfg.DCECooldown()).Sub(m.now())); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	next.LastExport = m.now().UTC()
	return m.saveState(next)
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// saveState commits a snapshot before exposing it to exports or deleting old files.
func (m *Manager) saveState(next UpdaterState) error {
	if err := SaveUpdaterState(m.statePath, &next); err != nil {
		return fmt.Errorf("save DCE updater state: %w", err)
	}
	m.mu.Lock()
	m.state = &next
	m.mu.Unlock()
	return nil
}

func (m *Manager) removeVersion(path string) {
	if path != "" && filepath.Clean(path) != filepath.Clean(m.bootstrapPath) &&
		isManagedPath(filepath.Join(m.dataDir, "dce", "versions"), filepath.Dir(path)) {
		_ = os.RemoveAll(filepath.Dir(path))
	}
}

func (m *Manager) promoteCandidate(candVer, candPath string) error {
	next := m.State()
	oldActivePath := next.ActivePath
	next.ActiveVersion, next.ActivePath = candVer, candPath
	next.CandidateVersion, next.CandidatePath = "", ""
	if err := m.saveState(next); err != nil {
		return err
	}
	if oldActivePath != candPath {
		m.removeVersion(oldActivePath)
	}
	return nil
}

func (m *Manager) rejectCandidate(candVer, candPath string) error {
	next := m.State()
	next.RejectedVersion = candVer
	next.CandidateVersion, next.CandidatePath = "", ""
	if err := m.saveState(next); err != nil {
		return err
	}
	if candPath != next.ActivePath {
		m.removeVersion(candPath)
	}
	return nil
}

// Ensure installs a first candidate if necessary, without executing it. A requested
// official tag pins updates; "latest" removes the pin. Empty keeps the saved policy.
func (m *Manager) Ensure(ctx context.Context, requested string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if m.token == "" {
		return errors.New("DISCORD_TOKEN is required")
	}
	next := m.State()
	// Recover missing installed files; never promote an untested download to active.
	for _, entry := range []struct{ path, version *string }{
		{&next.ActivePath, &next.ActiveVersion}, {&next.CandidatePath, &next.CandidateVersion},
	} {
		if *entry.path == "" {
			continue
		}
		fi, err := os.Stat(*entry.path)
		if errors.Is(err, os.ErrNotExist) {
			*entry.path, *entry.version = "", ""
		} else if err != nil || !fi.Mode().IsRegular() {
			return errors.New("DCE executable is not accessible as a regular file")
		}
	}
	if next != m.State() {
		if err := m.saveState(next); err != nil {
			return err
		}
	}
	if requested == "" && m.IsConfigured() {
		return nil
	}
	pin := next.PinnedVersion
	if requested != "" {
		pin = requested
		if requested == "latest" {
			pin = ""
		}
	}
	if _, err := m.checkForUpdates(ctx, pin, requested != ""); err != nil {
		return err
	}
	if !m.IsConfigured() {
		return errors.New("no usable DCE release; retry with --dce-version and another official tag")
	}
	return nil
}

// CheckForUpdates shares the same verified installer as first launch and version pins.
func (m *Manager) CheckForUpdates(ctx context.Context) (bool, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.checkForUpdates(ctx, m.State().PinnedVersion, false)
}

func (m *Manager) checkForUpdates(ctx context.Context, pin string, explicit bool) (bool, error) {
	next := m.State()
	if pin != "" {
		if _, err := ParseVersion(pin); err != nil {
			return false, errors.New("DCE version must be an official numeric release tag, or latest")
		}
		if sameVersion(pin, next.ActiveVersion) || sameVersion(pin, next.CandidateVersion) {
			oldCandidate := next.CandidatePath
			next.LastCheck = m.now().UTC()
			if sameVersion(pin, next.ActiveVersion) {
				next.CandidateVersion, next.CandidatePath = "", ""
			}
			next.PinnedVersion = pin
			if err := m.saveState(next); err != nil {
				return false, err
			}
			if oldCandidate != next.CandidatePath && oldCandidate != next.ActivePath {
				m.removeVersion(oldCandidate)
			}
			return false, nil
		}
	}
	// Record attempts too, so a failed weekly check does not spin.
	next.LastCheck = m.now().UTC()
	if err := m.saveState(next); err != nil {
		return false, err
	}
	rel, err := m.releaseClient.FetchRelease(ctx, pin)
	if err != nil {
		return false, fmt.Errorf("fetch official DCE release: %w", err)
	}
	relVer, err := ParseVersion(rel.TagName)
	if err != nil {
		return false, fmt.Errorf("parse release tag: %w", err)
	}
	if sameVersion(rel.TagName, next.RejectedVersion) {
		if explicit && pin != "" {
			return false, errors.New("requested DCE version was rejected; choose another official tag")
		}
		if explicit {
			next.PinnedVersion = ""
			return false, m.saveState(next)
		}
		return false, nil
	}
	if pin == "" && next.ActivePath != "" {
		active, err := ParseVersion(next.ActiveVersion)
		if err != nil {
			return false, errors.New("cannot automatically replace DCE with an unknown active version; use --dce-version to choose explicitly")
		}
		if relVer.Compare(active) <= 0 {
			oldCandidate := next.CandidatePath
			if explicit {
				next.CandidatePath, next.CandidateVersion = "", ""
			}
			next.PinnedVersion = ""
			if err := m.saveState(next); err != nil {
				return false, err
			}
			if explicit && oldCandidate != next.ActivePath {
				m.removeVersion(oldCandidate)
			}
			return false, nil
		}
	}
	if next.CandidatePath != "" {
		candidate, err := ParseVersion(next.CandidateVersion)
		if err == nil && (relVer.Compare(candidate) == 0 || (pin == "" && relVer.Compare(candidate) < 0)) {
			next.PinnedVersion = pin
			return false, m.saveState(next)
		}
	}
	asset, err := m.releaseClient.FindAsset(rel, m.goos, m.goarch)
	if err != nil {
		return false, err
	}
	tmpFile, err := os.CreateTemp("", "cordbrief-dce-*.zip")
	if err != nil {
		return false, err
	}
	tmpZipPath := tmpFile.Name()
	_ = tmpFile.Close()
	defer os.Remove(tmpZipPath)
	if err := m.releaseClient.DownloadAndVerify(ctx, *asset, tmpZipPath); err != nil {
		return false, fmt.Errorf("download and verify DCE: %w", err)
	}
	targetDir := filepath.Join(m.dataDir, "dce", "versions", relVer.String())
	exePath := filepath.Join(targetDir, ExpectedExecutableName(m.goos))
	// An explicitly selected active version only changes policy, never overwrites its files.
	if filepath.Clean(exePath) == filepath.Clean(next.ActivePath) {
		return false, errors.New("refusing to overwrite the active DCE executable")
	}
	if err := ExtractReleaseZip(tmpZipPath, targetDir, m.goos); err != nil {
		return false, fmt.Errorf("extract DCE: %w", err)
	}
	oldCandidate := next.CandidatePath
	next.CandidateVersion, next.CandidatePath = relVer.String(), exePath
	next.PinnedVersion = pin
	if err := m.saveState(next); err != nil {
		m.removeVersion(exePath)
		return false, err
	}
	if oldCandidate != exePath && oldCandidate != next.ActivePath {
		m.removeVersion(oldCandidate)
	}
	return true, nil
}

func sameVersion(a, b string) bool {
	x, errA := ParseVersion(a)
	y, errB := ParseVersion(b)
	return errA == nil && errB == nil && x.Compare(y) == 0
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

			checkCtx, cancel := context.WithTimeout(ctx, PreparationTimeout)
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
