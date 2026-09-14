package dce

import "github.com/pyed/CordBrief/internal/state"

// CompareSnowflake compares two positive decimal numeric strings without integer conversion.
// Returns -1 if a < b, 1 if a > b, or 0 if a == b.
// If either string is not a valid decimal string, falls back to lexicographic comparison.
func CompareSnowflake(a, b string) int {
	if a == b {
		return 0
	}
	if !state.IsDecimalString(a) || !state.IsDecimalString(b) {
		if a < b {
			return -1
		}
		return 1
	}
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	if a < b {
		return -1
	}
	return 1
}
