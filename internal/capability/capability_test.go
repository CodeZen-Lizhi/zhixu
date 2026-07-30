package capability

import (
	"errors"
	"testing"
)

func TestAllReturnsUniqueCanonicalCapabilitiesAndIndependentCopies(t *testing.T) {
	expected := []Capability{
		"READ_LOCAL",
		"READ_EXTERNAL",
		"WRITE_PROPOSAL",
		"WRITE_KNOWLEDGE",
		"GIT_WRITE",
		"INDEX_MAINTENANCE",
		"EVALUATION_RUN",
		"MANAGE_SYSTEM_SETTINGS",
	}
	first := All()
	if len(first) != len(expected) {
		t.Fatalf("capability count = %d, want %d", len(first), len(expected))
	}
	seen := make(map[Capability]struct{}, len(first))
	for i, value := range first {
		if value != expected[i] {
			t.Fatalf("capability[%d] = %q, want %q", i, value, expected[i])
		}
		if _, exists := seen[value]; exists {
			t.Fatalf("duplicate capability %q", value)
		}
		seen[value] = struct{}{}
		if err := Validate(value); err != nil {
			t.Fatalf("Validate(%q) = %v", value, err)
		}
		if !IsKnown(value) {
			t.Fatalf("IsKnown(%q) = false", value)
		}
	}

	first[0] = "MUTATED"
	if again := All(); again[0] != ReadLocal {
		t.Fatalf("All shared mutable storage: %#v", again)
	}
}

func TestParseTrimsCanonicalValuesAndRejectsUnknownOrLegacyValues(t *testing.T) {
	parsed, err := Parse(" \tINDEX_MAINTENANCE\n")
	if err != nil || parsed != IndexMaintenance {
		t.Fatalf("Parse() = %q, %v", parsed, err)
	}
	for _, value := range []string{"", "UNKNOWN", "ADMIN_MAINTENANCE"} {
		if _, err := Parse(value); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Parse(%q) error = %v, want ErrInvalid", value, err)
		}
	}
	if err := Validate(" READ_LOCAL "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate(non-canonical) = %v, want ErrInvalid", err)
	}
	if IsKnown("ADMIN_MAINTENANCE") {
		t.Fatal("IsKnown accepted legacy capability")
	}
}
