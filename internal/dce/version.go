package dce

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Version represents a parsed numeric release version (e.g., 2.48 or 2.48.1).
type Version struct {
	Major int
	Minor int
	Patch int
}

// ParseVersion parses a stable version string, optionally prefixed with "v".
// It requires exactly 2 or 3 non-negative integer components separated by dots (Major.Minor or Major.Minor.Patch).
func ParseVersion(s string) (Version, error) {
	clean := strings.TrimSpace(s)
	clean = strings.TrimPrefix(clean, "v")
	clean = strings.TrimPrefix(clean, "V")
	if clean == "" {
		return Version{}, errors.New("version string cannot be empty")
	}

	parts := strings.Split(clean, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return Version{}, fmt.Errorf("invalid version %q: expected Major.Minor or Major.Minor.Patch", s)
	}

	var nums [3]int
	for i, part := range parts {
		if part == "" {
			return Version{}, fmt.Errorf("invalid version %q: empty component", s)
		}
		val, err := strconv.Atoi(part)
		if err != nil || val < 0 {
			return Version{}, fmt.Errorf("invalid version %q: component %q is not a non-negative integer", s, part)
		}
		nums[i] = val
	}

	return Version{
		Major: nums[0],
		Minor: nums[1],
		Patch: nums[2],
	}, nil
}

// Compare returns -1 if v < other, 0 if v == other, and 1 if v > other.
func (v Version) Compare(other Version) int {
	if v.Major != other.Major {
		if v.Major < other.Major {
			return -1
		}
		return 1
	}
	if v.Minor != other.Minor {
		if v.Minor < other.Minor {
			return -1
		}
		return 1
	}
	if v.Patch != other.Patch {
		if v.Patch < other.Patch {
			return -1
		}
		return 1
	}
	return 0
}

// CompareVersions compares two Version structs.
func CompareVersions(a, b Version) int {
	return a.Compare(b)
}

// String returns the canonical dot-separated version representation.
func (v Version) String() string {
	if v.Patch > 0 {
		return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	}
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}
