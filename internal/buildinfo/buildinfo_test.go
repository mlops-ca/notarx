package buildinfo

import "testing"

func TestCore(t *testing.T) {
	for version, want := range map[string]string{
		"0.3.0":               "0.3.0",
		"v0.3.0":              "0.3.0",
		"0.3.0-nightly.2":     "0.3.0",
		"0.0.0-dev":           "0.0.0",
		"0.0.0-dev-main.abc1": "0.0.0",
		"1.2.3+build.5":       "1.2.3",
		"10.20.30-rc.1+x":     "10.20.30",
		"dev":                 "",
		"":                    "",
		"1.2":                 "",
		"1.2.x":               "",
		"01.2.3":              "",
		"1..3":                "",
		"(devel)":             "",
	} {
		if got := Core(version); got != want {
			t.Errorf("Core(%q) = %q, want %q", version, got, want)
		}
	}
}

func TestInfoCoreUsesInjectedVersion(t *testing.T) {
	previous := injectedVersion
	t.Cleanup(func() { injectedVersion = previous })
	injectedVersion = "0.3.0-nightly.2"
	if got := Read().Core(); got != "0.3.0" {
		t.Fatalf("Read().Core() = %q, want 0.3.0", got)
	}
}
