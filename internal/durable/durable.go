package durable

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// SyncDir flushes directory metadata to disk on systems that support it (POSIX/Linux).
// On Windows or platforms/filesystems where directory syncing is unsupported (EINVAL, ENOTSUP, EISDIR), it returns nil.
// Genuine I/O errors (such as EIO) are returned.
func SyncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()

	if err := d.Sync(); err != nil {
		if isUnsupportedDirSyncErr(err) {
			// Ignore unsupported operations on specific filesystems
			return nil
		}
		return err
	}
	return nil
}

func isUnsupportedDirSyncErr(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.EINVAL, syscall.ENOTSUP, syscall.EISDIR:
			return true
		}
	}
	return false
}

// AtomicWriteFile writes data to dest atomically:
// 1. Writes to an unpublished collision-safe temp file in the same directory (via os.CreateTemp)
// 2. Flushes file bytes to disk (Sync)
// 3. Closes the file descriptor
// 4. Replaces the destination file atomically (os.Rename)
// 5. Flushes parent directory metadata (SyncDir)
// 6. Guarantees temp file cleanup on failure
func AtomicWriteFile(dest string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".durable-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if perm != 0 {
		_ = os.Chmod(tmpName, perm)
	}

	if err := renameWithRetry(tmpName, dest); err != nil {
		return fmt.Errorf("atomic rename to destination: %w", err)
	}

	cleanup = false
	if err := SyncDir(dir); err != nil {
		return fmt.Errorf("sync directory metadata: %w", err)
	}
	return nil
}

// renameWithRetry handles Windows transient file sharing locks during atomic replacement.
func renameWithRetry(src, dst string) error {
	var err error
	for i := 0; i < 5; i++ {
		err = os.Rename(src, dst)
		if err == nil {
			return nil
		}
		if runtime.GOOS != "windows" {
			return err
		}
		// On Windows, retry briefly if file is momentarily locked by another process or AV
		time.Sleep(time.Duration(10*(i+1)) * time.Millisecond)
	}
	return err
}

// AtomicWriteFileExclusive writes data to dest atomically ONLY IF dest does not already exist.
//
// INVARIANTS:
// - dest is NEVER opened or created for direct writing: a reader can observe ONLY
//   (A) dest absent, or (B) complete, fsynced, valid payload. dest is never observable as partial content.
// - Linux Production:
//   1. Full payload written to collision-safe temp file (.durable-excl-*) in same directory.
//   2. Temp file fsynced (Sync) and closed.
//   3. Temp file atomically linked to dest via os.Link (link(2)).
//      - If dest already exists, returns os.ErrExist.
//      - If link fails for any other reason, FAILS CLOSED immediately (no direct-to-final fallback).
//   4. Parent directory metadata fsynced (SyncDir).
//   5. Temp file unlinked.
// - Windows Development:
//   Uses os.Link (CreateHardLinkW) to atomically link to the already-closed, fully-written temp file.
//   If dest already exists, returns os.ErrExist. If linking fails, FAILS CLOSED immediately.
//   dest is never opened for direct writing.
func AtomicWriteFileExclusive(dest string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".durable-excl-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if perm != 0 {
		_ = os.Chmod(tmpName, perm)
	}

	// Atomic create-if-absent publication via hardlink
	if err := os.Link(tmpName, dest); err != nil {
		if os.IsExist(err) || errors.Is(err, os.ErrExist) {
			return os.ErrExist
		}
		return fmt.Errorf("atomic link exclusive publication: %w", err)
	}

	cleanup = false
	_ = os.Remove(tmpName)
	if err := SyncDir(dir); err != nil {
		return fmt.Errorf("sync directory metadata: %w", err)
	}
	return nil
}

// AtomicWriteJSON marshals v with 2-space indentation and writes to dest using AtomicWriteFile.
func AtomicWriteJSON(dest string, v any, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	data = append(data, '\n')
	return AtomicWriteFile(dest, data, perm)
}

// AtomicWriteJSONExclusive marshals v and writes to dest using AtomicWriteFileExclusive.
func AtomicWriteJSONExclusive(dest string, v any, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	data = append(data, '\n')
	return AtomicWriteFileExclusive(dest, data, perm)
}
