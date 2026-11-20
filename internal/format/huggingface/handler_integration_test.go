//go:build integration

package huggingface

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

type protocolMetrics struct {
	recordingMetrics
	mu2  sync.Mutex
	xets int
}

func (m *protocolMetrics) RecordHuggingFaceXetOnly() {
	m.mu2.Lock()
	m.xets++
	m.mu2.Unlock()
}

func (m *protocolMetrics) xetCount() int {
	m.mu2.Lock()
	defer m.mu2.Unlock()
	return m.xets
}

type recordingAudit struct {
	mu      sync.Mutex
	records []repository.AuditRecord
}

func (a *recordingAudit) RecordAudit(_ context.Context, record repository.AuditRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, record)
	return nil
}

func (a *recordingAudit) snapshot() []repository.AuditRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]repository.AuditRecord(nil), a.records...)
}

type protocolEnv struct {
	audit   *recordingAudit
	f       *fixture
	hub     *fakeHub
	repos   *repository.MemoryStore
	objects objectstore.Store
	module  *Module
	server  *httptest.Server
	metrics *protocolMetrics
	repo    repository.HostedRepository
	shaA    string
}

const adminToken = "admin-secret"

// newProtocolEnv serves a Hugging Face Proxy repository of the memory store (grants, keys,
// audit) over the PostgreSQL hf_* tables, against a fake Hub.
func newProtocolEnv(t *testing.T, repoType repository.RepositoryType, files map[string]*hubFile, mutate func(*Config)) *protocolEnv {
	t.Helper()
	f := newFixture(t)
	f.requirePostgres()
	hub := newFakeHub(t)
	shaA := commitID(0xa1)
	hub.addCommit(shaA, "main", files)
	repos := repository.NewMemoryStore()
	repo, _, err := repos.CreateHostedRepositoryIdempotently(context.Background(), repository.HostedRepository{
		ID: f.repositoryID, Name: f.name, Format: repository.FormatHuggingFace, Type: repoType, Endpoint: "https://example.com", Visibility: repository.VisibilityPrivate,
	}, "admin", uuid.NewString(), "hash")
	if err != nil {
		t.Fatal(err)
	}
	auth := authorization.Authenticator{AdminToken: adminToken, AdminActor: "admin", APIKeys: repos, ServiceAccounts: repos}
	cfg := DefaultConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	metrics := &protocolMetrics{}
	audit := &recordingAudit{}
	objects := objectstore.NewMemoryStore()
	upstream := hub.upstream()
	module, err := New(Options{Store: f.store, Objects: objects, Read: &ReadOptions{
		Repositories: repos, Groups: repos, Authenticator: auth, Authorizer: authorization.RepositoryAuthorizer{Grants: repos, Legacy: auth},
		Audit: audit, Anonymous: repos, Config: cfg, Metrics: metrics,
		Upstream: func(repository.HostedRepository) *Upstream { return upstream },
	}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for _, route := range module.Routes() {
		mux.Handle(route.Pattern, route.Handler)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	module.read.fetcher.HoldBack = 16
	return &protocolEnv{audit: audit, f: f, hub: hub, repos: repos, objects: objects, module: module, server: server, metrics: metrics, repo: repo, shaA: shaA}
}

func (e *protocolEnv) url(rest string) string { return e.server.URL + "/hf/" + e.f.name + "/" + rest }

func (e *protocolEnv) do(t *testing.T, method, rest string, header http.Header) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, e.url(rest), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+adminToken)
	for name, values := range header {
		request.Header[name] = values
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	return response, body
}

func requireHubError(t *testing.T, response *http.Response, body []byte, status int, code string) {
	t.Helper()
	if response.StatusCode != status || response.Header.Get("X-Error-Code") != code {
		t.Fatalf("response = %d %q, want %d %q (%s)", response.StatusCode, response.Header.Get("X-Error-Code"), status, code, body)
	}
	var payload map[string]string
	if err := json.Unmarshal(body, &payload); err != nil || payload["error"] == "" {
		t.Fatalf("not a Hub-shaped error body: %q (%v)", body, err)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("content type = %q", response.Header.Get("Content-Type"))
	}
}

func requireNoXetHeaders(t *testing.T, response *http.Response) {
	t.Helper()
	for name := range response.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-xet") {
			t.Fatalf("a Xet header reached the client: %s", name)
		}
	}
}

