package digest

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateDigest enforces strict structural and source grounding constraints on the LLM digest.
func ValidateDigest(d *Digest, b *Batch) error {
	if d == nil {
		return fmt.Errorf("digest cannot be nil")
	}

	d.Title = strings.TrimSpace(d.Title)
	if d.Title == "" {
		return fmt.Errorf("digest title cannot be empty")
	}

	d.Overview = strings.TrimSpace(d.Overview)
	if d.Overview == "" {
		return fmt.Errorf("digest overview cannot be empty")
	}

	if b == nil {
		return fmt.Errorf("batch context cannot be nil")
	}

	// Validate each item
	for i := range d.Items {
		item := &d.Items[i]
		item.Text = strings.TrimSpace(item.Text)
		if item.Text == "" {
			return fmt.Errorf("digest item [%d] has empty text", i)
		}

		item.Kind = strings.ToLower(strings.TrimSpace(item.Kind))
		if _, ok := ValidKinds[item.Kind]; !ok {
			return fmt.Errorf("digest item [%d] has invalid kind %q", i, item.Kind)
		}

		if len(item.SourceIDs) == 0 {
			return fmt.Errorf("digest item [%d] (%s: %q) has no source grounding (source_ids is empty)", i, item.Kind, item.Text)
		}

		// Deduplicate and validate each cited source ID
		seen := make(map[string]struct{})
		var validIDs []string
		for _, sID := range item.SourceIDs {
			sID = strings.TrimSpace(sID)
			if sID == "" {
				continue
			}
			if _, exists := b.SourceMap[sID]; !exists {
				return fmt.Errorf("digest item [%d] cites unknown source_id %q (not present in current batch)", i, sID)
			}
			if _, dup := seen[sID]; !dup {
				seen[sID] = struct{}{}
				validIDs = append(validIDs, sID)
			}
		}

		if len(validIDs) == 0 {
			return fmt.Errorf("digest item [%d] has no valid non-empty source IDs after normalization", i)
		}

		sort.Strings(validIDs)
		item.SourceIDs = validIDs
	}

	return nil
}
