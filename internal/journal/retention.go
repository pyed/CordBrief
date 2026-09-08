package journal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Retention manifests certify identity evidence, never permission to consume
// missing transcripts. Core cursors must already be beyond the retired prefix.
type retentionManifest struct {
	Version        int                `json:"version"`
	RetiredThrough uint64             `json:"retired_through"`
	Segments       []retentionSegment `json:"segments"`
}
type retentionSegment struct {
	Segment       uint64 `json:"segment"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	SidecarSHA256 string `json:"sidecar_sha256"`
}
type retentionSidecar struct {
	Version int                 `json:"version"`
	Segment uint64              `json:"segment"`
	Size    int64               `json:"size"`
	SHA256  string              `json:"sha256"`
	Records []retentionIdentity `json:"records"`
}
type retentionIdentity struct {
	MessageID  string `json:"message_id"`
	ChannelID  string `json:"channel_id"`
	Offset     *int64 `json:"offset"`
	NextOffset *int64 `json:"next_offset"`
}

func strictRetentionJSON(data []byte, target any) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	required := []string{"version", "retired_through", "segments"}
	childKey := "segments"
	childRequired := []string{"segment", "size", "sha256", "sidecar_sha256"}
	if _, ok := target.(*retentionSidecar); ok {
		required = []string{"version", "segment", "size", "sha256", "records"}
		childKey = "records"
		childRequired = []string{"message_id", "channel_id", "offset", "next_offset"}
	}
	check := func(object map[string]json.RawMessage, fields []string) error {
		for _, field := range fields {
			if len(object[field]) == 0 || bytes.Equal(bytes.TrimSpace(object[field]), []byte("null")) {
				return fmt.Errorf("missing retention field %s", field)
			}
		}
		return nil
	}
	if err := check(object, required); err != nil {
		return err
	}
	var children []map[string]json.RawMessage
	if err := json.Unmarshal(object[childKey], &children); err != nil {
		return err
	}
	for _, child := range children {
		if err := check(child, childRequired); err != nil {
			return err
		}
	}
	// Reject duplicate keys as well as unknown fields: different readers must
	// never interpret one correctness document differently.
	d := json.NewDecoder(bytes.NewReader(data))
	var value func() error
	value = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := t.(json.Delim); ok {
			keys := map[string]bool{}
			for d.More() {
				if delim == '{' {
					k, err := d.Token()
					if err != nil {
						return err
					}
					key := k.(string)
					if keys[key] {
						return fmt.Errorf("duplicate retention key %q", key)
					}
					keys[key] = true
				}
				if err := value(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing retention JSON")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(target)
}

func retentionHash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func validRetentionID(id string) bool {
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == id
}

func readRetentionFile(file string) ([]byte, error) {
	info, err := os.Lstat(file)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("non-regular retention file")
	}
	return os.ReadFile(file)
}

// validateRetention verifies every certified segment, including completeness
// against source bytes whenever those bytes still exist. After removal the
// atomically published sidecar hash is the durable completeness authority.
func validateRetention(eventsDir string, physical []uint64) (uint64, error) {
	entries, err := os.ReadDir(eventsDir)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".ndjson") {
			if _, err := ParseSegmentNumber(entry.Name()); err != nil || entry.IsDir() {
				return 0, fmt.Errorf("invalid journal segment entry")
			}
		}
	}
	for _, segment := range physical {
		if segment > 9007199254740991 {
			return 0, fmt.Errorf("journal segment exceeds collector integer range")
		}
	}
	data, err := readRetentionFile(filepath.Join(filepath.Dir(eventsDir), "retention-manifest.json"))
	if os.IsNotExist(err) {
		for i, s := range physical {
			if s != uint64(i+1) {
				return 0, fmt.Errorf("uncertified journal gap before %d", s)
			}
		}
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var m retentionManifest
	if err := strictRetentionJSON(data, &m); err != nil {
		return 0, fmt.Errorf("retention manifest: %w", err)
	}
	if m.Version != 1 || m.RetiredThrough == 0 || m.RetiredThrough != uint64(len(m.Segments)) {
		return 0, fmt.Errorf("invalid retention coverage")
	}
	if len(physical) == 0 || physical[0] > m.RetiredThrough+1 || physical[len(physical)-1] <= m.RetiredThrough {
		return 0, fmt.Errorf("missing retained journal suffix")
	}
	for i := 1; i < len(physical); i++ {
		if physical[i] != physical[i-1]+1 {
			return 0, fmt.Errorf("journal has interior gap")
		}
	}
	seen := map[string]bool{}
	info, err := os.Lstat(filepath.Join(filepath.Dir(eventsDir), "retention"))
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("invalid retention directory")
	}
	for i, s := range m.Segments {
		if s.Segment != uint64(i+1) || s.Size < 0 || s.Size > 9007199254740991 || len(s.SHA256) != 64 {
			return 0, fmt.Errorf("invalid retired segment metadata")
		}
		if h, err := hex.DecodeString(s.SHA256); err != nil || hex.EncodeToString(h) != s.SHA256 {
			return 0, fmt.Errorf("invalid retired source hash")
		}
		body, err := readRetentionFile(filepath.Join(filepath.Dir(eventsDir), "retention", fmt.Sprintf("%016d.ids.json", s.Segment)))
		if err != nil {
			return 0, err
		}
		if retentionHash(body) != s.SidecarSHA256 {
			return 0, fmt.Errorf("retention sidecar hash mismatch")
		}
		var c retentionSidecar
		if err := strictRetentionJSON(body, &c); err != nil {
			return 0, err
		}
		if c.Version != 1 || c.Segment != s.Segment || c.Size != s.Size || c.SHA256 != s.SHA256 {
			return 0, fmt.Errorf("retention sidecar metadata mismatch")
		}
		var end int64
		for _, r := range c.Records {
			if !validRetentionID(r.MessageID) || !validRetentionID(r.ChannelID) || seen[r.MessageID] || r.Offset == nil || r.NextOffset == nil || *r.Offset != end || *r.NextOffset <= end || *r.NextOffset > s.Size {
				return 0, fmt.Errorf("incomplete or invalid retention identities")
			}
			seen[r.MessageID] = true
			end = *r.NextOffset
		}
		if end != s.Size {
			return 0, fmt.Errorf("incomplete retention byte coverage")
		}
		raw, err := readRetentionFile(filepath.Join(eventsDir, FormatSegmentFilename(s.Segment)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if int64(len(raw)) != s.Size || retentionHash(raw) != s.SHA256 {
			return 0, fmt.Errorf("retired transcript changed")
		}
		for _, r := range c.Records {
			line := raw[*r.Offset:*r.NextOffset]
			var e Event
			if line[len(line)-1] != '\n' || bytes.Count(line, []byte{'\n'}) != 1 || json.Unmarshal(line, &e) != nil || e.Version != CurrentSchemaVersion || e.Event != "message_create" || e.MessageID != r.MessageID || e.ChannelID != r.ChannelID {
				return 0, fmt.Errorf("retention identity differs from source")
			}
		}
	}
	return m.RetiredThrough, nil
}