var protocolFiles = func() map[string]*hubFile {
	return map[string]*hubFile{
		"config.json":       {Content: []byte(`{"k":1}`)},
		"model.safetensors": {Content: weights(4000), LFS: true},
		"other.bin":         {Content: weights(300), LFS: true},
	}
}

func TestColdWarmAndOfflineHead(t *testing.T) {
	env := newProtocolEnv(t, repository.RepositoryTypeProxy, protocolFiles(), nil)
	file := env.hub.commits[env.shaA]["model.safetensors"]
	// Cold: the ref is resolved and the tree ingested; no bytes are fetched for a HEAD.
	cold, _ := env.do(t, http.MethodHead, "acme/widget/resolve/main/model.safetensors", nil)
	if cold.StatusCode != 200 || cold.Header.Get("X-Repo-Commit") != env.shaA || cold.Header.Get("ETag") != `"`+file.sha256()+`"` ||
		cold.Header.Get("X-Linked-Etag") != `"`+file.sha256()+`"` || cold.Header.Get("X-Linked-Size") != "4000" || cold.Header.Get("Content-Length") != "4000" || cold.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("cold HEAD = %d %v", cold.StatusCode, cold.Header)
	}
	requireNoXetHeaders(t, cold)
	if env.hub.count("GET /cdn/") != 0 || env.hub.count("HEAD /cdn/") != 0 {
		t.Fatal("a HEAD fetched bytes")
	}
	regular, _ := env.do(t, http.MethodHead, "acme/widget/resolve/"+env.shaA+"/config.json", nil)
	config := env.hub.commits[env.shaA]["config.json"]
	if regular.StatusCode != 200 || regular.Header.Get("ETag") != `"`+config.gitBlob()+`"` || regular.Header.Get("X-Linked-Etag") != "" || regular.Header.Get("X-Linked-Size") != "" {
		t.Fatalf("regular HEAD = %d %v", regular.StatusCode, regular.Header)
	}
	// Warm: nothing goes upstream any more.
	upstreamBefore := env.hub.count("")
	warm, _ := env.do(t, http.MethodHead, "acme/widget/resolve/main/model.safetensors", nil)
	if warm.StatusCode != 200 || warm.Header.Get("X-Repo-Commit") != env.shaA || env.hub.count("") != upstreamBefore {
		t.Fatalf("warm HEAD went upstream: %d, %d requests", warm.StatusCode, env.hub.count("")-upstreamBefore)
	}
	// Fetch one file, then take the Hub down.
	get, body := env.do(t, http.MethodGet, "acme/widget/resolve/"+env.shaA+"/model.safetensors", nil)
	if get.StatusCode != 200 || string(body) != string(file.Content) || get.Header.Get("X-Repo-Commit") != env.shaA {
		t.Fatalf("GET = %d len %d", get.StatusCode, len(body))
	}
	env.hub.setDown(true)
	offline, _ := env.do(t, http.MethodHead, "acme/widget/resolve/main/other.bin", nil)
	if offline.StatusCode != 200 || offline.Header.Get("X-Repo-Commit") != env.shaA {
		t.Fatalf("a cached commit must answer HEAD offline: %d %v", offline.StatusCode, offline.Header)
	}
	cached, cachedBody := env.do(t, http.MethodGet, "acme/widget/resolve/main/model.safetensors", nil)
	if cached.StatusCode != 200 || string(cachedBody) != string(file.Content) {
		t.Fatalf("a cached file must be served offline: %d", cached.StatusCode)
	}
	// A file that was never fetched fails; metadata is never fabricated.
	response, errBody := env.do(t, http.MethodGet, "acme/widget/resolve/main/other.bin", nil)
	requireHubError(t, response, errBody, http.StatusGatewayTimeout, "upstream_unavailable")
	unknown, unknownBody := env.do(t, http.MethodHead, "acme/widget/resolve/"+commitID(0xdead)+"/config.json", nil)
	if unknown.StatusCode != http.StatusGatewayTimeout || len(unknownBody) != 0 && unknown.Header.Get("X-Error-Code") != "upstream_unavailable" {
		t.Fatalf("an unknown commit with the Hub down = %d %v", unknown.StatusCode, unknown.Header)
	}
}

