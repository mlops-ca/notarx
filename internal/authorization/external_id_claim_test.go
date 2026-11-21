package authorization

import (
	"encoding/json"
	"testing"
)

func TestExternalIDClaimReadsStringsAndNumbers(t *testing.T) {
	for name, test := range map[string]struct {
		value any
		want  string
	}{
		"string":       {" E-100 ", "E-100"},
		"integer":      {float64(12345), "12345"},
		"large number": {float64(9007199254740991), "9007199254740991"},
		"decimal":      {1.5, "1.5"},
		"json number":  {json.Number("42"), "42"},
		"bool":         {true, ""},
		"list":         {[]any{"a"}, ""},
		"object":       {map[string]any{"a": 1}, ""},
		"nil":          {nil, ""},
	} {
		if got := externalIDClaim(test.value); got != test.want {
			t.Errorf("%s: externalIDClaim(%v) = %q, want %q", name, test.value, got, test.want)
		}
	}
}
