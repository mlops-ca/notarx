package ids

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
)

func TestNewReturnsUUIDv7(t *testing.T) {
	id, err := uuid.Parse(New())
	if err != nil {
		t.Fatalf("New() is not a UUID: %v", err)
	}
	if id.Version() != 7 {
		t.Fatalf("version = %d, want 7", id.Version())
	}
	if id.Variant() != uuid.RFC4122 {
		t.Fatalf("variant = %v, want RFC4122", id.Variant())
	}
}

func TestNewUUIDHasMonotonicTimestampPrefix(t *testing.T) {
	previous := NewUUID()
	for i := 0; i < 2000; i++ {
		next := NewUUID()
		// The first 48 bits are a millisecond timestamp and must never go back.
		if bytes.Compare(next[:6], previous[:6]) < 0 {
			t.Fatalf("timestamp prefix went backwards: %s after %s", next, previous)
		}
		previous = next
	}
}

func TestNewIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 5000; i++ {
		id := New()
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
}

func TestValidAcceptsOnlyCanonicalForm(t *testing.T) {
	for _, ok := range []string{New(), "11111111-1111-4111-8111-111111111111", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"} {
		if !Valid(ok) {
			t.Errorf("Valid(%q) = false", ok)
		}
	}
	for _, bad := range []string{
		"", "not-a-uuid", "12345", "11111111-1111-4111-8111",
		"urn:uuid:11111111-1111-4111-8111-111111111111",
		"{11111111-1111-4111-8111-111111111111}",
		"11111111111141118111111111111111",
		"11111111-1111-4111-8111-11111111111g",
		"' OR 1=1",
	} {
		if Valid(bad) {
			t.Errorf("Valid(%q) = true", bad)
		}
	}
}