func TestExpiredRefIsServedStaleWhileTheHubIsDown(t *testing.T) {
	env := newProtocolEnv(t, repository.RepositoryTypeProxy, protocolFiles(), func(cfg *Config) { cfg.RefTTL = 0 })
	if response, _ := env.do(t, http.MethodHead, "acme/widget/resolve/main/config.json", nil); response.StatusCode != 200 || response.Header.Get("X-Notarx-Stale") != "" {
		t.Fatalf("first HEAD = %d %v", response.StatusCode, response.Header)
	}
	env.hub.setDown(true)
	stale, _ := env.do(t, http.MethodHead, "acme/widget/resolve/main/config.json", nil)
	if stale.StatusCode != 200 || stale.Header.Get("X-Notarx-Stale") != "true" || stale.Header.Get("X-Repo-Commit") != env.shaA {
		t.Fatalf("stale HEAD = %d %v", stale.StatusCode, stale.Header)
	}
}

func TestPinnedGetStaysPinnedAfterTheRefMoves(t *testing.T) {
	env := newProtocolEnv(t, repository.RepositoryTypeProxy, protocolFiles(), func(cfg *Config) { cfg.RefTTL = 0 })
	head, _ := env.do(t, http.MethodHead, "acme/widget/resolve/main/config.json", nil)
	pinned := head.Header.Get("X-Repo-Commit")
	shaB := commitID(0xb2)
	env.hub.addCommit(shaB, "", map[string]*hubFile{"config.json": {Content: []byte(`{"k":2}`)}})
	env.hub.setRef("main", shaB)
	// A GET by sha is the commit named in X-Repo-Commit, whatever main is now.
	bySha, body := env.do(t, http.MethodGet, "acme/widget/resolve/"+pinned+"/config.json", nil)
	if bySha.StatusCode != 200 || string(body) != `{"k":1}` || bySha.Header.Get("X-Repo-Commit") != pinned {
		t.Fatalf("GET by sha = %d %q", bySha.StatusCode, body)
	}
	// A GET that names the ref right after the HEAD does not re-resolve it, even with a TTL of zero.
	byRef, refBody := env.do(t, http.MethodGet, "acme/widget/resolve/main/config.json", nil)
	if byRef.StatusCode != 200 || string(refBody) != `{"k":1}` || byRef.Header.Get("X-Repo-Commit") != pinned {
		t.Fatalf("GET by ref after HEAD moved to %q: %d %q", byRef.Header.Get("X-Repo-Commit"), byRef.StatusCode, refBody)
	}
	// A HEAD revalidates and sees the move.
	moved, _ := env.do(t, http.MethodHead, "acme/widget/resolve/main/config.json", nil)
	if moved.Header.Get("X-Repo-Commit") != shaB {
		t.Fatalf("HEAD after the move = %q", moved.Header.Get("X-Repo-Commit"))
	}
}

func TestXetOnlyFileFailsWithAStableCodeAndTheRestOfTheCommitStaysServable(t *testing.T) {
	files := protocolFiles()
	files["xet-only.bin"] = &hubFile{Content: weights(500), LFS: true, Xet: true, XetOnly: true}
	files["bridged.bin"] = &hubFile{Content: weights(600), LFS: true, Xet: true}
	env := newProtocolEnv(t, repository.RepositoryTypeProxy, files, nil)
	response, body := env.do(t, http.MethodGet, "acme/widget/resolve/main/xet-only.bin", nil)
	requireHubError(t, response, body, http.StatusBadGateway, "upstream_xet_only")
	requireNoXetHeaders(t, response)
	if env.metrics.xetCount() != 1 {
		t.Fatalf("notarx_hf_xet_only_total = %d, want 1", env.metrics.xetCount())
	}
	for _, path := range []string{"config.json", "model.safetensors", "bridged.bin"} {
		get, got := env.do(t, http.MethodGet, "acme/widget/resolve/main/"+path, nil)
		if get.StatusCode != 200 || len(got) == 0 {
			t.Fatalf("%s must stay servable: %d", path, get.StatusCode)
		}
		requireNoXetHeaders(t, get)
	}
	head, _ := env.do(t, http.MethodHead, "acme/widget/resolve/main/bridged.bin", nil)
	requireNoXetHeaders(t, head)
	if env.metrics.xetCount() != 1 {
		t.Fatalf("a servable Xet-backed file was counted as Xet-only: %d", env.metrics.xetCount())
	}
	again, againBody := env.do(t, http.MethodGet, "acme/widget/resolve/main/xet-only.bin", nil)
	requireHubError(t, again, againBody, http.StatusBadGateway, "upstream_xet_only")
	if env.metrics.xetCount() != 2 {
		t.Fatalf("every refusal counts: %d", env.metrics.xetCount())
	}
}

