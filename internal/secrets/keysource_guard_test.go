package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnvIsReadOnlyByEnvKeySource keeps the ring behind the KeySource port:
// no non-test file in the package other than keysource.go may read the
// process environment.
func TestEnvIsReadOnlyByEnvKeySource(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "keysource.go" {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "os.Getenv") || strings.Contains(string(body), "os.LookupEnv") {
			t.Errorf("%s reads the environment directly; the ring must come from a KeySource", name)
		}
	}
}
