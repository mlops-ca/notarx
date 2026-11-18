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
