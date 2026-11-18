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
	"math/big"
	"reflect"
	"strings"
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
	if document, ok := decode(trimmed); !ok || containsNUL(document) {
		return fmt.Errorf("%w: a string holds U+0000, which jsonb cannot store", ErrInvalid)
	}
	return nil
}

// ValidateSize accepts raw when it is valid JSON of at most maxBytes bytes.
func ValidateSize(raw []byte, maxBytes int) error {
	if maxBytes > 0 && len(raw) > maxBytes {
		return fmt.Errorf("%w: %d bytes exceeds the %d byte limit", ErrInvalid, len(raw), maxBytes)
	}
	document, ok := decode(raw)
	if !ok {
		return fmt.Errorf("%w: not valid JSON", ErrInvalid)
	}
	if containsNUL(document) {
		return fmt.Errorf("%w: a string holds U+0000, which jsonb cannot store", ErrInvalid)
	}
	return nil
}

// Equal reports whether a and b are the same JSON document, ignoring key order,
// whitespace and the spelling of numbers (jsonb stores 1e2 as 100). Invalid JSON is
// never equal.
func Equal(a, b []byte) bool {
	left, ok := decode(a)
	if !ok {
		return false
	}
	right, ok := decode(b)
	return ok && equalValue(left, right)
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

func equalValue(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for key, value := range x {
			other, present := y[key]
			if !present || !equalValue(value, other) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equalValue(x[i], y[i]) {
				return false
			}
		}
		return true
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		left, okLeft := new(big.Rat).SetString(x.String())
		right, okRight := new(big.Rat).SetString(y.String())
		return okLeft && okRight && left.Cmp(right) == 0
	default:
		return reflect.DeepEqual(a, b)
	}
}

// containsNUL reports whether any string or key in the decoded document holds U+0000,
// which jsonb rejects.
func containsNUL(value any) bool {
	switch x := value.(type) {
	case string:
		return strings.ContainsRune(x, 0)
	case map[string]any:
		for key, item := range x {
			if strings.ContainsRune(key, 0) || containsNUL(item) {
				return true
			}
		}
	case []any:
		for _, item := range x {
			if containsNUL(item) {
				return true
			}
		}
	}
	return false
}
