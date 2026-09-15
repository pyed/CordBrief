package dce_test

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyed/CordBrief/internal/dce"
)

func createTestZip(t *testing.T, files map[string]string) string {
	t.Helper()
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("failed to create zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("failed to write zip content %s: %v", name, err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatalf("failed to close zip writer: %v", err)
	}

	tmpFile, err := os.CreateTemp(t.TempDir(), "test-*.zip")
	if err != nil {
		t.Fatalf("failed to create temp zip file: %v", err)
	}
	defer tmpFile.Close()

	if _, err := tmpFile.Write(buf.Bytes()); err != nil {
		t.Fatalf("failed to write temp zip file: %v", err)
	}

	return tmpFile.Name()
}

func TestExtract(t *testing.T) {
	t.Run("successful extraction of valid multi-file archive", func(t *testing.T) {
		zipPath := createTestZip(t, map[string]string{
			"DiscordChatExporter.Cli.exe":  "mock-executable-bytes",
			"DiscordChatExporter.Core.dll": "core-dll-bytes",
			"sub/helper.txt":               "helper content",
		})

		destDir := filepath.Join(t.TempDir(), "installed")
		if err := dce.ExtractReleaseZip(zipPath, destDir, "windows"); err != nil {
			t.Fatalf("extraction failed: %v", err)
		}

		// Verify files exist
		exeBytes, err := os.ReadFile(filepath.Join(destDir, "DiscordChatExporter.Cli.exe"))
		if err != nil || string(exeBytes) != "mock-executable-bytes" {
			t.Fatalf("executable missing or corrupt: %v", err)
		}

		dllBytes, err := os.ReadFile(filepath.Join(destDir, "DiscordChatExporter.Core.dll"))
		if err != nil || string(dllBytes) != "core-dll-bytes" {
			t.Fatalf("dll missing or corrupt: %v", err)
		}
	})

	t.Run("zip-slip traversal rejected", func(t *testing.T) {
		zipPath := createTestZip(t, map[string]string{
			"DiscordChatExporter.Cli.exe": "mock",
			"../evil.txt":                 "malicious",
		})

		destDir := filepath.Join(t.TempDir(), "installed")
		err := dce.ExtractReleaseZip(zipPath, destDir, "windows")
		if err == nil {
			t.Fatal("expected error on zip-slip traversal, got nil")
		}
		if !strings.Contains(err.Error(), "traversal") {
			t.Fatalf("unexpected error message: %v", err)
		}

		// Ensure final directory was not created
		if _, err := os.Stat(destDir); !os.IsNotExist(err) {
			t.Fatal("destDir should not exist after failed extraction")
		}
	})

	t.Run("missing executable rejected", func(t *testing.T) {
		zipPath := createTestZip(t, map[string]string{
			"SomeOtherFile.exe": "mock",
		})

		destDir := filepath.Join(t.TempDir(), "installed")
		err := dce.ExtractReleaseZip(zipPath, destDir, "windows")
		if err == nil {
			t.Fatal("expected error on missing executable, got nil")
		}
		if !strings.Contains(err.Error(), "missing required executable") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("unix executable permissions", func(t *testing.T) {
		zipPath := createTestZip(t, map[string]string{
			"DiscordChatExporter.Cli": "mock-unix-binary",
			"readme.txt":              "hello",
		})

		destDir := filepath.Join(t.TempDir(), "installed")
		if err := dce.ExtractReleaseZip(zipPath, destDir, "linux"); err != nil {
			t.Fatalf("extraction failed: %v", err)
		}

		fi, err := os.Stat(filepath.Join(destDir, "DiscordChatExporter.Cli"))
		if err != nil {
			t.Fatalf("stat executable failed: %v", err)
		}
		if fi.Mode().Perm()&0111 == 0 && os.Getenv("OS") != "Windows_NT" {
			t.Fatalf("expected executable permission on unix: %v", fi.Mode())
		}
	})
}
