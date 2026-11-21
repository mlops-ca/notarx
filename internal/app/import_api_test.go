package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

// importFixture is an empty Raw target with the memory stores behind a gateway whose import endpoints are enabled; the
// administrator token holds ImportWrite, the resolver token does not.
type importFixture struct {
	t       *testing.T
	store   *repository.MemoryStore
	staging *objectstore.MemoryStore
	handler http.Handler
	target  repository.HostedRepository
	local   string
}

func newImportFixture(t *testing.T, mutate ...func(*GatewayOptions)) importFixture {
	t.Helper()
	ctx := context.Background()
	store := repository.NewMemoryStore()
	target, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "import-target", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	local, err := store.LocalInstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	staging := objectstore.NewMemoryStore()
	options := GatewayOptions{Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator(), ImportStaging: staging, PromotionMaxHops: 3,
		ImportConfig: config.ImportConfig{SessionTTL: time.Hour, MaxBlobBytes: 1 << 10, MaxSessionBytes: 4 << 10, MaxBlobsPerSession: 4, MaxOpenSessionsPerPrincipal: 2, StagingQuotaBytes: 8 << 10}}
	for _, apply := range mutate {
		apply(&options)
	}
	return importFixture{t: t, store: store, staging: staging, handler: newTestGateway(t, options), target: target, local: local}
}

func (f importFixture) do(method, path, token string, body io.Reader) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, path, body)
	if token != "" {
		authorize(r, token)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func (f importFixture) problemCode(w *httptest.ResponseRecorder) string {
	var problem struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &problem)
	return problem.Code
}

func sha(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// claim is the body that opens a session for one Raw file.
func claim(path, content, key string, extra string) string {
	return fmt.Sprintf(`{"coordinate":%q,"digest":"sha256:%s","formatManifest":{"path":%q,"contentType":"text/plain"},"blobs":[{"digest":"sha256:%s","size":%d}],"idempotencyKey":%q%s}`,
		path, sha(content), path, sha(content), len(content), key, extra)
}

func (f importFixture) open(body string) (*httptest.ResponseRecorder, map[string]any) {
	f.t.Helper()
	w := f.do(http.MethodPost, "/api/v2/repositories/"+f.target.ID+"/imports", "admin-secret", strings.NewReader(body))
	var session map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &session)
	return w, session
}

func (f importFixture) upload(id, digestHex, content string) *httptest.ResponseRecorder {
	return f.do(http.MethodPut, "/api/v2/imports/"+id+"/blobs/"+digestHex, "admin-secret", strings.NewReader(content))
}

func (f importFixture) commit(id string) *httptest.ResponseRecorder {
	return f.do(http.MethodPost, "/api/v2/imports/"+id+"/commit", "admin-secret", nil)
}

