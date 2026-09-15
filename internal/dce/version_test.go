package dce_test

import (
	"testing"

	"github.com/pyed/CordBrief/internal/dce"
)

func TestVersion(t *testing.T) {
	t.Run("comparison ordering", func(t *testing.T) {
		tests := []struct {
			a, b     string
			expected int // -1 for a < b, 0 for a == b, 1 for a > b
		}{
			{"2.9", "2.10", -1},
			{"2.10", "2.9", 1},
			{"2.48", "2.48.1", -1},
			{"2.48.1", "2.48", 1},
			{"2.48.1", "2.49", -1},
			{"2.49", "2.48.1", 1},
			{"2.48", "2.48.0", 0},
			{"v2.48", "2.48", 0},
			{"V2.48.1", "2.48.1", 0},
			{"3.0", "2.99.99", 1},
			{"1.0.0", "1.0.1", -1},
			{"2.48.2", "2.48.2", 0},
		}

		for _, tc := range tests {
			va, err := dce.ParseVersion(tc.a)
			if err != nil {
				t.Fatalf("failed to parse version %q: %v", tc.a, err)
			}
			vb, err := dce.ParseVersion(tc.b)
			if err != nil {
				t.Fatalf("failed to parse version %q: %v", tc.b, err)
			}

			cmp := va.Compare(vb)
			if cmp != tc.expected {
				t.Errorf("Compare(%q, %q) = %d, want %d", tc.a, tc.b, cmp, tc.expected)
			}
		}
	})

	t.Run("invalid version strings fail closed", func(t *testing.T) {
		invalids := []string{
			"",
			"   ",
			"v",
			"2",
			"v2",
			"foo",
			"2.48.1.5",
			"2.-1",
			"2.48.a",
			"2..48",
			".2.48",
			"2.48.",
		}

		for _, inv := range invalids {
			_, err := dce.ParseVersion(inv)
			if err == nil {
				t.Errorf("expected error for invalid version %q, got nil", inv)
			}
		}
	})

	t.Run("string formatting", func(t *testing.T) {
		v1, _ := dce.ParseVersion("2.48")
		if v1.String() != "2.48" {
			t.Errorf("expected %q, got %q", "2.48", v1.String())
		}

		v2, _ := dce.ParseVersion("v2.48.1")
		if v2.String() != "2.48.1" {
			t.Errorf("expected %q, got %q", "2.48.1", v2.String())
		}
	})
}
