package journal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCursor_DefaultAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	curPath := filepath.Join(tmpDir, "core-ack.json")

	// 1. Missing file returns default cursor (Segment 1, Offset 0)
	c, err := LoadCursor(curPath)
	if err != nil {
		t.Fatalf("unexpected error loading missing cursor: %v", err)
	}
	if c.Version != CurrentSchemaVersion || c.Segment != 1 || c.Offset != 0 {
		t.Fatalf("unexpected default cursor: %+v", c)
	}

	// 2. Save valid cursor and reload
	c.Segment = 5
	c.Offset = 1048576
	if err := SaveCursor(curPath, c); err != nil {
		t.Fatalf("failed to save cursor: %v", err)
	}

	loaded, err := LoadCursor(curPath)
	if err != nil {
		t.Fatalf("failed to reload saved cursor: %v", err)
	}
	if *loaded != *c {
		t.Fatalf("cursor mismatch: got %+v, want %+v", loaded, c)
	}
}

func TestCursor_Validation(t *testing.T) {
	tests := []struct {
		name    string
		cursor  *Cursor
		wantErr bool
	}{
		{"nil cursor", nil, true},
		{"unsupported version", &Cursor{Version: 2, Segment: 1, Offset: 0}, true},
		{"zero segment", &Cursor{Version: 1, Segment: 0, Offset: 0}, true},
		{"negative offset", &Cursor{Version: 1, Segment: 1, Offset: -1}, true},
		{"valid cursor", &Cursor{Version: 1, Segment: 1, Offset: 0}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cursor.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("cursor.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCursor_StrictJSON(t *testing.T) {
	tmpDir := t.TempDir()
	curPath := filepath.Join(tmpDir, "core-ack.json")

	// Unknown fields should be rejected
	data := `{"version": 1, "segment": 1, "offset": 0, "unknown_field": "slop"}`
	if err := os.WriteFile(curPath, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadCursor(curPath)
	if err == nil {
		t.Fatal("expected error on unknown field, got nil")
	}
}