func TestImportPushesVerifiedBytesIntoARawRepositoryAndIsIdempotent(t *testing.T) {
	f := newImportFixture(t)
	const content = "release notes\n"
	opened, session := f.open(claim("docs/notes.txt", content, "k1", `,"chainPath":[{"instance":"0197c0de-0000-7000-8000-000000000001","repository":"dev","ruleId":"","at":"2026-01-01T00:00:00Z"}],"evidence":{"scan":"clean"}`))
	if opened.Code != http.StatusCreated || session["state"] != "open" {
		t.Fatalf("open = %d %s", opened.Code, opened.Body.String())
	}
	id := session["id"].(string)
	if missing, _ := session["missing"].([]any); len(missing) != 1 {
		t.Fatalf("missing = %v", session["missing"])
	}
	stored, err := f.store.GetImportSession(context.Background(), id)
	if err != nil || len(stored.ChainPath) != 2 || stored.ChainPath[1].Instance != f.local || stored.ChainPath[1].Repository != "import-target" || !strings.Contains(string(stored.Evidence), "clean") {
		t.Fatalf("stored = %+v %v", stored, err)
	}

	// The same key finds the session instead of opening another one.
	if again, replay := f.open(claim("docs/notes.txt", content, "k1", "")); again.Code != http.StatusOK || replay["id"] != id {
		t.Fatalf("replay = %d %s", again.Code, again.Body.String())
	}
	// Nothing is committed while a blob is still missing.
	if got := f.commit(id); got.Code != http.StatusConflict || f.problemCode(got) != "import_blobs_missing" {
		t.Fatalf("early commit = %d %s", got.Code, got.Body.String())
	}
	// A digest or size that differs from the claim is refused before anything is visible, and stages nothing.
	if got := f.upload(id, sha(content), "release notez\n"); got.Code != http.StatusUnprocessableEntity || f.problemCode(got) != "digest_mismatch" {
		t.Fatalf("tampered upload = %d %s", got.Code, got.Body.String())
	}
	if got := f.upload(id, sha(content), content+"x"); got.Code != http.StatusUnprocessableEntity || f.problemCode(got) != "size_mismatch" {
		t.Fatalf("long upload = %d %s", got.Code, got.Body.String())
	}
	if keys, _ := f.staging.List(context.Background(), "native/imports/"); len(keys) != 0 {
		t.Fatalf("a refused upload staged %v", keys)
	}
	if got := f.upload(id, sha("other"), "other"); got.Code != http.StatusNotFound || f.problemCode(got) != "import_blob_not_claimed" {
		t.Fatalf("unclaimed blob = %d %s", got.Code, got.Body.String())
	}
	if got := f.upload(id, sha(content), content); got.Code != http.StatusNoContent {
		t.Fatalf("upload = %d %s", got.Code, got.Body.String())
	}
	if got := f.upload(id, sha(content), content); got.Code != http.StatusNoContent {
		t.Fatalf("repeated upload = %d %s", got.Code, got.Body.String())
	}
	if _, getErr := f.store.GetRawAsset(context.Background(), f.target.ID, "docs/notes.txt"); !errors.Is(getErr, repository.ErrNotFound) {
		t.Fatalf("asset visible before the commit: %v", getErr)
	}

	committed := f.commit(id)
	var result map[string]any
	_ = json.Unmarshal(committed.Body.Bytes(), &result)
	if committed.Code != http.StatusOK || result["state"] != "committed" || len(result["missing"].([]any)) != 0 {
		t.Fatalf("commit = %d %s", committed.Code, committed.Body.String())
	}
	asset, err := f.store.GetRawAsset(context.Background(), f.target.ID, "docs/notes.txt")
	if err != nil || asset.Digest != "sha256:"+sha(content) || asset.Size != int64(len(content)) || asset.ContentType != "text/plain" {
		t.Fatalf("asset = %+v %v", asset, err)
	}
	if again := f.commit(id); again.Code != http.StatusOK || again.Body.String() != committed.Body.String() {
		t.Fatalf("repeated commit = %d %s", again.Code, again.Body.String())
	}
	if again, replay := f.open(claim("docs/notes.txt", content, "k1", "")); again.Code != http.StatusOK || replay["state"] != "committed" {
		t.Fatalf("replay after commit = %d %s", again.Code, again.Body.String())
	}
}

