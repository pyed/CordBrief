package journal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRetentionRejectsSymlinks(t *testing.T) {
	for _, target := range []string{"retention-manifest.json", "events/0000000000000001.ndjson"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			events := filepath.Join(root, "events")
			if err := os.Mkdir(events, 0700); err != nil {
				t.Fatal(err)
			}
			// Even a dangling manifest is corruption, not a missing first-run manifest.
			if err := os.Symlink(filepath.Join(root, "absent"), filepath.Join(root, target)); err != nil {
				t.Fatal(err)
			}
			if _, err := CaptureWatermark(events); err == nil {
				t.Fatal("accepted symlink")
			}
		})
	}
}
