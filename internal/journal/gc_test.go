package journal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRetentionNeverAuthorizesEvidenceLoss(t *testing.T) {
	dir := t.TempDir()
	// Invalid contents and a gap cannot grant eligibility: this reports names,
	// not a replacement for native correctness validation.
	for _, segment := range []uint64{1, 3, 4} {
		if err := os.WriteFile(filepath.Join(dir, FormatSegmentFilename(segment)), []byte("unchanged"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	report, err := AssessRetention(dir)
	if err != nil || len(report) != 3 {
		t.Fatalf("report=%v error=%v", report, err)
	}
	for _, row := range report {
		if row.Eligible || row.Reason == "" {
			t.Fatalf("unsafe assessment: %+v", row)
		}
		data, err := os.ReadFile(filepath.Join(dir, FormatSegmentFilename(row.Segment)))
		if err != nil || string(data) != "unchanged" {
			t.Fatal("assessment changed evidence", err)
		}
	}
}
