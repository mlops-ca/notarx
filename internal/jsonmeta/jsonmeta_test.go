package jsonmeta

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateObjectReplacesTheDroppedChecks(t *testing.T) {
	for _, ok := range []string{`{}`, ` {"a":1} `, `{"nested":{"x":[1,2]}}`} {
		if err := ValidateObject([]byte(ok), 0); err != nil {
			t.Errorf("ValidateObject(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "[]", "null", "1", `"x"`, "{", `{"a":}`, `{} {}`} {
		if err := ValidateObject([]byte(bad), 0); !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidateObject(%q) = %v, want ErrInvalid", bad, err)
		}
	}
	if err := ValidateObject([]byte(`{"a":"`+strings.Repeat("x", 100)+`"}`), 50); !errors.Is(err, ErrInvalid) {
		t.Errorf("an oversized object was accepted: %v", err)
	}
}

func TestValidateSize(t *testing.T) {
	if err := ValidateSize([]byte(`[1,2]`), 10); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSize([]byte(`[1,2]`), 3); !errors.Is(err, ErrInvalid) {
		t.Errorf("size limit not enforced: %v", err)
	}
	if err := ValidateSize([]byte(`[1,`), 10); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid JSON accepted: %v", err)
	}
}

func TestEqualIgnoresKeyOrderAndWhitespace(t *testing.T) {
	if !Equal([]byte(`{"b":2,"a":[1,true,null]}`), []byte(`{"a": [1, true, null], "b": 2}`)) {
		t.Error("the jsonb normalized form must equal the Go encoding")
	}
	for _, pair := range [][2]string{{`{"a":1}`, `{"a":2}`}, {`{"a":1}`, `{"a":1,"b":2}`}, {`{`, `{`}, {`{} {}`, `{}`}, {`[1]`, `[1,2]`}} {
		if Equal([]byte(pair[0]), []byte(pair[1])) {
			t.Errorf("Equal(%s, %s) = true", pair[0], pair[1])
		}
	}
}
