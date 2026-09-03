package journal

import (
	"encoding/json"
	"fmt"
	"os"
)

// DefaultStatusFilename is the default filename for collector operational telemetry.
const DefaultStatusFilename = "collector-status.json"

// DefaultCatalogFilename is the default filename for discovered channel metadata.
const DefaultCatalogFilename = "catalog.json"

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
