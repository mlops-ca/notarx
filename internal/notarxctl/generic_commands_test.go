package notarxctl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

// genericGateway is a fake versioned Raw repository: create-only files, versions that seal into a canonical manifest,
// and the same refusals the Gateway makes. Its knobs corrupt what it serves, to prove the client verifies.
type genericGateway struct {
	server *httptest.Server
	mu     sync.Mutex

	layout   string
	files    map[string]map[string]string // name@version -> relative path -> content
	state    map[string]string
	props    map[string]map[string]string
	manifest map[string]string
	sealed   map[string]time.Time
	sealBody []map[string]any
	puts     int
	failPut  map[string]int

	// corrupt serves other bytes for a path; tamperManifest alters the served manifest; pathEscape lists a file whose
	// path leaves the destination.
	corrupt        map[string]string
	tamperManifest bool
	extraListed    []map[string]any
}

func newGenericGateway(t *testing.T) *genericGateway {
	t.Helper()
	isolatedCLI(t)
	gateway := &genericGateway{layout: "versioned", files: map[string]map[string]string{}, state: map[string]string{}, props: map[string]map[string]string{}, manifest: map[string]string{}, sealed: map[string]time.Time{}, failPut: map[string]int{}, corrupt: map[string]string{}}
	gateway.server = httptest.NewServer(http.HandlerFunc(gateway.serve))
	t.Cleanup(gateway.server.Close)
	t.Setenv("NOTARXCTL_SERVER", gateway.server.URL)
	t.Setenv("NOTARXCTL_TOKEN", repoTestToken)
	return gateway
}

func hexSHA(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func (g *genericGateway) assets(key string) []repository.RawAsset {
	name, version, _ := strings.Cut(key, "@")
	var assets []repository.RawAsset
	for path, content := range g.files[key] {
		assets = append(assets, repository.RawAsset{Path: name + "/" + version + "/" + path, Digest: "sha256:" + hexSHA(content), Size: int64(len(content)), ContentType: "application/octet-stream"})
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Path < assets[j].Path })
	return assets
}

func (g *genericGateway) versionJSON(key string, withFiles bool) map[string]any {
	name, version, _ := strings.Cut(key, "@")
	body := map[string]any{"name": name, "version": version, "state": g.state[key], "fileCount": len(g.files[key]), "sizeBytes": 0, "properties": g.props[key], "createdAt": "2026-11-20T09:00:00Z", "lastWriteAt": "2026-11-20T09:00:00Z"}
	if body["properties"] == nil {
		body["properties"] = map[string]string{}
	}
	if g.state[key] == "sealed" {
		body["manifestDigest"] = g.manifest[key]
		body["sealedAt"] = g.sealed[key].UTC().Format(time.RFC3339)
	}
	if withFiles {
		files := []map[string]any{}
		for _, asset := range g.assets(key) {
			files = append(files, map[string]any{"path": strings.TrimPrefix(asset.Path, name+"/"+version+"/"), "sha256": strings.TrimPrefix(asset.Digest, "sha256:"), "size": asset.Size})
		}
		for _, extra := range g.extraListed {
			files = append(files, extra)
		}
		body["files"] = files
	}
	return body
}

func (g *genericGateway) manifestBytes(key string) []byte {
	name, version, _ := strings.Cut(key, "@")
	manifest, _ := repository.RawVersionManifest(name, version, g.assets(key), g.props[key])
	return manifest
}

func (g *genericGateway) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	repoBase := "/api/v2/repositories/" + testRepoID
	switch {
	case r.URL.Path == repoBase:
		writeJSON(w, http.StatusOK, map[string]any{"id": testRepoID, "name": "releases", "format": "raw", "type": "hosted", "state": "active", "version": "1", "visibility": "private", "mavenStrictPublication": false, "layout": g.layout})
	case strings.HasPrefix(r.URL.Path, "/raw/releases/"):
		g.serveRaw(w, r, strings.TrimPrefix(r.URL.Path, "/raw/releases/"), body)
	case r.URL.Path == repoBase+"/packages":
		g.listPackages(w)
	case strings.HasPrefix(r.URL.Path, repoBase+"/packages/"):
		g.serveVersions(w, r, strings.TrimPrefix(r.URL.Path, repoBase+"/packages/"), body)
	default:
		http.NotFound(w, r)
	}
}

