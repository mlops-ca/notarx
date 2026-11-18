// Package ids creates identifiers for new rows.
//
// Identifiers are UUIDv7 (ADR 0023 section 4): the leading 48 bits are a
// millisecond timestamp, so inserts append to the right edge of btree and
// clustered primary keys instead of scattering across them as UUIDv4 does.
// Existing rows are not backfilled and nothing may depend on identifier order.
package ids

import "github.com/google/uuid"

// New returns a new UUIDv7 in its canonical string form.
func New() string {
	return NewUUID().String()
}

// NewUUID returns a new UUIDv7. It panics only when the operating system random
// source fails, which the standard library treats as unrecoverable as well.
func NewUUID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}
