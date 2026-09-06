package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"cordbrief/internal/durable"
)

// DefaultAckFilename is the default filename for the core checkpoint cursor.
const DefaultAckFilename = "core-ack.json"

// Validate verifies that the cursor conforms to schema v1 constraints.
func (c *Cursor) Validate() error {
	if c == nil {
		return errors.New("cursor cannot be nil")
	}
	if c.Version != CurrentSchemaVersion {
		return fmt.Errorf("unsupported cursor version: %d (expected %d)", c.Version, CurrentSchemaVersion)
	}
	if c.Segment < 1 {
		return fmt.Errorf("invalid segment number: %d (must be >= 1)", c.Segment)
	}
	if c.Offset < 0 {
		return fmt.Errorf("invalid byte offset: %d (must be >= 0)", c.Offset)
	}
	return nil
}

// LoadCursor reads and validates the durable cursor from core-ack.json.
// If the file does not exist, it cleanly returns the initial cursor (Segment 1, Offset 0).
func LoadCursor(path string) (*Cursor, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// First run: begin from the start of segment 1
			return &Cursor{
				Version: CurrentSchemaVersion,
				Segment: 1,
				Offset:  0,
			}, nil
		}
		return nil, fmt.Errorf("open cursor file: %w", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()

	var c Cursor
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("strict decode cursor json: %w", err)
	}

	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("validate cursor: %w", err)
	}

	return &c, nil
}

// SaveCursor persists the cursor to core-ack.json using crash-resistant safe replacement
// via durable.AtomicWriteJSON (write temp, fsync, close, rename, parent dir fsync).
func SaveCursor(path string, c *Cursor) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("cannot save invalid cursor: %w", err)
	}
	return durable.AtomicWriteJSON(path, c, 0644)
}

