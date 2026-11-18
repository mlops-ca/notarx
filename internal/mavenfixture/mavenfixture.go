// Package mavenfixture serves recorded Maven Central and Gradle plugin portal
// responses from a directory, so Maven and Gradle client tests and end-to-end
// scripts never depend on an external service (issue #333).
//
// A request path starts with the route of one upstream: "maven2/" is Maven
// Central and "gradle-plugins/" is the Gradle plugin portal. A path that was
// not recorded answers 404 and is logged with the re-record hint. In record
// mode a miss is fetched from the upstream, stored under the fixture directory
// and served; the "record-maven-fixtures" workflow does that (docs/ci.md).
package mavenfixture

import (
	"crypto/sha1" // #nosec G505 -- compares against the SHA-1 sidecars Maven Central publishes
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// RecordEnv switches the fixture to record mode when it is "1".
	RecordEnv = "MAVEN_CENTRAL_FIXTURE_RECORD"
	// DirEnv overrides the fixture directory.
	DirEnv = "MAVEN_CENTRAL_FIXTURE_DIR"
	// CentralRoute and GradlePluginsRoute are the first path segments of the two upstreams.
	CentralRoute       = "maven2"
	GradlePluginsRoute = "gradle-plugins"
)

// Upstreams maps a route to the base URL a miss is recorded from.
var Upstreams = map[string]string{
	CentralRoute:       "https://maven-central.storage-download.googleapis.com/maven2/",
	GradlePluginsRoute: "https://plugins.gradle.org/m2/",
}

// RecordMode reports whether the environment asks for recording.
func RecordMode() bool { return os.Getenv(RecordEnv) == "1" }

// DefaultDir is the recorded fixture directory next to this package, or the
// value of DirEnv.
func DefaultDir() string {
	if dir := os.Getenv(DirEnv); dir != "" {
		return dir
	}
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", "maven-central-fixture")
}

// Handler serves the fixture directory.
type Handler struct {
	Dir    string
	Record bool
	// Logf receives misses and recording problems; nil discards them.
	Logf   func(format string, args ...any)
	client *http.Client
	mu     sync.Mutex
}

// New returns a handler for dir. When record is true misses are recorded.
func New(dir string, record bool, logf func(string, ...any)) *Handler {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Handler{Dir: dir, Record: record, Logf: logf, client: &http.Client{Timeout: 2 * time.Minute}}
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "read only", http.StatusMethodNotAllowed)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/")
	route, rest, found := strings.Cut(rel, "/")
	upstream, known := Upstreams[route]
	if !found || !known || rest == "" || strings.Contains(rel, "..") {
		http.NotFound(w, r)
		return
	}
	file := filepath.Join(h.Dir, filepath.FromSlash(rel))
	if h.Record {
		h.mu.Lock()
		if _, err := os.Stat(file); err != nil {
			h.record(upstream+rest, file)
		}
		h.mu.Unlock()
	}
	body, err := os.ReadFile(file) // #nosec G304 -- rel has no parent segments and a known route
	if err != nil {
		h.Logf("Maven fixture miss %s: re-record with %s=1 (docs/ci.md)", r.URL.Path, RecordEnv)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

func (h *Handler) record(url, file string) {
	// Recording is a manual maintenance step, so it backs off when the upstream rate limits.
	var response *http.Response
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		response, err = h.client.Get(url) // #nosec G107 -- url is built from a fixed upstream and a cleaned path
		if err != nil || response.StatusCode != http.StatusTooManyRequests {
			break
		}
		_ = response.Body.Close()
		time.Sleep(3 * time.Second)
	}
	if err != nil {
		h.Logf("record %s: %v", url, err)
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		h.Logf("record %s: upstream answered %d", url, response.StatusCode)
		return
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		h.Logf("record %s: %v", url, err)
		return
	}
	if err = os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		h.Logf("record %s: %v", url, err)
		return
	}
	// Write atomically so a reader never sees a truncated fixture.
	temporary := file + ".recording"
	if err = os.WriteFile(temporary, body, 0o600); err == nil {
		err = os.Rename(temporary, file)
	}
	if err != nil {
		h.Logf("record %s: %v", url, err)
	}
}

// VerifyChecksums checks every recorded artifact against the SHA-1 sidecar
// published next to it and returns one message per mismatch.
func VerifyChecksums(dir string) ([]string, error) {
	var bad []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".sha1") {
			return err
		}
		want, err := os.ReadFile(path) // #nosec G304 -- walks the fixture directory
		if err != nil {
			return err
		}
		body, err := os.ReadFile(strings.TrimSuffix(path, ".sha1")) // #nosec G304 -- walks the fixture directory
		if errors.Is(err, fs.ErrNotExist) {
			return nil // the checksum was recorded without its artifact
		}
		if err != nil {
			return err
		}
		sum := sha1.Sum(body) // #nosec G401 -- integrity check of recorded bytes, not security
		fields := strings.Fields(string(want))
		if len(fields) == 0 || !strings.EqualFold(fields[0], hex.EncodeToString(sum[:])) {
			bad = append(bad, fmt.Sprintf("%s does not match its recorded checksum", strings.TrimSuffix(path, ".sha1")))
		}
		return nil
	})
	return bad, err
}