func TestImportRecomputesTheCoordinateAndRefusesWhatTheBytesContradict(t *testing.T) {
	f := newImportFixture(t, func(o *GatewayOptions) { o.ImportConfig.MaxOpenSessionsPerPrincipal = 20 })
	const content = "payload"
	commit := func(key, body string) *httptest.ResponseRecorder {
		t.Helper()
		opened, session := f.open(body)
		if opened.Code != http.StatusCreated {
			t.Fatalf("open %s = %d %s", key, opened.Code, opened.Body.String())
		}
		id := session["id"].(string)
		if got := f.upload(id, sha(content), content); got.Code != http.StatusNoContent {
			t.Fatalf("upload %s = %d %s", key, got.Code, got.Body.String())
		}
		return f.commit(id)
	}
	// The claimed coordinate is not the path of the manifest.
	body := fmt.Sprintf(`{"coordinate":"a.txt","digest":"sha256:%s","formatManifest":{"path":"b.txt"},"blobs":[{"digest":"sha256:%s","size":%d}],"idempotencyKey":"m1"}`, sha(content), sha(content), len(content))
	if got := commit("m1", body); got.Code != http.StatusUnprocessableEntity || f.problemCode(got) != "import_mismatch" {
		t.Fatalf("coordinate mismatch = %d %s", got.Code, got.Body.String())
	}
	// A manifest that is not a canonical Raw path, or has fields the format does not know.
	for key, manifest := range map[string]string{"m2": `{"path":"../escape"}`, "m3": `{"path":"c.txt","size":7}`} {
		body = fmt.Sprintf(`{"coordinate":"c.txt","digest":"sha256:%s","formatManifest":%s,"blobs":[{"digest":"sha256:%s","size":%d}],"idempotencyKey":%q}`, sha(content), manifest, sha(content), len(content), key)
		if got := commit(key, body); got.Code != http.StatusUnprocessableEntity || f.problemCode(got) != "import_invalid_unit" {
			t.Fatalf("%s = %d %s", manifest, got.Code, got.Body.String())
		}
	}
	// The artifact digest is not the digest of the file.
	body = fmt.Sprintf(`{"coordinate":"d.txt","digest":"sha256:%s","formatManifest":{"path":"d.txt"},"blobs":[{"digest":"sha256:%s","size":%d}],"idempotencyKey":"m4"}`, sha("something else"), sha(content), len(content))
	if got := commit("m4", body); got.Code != http.StatusUnprocessableEntity || f.problemCode(got) != "import_invalid_unit" {
		t.Fatalf("digest mismatch = %d %s", got.Code, got.Body.String())
	}
	for _, path := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		if _, err := f.store.GetRawAsset(context.Background(), f.target.ID, path); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("%s was published: %v", path, err)
		}
	}
	// A path that holds another digest is a conflict; the same digest is a replay.
	if got := commit("c1", claim("held.txt", content, "c1", "")); got.Code != http.StatusOK {
		t.Fatalf("first import = %d %s", got.Code, got.Body.String())
	}
	if got := commit("c2", claim("held.txt", content, "c2", "")); got.Code != http.StatusOK {
		t.Fatalf("same bytes again = %d %s", got.Code, got.Body.String())
	}
	opened, session := f.open(claim("held.txt", "different", "c3", ""))
	if opened.Code != http.StatusCreated {
		t.Fatalf("open = %d %s", opened.Code, opened.Body.String())
	}
	id := session["id"].(string)
	if got := f.upload(id, sha("different"), "different"); got.Code != http.StatusNoContent {
		t.Fatalf("upload = %d", got.Code)
	}
	if got := f.commit(id); got.Code != http.StatusConflict || f.problemCode(got) != "import_conflict" {
		t.Fatalf("conflict = %d %s", got.Code, got.Body.String())
	}
}