func TestUnsupportedRoutesAreHubShaped404s(t *testing.T) {
	env := newProtocolEnv(t, repository.RepositoryTypeProxy, protocolFiles(), nil)
	for _, tc := range []struct{ path, code string }{
		{"spaces/acme/app/resolve/main/app.py", "unsupported_repo_type"},
		{"api/spaces/acme/app", "unsupported_repo_type"},
		{"api/models/acme/widget/xet-read-token/main", "xet_not_supported"},
		{"api/datasets/acme/data/xet-read-token/main", "xet_not_supported"},
		{"api/models/acme/widget", "not_found"},
		{"nothing", "not_found"},
		{"acme/widget/blob/main/config.json", "not_found"},
		{"acme/widget/resolve/main", "not_found"},
		{"acme/widget/resolve/main/../secret", "not_found"},
		{"-bad/widget/resolve/main/config.json", "not_found"},
	} {
		response, body := env.do(t, http.MethodGet, tc.path, nil)
		requireHubError(t, response, body, http.StatusNotFound, tc.code)
	}
	response, body := env.do(t, http.MethodPost, "acme/widget/resolve/main/config.json", nil)
	requireHubError(t, response, body, http.StatusMethodNotAllowed, "method_not_allowed")
	if response.Header.Get("Allow") != "GET, HEAD" {
		t.Fatalf("Allow = %q", response.Header.Get("Allow"))
	}
	missing, missingBody := env.do(t, http.MethodGet, "acme/widget/resolve/main/absent.bin", nil)
	requireHubError(t, missing, missingBody, http.StatusNotFound, "EntryNotFound")
	revision, revisionBody := env.do(t, http.MethodGet, "acme/widget/resolve/no-such-branch/config.json", nil)
	requireHubError(t, revision, revisionBody, http.StatusNotFound, "RevisionNotFound")
	if env.hub.count("GET /api/spaces") != 0 {
		t.Fatal("a Space request went upstream")
	}
}

