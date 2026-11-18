package config

import "testing"

func TestGroupVisibilityModeDefaultsToStrictAndRejectsUnknownValues(t *testing.T) {
	for value, want := range map[string]string{"": "strict", "strict": "strict", " lenient ": "lenient"} {
		t.Setenv("NOTARX_GROUP_VISIBILITY_MODE", value)
		got, err := loadGroupVisibilityMode()
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", value, got, err, want)
		}
	}
	for _, value := range []string{"off", "Strict", "true"} {
		t.Setenv("NOTARX_GROUP_VISIBILITY_MODE", value)
		if _, err := loadGroupVisibilityMode(); err == nil {
			t.Errorf("%q must be rejected", value)
		}
	}
}