func (g *genericGateway) listPackages(w http.ResponseWriter) {
	names := map[string]int{}
	latest := map[string]string{}
	for key := range g.files {
		name, version, _ := strings.Cut(key, "@")
		names[name]++
		if g.state[key] == "sealed" {
			latest[name] = version
		}
	}
	items := []map[string]any{}
	for name, count := range names {
		item := map[string]any{"name": name, "versionCount": count}
		if latest[name] != "" {
			item["latestVersion"] = latest[name]
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["name"].(string) < items[j]["name"].(string) })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (g *genericGateway) serveRaw(w http.ResponseWriter, r *http.Request, rest string, body []byte) {
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 3 {
		writeProblem(w, http.StatusUnprocessableEntity, "invalid_generic_path", "not name/version/file")
		return
	}
	key, path := parts[0]+"@"+parts[1], parts[2]
	switch r.Method {
	case http.MethodPut:
		g.puts++
		if g.failPut[path] > 0 {
			g.failPut[path]--
			writeProblem(w, http.StatusServiceUnavailable, "unavailable", "try again")
			return
		}
		if g.files[key] == nil {
			g.files[key], g.state[key] = map[string]string{}, "open"
		}
		digest := hexSHA(string(body))
		if r.Header.Get("Digest") == "" {
			writeProblem(w, http.StatusBadRequest, "invalid_request", "Digest header required")
			return
		}
		if existing, ok := g.files[key][path]; ok {
			if existing == string(body) {
				w.Header().Set("ETag", `"sha256:`+digest+`"`)
				w.WriteHeader(http.StatusOK)
				return
			}
			writeProblem(w, http.StatusConflict, "version_file_exists", "version file already exists with different content")
			return
		}
		if g.state[key] == "sealed" {
			writeProblem(w, http.StatusConflict, "version_sealed", "version is sealed")
			return
		}
		g.files[key][path] = string(body)
		w.Header().Set("ETag", `"sha256:`+digest+`"`)
		w.WriteHeader(http.StatusCreated)
	case http.MethodHead:
		content, ok := g.files[key][path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", `"sha256-`+hexSHA(content)+`"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		content, ok := g.files[key][path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if other, tampered := g.corrupt[path]; tampered {
			content = other
		}
		_, _ = io.WriteString(w, content)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (g *genericGateway) serveVersions(w http.ResponseWriter, r *http.Request, rest string, body []byte) {
	name, rest, _ := strings.Cut(rest, "/versions")
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		g.listVersions(w, r, name)
		return
	}
	version, action, _ := strings.Cut(rest, "/")
	if strings.HasSuffix(version, ":seal") {
		version, action = strings.TrimSuffix(version, ":seal"), "seal"
	}
	key := name + "@" + version
	if _, ok := g.files[key]; !ok && r.Method == http.MethodPut && action == "" {
		g.files[key], g.state[key] = map[string]string{}, "open"
	}
	if _, ok := g.files[key]; !ok {
		writeProblem(w, http.StatusNotFound, "not_found", "version not found")
		return
	}
	switch {
	case r.Method == http.MethodPut && action == "":
		var request struct{ Properties map[string]string }
		_ = json.Unmarshal(body, &request)
		if g.state[key] == "sealed" {
			// A create that repeats a sealed version is idempotent; one that would change it is refused.
			if len(request.Properties) != len(g.props[key]) {
				writeProblem(w, http.StatusConflict, "version_sealed", "version is sealed")
				return
			}
			for name, value := range request.Properties {
				if g.props[key][name] != value {
					writeProblem(w, http.StatusConflict, "version_sealed", "version is sealed")
					return
				}
			}
			writeJSON(w, http.StatusOK, g.versionJSON(key, false))
			return
		}
		g.props[key] = request.Properties
		writeJSON(w, http.StatusOK, g.versionJSON(key, false))
	case r.Method == http.MethodPut && action == "properties":
		var request struct{ Properties map[string]string }
		_ = json.Unmarshal(body, &request)
		g.props[key] = request.Properties
		writeJSON(w, http.StatusOK, g.versionJSON(key, false))
	case r.Method == http.MethodPost && action == "seal":
		g.seal(w, key, body)
	case r.Method == http.MethodGet && action == "":
		writeJSON(w, http.StatusOK, g.versionJSON(key, true))
	case r.Method == http.MethodGet && action == "manifest":
		manifest := g.manifestBytes(key)
		if g.tamperManifest {
			manifest = append([]byte(nil), manifest...)
			manifest[len(manifest)-3] = 'X'
		}
		_, _ = w.Write(manifest)
	case r.Method == http.MethodGet && action == "checksums":
		name, version, _ := strings.Cut(key, "@")
		_, _ = io.WriteString(w, repository.RawVersionChecksums(name, version, g.assets(key)))
	default:
		http.NotFound(w, r)
	}
}

func (g *genericGateway) listVersions(w http.ResponseWriter, r *http.Request, name string) {
	items := []map[string]any{}
	for key := range g.files {
		if strings.HasPrefix(key, name+"@") && (r.URL.Query().Get("state") == "" || r.URL.Query().Get("state") == g.state[key]) {
			items = append(items, g.versionJSON(key, false))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["version"].(string) < items[j]["version"].(string) })
	g.sealBody = append(g.sealBody, map[string]any{"listQuery": r.URL.RawQuery})
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (g *genericGateway) seal(w http.ResponseWriter, key string, body []byte) {
	var request map[string]any
	_ = json.Unmarshal(body, &request)
	g.sealBody = append(g.sealBody, request)
	if g.state[key] == "sealed" {
		writeJSON(w, http.StatusOK, g.versionJSON(key, true))
		return
	}
	if len(g.files[key]) == 0 {
		writeProblem(w, http.StatusUnprocessableEntity, "version_empty", "a version without files cannot be sealed")
		return
	}
	if expected, ok := request["files"].([]any); ok {
		if len(expected) != len(g.files[key]) {
			writeProblem(w, http.StatusUnprocessableEntity, "checksum_mismatch", "the stored files do not match the expected files")
			return
		}
		for _, entry := range expected {
			file := entry.(map[string]any)
			if content, ok := g.files[key][file["path"].(string)]; !ok || hexSHA(content) != file["sha256"] {
				writeProblem(w, http.StatusUnprocessableEntity, "checksum_mismatch", "the stored files do not match the expected files")
				return
			}
		}
	}
	if sums, ok := request["checksumsFile"].(string); ok {
		for _, line := range strings.Split(strings.TrimSpace(g.files[key][sums]), "\n") {
			sum, file, _ := strings.Cut(line, "  ")
			if content, ok := g.files[key][file]; !ok || hexSHA(content) != sum {
				writeProblem(w, http.StatusUnprocessableEntity, "checksum_mismatch", "the checksums file does not match the stored files")
				return
			}
		}
	}
	name, version, _ := strings.Cut(key, "@")
	_, g.manifest[key] = repository.RawVersionManifest(name, version, g.assets(key), g.props[key])
	g.state[key], g.sealed[key] = "sealed", time.Now()
	writeJSON(w, http.StatusOK, g.versionJSON(key, true))
}

func pushArgs(dir string, extra ...string) []string {
	return append([]string{"generic", "push", dir, "--repo", testRepoID, "--name", "tool", "--version", "1.0.0", "--json"}, extra...)
}

func decodePush(t *testing.T, stdout string) (statuses map[string]string, data genericPushResult) {
	t.Helper()
	envelope := decodeEnvelope(t, stdout)
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("push result: %v\n%s", err, stdout)
	}
	statuses = map[string]string{}
	for _, object := range data.Objects {
		statuses[object.Path] = object.Status
	}
	return statuses, data
}

func TestGenericPushUploadsAndSealsWithTheExpectedFileList(t *testing.T) {
	gateway := newGenericGateway(t)
	dir := writeTree(t, map[string]string{"bin/tool-linux": "linux", "README": "read me"})
	code, stdout, stderr := runCLI(t, pushArgs(dir, "--property", "git.sha=abc123", "--property", "channel=stable")...)
	if code != ExitOK {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	statuses, result := decodePush(t, stdout)
	if statuses["tool/1.0.0/README"] != "uploaded" || statuses["tool/1.0.0/bin/tool-linux"] != "uploaded" || result.State != "sealed" || !strings.HasPrefix(result.ManifestDigest, "sha256:") {
		t.Fatalf("result: %s", stdout)
	}
	if gateway.props["tool@1.0.0"]["git.sha"] != "abc123" || gateway.props["tool@1.0.0"]["channel"] != "stable" {
		t.Fatalf("properties: %v", gateway.props)
	}
	seal := gateway.sealBody[len(gateway.sealBody)-1]
	files, _ := seal["files"].([]any)
	if len(files) != 2 || seal["checksumsFile"] != nil {
		t.Fatalf("seal body: %v", seal)
	}
	// A retry of a push that already finished is safe, properties included: the same bytes are unchanged and the seal
	// answers the same digest. Another value for a property of a sealed version is refused as a conflict.
	if code, stdout, _ = runCLI(t, pushArgs(dir, "--property", "git.sha=different", "--property", "channel=stable")...); code != ExitConflict {
		t.Fatalf("a changed property of a sealed version: exit=%d %s", code, stdout)
	}
	code, stdout, _ = runCLI(t, pushArgs(dir, "--property", "git.sha=abc123", "--property", "channel=stable")...)
	statuses, again := decodePush(t, stdout)
	if code != ExitOK || statuses["tool/1.0.0/README"] != "unchanged" || again.ManifestDigest != result.ManifestDigest {
		t.Fatalf("replay: exit=%d %s", code, stdout)
	}
}

func TestGenericPushConflictExitsSevenAndNeverSeals(t *testing.T) {
	gateway := newGenericGateway(t)
	dir := writeTree(t, map[string]string{"a.txt": "alpha", "b.txt": "bravo"})
	if code, stdout, _ := runCLI(t, pushArgs(dir, "--no-seal")...); code != ExitOK || !strings.Contains(stdout, `"state": "open"`) {
		t.Fatalf("first push exit=%d %s", code, stdout)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("changed after publication"), 0o644); err != nil {
		t.Fatal(err)
	}
	sealsBefore := len(gateway.sealBody)
	code, stdout, stderr := runCLI(t, pushArgs(dir)...)
	if code != ExitConflict {
		t.Fatalf("exit=%d, want %d\n%s\n%s", code, ExitConflict, stdout, stderr)
	}
	envelope := decodeEnvelope(t, stdout)
	if envelope.Error == nil || envelope.Error.Code != "version_file_exists" {
		t.Fatalf("error: %s", stdout)
	}
	statuses, _ := decodePush(t, stdout)
	if statuses["tool/1.0.0/a.txt"] != "unchanged" || statuses["tool/1.0.0/b.txt"] != "conflict" {
		t.Fatalf("statuses: %v", statuses)
	}
	if gateway.files["tool@1.0.0"]["b.txt"] != "bravo" || len(gateway.sealBody) != sealsBefore || gateway.state["tool@1.0.0"] != "open" {
		t.Fatal("a conflicting push must neither change the stored file nor seal the version")
	}
}

func TestGenericPushPartialFailureExitsThreeAndRetries(t *testing.T) {
	gateway := newGenericGateway(t)
	gateway.failPut["flaky.txt"] = 100
	dir := writeTree(t, map[string]string{"ok.txt": "fine", "flaky.txt": "unreliable"})
	code, stdout, _ := runCLI(t, pushArgs(dir, "--parallel", "1")...)
	if code != ExitPartial {
		t.Fatalf("exit=%d\n%s", code, stdout)
	}
	statuses, _ := decodePush(t, stdout)
	if statuses["tool/1.0.0/ok.txt"] != "uploaded" || (statuses["tool/1.0.0/flaky.txt"] != "failed" && statuses["tool/1.0.0/flaky.txt"] != "unknown") || gateway.state["tool@1.0.0"] != "open" {
		t.Fatalf("statuses=%v state=%s", statuses, gateway.state["tool@1.0.0"])
	}
	gateway.failPut["flaky.txt"] = 0
	if code, stdout, _ = runCLI(t, pushArgs(dir)...); code != ExitOK || gateway.state["tool@1.0.0"] != "sealed" {
		t.Fatalf("retry exit=%d %s", code, stdout)
	}
}

func TestGenericPushChecksumsRoundTrip(t *testing.T) {
	gateway := newGenericGateway(t)
	dir := writeTree(t, map[string]string{"bin/tool": "binary", "docs/guide.md": "guide"})
	code, stdout, stderr := runCLI(t, pushArgs(dir, "--checksums")...)
	if code != ExitOK {
		t.Fatalf("exit=%d %s %s", code, stdout, stderr)
	}
	seal := gateway.sealBody[len(gateway.sealBody)-1]
	if seal["checksumsFile"] != "SHA256SUMS" {
		t.Fatalf("the seal must name the checksums file: %v", seal)
	}
	if files, _ := seal["files"].([]any); len(files) != 3 {
		t.Fatalf("expected list includes SHA256SUMS: %v", seal)
	}
	stored := gateway.files["tool@1.0.0"]["SHA256SUMS"]
	want := hexSHA("binary") + "  bin/tool\n" + hexSHA("guide") + "  docs/guide.md\n"
	if stored != want {
		t.Fatalf("SHA256SUMS:\n%s\nwant:\n%s", stored, want)
	}
	// The printed checksums verify the pulled tree the way sha256sum -c does, and the pull verifies the manifest.
	out := t.TempDir()
	code, stdout, stderr = runCLI(t, "generic", "pull", "--repo", testRepoID, "--name", "tool", "--version", "1.0.0", "--to", out, "--json")
	if code != ExitOK {
		t.Fatalf("pull exit=%d %s %s", code, stdout, stderr)
	}
	var pulled genericPullResult
	if err := json.Unmarshal(decodeEnvelope(t, stdout).Data, &pulled); err != nil || !pulled.ManifestVerified || len(pulled.Files) != 3 {
		t.Fatalf("pull result: %s %v", stdout, err)
	}
	code, stdout, _ = runCLI(t, "generic", "show", "--repo", testRepoID, "--name", "tool", "--version", "1.0.0", "--checksums")
	if code != ExitOK {
		t.Fatalf("show --checksums exit=%d", code)
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		sum, file, _ := strings.Cut(line, "  ")
		content, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(file)))
		if err != nil || hexSHA(string(content)) != sum {
			t.Fatalf("sha256sum -c would fail for %q: %v", file, err)
		}
	}
	// A file named SHA256SUMS of its own conflicts with --checksums before anything is sent.
	own := writeTree(t, map[string]string{"SHA256SUMS": "mine"})
	if code, _, _ = runCLI(t, pushArgs(own, "--checksums")...); code != ExitUsage {
		t.Fatalf("own checksums file with --checksums: exit=%d", code)
	}
}

func TestGenericPushChecksumsMismatchIsRejectedAtSeal(t *testing.T) {
	gateway := newGenericGateway(t)
	dir := writeTree(t, map[string]string{"a": "alpha"})
	// Another publisher left a file in the open version, so the expected list no longer matches the stored set.
	gateway.files["tool@1.0.0"] = map[string]string{"stray": "unexpected"}
	gateway.state["tool@1.0.0"] = "open"
	code, stdout, _ := runCLI(t, pushArgs(dir, "--checksums")...)
	if code != ExitFailure {
		t.Fatalf("exit=%d\n%s", code, stdout)
	}
	envelope := decodeEnvelope(t, stdout)
	if envelope.Error == nil || envelope.Error.Code != "checksum_mismatch" || gateway.state["tool@1.0.0"] != "open" {
		t.Fatalf("seal failure: %s", stdout)
	}
	if !strings.Contains(stdout, "uploaded") {
		t.Fatalf("the failure still reports what was uploaded: %s", stdout)
	}
}

func TestGenericPushDryRunSendsNothingAndPathRepositoriesAreRefused(t *testing.T) {
	gateway := newGenericGateway(t)
	dir := writeTree(t, map[string]string{"a": "alpha"})
	code, stdout, _ := runCLI(t, append(pushArgs(dir), "--dry-run")...)
	statuses, _ := decodePush(t, stdout)
	if code != ExitOK || statuses["tool/1.0.0/a"] != "planned" || gateway.puts != 0 || len(gateway.files) != 0 {
		t.Fatalf("dry run: exit=%d puts=%d %s", code, gateway.puts, stdout)
	}
	gateway.layout = "path"
	code, stdout, _ = runCLI(t, pushArgs(dir)...)
	envelope := decodeEnvelope(t, stdout)
	if code != ExitUsage || envelope.Error == nil || !strings.Contains(envelope.Error.Hint, "publish raw") || gateway.puts != 0 {
		t.Fatalf("path repository: exit=%d %s", code, stdout)
	}
}

func TestPublishRawPointsAtGenericPushForAVersionedRepository(t *testing.T) {
	gateway := newRawGateway(t)
	// The same fake repository, but its capabilities report the versioned layout.
	inner := gateway.server.Config.Handler
	gateway.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/repositories/"+testRepoID+"/capabilities" {
			writeJSON(w, 200, map[string]any{"format": "raw", "type": "hosted", "operations": []string{"read", "publish"}, "layout": "versioned", "versions": true, "strict": false})
			return
		}
		inner.ServeHTTP(w, r)
	})
	dir := writeTree(t, map[string]string{"a.txt": "alpha"})
	code, stdout, _ := runCLI(t, "publish", "raw", dir, "--repo", testRepoID, "--json")
	envelope := decodeEnvelope(t, stdout)
	if code != ExitUsage || envelope.Error == nil || !strings.Contains(envelope.Error.Hint, "generic push") || len(gateway.puts) != 0 {
		t.Fatalf("exit=%d puts=%v %s", code, gateway.puts, stdout)
	}
}

