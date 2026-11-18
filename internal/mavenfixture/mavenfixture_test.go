package mavenfixture

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHandlerServesRecordedFilesOnly(t *testing.T) {
	dir := t.TempDir()
	recorded := filepath.Join(dir, "maven2", "g", "a.pom")
	if err := os.MkdirAll(filepath.Dir(recorded), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recorded, []byte("pom"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logged int
	server := httptest.NewServer(New(dir, false, func(string, ...any) { logged++ }))
	defer server.Close()
	for path, want := range map[string]int{
		"/maven2/g/a.pom":           http.StatusOK,
		"/maven2/g/missing.pom":     http.StatusNotFound,
		"/gradle-plugins/g/a.pom":   http.StatusNotFound,
		"/other/g/a.pom":            http.StatusNotFound,
		"/maven2/":                  http.StatusNotFound,
		"/maven2/../maven2/g/a.pom": http.StatusNotFound,
	} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != want {
			t.Errorf("%s=%d want %d", path, response.StatusCode, want)
		}
	}
	if logged != 2 {
		t.Errorf("logged %d misses, want 2", logged)
	}
	request, _ := http.NewRequest(http.MethodPut, server.URL+"/maven2/g/a.pom", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("PUT=%d", response.StatusCode)
	}
}

func TestRecordedFixtureMatchesRecordedChecksums(t *testing.T) {
	bad, err := VerifyChecksums(DefaultDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range bad {
		t.Error(message)
	}
}
