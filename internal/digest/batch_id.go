package digest

import (
	"errors"
	"regexp"
)

var (
	// batchIDRegex strictly matches 64 lowercase hexadecimal characters (SHA-256).
	batchIDRegex = regexp.MustCompile(`^[0-9a-f]{64}$`)

	// ErrInvalidBatchID is returned when a batch ID fails canonical format validation.
	ErrInvalidBatchID = errors.New("invalid batch ID: must be exactly 64 lowercase hexadecimal characters")
)

// ValidateBatchID verifies that id is strictly 64 lowercase hexadecimal characters.
func ValidateBatchID(id string) error {
	if !batchIDRegex.MatchString(id) {
		return ErrInvalidBatchID
	}
	return nil
}

// IsValidBatchID returns true if id is strictly 64 lowercase hexadecimal characters.
func IsValidBatchID(id string) bool {
	return batchIDRegex.MatchString(id)
}
