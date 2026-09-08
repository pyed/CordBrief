package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func retentionFixture(t *testing.T, includeRaw bool, mutate func(*retentionManifest, *retentionSidecar)) string {
	t.Helper()
	dir := t.TempDir()
	events := filepath.Join(dir, "events")
	if err := os.MkdirAll(events, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "retention"), 0755); err != nil {
		t.Fatal(err)
	}
	raw := []byte("{\"version\":1,\"event\":\"message_create\",\"message_id\":\"100\",\"channel_id\":\"200\"}\n")
	z, n := int64(0), int64(len(raw))
	c := retentionSidecar{Version: 1, Segment: 1, Size: n, SHA256: retentionHash(raw), Records: []retentionIdentity{{MessageID: "100", ChannelID: "200", Offset: &z, NextOffset: &n}}}
	m := retentionManifest{Version: 1, RetiredThrough: 1, Segments: []retentionSegment{{Segment: 1, Size: n, SHA256: c.SHA256}}}
	if mutate != nil {
		mutate(&m, &c)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	m.Segments[0].SidecarSHA256 = retentionHash(b)
	write := func(p string, b []byte) {
		t.Helper()
		if err := os.WriteFile(p, b, 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "retention", "0000000000000001.ids.json"), b)
	b, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "retention-manifest.json"), b)
	if includeRaw {
		write(filepath.Join(events, FormatSegmentFilename(1)), raw)
	}
	write(filepath.Join(events, FormatSegmentFilename(2)), []byte("{\"version\":1,\"message_id\":\"101\",\"channel_id\":\"200\"}\n"))
	return events
}

func TestRetentionCertifiedPrefix(t *testing.T) {
	for _, raw := range []bool{true, false} {
		dir := retentionFixture(t, raw, nil)
		wm, err := CaptureWatermark(dir)
		if err != nil {
			t.Fatal(err)
		}
		if wm.RetiredThrough != 1 || len(wm.Segments) != 1 || wm.Segments[0] != 2 {
			t.Fatalf("bad logical suffix: %+v", wm)
		}
		r := NewReader(dir, wm)
		if _, _, err := r.ReadBatch(Cursor{Version: 1, Segment: 1}, 100); err == nil {
			t.Fatal("retired cursor silently skipped")
		}
		recs, _, err := r.ReadBatch(Cursor{Version: 1, Segment: 2}, 100)
		if err != nil || len(recs) != 1 || recs[0].Event.MessageID != "101" {
			t.Fatalf("suffix read: %v %v", recs, err)
		}
	}
}

func TestRetentionCorruption(t *testing.T) {
	cases := map[string]func(*retentionManifest, *retentionSidecar){
		"coverage":        func(m *retentionManifest, c *retentionSidecar) { m.RetiredThrough = 2 },
		"empty":           func(m *retentionManifest, c *retentionSidecar) { c.Records = nil },
		"missing offset":  func(m *retentionManifest, c *retentionSidecar) { c.Records[0].Offset = nil },
		"gap":             func(m *retentionManifest, c *retentionSidecar) { n := int64(1); c.Records[0].Offset = &n },
		"short":           func(m *retentionManifest, c *retentionSidecar) { n := c.Size - 1; c.Records[0].NextOffset = &n },
		"wrong identity":  func(m *retentionManifest, c *retentionSidecar) { c.Records[0].MessageID = "999" },
		"wrong channel":   func(m *retentionManifest, c *retentionSidecar) { c.Records[0].ChannelID = "999" },
		"noncanonical ID": func(m *retentionManifest, c *retentionSidecar) { c.Records[0].MessageID = "0100" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			dir := retentionFixture(t, true, mutate)
			if _, err := CaptureWatermark(dir); err == nil {
				t.Fatal("accepted corrupt evidence")
			}
		})
	}
	for _, body := range []string{`{"version":1,"version":1}`, `null`, `{} {}`} {
		dir := retentionFixture(t, false, nil)
		if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "retention-manifest.json"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := CaptureWatermark(dir); err == nil {
			t.Fatal("accepted malformed manifest")
		}
	}
	t.Run("hash", func(t *testing.T) {
		dir := retentionFixture(t, false, nil)
		p := filepath.Join(filepath.Dir(dir), "retention", "0000000000000001.ids.json")
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString(" ")
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CaptureWatermark(dir); err == nil {
			t.Fatal("accepted changed sidecar")
		}
	})
}

func TestRetentionUncertifiedTopology(t *testing.T) {
	t.Run("invalid segment name", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "bad.ndjson"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := CaptureWatermark(dir); err == nil {
			t.Fatal("ignored malformed journal segment name")
		}
	})
	for _, segments := range [][]uint64{{2}, {1, 3}} {
		dir := t.TempDir()
		for _, s := range segments {
			if err := os.WriteFile(filepath.Join(dir, FormatSegmentFilename(s)), nil, 0644); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := CaptureWatermark(dir); err == nil {
			t.Fatal("accepted uncertified gap")
		}
	}
	// A certified retired prefix does not authorize an interior retained gap.
	dir := retentionFixture(t, false, nil)
	if err := os.WriteFile(filepath.Join(dir, FormatSegmentFilename(4)), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureWatermark(dir); err == nil {
		t.Fatal("accepted retained hole")
	}
}
