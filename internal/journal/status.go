package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cordbrief/internal/durable"
)

// ErrCommandPending is returned when attempting to write a new collector command while a previous one is still pending.
var ErrCommandPending = errors.New("command already pending: a previous collector command has not been consumed yet")

// DefaultStatusFilename is the default filename for collector operational telemetry.
const DefaultStatusFilename = "collector-status.json"

// DefaultCatalogFilename is the default filename for discovered channel metadata.
const DefaultCatalogFilename = "catalog.json"

// DefaultCommandFilename is the exchange file for Core-to-Collector commands.
const DefaultCommandFilename = "collector-command.json"

// DefaultCommandAckFilename is the exchange file for Collector command acknowledgements.
const DefaultCommandAckFilename = "collector-command-ack.json"

// IsFresh returns true if the status record was updated within the specified max age.
func (s *CollectorStatus) IsFresh(now time.Time, maxAge time.Duration) bool {
	if s == nil || s.UpdatedAt.IsZero() {
		return false
	}
	if now.Before(s.UpdatedAt) {
		// Clock skew tolerance within 5 seconds
		return s.UpdatedAt.Sub(now) <= 5*time.Second
	}
	return now.Sub(s.UpdatedAt) <= maxAge
}

// ReadCollectorStatus strictly parses collector-status.json from disk.
func ReadCollectorStatus(path string) (*CollectorStatus, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open collector status file: %w", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()

	var s CollectorStatus
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("strict decode collector status: %w", err)
	}

	if s.Version != CurrentSchemaVersion {
		return nil, fmt.Errorf("unsupported collector status version %d (expected %d)", s.Version, CurrentSchemaVersion)
	}

	return &s, nil
}

// WriteCollectorCommand writes an atomic command file in exchange directory.
// Uses durable.AtomicWriteJSONExclusive to guarantee:
// - Write to unpublished temp file, fsync, and close
// - Atomic create-if-absent publication via os.Link (fails if command already pending)
// - Zero TOCTOU window between concurrent writers
// - Directory fsync on parent directory
// - Guaranteed cleanup of temp file
func WriteCollectorCommand(exchangeDir string, cmd CollectorCommand) error {
	if cmd.Version == 0 {
		cmd.Version = CurrentSchemaVersion
	}
	if cmd.RequestID == "" {
		return fmt.Errorf("collector command requires non-empty request_id")
	}
	if cmd.Command == "" {
		return fmt.Errorf("collector command requires non-empty command")
	}
	if cmd.RequestedAt.IsZero() {
		cmd.RequestedAt = time.Now().UTC()
	}

	targetPath := filepath.Join(exchangeDir, DefaultCommandFilename)
	if err := durable.AtomicWriteJSONExclusive(targetPath, cmd, 0644); err != nil {
		if errors.Is(err, os.ErrExist) || os.IsExist(err) {
			return ErrCommandPending
		}
		return fmt.Errorf("write collector command: %w", err)
	}

	return nil
}

// ReadCollectorCommandAck strictly parses collector-command-ack.json from exchange directory.
func ReadCollectorCommandAck(exchangeDir string) (*CollectorCommandAck, error) {
	path := filepath.Join(exchangeDir, DefaultCommandAckFilename)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open collector command ack: %w", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()

	var ack CollectorCommandAck
	if err := dec.Decode(&ack); err != nil {
		return nil, fmt.Errorf("strict decode collector command ack: %w", err)
	}

	if ack.Version != CurrentSchemaVersion {
		return nil, fmt.Errorf("unsupported command ack version %d (expected %d)", ack.Version, CurrentSchemaVersion)
	}

	return &ack, nil
}

// ReadCatalog strictly parses catalog.json from disk.
func ReadCatalog(path string) (*Catalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open catalog file: %w", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()

	var c Catalog
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("strict decode catalog: %w", err)
	}

	if c.Version != CurrentSchemaVersion {
		return nil, fmt.Errorf("unsupported catalog version %d (expected %d)", c.Version, CurrentSchemaVersion)
	}

	return &c, nil
}
