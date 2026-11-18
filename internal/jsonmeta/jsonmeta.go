// Package jsonmeta validates and compares the JSON metadata documents that format
// stores keep in jsonb columns (ADR 0023 section 9).
//
// PostgreSQL's jsonb normalizes key order and whitespace, so a stored document is
// semantically but not byte-wise equal to the encoding the Go code produced. The
// shape rules that used to live in CHECK constraints (jsonb_typeof(...) = 'object'
// and octet_length limits) are enforced here before a write.
package jsonmeta

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("invalid metadata document")

// ValidateObject accepts raw when it is one JSON object of at most maxBytes bytes. A
// maxBytes of zero or less means no size limit.
func ValidateObject(raw []byte, maxBytes int) error {
	if maxBytes > 0 && len(raw) > maxBytes {
		return fmt.Errorf("%w: %d bytes exceeds the %d byte limit", ErrInvalid, len(raw), maxBytes)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return fmt.Errorf("%w: not a JSON object", ErrInvalid)
	}
	return nil
}

// ValidateSize accepts raw when it is valid JSON of at most maxBytes bytes.
func ValidateSize(raw []byte, maxBytes int) error {
	if maxBytes > 0 && len(raw) > maxBytes {
		return fmt.Errorf("%w: %d bytes exceeds the %d byte limit", ErrInvalid, len(raw), maxBytes)
	}
	if !json.Valid(raw) {
		return fmt.Errorf("%w: not valid JSON", ErrInvalid)
	}
	return nil
}

// Equal reports whether a and b are the same JSON document, ignoring key order and
// whitespace. Invalid JSON is never equal.
func Equal(a, b []byte) bool {
	left, ok := decode(a)
	if !ok {
		return false
	}
	right, ok := decode(b)
	return ok && reflect.DeepEqual(left, right)
}

func decode(raw []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	if _, err := decoder.Token(); err == nil {
		return nil, false
	}
	return value, true
}
