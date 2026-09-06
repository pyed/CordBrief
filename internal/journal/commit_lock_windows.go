//go:build windows

package journal

import (
	"fmt"
	"net"
	"os"
	"strconv"
)

// DefaultCommitLockPort is the default localhost TCP port held by a committing Core process on Windows.
const DefaultCommitLockPort = 28743

// CommitLock represents exclusive ownership of committing transactions across processes on Windows.
// Backed by an exclusive loopback socket listener that is automatically released by the OS on process exit or crash.
type CommitLock struct {
	listener net.Listener
	addr     string
}

// GetCommitLockAddr returns the configured loopback address for the commit lock on Windows.
func GetCommitLockAddr() string {
	port := DefaultCommitLockPort
	if env := os.Getenv("CORDBRIEF_COMMIT_LOCK_PORT"); env != "" {
		if p, err := strconv.Atoi(env); err == nil && p > 0 {
			port = p
		}
	}
	return fmt.Sprintf("127.0.0.1:%d", port)
}

// AcquireCommitLock attempts to acquire exclusive committing ownership on Windows via loopback TCP listener.
// dataDir is accepted for API consistency across platforms.
func AcquireCommitLock(dataDir string) (*CommitLock, error) {
	addr := GetCommitLockAddr()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w (%s: %v)", ErrCommitLockActive, addr, err)
	}

	return &CommitLock{
		listener: ln,
		addr:     ln.Addr().String(),
	}, nil
}

// Release releases the commit lock, allowing other committing processes to acquire it.
func (l *CommitLock) Release() error {
	if l == nil || l.listener == nil {
		return nil
	}
	err := l.listener.Close()
	l.listener = nil
	return err
}

// Addr returns the bound address of the commit lock.
func (l *CommitLock) Addr() string {
	if l == nil {
		return ""
	}
	return l.addr
}