func TestImportEnforcesTheReceiversPolicyAndQuarantine(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	const content = "gated"
	opened, session := f.open(claim("gated.txt", content, "p1", ""))
	if opened.Code != http.StatusCreated {
		t.Fatalf("open = %d %s", opened.Code, opened.Body.String())
	}
	id := session["id"].(string)
	if got := f.upload(id, sha(content), content); got.Code != http.StatusNoContent {
		t.Fatalf("upload = %d", got.Code)
	}
	// The sender's evidence is never counted: a policy that needs an SBOM refuses the import whatever it ships.
	initial, err := f.store.GetRepositorySecurityPolicy(ctx, f.target.ID)
	if err != nil {
		t.Fatal(err)
	}
	strict, err := f.store.ReplaceRepositorySecurityPolicy(ctx, f.target.ID, repository.RepositorySecurityPolicy{Enabled: true, RequireSBOM: true}, initial.Version)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.commit(id); got.Code != http.StatusUnprocessableEntity || f.problemCode(got) != "admission_refused" || !strings.Contains(got.Body.String(), "sbom_required") {
		t.Fatalf("policy = %d %s", got.Code, got.Body.String())
	}
	if _, err = f.store.ReplaceRepositorySecurityPolicy(ctx, f.target.ID, repository.RepositorySecurityPolicy{}, strict.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ReplaceArtifactQuarantine(ctx, repository.ArtifactQuarantine{RepositoryID: f.target.ID, Format: repository.FormatRaw, Coordinate: "gated.txt", Digest: "sha256:" + sha(content), State: repository.ArtifactQuarantineStateQuarantined, Reason: "test", UpdatedBy: "user:security-admin"}, "0"); err != nil {
		t.Fatal(err)
	}
	if got := f.commit(id); got.Code != http.StatusUnprocessableEntity || f.problemCode(got) != "artifact_quarantined" {
		t.Fatalf("quarantine = %d %s", got.Code, got.Body.String())
	}
	if _, err = f.store.GetRawAsset(ctx, f.target.ID, "gated.txt"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("a refused import was published: %v", err)
	}
}

func TestImportRejectsLongOrLoopingChainsAndOwnHopIsAppended(t *testing.T) {
	f := newImportFixture(t)
	hop := func(instance, repo string) string {
		return fmt.Sprintf(`{"instance":%q,"repository":%q,"ruleId":"","at":"2026-01-01T00:00:00Z"}`, instance, repo)
	}
	chain := func(hops ...string) string { return `,"chainPath":[` + strings.Join(hops, ",") + `]` }
	peer := "0197c0de-0000-7000-8000-000000000001"
	if w, _ := f.open(claim("a.txt", "a", "h1", chain(hop(peer, "r1"), hop(peer, "r2"), hop(peer, "r3"), hop(peer, "r4")))); w.Code != http.StatusUnprocessableEntity || f.problemCode(w) != "chain_too_long" {
		t.Fatalf("long chain = %d %s", w.Code, w.Body.String())
	}
	if w, _ := f.open(claim("a.txt", "a", "h2", chain(hop(peer, "r1"), hop(f.local, "import-target")))); w.Code != http.StatusUnprocessableEntity || f.problemCode(w) != "chain_loop" {
		t.Fatalf("looping chain = %d %s", w.Code, w.Body.String())
	}
	// The same repository name on another instance is not a loop; a missing chain is depth 0.
	if w, _ := f.open(claim("a.txt", "a", "h3", chain(hop(peer, "import-target")))); w.Code != http.StatusCreated {
		t.Fatalf("other instance = %d %s", w.Code, w.Body.String())
	}
	_, session := f.open(claim("b.txt", "b", "h4", ""))
	stored, err := f.store.GetImportSession(context.Background(), session["id"].(string))
	if err != nil || len(stored.ChainPath) != 1 || stored.ChainPath[0].Instance != f.local {
		t.Fatalf("depth 0 session = %+v %v", stored, err)
	}
}

func TestImportRefusesWhatTheSessionLimitsForbidWithTheirOwnCodes(t *testing.T) {
	f := newImportFixture(t)
	big := strings.Repeat("x", 2<<10)
	if w, _ := f.open(claim("big.bin", big, "l1", "")); w.Code != http.StatusRequestEntityTooLarge || f.problemCode(w) != "import_blob_too_large" {
		t.Fatalf("blob = %d %s", w.Code, w.Body.String())
	}
	blobs := make([]string, 5)
	for i := range blobs {
		blobs[i] = fmt.Sprintf(`{"digest":"sha256:%s","size":1}`, sha(fmt.Sprint(i)))
	}
	many := fmt.Sprintf(`{"coordinate":"m.txt","digest":"sha256:%s","formatManifest":{"path":"m.txt"},"blobs":[%s],"idempotencyKey":"l2"}`, sha("0"), strings.Join(blobs, ","))
	if w, _ := f.open(many); w.Code != http.StatusUnprocessableEntity || f.problemCode(w) != "import_too_many_blobs" {
		t.Fatalf("blob count = %d %s", w.Code, w.Body.String())
	}
	if w, _ := f.open(claim("s1.txt", "s", "l3", "")); w.Code != http.StatusCreated {
		t.Fatalf("session 1 = %d", w.Code)
	}
	if w, _ := f.open(claim("s2.txt", "t", "l4", "")); w.Code != http.StatusCreated {
		t.Fatalf("session 2 = %d", w.Code)
	}
	if w, _ := f.open(claim("s3.txt", "u", "l5", "")); w.Code != http.StatusTooManyRequests || f.problemCode(w) != "import_too_many_sessions" {
		t.Fatalf("open sessions = %d %s", w.Code, w.Body.String())
	}
	// The same key under another claim conflicts, and malformed claims are bad requests.
	if w, _ := f.open(claim("other.txt", "s", "l3", "")); w.Code != http.StatusConflict || f.problemCode(w) != "idempotency_conflict" {
		t.Fatalf("conflicting key = %d %s", w.Code, w.Body.String())
	}
	if w, _ := f.open(`{"coordinate":"x","digest":"md5:1","formatManifest":{},"blobs":[],"idempotencyKey":"k"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad digest = %d %s", w.Code, w.Body.String())
	}
}

func TestImportNeedsImportWriteAndOnlyTheOpeningPrincipalMayUseASession(t *testing.T) {
	f := newImportFixture(t)
	path := "/api/v2/repositories/" + f.target.ID + "/imports"
	if got := f.do(http.MethodPost, path, "", strings.NewReader(claim("a.txt", "a", "a1", ""))); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", got.Code)
	}
	// The resolver token holds no grant on the target: ImportWrite is not implied by anything weaker.
	if got := f.do(http.MethodPost, path, "resolver-secret", strings.NewReader(claim("a.txt", "a", "a1", ""))); got.Code != http.StatusForbidden {
		t.Fatalf("resolver = %d %s", got.Code, got.Body.String())
	}
	_, session := f.open(claim("a.txt", "a", "a1", ""))
	id := session["id"].(string)
	for _, call := range []struct{ method, path string }{{http.MethodPut, "/api/v2/imports/" + id + "/blobs/" + sha("a")}, {http.MethodPost, "/api/v2/imports/" + id + "/commit"}} {
		if got := f.do(call.method, call.path, "resolver-secret", strings.NewReader("a")); got.Code != http.StatusForbidden {
			t.Fatalf("resolver %s = %d", call.path, got.Code)
		}
		if got := f.do(call.method, call.path, "", nil); got.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s = %d", call.path, got.Code)
		}
	}
	unknown := uuid.NewString()
	if got := f.commit(unknown); got.Code != http.StatusNotFound {
		t.Fatalf("unknown session = %d", got.Code)
	}
	// A session of another principal is not confirmed to exist.
	foreign, _, err := f.store.CreateImportSession(context.Background(), repository.ImportSession{ID: uuid.NewString(), TargetRepositoryID: f.target.ID, Principal: "someone-else", Format: repository.FormatRaw,
		Coordinate: "f.txt", Digest: "sha256:" + sha("f"), IdempotencyKey: "foreign", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, []repository.ImportBlob{{Digest: "sha256:" + sha("f"), Size: 1}}, repository.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.commit(foreign.ID); got.Code != http.StatusNotFound {
		t.Fatalf("foreign session = %d %s", got.Code, got.Body.String())
	}
	if got := f.upload(foreign.ID, sha("f"), "f"); got.Code != http.StatusNotFound {
		t.Fatalf("foreign upload = %d %s", got.Code, got.Body.String())
	}
}

func TestImportEndpointsAnswerUnavailableWithoutStaging(t *testing.T) {
	f := newImportFixture(t, func(o *GatewayOptions) { o.ImportStaging = nil })
	if w, _ := f.open(claim("a.txt", "a", "u1", "")); w.Code != http.StatusServiceUnavailable || f.problemCode(w) != "import_unavailable" {
		t.Fatalf("open = %d %s", w.Code, w.Body.String())
	}
}

// A blob staged after the session closed under the upload is deleted again: the reaper may already have emptied the
// session's staging keys, and nothing else would remove an object written after that.
type closingImportStore struct{ repository.ImportSessionStore }

func (closingImportStore) RecordImportBlob(context.Context, string, string, int64, time.Time) error {
	return repository.ErrImportSessionClosed
}

func TestImportUploadDeletesTheObjectItStagedWhenTheSessionClosed(t *testing.T) {
	f := newImportFixture(t)
	const content = "late"
	_, session := f.open(claim("late.txt", content, "d1", ""))
	id := session["id"].(string)
	scope := newManagementScope(f.store, f.store, f.store, f.store, testAuthenticator(), nil)
	handler := importHandler{managementScope: scope, sessions: closingImportStore{f.store}, staging: f.staging, now: func() time.Time { return time.Now().UTC() }}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /imports/{importId}/blobs/{sha256}", func(w http.ResponseWriter, r *http.Request) {
		handler.UploadImportBlob(w, r, uuid.MustParse(r.PathValue("importId")), r.PathValue("sha256"))
	})
	r := httptest.NewRequest(http.MethodPut, "/imports/"+id+"/blobs/"+sha(content), strings.NewReader(content))
	authorize(r, "admin-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusConflict || f.problemCode(w) != "import_session_closed" {
		t.Fatalf("upload = %d %s", w.Code, w.Body.String())
	}
	if keys, _ := f.staging.List(context.Background(), "native/imports/"); len(keys) != 0 {
		t.Fatalf("a closed session kept %v", keys)
	}
}

func TestInstanceDocumentListsOnlyFormatsThatImport(t *testing.T) {
	f := newImportFixture(t)
	w := f.do(http.MethodGet, "/.well-known/notarx", "", nil)
	if !strings.Contains(w.Body.String(), `"formats":["raw"]`) {
		t.Fatalf("document = %s", w.Body.String())
	}
}
