package repository

import (
	"time"

	"github.com/google/uuid"
)

// Newest-first keyset cursors on (created_at, id), shared by the lifecycle job and webhook listings (ADR 0023
// section 8). The token is bound to the filter string of the query that issued it.

func encodeCreatedAtToken(filter string, createdAt time.Time, id string) string {
	return EncodeKeysetToken(filter, createdAt.UTC().Format(time.RFC3339Nano), id)
}

// decodeCreatedAtToken returns the (created_at, id) of the last row served; an empty token means the first page.
func decodeCreatedAtToken(token, filter string) (createdAt time.Time, id string, present bool, err error) {
	if token == "" {
		return time.Time{}, "", false, nil
	}
	keys, err := DecodeKeysetToken(token, filter, 2)
	if err != nil {
		return time.Time{}, "", false, err
	}
	createdAt, err = time.Parse(time.RFC3339Nano, keys[0])
	// The keyset rows are keyed by uuid; a token that carries anything else is refused here, the same
	// for every store, instead of failing in the database as an internal error.
	if _, parseErr := uuid.Parse(keys[1]); err != nil || parseErr != nil {
		return time.Time{}, "", false, ErrInvalidPageToken
	}
	return createdAt, keys[1], true, nil
}

// createdAtBefore reports whether (createdAt, id) sorts after the cursor in newest-first order, that is, is
// strictly older than the cursor row.
func createdAtBefore(createdAt time.Time, id string, cursorAt time.Time, cursorID string) bool {
	if createdAt.Equal(cursorAt) {
		return id < cursorID
	}
	return createdAt.Before(cursorAt)
}

// newerFirst orders (createdAt, id) pairs newest first, the order of every keyset listing here.
func newerFirst(createdAt time.Time, id string, otherAt time.Time, otherID string) bool {
	if createdAt.Equal(otherAt) {
		return id > otherID
	}
	return createdAt.After(otherAt)
}

// pageLimit applies the default and the maximum of a listing page size.
func pageLimit(limit, fallback, maximum int) int {
	if limit <= 0 || limit > maximum {
		return fallback
	}
	return limit
}

// decodeNameToken returns the unique name of the last row served by a listing ordered by name; an empty
// token means the first page.
func decodeNameToken(token, filter string) (name string, present bool, err error) {
	if token == "" {
		return "", false, nil
	}
	keys, err := DecodeKeysetToken(token, filter, 1)
	if err != nil {
		return "", false, err
	}
	return keys[0], true, nil
}
