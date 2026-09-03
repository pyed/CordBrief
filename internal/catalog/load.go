package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Load reads and validates catalog.json from the given exchange directory.
func Load(exchangeDir string) (*Catalog, error) {
	catPath := filepath.Join(exchangeDir, "catalog.json")
	data, err := os.ReadFile(catPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrCatalogNotFound
		}
		return nil, fmt.Errorf("reading %s: %w", catPath, err)
	}

	var cat Catalog
	if err := json.Unmarshal(data, &cat); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCatalog, err)
	}

	if cat.Version != 1 {
		return nil, fmt.Errorf("%w: got %d", ErrUnsupportedVersion, cat.Version)
	}

	return &cat, nil
}