func TestGenericPushRejectsInvalidInputBeforeAnyRequest(t *testing.T) {
	gateway := newGenericGateway(t)
	dir := writeTree(t, map[string]string{"a": "alpha"})
	for name, args := range map[string][]string{
		"bad name":         {"generic", "push", dir, "--repo", testRepoID, "--name", "Tool", "--version", "1"},
		"bad version":      {"generic", "push", dir, "--repo", testRepoID, "--name", "tool", "--version", "-1"},
		"missing version":  {"generic", "push", dir, "--repo", testRepoID, "--name", "tool"},
		"bad property":     {"generic", "push", dir, "--repo", testRepoID, "--name", "tool", "--version", "1", "--property", "Bad Key=x"},
		"duplicate key":    {"generic", "push", dir, "--repo", testRepoID, "--name", "tool", "--version", "1", "--property", "a=1", "--property", "a=2"},
		"parallel":         {"generic", "push", dir, "--repo", testRepoID, "--name", "tool", "--version", "1", "--parallel", "99"},
		"reserved dir":     {"generic", "push", writeTree(t, map[string]string{".notarx/x": "y"}), "--repo", testRepoID, "--name", "tool", "--version", "1"},
		"chunk without -r": {"generic", "push", dir, "--repo", testRepoID, "--name", "tool", "--version", "1", "--chunk-size", "10"},
	} {
		if code, stdout, _ := runCLI(t, append(args, "--json")...); code != ExitUsage {
			t.Errorf("%s: exit=%d %s", name, code, stdout)
		}
	}
	if gateway.puts != 0 {
		t.Fatal("invalid input must not reach the server")
	}
}

