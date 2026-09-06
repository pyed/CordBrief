package durable

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDurableAtomicWrite(t *testing.T) {
	t.Run("basic_atomic_write_file", func(t *testing.T) {
		tmpDir := t.TempDir()
		target := filepath.Join(tmpDir, "sub", "test.txt")
		data := []byte("hello durable world\n")

		if err := AtomicWriteFile(target, data, 0644); err != nil {
			t.Fatalf("AtomicWriteFile failed: %v", err)
		}

		read, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("ReadFile failed: %v", err)
		}
		if string(read) != string(data) {
			t.Fatalf("unexpected content: got %q, want %q", string(read), string(data))
		}
	})

	t.Run("basic_atomic_write_json", func(t *testing.T) {
		tmpDir := t.TempDir()
		target := filepath.Join(tmpDir, "config.json")
		val := map[string]string{"key": "value"}

		if err := AtomicWriteJSON(target, val, 0644); err != nil {
			t.Fatalf("AtomicWriteJSON failed: %v", err)
		}

		read, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("ReadFile failed: %v", err)
		}
		expected := "{\n  \"key\": \"value\"\n}\n"
		if string(read) != expected {
			t.Fatalf("unexpected json: got %q, want %q", string(read), expected)
		}
	})

	t.Run("exclusive_publication_and_concurrent_race", func(t *testing.T) {
		tmpDir := t.TempDir()
		target := filepath.Join(tmpDir, "exclusive.json")

		const numGoroutines = 10
		startGate := make(chan struct{})
		var wg sync.WaitGroup
		results := make([]error, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-startGate
				data := map[string]int{"writer": idx}
				results[idx] = AtomicWriteJSONExclusive(target, data, 0644)
			}(i)
		}

		close(startGate)
		wg.Wait()

		successCount := 0
		existCount := 0
		for _, err := range results {
			if err == nil {
				successCount++
			} else if errors.Is(err, os.ErrExist) || os.IsExist(err) {
				existCount++
			} else {
				t.Errorf("unexpected error: %v", err)
			}
		}

		if successCount != 1 {
			t.Fatalf("expected exactly 1 successful writer, got %d", successCount)
		}
		if existCount != numGoroutines-1 {
			t.Fatalf("expected %d ErrExist, got %d", numGoroutines-1, existCount)
		}

		// Ensure target file is completely valid and preserved
		if !fileIsValidJSON(target) {
			t.Fatalf("target file was corrupted during race")
		}
	})

	t.Run("existing_destination_cannot_be_replaced", func(t *testing.T) {
		tmpDir := t.TempDir()
		target := filepath.Join(tmpDir, "target.json")
		if err := AtomicWriteJSON(target, map[string]string{"orig": "value"}, 0644); err != nil {
			t.Fatalf("setup failed: %v", err)
		}
		err := AtomicWriteJSONExclusive(target, map[string]string{"new": "value"}, 0644)
		if err == nil || (!errors.Is(err, os.ErrExist) && !os.IsExist(err)) {
			t.Fatalf("expected ErrExist on existing destination, got: %v", err)
		}
		// Confirm original content unchanged
		data, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("read failed: %v", err)
		}
		if !strings.Contains(string(data), `"orig": "value"`) {
			t.Fatalf("original content was overwritten or modified: %s", string(data))
		}
	})

	t.Run("sync_dir_safety", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := SyncDir(tmpDir); err != nil {
			t.Fatalf("SyncDir failed on valid dir: %v", err)
		}
	})

	t.Run("never_partially_written_during_publication", func(t *testing.T) {
		tmpDir := t.TempDir()
		target := filepath.Join(tmpDir, "collector-command.json")

		payload := map[string]any{
			"version":    1,
			"command":    "enter_reauth",
			"request_id": "req-12345678-abcdef",
			"issued_at":  time.Now().UTC().Format(time.RFC3339Nano),
			"padding":    strings.Repeat("ABCDEFGHIJ1234567890", 5000), // ~100KB payload
		}

		stop := make(chan struct{})
		errChan := make(chan error, 10)

		// Concurrent publisher repeatedly writes and cleans up
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					err := AtomicWriteJSONExclusive(target, payload, 0644)
					if err == nil {
						time.Sleep(100 * time.Microsecond)
						_ = os.Remove(target)
					}
				}
			}
		}()

		// Multiple concurrent readers reading as fast as possible
		const numReaders = 6
		for r := 0; r < numReaders; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
						data, err := os.ReadFile(target)
						if err != nil {
							// State A: target absent (ErrNotExist) or momentarily in unlinked state
							if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
								continue
							}
							continue
						}
						// State B: target present -> MUST be complete, valid JSON matching payload
						if len(data) == 0 {
							select {
							case errChan <- errors.New("observed empty file during publication"):
							default:
							}
							return
						}
						var parsed map[string]any
						if err := json.Unmarshal(data, &parsed); err != nil {
							select {
							case errChan <- fmt.Errorf("observed partial/corrupted JSON during publication: %w", err):
							default:
							}
							return
						}
						if parsed["command"] != "enter_reauth" || parsed["request_id"] != "req-12345678-abcdef" {
							select {
							case errChan <- errors.New("observed corrupted payload fields"):
							default:
							}
							return
						}
					}
				}
			}()
		}

		time.Sleep(400 * time.Millisecond)
		close(stop)
		wg.Wait()

		select {
		case err := <-errChan:
			t.Fatalf("partial observation detected: %v", err)
		default:
			// Passed: reader observed ONLY absent or complete valid JSON
		}
	})
}

func fileIsValidJSON(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return len(data) > 0 && data[0] == '{' && data[len(data)-1] == '\n'
}
