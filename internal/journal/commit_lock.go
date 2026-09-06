package journal

import (
	"errors"
	"path/filepath"
)

// ErrCommitLockActive is returned when another process already holds the commit lock.
var ErrCommitLockActive = errors.New("cannot acquire commit lock: another committing CordBrief process is currently active")

// DefaultLockFileName is the stable filename used for the commit lock in dataDir.
const DefaultLockFileName = "commit.lock"

// DefaultLockPath returns the full path to the commit lock file within dataDir.
func DefaultLockPath(dataDir string) string {
	if dataDir == "" {
		dataDir = "."
	}
	return filepath.Join(dataDir, DefaultLockFileName)
}
