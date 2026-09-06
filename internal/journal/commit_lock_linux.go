//go:build linux

package journal

import (
	"fmt"
	"os"
	"syscall"
)

// CommitLock represents exclusive ownership of committing transactions across processes.
// On Linux production, this is backed by an advisory flock on <dataDir>/commit.lock which is
// automatically released by the kernel upon process termination or crash.
// The lock file itself is NEVER deleted or unlinked by Release to avoid inode races.
type CommitLock struct {
	file *os.File
	path string
}

// AcquireCommitLock attempts to acquire exclusive committing ownership via kernel flock on <dataDir>/commit.lock.
func AcquireCommitLock(dataDir string) (*CommitLock, error) {
	lockPath := DefaultLockPath(dataDir)

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open commit lock file %s: %w", lockPath, err)
	}

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, fmt.Errorf("%w (%s: %v)", ErrCommitLockActive, lockPath, err)
		}
		return nil, fmt.Errorf("failed to acquire flock on %s: %w", lockPath, err)
	}

	return &CommitLock{
		file: f,
		path: lockPath,
	}, nil
}

// Release releases the flock and closes the lock file descriptor.
// It explicitly DOES NOT delete or unlink the lock file.
func (l *CommitLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	err := l.file.Close()
	l.file = nil
	return err
}

// Addr returns the path of the lock file.
func (l *CommitLock) Addr() string {
	if l == nil {
		return ""
	}
	return l.path
}
