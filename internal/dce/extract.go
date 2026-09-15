package dce

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	MaxZipFileCount         = 500
	MaxZipUncompressedBytes = 300 * 1024 * 1024 // 300 MB
)

// ExpectedExecutableName returns the name of the DCE binary for the given OS.
func ExpectedExecutableName(goos string) string {
	if goos == "windows" {
		return "DiscordChatExporter.Cli.exe"
	}
	return "DiscordChatExporter.Cli"
}

// ExtractReleaseZip safely extracts a DCE release ZIP into destDir with zip-slip protection
// and resource limits. It verifies that the expected CLI executable is present before returning.
func ExtractReleaseZip(zipPath, destDir, goos string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}
	defer r.Close()

	if len(r.File) > MaxZipFileCount {
		return fmt.Errorf("archive contains too many files: %d (max %d)", len(r.File), MaxZipFileCount)
	}

	cleanDest := filepath.Clean(destDir)
	tmpDest := cleanDest + ".extract.tmp"
	_ = os.RemoveAll(tmpDest)
	if err := os.MkdirAll(tmpDest, 0700); err != nil {
		return fmt.Errorf("create temp extraction dir: %w", err)
	}

	cleanedUp := false
	defer func() {
		if !cleanedUp {
			_ = os.RemoveAll(tmpDest)
		}
	}()

	var totalBytes int64
	execName := ExpectedExecutableName(goos)
	hasExecutable := false

	for _, f := range r.File {
		// Zip-slip security checks
		if filepath.IsAbs(f.Name) || strings.HasPrefix(f.Name, "/") || strings.HasPrefix(f.Name, "\\") {
			return fmt.Errorf("archive contains invalid absolute path: %q", f.Name)
		}
		if strings.Contains(f.Name, "..") {
			return fmt.Errorf("archive contains directory traversal sequence: %q", f.Name)
		}

		targetPath := filepath.Join(tmpDest, f.Name)
		cleanTarget := filepath.Clean(targetPath)
		if !strings.HasPrefix(cleanTarget, tmpDest+string(filepath.Separator)) && cleanTarget != tmpDest {
			return fmt.Errorf("path traversal attempt detected: %q", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(cleanTarget, 0700); err != nil {
				return fmt.Errorf("create dir %q: %w", f.Name, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(cleanTarget), 0700); err != nil {
			return fmt.Errorf("create parent dir for %q: %w", f.Name, err)
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open file in zip %q: %w", f.Name, err)
		}

		mode := os.FileMode(0600)
		baseName := filepath.Base(f.Name)
		if baseName == execName {
			hasExecutable = true
			if goos != "windows" {
				mode = 0755
			}
		}

		outFile, err := os.OpenFile(cleanTarget, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			_ = rc.Close()
			return fmt.Errorf("create file %q: %w", f.Name, err)
		}

		remaining := MaxZipUncompressedBytes - totalBytes
		if remaining < 0 {
			remaining = 0
		}
		written, copyErr := io.Copy(outFile, io.LimitReader(rc, remaining+1))
		_ = rc.Close()
		_ = outFile.Close()

		if copyErr != nil {
			return fmt.Errorf("extract file %q: %w", f.Name, copyErr)
		}

		totalBytes += written
		if totalBytes > MaxZipUncompressedBytes {
			return fmt.Errorf("archive uncompressed size exceeded maximum allowed (%d bytes)", MaxZipUncompressedBytes)
		}
	}

	if !hasExecutable {
		return fmt.Errorf("extracted archive is missing required executable %q", execName)
	}

	// Verify the executable actually exists and is a regular file
	execPath := filepath.Join(tmpDest, execName)
	fi, err := os.Stat(execPath)
	if err != nil || fi.IsDir() {
		return fmt.Errorf("executable %q not found or is a directory", execName)
	}

	// Atomically move/rename temporary extraction directory to destDir
	_ = os.RemoveAll(cleanDest)
	if err := os.Rename(tmpDest, cleanDest); err != nil {
		return fmt.Errorf("commit extracted archive: %w", err)
	}
	cleanedUp = true

	return nil
}
