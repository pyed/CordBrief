package journal

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCommitLock(t *testing.T) {
	testDir := t.TempDir()

	if runtime.GOOS == "windows" {
		// Use an ephemeral port for Windows to prevent collisions with any running services
		lnTmp, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("failed finding free port on windows: %v", err)
		}
		_, portStr, _ := net.SplitHostPort(lnTmp.Addr().String())
		_ = lnTmp.Close()
		t.Setenv("CORDBRIEF_COMMIT_LOCK_PORT", portStr)
	}

	// 1. Process 1 acquires commit lock
	lock1, err := AcquireCommitLock(testDir)
	if err != nil {
		t.Fatalf("first AcquireCommitLock failed: %v", err)
	}
	defer lock1.Release()

	// On Linux/unix, verify the lock file was created
	if runtime.GOOS != "windows" {
		lockPath := filepath.Join(testDir, DefaultLockFileName)
		if _, err := os.Stat(lockPath); err != nil {
			t.Fatalf("expected lock file at %s, got err: %v", lockPath, err)
		}
	}

	// 2. Process 2 attempts acquisition -> MUST fail with ErrCommitLockActive
	lock2, err := AcquireCommitLock(testDir)
	if err == nil {
		_ = lock2.Release()
		t.Fatal("expected second AcquireCommitLock to fail while lock1 is held, got nil error")
	}
	if !errors.Is(err, ErrCommitLockActive) {
		t.Fatalf("expected ErrCommitLockActive, got: %v", err)
	}

	// 3. Process 1 releases lock cleanly
	if err := lock1.Release(); err != nil {
		t.Fatalf("lock1.Release() failed: %v", err)
	}

	// On Linux/unix, verify the lock file was NOT deleted/unlinked
	if runtime.GOOS != "windows" {
		lockPath := filepath.Join(testDir, DefaultLockFileName)
		if _, err := os.Stat(lockPath); err != nil {
			t.Fatalf("lock file was unexpectedly removed after release: %v", err)
		}
	}

	// 4. Now process 2 (or 3) can acquire lock successfully
	lock3, err := AcquireCommitLock(testDir)
	if err != nil {
		t.Fatalf("AcquireCommitLock failed after release: %v", err)
	}
	defer lock3.Release()
}