func TestAuthenticationAndAuthorizationAreHubShaped(t *testing.T) {
	env := newProtocolEnv(t, repository.RepositoryTypeProxy, protocolFiles(), nil)
	anonymous, body := env.do(t, http.MethodGet, "acme/widget/resolve/main/config.json", http.Header{"Authorization": nil})
	requireHubError(t, anonymous, body, http.StatusUnauthorized, "authentication_required")
	if !strings.Contains(anonymous.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("WWW-Authenticate = %q", anonymous.Header.Get("WWW-Authenticate"))
	}
	bad, badBody := env.do(t, http.MethodGet, "acme/widget/resolve/main/config.json", http.Header{"Authorization": {"Bearer wrong"}})
	requireHubError(t, bad, badBody, http.StatusUnauthorized, "authentication_required")
	if env.hub.count("") != 0 {
		t.Fatal("an unauthenticated request reached the upstream")
	}
	// Basic with the token as the password is accepted as well.
	basic, _ := env.do(t, http.MethodHead, "acme/widget/resolve/main/config.json", http.Header{"Authorization": {"Basic " + btoa("user:"+adminToken)}})
	if basic.StatusCode != 200 {
		t.Fatalf("basic = %d", basic.StatusCode)
	}
	// A key without a grant is denied; the repository stays hidden from unauthenticated callers.
	key := repository.APIKey{ID: uuid.NewString(), Name: "reader", SecretHash: authorization.HashAPIKey("reader-token"), Roles: []string{"member"}}
	if _, err := env.repos.CreateAPIKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	denied, deniedBody := env.do(t, http.MethodGet, "acme/widget/resolve/main/config.json", http.Header{"Authorization": {"Bearer reader-token"}})
	requireHubError(t, denied, deniedBody, http.StatusForbidden, "access_denied")
	if _, err := env.repos.ReplaceRepositoryGrants(context.Background(), env.repo.ID, []repository.RepositoryGrant{{Principal: "api-key:" + key.ID, Scopes: []string{"repositories:read"}}}, "1"); err != nil {
		t.Fatal(err)
	}
	allowed, allowedBody := env.do(t, http.MethodGet, "acme/widget/resolve/main/config.json", http.Header{"Authorization": {"Bearer reader-token"}})
	if allowed.StatusCode != 200 || string(allowedBody) != `{"k":1}` {
		t.Fatalf("granted key = %d %q", allowed.StatusCode, allowedBody)
	}
	missing, err := http.NewRequest(http.MethodGet, env.server.URL+"/hf/no-such-repository/acme/widget/resolve/main/config.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	missing.Header.Set("Authorization", "Bearer "+adminToken)
	response, err := http.DefaultClient.Do(missing)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNotFound || response.Header.Get("X-Error-Code") != "RepoNotFound" {
		t.Fatalf("missing repository = %d %q", response.StatusCode, response.Header.Get("X-Error-Code"))
	}
	noCredentials, err := http.Get(env.server.URL + "/hf/no-such-repository/acme/widget/resolve/main/config.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = noCredentials.Body.Close() }()
	if noCredentials.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a missing repository must look like a private one to an anonymous caller: %d", noCredentials.StatusCode)
	}
}

func TestCommitsOfAHostedRepositoryAreServedFromRowsOnly(t *testing.T) {
	env := newProtocolEnv(t, repository.RepositoryTypeHosted, protocolFiles(), nil)
	response, body := env.do(t, http.MethodHead, "acme/widget/resolve/main/config.json", nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("a hosted repository with no commits = %d %s", response.StatusCode, body)
	}
	if env.hub.count("") != 0 {
		t.Fatal("a hosted repository went upstream")
	}
}

func TestOversizedTreeIsAStableBadGateway(t *testing.T) {
	env := newProtocolEnv(t, repository.RepositoryTypeProxy, protocolFiles(), func(cfg *Config) { cfg.MaxFilesPerCommit = 2 })
	response, body := env.do(t, http.MethodGet, "acme/widget/resolve/main/config.json", nil)
	requireHubError(t, response, body, http.StatusBadGateway, "upstream_tree_too_large")
	head, _ := env.do(t, http.MethodHead, "acme/widget/resolve/main/config.json", nil)
	if head.StatusCode != http.StatusBadGateway || head.Header.Get("X-Error-Code") != "upstream_tree_too_large" {
		t.Fatalf("HEAD = %d %q", head.StatusCode, head.Header.Get("X-Error-Code"))
	}
}

func TestAuditRecordsReads(t *testing.T) {
	env := newProtocolEnv(t, repository.RepositoryTypeProxy, protocolFiles(), nil)
	env.do(t, http.MethodGet, "acme/widget/resolve/main/config.json", nil)
	env.do(t, http.MethodGet, "acme/widget/resolve/main/absent.bin", nil)
	records := env.audit.snapshot()
	if len(records) != 2 {
		t.Fatalf("audit records = %d, want one per request", len(records))
	}
	read, absent := records[0], records[1]
	if read.Format != "huggingface" || read.Repository != env.f.name || read.Actor != "admin" || read.Operation != "get" || read.Outcome != repository.AuditResolved || read.Status != 200 || read.Bytes != 7 || !strings.Contains(read.Resource, "model/acme/widget@main/config.json") {
		t.Fatalf("read record = %+v", read)
	}
	if absent.Outcome != repository.AuditNotFound || absent.Status != 404 {
		t.Fatalf("absent record = %+v", absent)
	}
}

func TestLargeObjectStreamsThroughTheTee(t *testing.T) {
	size := int64(5) << 30
	if value := os.Getenv("NOTARX_HF_TEST_LARGE_BYTES"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 1 {
			t.Fatalf("NOTARX_HF_TEST_LARGE_BYTES = %q", value)
		}
		size = parsed
	}
	if testing.Short() {
		t.Skip("streams a multi-gigabyte synthetic object")
	}
	file := newSynthFile(size)
	env := newProtocolEnv(t, repository.RepositoryTypeProxy, map[string]*hubFile{"model-00001.safetensors": file, "config.json": {Content: []byte("{}")}}, nil)
	env.module.read.fetcher.HoldBack = 1 << 20
	request, _ := http.NewRequest(http.MethodGet, env.url("acme/widget/resolve/main/model-00001.safetensors"), nil)
	request.Header.Set("Authorization", "Bearer "+adminToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 || response.ContentLength != size {
		t.Fatalf("status %d length %d, want %d", response.StatusCode, response.ContentLength, size)
	}
	counter := &digestWriter{}
	n, err := io.Copy(counter, response.Body)
	if err != nil || n != size || counter.hex() != file.SynthDigest {
		t.Fatalf("streamed %d of %d bytes, digest ok=%v, err=%v", n, size, counter.hex() == file.SynthDigest, err)
	}
	cached, _ := env.f.store.GetFile(context.Background(), mustCommit(t, env), "model-00001.safetensors")
	if !cached.Cached || cached.SHA256 != file.SynthDigest {
		t.Fatalf("the object was not committed after the stream: %+v", cached)
	}
	if bytes, objects := env.f.counter(); bytes != size || objects != 1 {
		t.Fatalf("counter = %d/%d, want %d/1", bytes, objects, size)
	}
	// A range of the cached object comes from the store.
	ranged, body := env.do(t, http.MethodGet, "acme/widget/resolve/main/model-00001.safetensors", http.Header{"Range": {"bytes=" + strconv.FormatInt(size-10, 10) + "-"}})
	if ranged.StatusCode != http.StatusPartialContent || len(body) != 10 {
		t.Fatalf("tail range = %d len %d", ranged.StatusCode, len(body))
	}
}

func mustCommit(t *testing.T, env *protocolEnv) string {
	t.Helper()
	repo, err := env.f.store.GetRepo(context.Background(), env.f.repositoryID, RepoTypeModel, "acme/widget")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := env.f.store.GetCommit(context.Background(), repo.ID, env.shaA)
	if err != nil {
		t.Fatal(err)
	}
	return commit.ID
}

type digestWriter struct{ sum hash.Hash }

func (d *digestWriter) Write(p []byte) (int, error) {
	if d.sum == nil {
		d.sum = sha256.New()
	}
	return d.sum.Write(p)
}

func (d *digestWriter) hex() string { return hex.EncodeToString(d.sum.Sum(nil)) }

func btoa(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestUnknownUpstreamRepositoryCreatesNoRowAndIsNotAskedAgain(t *testing.T) {
	env := newProtocolEnv(t, repository.RepositoryTypeProxy, protocolFiles(), nil)
	for range 3 {
		response, body := env.do(t, http.MethodGet, "nobody/nothing/resolve/main/x.bin", nil)
		requireHubError(t, response, body, http.StatusNotFound, "RevisionNotFound")
	}
	var rows int
	if err := env.f.db.QueryRowContext(context.Background(), env.f.store.rebind("SELECT COUNT(*) FROM hf_repos WHERE repository_id = ?"), env.f.repositoryID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("hf_repos rows = %d, %v: a name upstream does not know must not create a row", rows, err)
	}
	if asked := env.hub.count("GET /api/models/nobody/nothing/revision/"); asked != 1 {
		t.Fatalf("upstream was asked %d times for the same missing repository", asked)
	}
	// A real repository still works and creates its row.
	if response, _ := env.do(t, http.MethodHead, "acme/widget/resolve/main/config.json", nil); response.StatusCode != 200 {
		t.Fatalf("real repository = %d", response.StatusCode)
	}
	if err := env.f.db.QueryRowContext(context.Background(), env.f.store.rebind("SELECT COUNT(*) FROM hf_repos WHERE repository_id = ?"), env.f.repositoryID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("hf_repos rows = %d, %v", rows, err)
	}
}

func TestReadsUpdateLastReadAtAtMostOncePerInterval(t *testing.T) {
	f := newFixture(t)
	hub, shaA := hubWithFiles(t)
	ingest, repo, clk := newIngest(t, f, hub)
	ctx := context.Background()
	first, err := ingest.ResolveRevision(ctx, repo, shaA)
	if err != nil {
		t.Fatal(err)
	}
	read := func() time.Time {
		commit, err := f.store.GetCommit(ctx, repo.ID, shaA)
		if err != nil {
			t.Fatal(err)
		}
		return commit.LastReadAt
	}
	created := read()
	clk.advance(10 * time.Minute)
	if _, err := ingest.ResolveRevision(ctx, repo, shaA); err != nil {
		t.Fatal(err)
	}
	if !read().Equal(created) {
		t.Fatal("a read within the interval wrote last_read_at: a snapshot download would update one row per file")
	}
	f.exec("UPDATE hf_commits SET last_read_at = ? WHERE id = ?", clk.Now().Add(-2*time.Hour), first.Commit.ID)
	stale := read()
	if _, err := ingest.ResolveRevision(ctx, repo, shaA); err != nil {
		t.Fatal(err)
	}
	if !read().After(stale) {
		t.Fatal("a read after the interval must refresh last_read_at")
	}
}

func TestResolveForReadPinsOnlyWithinTheWindow(t *testing.T) {
	f := newFixture(t)
	hub, shaA := hubWithFiles(t)
	shaB := commitID(0xb2)
	hub.addCommit(shaB, "", map[string]*hubFile{"config.json": {Content: []byte("v2")}})
	ingest, repo, clk := newIngest(t, f, hub)
	ingest.Config.RefTTL = 0
	ctx := context.Background()
	if _, err := ingest.ResolveRevision(ctx, repo, "main"); err != nil {
		t.Fatal(err)
	}
	hub.setRef("main", shaB)
	pinned, err := ingest.ResolveForRead(ctx, repo, "main")
	if err != nil || pinned.Commit.SHA != shaA || !pinned.Cached {
		t.Fatalf("within the window = %+v, %v", pinned, err)
	}
	clk.advance(pinWindow + time.Second)
	moved, err := ingest.ResolveForRead(ctx, repo, "main")
	if err != nil || moved.Commit.SHA != shaB {
		t.Fatalf("after the window the ref must be revalidated: %+v, %v", moved, err)
	}
}

func TestAGroupAnswersNotImplementedInsteadOfLookingAbsent(t *testing.T) {
	env := newProtocolEnv(t, repository.RepositoryTypeProxy, protocolFiles(), nil)
	_, _, err := env.repos.CreateHostedGroupIdempotently(context.Background(), repository.HostedGroup{
		Name: "hf-group", Format: repository.FormatHuggingFace, Visibility: repository.VisibilityPrivate,
		Members: []repository.GroupMember{{RepositoryID: env.repo.ID, Position: 0}},
	}, "admin", uuid.NewString(), "hash")
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, env.server.URL+"/hf/hf-group/acme/widget/resolve/main/config.json", nil)
	request.Header.Set("Authorization", "Bearer "+adminToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNotImplemented || response.Header.Get("X-Error-Code") != "group_not_supported" {
		t.Fatalf("group read = %d %q", response.StatusCode, response.Header.Get("X-Error-Code"))
	}
	// A principal that may not read the group learns nothing: the group looks like a missing repository.
	key := repository.APIKey{ID: uuid.NewString(), Name: "nobody", SecretHash: authorization.HashAPIKey("nobody-token"), Roles: []string{"member"}}
	if _, err := env.repos.CreateAPIKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	stranger, _ := http.NewRequest(http.MethodGet, env.server.URL+"/hf/hf-group/acme/widget/resolve/main/config.json", nil)
	stranger.Header.Set("Authorization", "Bearer nobody-token")
	strangerResponse, err := http.DefaultClient.Do(stranger)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = strangerResponse.Body.Close() }()
	if strangerResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("a caller without a read grant on the group learned it exists: %d", strangerResponse.StatusCode)
	}
	anonymous, err := http.Get(env.server.URL + "/hf/hf-group/acme/widget/resolve/main/config.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = anonymous.Body.Close() }()
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an anonymous caller must not learn that a group exists: %d", anonymous.StatusCode)
	}
}

func TestTheFirstRequestForARepositoryAsksUpstreamForTheRevisionOnce(t *testing.T) {
	env := newProtocolEnv(t, repository.RepositoryTypeProxy, protocolFiles(), nil)
	if response, _ := env.do(t, http.MethodHead, "acme/widget/resolve/main/config.json", nil); response.StatusCode != 200 {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if asked := env.hub.count("GET /api/models/" + hubModel + "/revision/"); asked != 1 {
		t.Fatalf("revision requests = %d: confirming the repository and resolving it must share one upstream call", asked)
	}
}