func sealedFixture(t *testing.T) *genericGateway {
	t.Helper()
	gateway := newGenericGateway(t)
	dir := writeTree(t, map[string]string{"a.txt": "alpha", "sub/b.txt": "bravo"})
	if code, stdout, _ := runCLI(t, pushArgs(dir)...); code != ExitOK {
		t.Fatalf("push exit=%d %s", code, stdout)
	}
	return gateway
}

func pullArgs(out string, extra ...string) []string {
	return append([]string{"generic", "pull", "--repo", testRepoID, "--name", "tool", "--version", "1.0.0", "--to", out, "--json"}, extra...)
}

func TestGenericPullDownloadsAndVerifiesTheManifest(t *testing.T) {
	sealedFixture(t)
	out := t.TempDir()
	code, stdout, stderr := runCLI(t, pullArgs(out)...)
	if code != ExitOK {
		t.Fatalf("exit=%d %s %s", code, stdout, stderr)
	}
	for path, content := range map[string]string{"a.txt": "alpha", "sub/b.txt": "bravo"} {
		if got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(path))); err != nil || string(got) != content {
			t.Fatalf("%s = %q %v", path, got, err)
		}
	}
	var result genericPullResult
	_ = json.Unmarshal(decodeEnvelope(t, stdout).Data, &result)
	if !result.ManifestVerified || !strings.HasPrefix(result.ManifestDigest, "sha256:") {
		t.Fatalf("manifest not verified: %s", stdout)
	}
	// A second pull finds the verified files in place.
	code, stdout, _ = runCLI(t, pullArgs(out)...)
	if code != ExitOK || strings.Count(stdout, `"unchanged"`) != 2 {
		t.Fatalf("second pull exit=%d %s", code, stdout)
	}
	// --file selects files; an unknown file is a usage error.
	one := t.TempDir()
	if code, stdout, _ = runCLI(t, pullArgs(one, "--file", "sub/b.txt")...); code != ExitOK {
		t.Fatalf("--file exit=%d %s", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(one, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("--file must download only the named file")
	}
	if code, _, _ = runCLI(t, pullArgs(one, "--file", "nope")...); code != ExitUsage {
		t.Fatalf("unknown file exit=%d", code)
	}
}

func TestGenericPullVerificationFailureDiscardsTheBytesAndExitsOne(t *testing.T) {
	gateway := sealedFixture(t)
	gateway.corrupt["sub/b.txt"] = "bravo, but tampered"
	out := t.TempDir()
	code, stdout, _ := runCLI(t, pullArgs(out)...)
	if code != ExitFailure {
		t.Fatalf("exit=%d, want %d\n%s", code, ExitFailure, stdout)
	}
	envelope := decodeEnvelope(t, stdout)
	if envelope.Error == nil || envelope.Error.Code != "verification_failed" {
		t.Fatalf("error: %s", stdout)
	}
	if _, err := os.Stat(filepath.Join(out, "sub", "b.txt")); !os.IsNotExist(err) {
		t.Fatal("a file that failed verification must not be left behind")
	}
	if got, err := os.ReadFile(filepath.Join(out, "a.txt")); err != nil || string(got) != "alpha" {
		t.Fatalf("a verified file is kept: %q %v", got, err)
	}
	entries, _ := os.ReadDir(filepath.Join(out, "sub"))
	for _, entry := range entries {
		t.Errorf("temporary file left behind: %s", entry.Name())
	}
}

func TestGenericPullRefusesAManifestThatDoesNotHashToTheDigest(t *testing.T) {
	gateway := sealedFixture(t)
	gateway.tamperManifest = true
	code, stdout, _ := runCLI(t, pullArgs(t.TempDir())...)
	envelope := decodeEnvelope(t, stdout)
	if code != ExitFailure || envelope.Error == nil || envelope.Error.Code != "verification_failed" {
		t.Fatalf("exit=%d %s", code, stdout)
	}
}

func TestGenericPullRefusesPathsThatLeaveTheDestination(t *testing.T) {
	gateway := sealedFixture(t)
	gateway.extraListed = []map[string]any{{"path": "../escape.txt", "sha256": hexSHA("x"), "size": 1}}
	out := t.TempDir()
	code, stdout, _ := runCLI(t, pullArgs(out)...)
	if code != ExitFailure || !strings.Contains(stdout, "verification_failed") {
		t.Fatalf("exit=%d %s", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(out), "escape.txt")); !os.IsNotExist(err) {
		t.Fatal("a server-supplied path escaped the destination")
	}
}

func TestGenericPullRefusesAnOpenVersionUnlessAllowed(t *testing.T) {
	gateway := newGenericGateway(t)
	dir := writeTree(t, map[string]string{"a.txt": "alpha"})
	if code, stdout, _ := runCLI(t, pushArgs(dir, "--no-seal")...); code != ExitOK {
		t.Fatalf("push exit=%d %s", code, stdout)
	}
	out := t.TempDir()
	if code, stdout, _ := runCLI(t, pullArgs(out)...); code != ExitUsage || !strings.Contains(stdout, "--allow-open") {
		t.Fatalf("open version: exit=%d %s", code, stdout)
	}
	if code, stdout, _ := runCLI(t, pullArgs(out, "--allow-open")...); code != ExitOK || gateway.state["tool@1.0.0"] != "open" {
		t.Fatalf("--allow-open: exit=%d %s", code, stdout)
	}
	var result genericPullResult
	_, stdout, _ := runCLI(t, pullArgs(out, "--allow-open")...)
	_ = json.Unmarshal(decodeEnvelope(t, stdout).Data, &result)
	if result.ManifestVerified {
		t.Fatal("an open version has no manifest to verify")
	}
}

func TestGenericListShowSealAndProperties(t *testing.T) {
	gateway := sealedFixture(t)
	code, stdout, _ := runCLI(t, "generic", "list", "--repo", testRepoID, "--json")
	if code != ExitOK || !strings.Contains(stdout, `"latestVersion": "1.0.0"`) {
		t.Fatalf("packages: exit=%d %s", code, stdout)
	}
	code, stdout, _ = runCLI(t, "generic", "list", "--repo", testRepoID, "--name", "tool", "--state", "sealed", "--property", "channel=stable", "--json")
	if code != ExitOK || !strings.Contains(stdout, `"version": "1.0.0"`) {
		t.Fatalf("versions: exit=%d %s", code, stdout)
	}
	last := gateway.sealBody[len(gateway.sealBody)-1]["listQuery"].(string)
	if !strings.Contains(last, "state=sealed") || !strings.Contains(last, "property=channel%3Astable") {
		t.Fatalf("list query: %s", last)
	}
	if code, _, _ = runCLI(t, "generic", "list", "--repo", testRepoID, "--state", "open"); code != ExitUsage {
		t.Fatalf("--state without --name: exit=%d", code)
	}
	if code, _, _ = runCLI(t, "generic", "list", "--repo", testRepoID, "--name", "tool", "--older-than-days", "7"); code != ExitUsage {
		t.Fatalf("--older-than-days without --state open: exit=%d", code)
	}
	if code, _, _ = runCLI(t, "generic", "list", "--repo", testRepoID, "--name", "tool", "--state", "open", "--older-than-days", "7"); code != ExitOK {
		t.Fatalf("stale open listing: exit=%d", code)
	}
	code, stdout, _ = runCLI(t, "generic", "show", "--repo", testRepoID, "--name", "tool", "--version", "1.0.0")
	if code != ExitOK || !strings.Contains(stdout, "sealed") || !strings.Contains(stdout, "sub/b.txt") {
		t.Fatalf("show: exit=%d %s", code, stdout)
	}
	code, stdout, _ = runCLI(t, "generic", "show", "--repo", testRepoID, "--name", "tool", "--version", "1.0.0", "--manifest")
	if code != ExitOK || !strings.HasPrefix(stdout, `{"files":[`) {
		t.Fatalf("show --manifest: exit=%d %s", code, stdout)
	}
	if code, _, _ = runCLI(t, "generic", "show", "--repo", testRepoID, "--name", "tool", "--version", "1.0.0", "--manifest", "--checksums"); code != ExitUsage {
		t.Fatalf("both documents: exit=%d", code)
	}
	if code, _, _ = runCLI(t, "generic", "show", "--repo", testRepoID, "--name", "tool", "--version", "9.9.9"); code != ExitNotFound {
		t.Fatalf("unknown version: exit=%d", code)
	}

	// Properties and seal on a second, open version.
	dir := writeTree(t, map[string]string{"c.txt": "charlie"})
	if code, stdout, _ = runCLI(t, "generic", "push", dir, "--repo", testRepoID, "--name", "tool", "--version", "2.0.0", "--no-seal", "--json"); code != ExitOK {
		t.Fatalf("push exit=%d %s", code, stdout)
	}
	if code, stdout, _ = runCLI(t, "generic", "properties", "--repo", testRepoID, "--name", "tool", "--version", "2.0.0", "--property", "build=7"); code != ExitOK || gateway.props["tool@2.0.0"]["build"] != "7" {
		t.Fatalf("properties: exit=%d %s", code, stdout)
	}
	if code, _, _ = runCLI(t, "generic", "properties", "--repo", testRepoID, "--name", "tool", "--version", "2.0.0"); code != ExitUsage {
		t.Fatalf("properties without --property or --clear: exit=%d", code)
	}
	if code, _, _ = runCLI(t, "generic", "seal", "--repo", testRepoID, "--name", "tool", "--version", "2.0.0", "--expect", "c.txt="+hexSHA("wrong")); code != ExitFailure {
		t.Fatalf("expected list mismatch: exit=%d", code)
	}
	if code, _, _ = runCLI(t, "generic", "seal", "--repo", testRepoID, "--name", "tool", "--version", "2.0.0", "--expect", "bad"); code != ExitUsage {
		t.Fatalf("malformed --expect: exit=%d", code)
	}
	if code, stdout, _ = runCLI(t, "generic", "seal", "--repo", testRepoID, "--name", "tool", "--version", "2.0.0", "--expect", "c.txt="+hexSHA("charlie"), "--json"); code != ExitOK || gateway.state["tool@2.0.0"] != "sealed" {
		t.Fatalf("seal: exit=%d %s", code, stdout)
	}
}
