//go:build mavenclient

package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	mavenCentralFixtureDir     = "testdata/maven-central-fixture"
	mavenCentralRecordEnv      = "MAVEN_CENTRAL_FIXTURE_RECORD"
	mavenCentralRecordUpstream = "https://maven-central.storage-download.googleapis.com/maven2/"
)

// startMavenCentralFixture serves recorded Maven Central responses from
// testdata/maven-central-fixture/maven2 on a loopback server, so no test depends
// on an external service. A path that was not recorded answers 404.
//
// To re-record after a Maven or plugin version change, run the Maven client
// tests once with MAVEN_CENTRAL_FIXTURE_RECORD=1: misses are then fetched from
// Maven Central, stored under the fixture directory and served. Commit the result.
func startMavenCentralFixture(t *testing.T) *httptest.Server {
	t.Helper()
	record := os.Getenv(mavenCentralRecordEnv) == "1"
	var recording sync.Mutex
	client := &http.Client{Timeout: 2 * time.Minute}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "read only", http.StatusMethodNotAllowed)
			return
		}
		rel := strings.TrimPrefix(r.URL.Path, "/")
		if !strings.HasPrefix(rel, "maven2/") || strings.Contains(rel, "..") {
			http.NotFound(w, r)
			return
		}
		file := filepath.Join(mavenCentralFixtureDir, filepath.FromSlash(rel))
		if record {
			recording.Lock()
			if _, err := os.Stat(file); err != nil {
				recordMavenCentralFixture(t, client, strings.TrimPrefix(rel, "maven2/"), file)
			}
			recording.Unlock()
		}
		body, err := os.ReadFile(file)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func recordMavenCentralFixture(t *testing.T, client *http.Client, path, file string) {
	t.Helper()
	// Recording is a manual maintenance step, so it backs off when Central rate limits.
	var response *http.Response
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		response, err = client.Get(mavenCentralRecordUpstream + path)
		if err != nil || response.StatusCode != http.StatusTooManyRequests {
			break
		}
		_ = response.Body.Close()
		time.Sleep(3 * time.Second)
	}
	if err != nil {
		t.Errorf("record %s: %v", path, err)
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Logf("record %s: upstream answered %d", path, response.StatusCode)
		return
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Errorf("record %s: %v", path, err)
		return
	}
	if err = os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Error(err)
		return
	}
	if err = os.WriteFile(file, body, 0o644); err != nil {
		t.Error(err)
	}
}
