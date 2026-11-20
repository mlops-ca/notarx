package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

type rawVersionHarness struct {
	t       *testing.T
	store   *repository.MemoryStore
	handler http.Handler
	repoID  string
	auth    Authenticator
	// reader holds a read grant, writer read and write; both are bearer tokens.
	reader, writer string
}

func newRawVersionHarness(t *testing.T, mutate func(*repository.HostedRepository)) *rawVersionHarness {
	t.Helper()
	store := repository.NewMemoryStore()
	repo := repository.HostedRepository{ID: "00000000-0000-7000-8000-0000000000aa", Name: "releases", Format: repository.FormatRaw, RawLayout: repository.RawLayoutVersioned}
	if mutate != nil {
		mutate(&repo)
	}
	created, err := store.CreateHostedRepository(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceRepositoryGrants(context.Background(), created.ID, []repository.RepositoryGrant{
		{Principal: "version-reader", Scopes: []string{"repositories:read"}},
		{Principal: "version-writer", Scopes: []string{"repositories:read", "repositories:write"}},
	}, "1"); err != nil {
		t.Fatal(err)
	}
	auth := testAuthenticator()
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: auth, Storage: ObjectStores{repository.FormatOCI: objectstore.NewMemoryStore(), repository.FormatRaw: objectstore.NewMemoryStore()}})
	return &rawVersionHarness{t: t, store: store, handler: handler, repoID: created.ID, auth: auth, reader: auth.IssueToken("version-reader"), writer: auth.IssueToken("version-writer")}
}

func (h *rawVersionHarness) do(method, path, token, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		authorize(request, token)
	}
	response := httptest.NewRecorder()
	h.handler.ServeHTTP(response, request)
	return response
}

func (h *rawVersionHarness) api(method, suffix, token, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(method, "/api/v2/repositories/"+h.repoID+suffix, token, body)
}

func (h *rawVersionHarness) upload(path, content string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodPut, "/raw/releases/"+path, "admin-secret", content)
}

func decodeJSON[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %q: %v", response.Body.String(), err)
	}
	return value
}

func problemCodeOf(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	return decodeJSON[struct {
		Code string `json:"code"`
	}](t, response).Code
}

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func TestRawRepositoryLayoutIsChosenAtCreationAndImmutable(t *testing.T) {
	store := repository.NewMemoryStore()
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	call := func(method, path, body, key string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		authorize(request, "admin-secret")
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		if method == http.MethodPatch {
			request.Header.Set("If-Match", "1")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	created := call(http.MethodPost, "/api/v2/repositories", `{"name":"strict-releases","format":"raw","layout":"versioned","sealMode":"idle","strict":true}`, "k1")
	if created.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	repo := decodeJSON[adminopenapi.Repository](t, created)
	if repo.Layout == nil || *repo.Layout != adminopenapi.RawLayoutVersioned || repo.SealMode == nil || *repo.SealMode != adminopenapi.Idle || repo.Strict == nil || !*repo.Strict {
		t.Fatalf("created repository: %s", created.Body.String())
	}
	capabilities := decodeJSON[adminopenapi.RepositoryCapabilities](t, call(http.MethodGet, "/api/v2/repositories/"+repo.Id+"/capabilities", "", ""))
	if capabilities.Layout == nil || *capabilities.Layout != adminopenapi.RawLayoutVersioned || capabilities.Versions == nil || !*capabilities.Versions || capabilities.Strict == nil || !*capabilities.Strict {
		t.Fatalf("capabilities: %#v", capabilities)
	}
	path := call(http.MethodPost, "/api/v2/repositories", `{"name":"plain-raw","format":"raw"}`, "k2")
	pathRepo := decodeJSON[adminopenapi.Repository](t, path)
	pathCapabilities := decodeJSON[adminopenapi.RepositoryCapabilities](t, call(http.MethodGet, "/api/v2/repositories/"+pathRepo.Id+"/capabilities", "", ""))
	if pathCapabilities.Layout == nil || *pathCapabilities.Layout != adminopenapi.RawLayoutPath || pathCapabilities.Versions == nil || *pathCapabilities.Versions || pathCapabilities.Strict == nil || *pathCapabilities.Strict {
		t.Fatalf("path capabilities: %#v", pathCapabilities)
	}
	for name, body := range map[string]string{
		"strict needs versioned": `{"name":"bad-a","format":"raw","strict":true}`,
		"idle needs versioned":   `{"name":"bad-b","format":"raw","sealMode":"idle"}`,
		"layout on npm":          `{"name":"bad-c","format":"npm","layout":"versioned"}`,
		"layout on proxy":        `{"name":"bad-d","format":"raw","type":"proxy","endpoint":"https://example.com","allowedHosts":["example.com"],"layout":"versioned"}`,
		"unknown layout":         `{"name":"bad-e","format":"raw","layout":"tree"}`,
		"unknown seal mode":      `{"name":"bad-f","format":"raw","layout":"versioned","sealMode":"never"}`,
	} {
		if response := call(http.MethodPost, "/api/v2/repositories", body, "key-"+name); response.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, response.Code, response.Body.String())
		}
	}
	// The same value is accepted; a different one is layout_immutable.
	if response := call(http.MethodPatch, "/api/v2/repositories/"+pathRepo.Id, `{"layout":"path","sealMode":"explicit","strict":false}`, ""); response.Code != http.StatusOK {
		t.Fatalf("PATCH with the current layout=%d %s", response.Code, response.Body.String())
	}
	for _, body := range []string{`{"layout":"versioned"}`, `{"sealMode":"idle"}`, `{"strict":true}`} {
		response := call(http.MethodPatch, "/api/v2/repositories/"+pathRepo.Id, body, "")
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "layout_immutable") {
			t.Errorf("PATCH %s=%d %s", body, response.Code, response.Body.String())
		}
	}
	stored, err := store.GetHostedRepository(context.Background(), pathRepo.Id)
	if err != nil || stored.RawLayout != repository.RawLayoutPath {
		t.Fatalf("layout changed: %#v %v", stored, err)
	}
}

func TestVersionedPackagesListSealAndRenderTheirDocuments(t *testing.T) {
	h := newRawVersionHarness(t, nil)
	files := map[string]string{"bin/tool-linux": "linux binary", "bin/tool-mac": "mac binary", "README": "readme"}
	for path, content := range files {
		if response := h.upload("tool/1.0.0/"+path, content); response.Code != http.StatusCreated {
			t.Fatalf("upload %s=%d %s", path, response.Code, response.Body.String())
		}
	}
	// A SHA256SUMS file as a publisher writes it: sha256sum output, one line per file.
	var sums strings.Builder
	for _, path := range []string{"README", "bin/tool-linux", "bin/tool-mac"} {
		fmt.Fprintf(&sums, "%s  %s\n", sha256Hex(files[path]), path)
	}
	if response := h.upload("tool/1.0.0/SHA256SUMS", sums.String()); response.Code != http.StatusCreated {
		t.Fatal(response.Code)
	}
	if response := h.upload("other_pkg/0.1.0/f", "x"); response.Code != http.StatusCreated {
		t.Fatal(response.Code)
	}

	packages := decodeJSON[adminopenapi.RawPackagePage](t, h.api(http.MethodGet, "/packages", "admin-secret", ""))
	if len(packages.Items) != 2 || packages.Items[0].Name != "other_pkg" || packages.Items[1].Name != "tool" || packages.Items[1].LatestVersion != nil || packages.Items[1].VersionCount != 1 {
		t.Fatalf("packages before seal: %#v", packages)
	}
	detail := decodeJSON[adminopenapi.RawVersion](t, h.api(http.MethodGet, "/packages/tool/versions/1.0.0", "admin-secret", ""))
	if detail.State != adminopenapi.RawVersionStateOpen || detail.FileCount != 4 || detail.Files == nil || len(*detail.Files) != 4 || detail.ManifestDigest != nil || detail.CreatedAt.IsZero() || detail.LastWriteAt.Before(detail.CreatedAt) {
		t.Fatalf("open detail: %#v", detail)
	}
	if manifest := h.api(http.MethodGet, "/packages/tool/versions/1.0.0/manifest", "admin-secret", ""); manifest.Code != http.StatusConflict || problemCodeOf(t, manifest) != "version_not_sealed" {
		t.Fatalf("manifest of an open version=%d %s", manifest.Code, manifest.Body.String())
	}

	sealRequest := fmt.Sprintf(`{"checksumsFile":"SHA256SUMS","properties":{"git.sha":"abc123","channel":"stable"},"files":[{"path":"README","sha256":"%s"},{"path":"SHA256SUMS","sha256":"%s"},{"path":"bin/tool-linux","sha256":"%s"},{"path":"bin/tool-mac","sha256":"%s"}]}`,
		sha256Hex("readme"), sha256Hex(sums.String()), sha256Hex("linux binary"), sha256Hex("mac binary"))
	sealed := h.api(http.MethodPost, "/packages/tool/versions/1.0.0:seal", h.writer, sealRequest)
	if sealed.Code != http.StatusOK {
		t.Fatalf("seal=%d %s", sealed.Code, sealed.Body.String())
	}
	version := decodeJSON[adminopenapi.RawVersion](t, sealed)
	if version.State != adminopenapi.RawVersionStateSealed || version.ManifestDigest == nil || version.SealedAt == nil || version.Properties["git.sha"] != "abc123" {
		t.Fatalf("sealed: %#v", version)
	}
	again := decodeJSON[adminopenapi.RawVersion](t, h.api(http.MethodPost, "/packages/tool/versions/1.0.0:seal", h.writer, ""))
	if *again.ManifestDigest != *version.ManifestDigest {
		t.Fatalf("a second seal changed the digest: %s vs %s", *again.ManifestDigest, *version.ManifestDigest)
	}

	manifest := h.api(http.MethodGet, "/packages/tool/versions/1.0.0/manifest", h.reader, "")
	if manifest.Code != http.StatusOK || manifest.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("manifest=%d %s", manifest.Code, manifest.Body.String())
	}
	// The digest is the sha256 of exactly the served bytes.
	if got := "sha256:" + sha256Hex(manifest.Body.String()); got != *version.ManifestDigest || manifest.Header().Get("ETag") != `"`+got+`"` {
		t.Fatalf("manifest digest %s, version says %s, etag %s", got, *version.ManifestDigest, manifest.Header().Get("ETag"))
	}
	var document struct {
		Name, Version string
		Files         []struct {
			Path, Sha256 string
			Size         int64
		}
		Properties map[string]string
	}
	if err := json.Unmarshal(manifest.Body.Bytes(), &document); err != nil || document.Name != "tool" || document.Version != "1.0.0" || len(document.Files) != 4 || document.Files[0].Path != "README" || document.Properties["channel"] != "stable" {
		t.Fatalf("manifest document: %s %v", manifest.Body.String(), err)
	}

	// The checksums document verifies every downloaded file the way sha256sum -c does.
	checksums := h.api(http.MethodGet, "/packages/tool/versions/1.0.0/checksums", h.reader, "")
	if checksums.Code != http.StatusOK || !strings.HasPrefix(checksums.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("checksums=%d", checksums.Code)
	}
	verified := 0
	for _, line := range strings.Split(strings.TrimSuffix(checksums.Body.String(), "\n"), "\n") {
		sum, path, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("not sha256sum format: %q", line)
		}
		downloaded := h.do(http.MethodGet, "/raw/releases/tool/1.0.0/"+path, "admin-secret", "")
		if downloaded.Code != http.StatusOK || sha256Hex(downloaded.Body.String()) != sum {
			t.Fatalf("%s fails verification", path)
		}
		verified++
	}
	if verified != 4 {
		t.Fatalf("verified %d files", verified)
	}

	packages = decodeJSON[adminopenapi.RawPackagePage](t, h.api(http.MethodGet, "/packages", h.reader, ""))
	if packages.Items[1].LatestVersion == nil || *packages.Items[1].LatestVersion != "1.0.0" {
		t.Fatalf("latest after seal: %#v", packages)
	}
	// After the seal the data plane refuses new files and deletes.
	if response := h.upload("tool/1.0.0/late", "late"); response.Code != http.StatusConflict {
		t.Fatalf("late upload=%d", response.Code)
	}
}

func TestVersionSealChecksVerifiesAndFreezes(t *testing.T) {
	h := newRawVersionHarness(t, nil)
	h.upload("tool/1.0.0/a", "alpha")
	h.upload("tool/1.0.0/SHA256SUMS", sha256Hex("alpha")+"  a\n")
	h.upload("tool/2.0.0/a", "alpha")
	h.upload("tool/2.0.0/SHA256SUMS", sha256Hex("different")+"  a\n")
	h.upload("tool/3.0.0/a", "alpha")
	h.upload("tool/3.0.0/SHA256SUMS", "this is not sha256sum output\n")

	if response := h.api(http.MethodPost, "/packages/missing/versions/1.0.0:seal", h.writer, ""); response.Code != http.StatusNotFound {
		t.Fatalf("unknown version=%d", response.Code)
	}
	if response := h.api(http.MethodPost, "/packages/tool/versions/2.0.0:seal", h.writer, `{"checksumsFile":"SHA256SUMS"}`); response.Code != http.StatusUnprocessableEntity || problemCodeOf(t, response) != "checksum_mismatch" {
		t.Fatalf("checksums mismatch=%d %s", response.Code, response.Body.String())
	}
	if response := h.api(http.MethodPost, "/packages/tool/versions/3.0.0:seal", h.writer, `{"checksumsFile":"SHA256SUMS"}`); response.Code != http.StatusUnprocessableEntity || problemCodeOf(t, response) != "checksum_mismatch" {
		t.Fatalf("malformed checksums=%d %s", response.Code, response.Body.String())
	}
	if response := h.api(http.MethodPost, "/packages/tool/versions/1.0.0:seal", h.writer, `{"checksumsFile":"NOT-THERE"}`); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("absent checksums file=%d", response.Code)
	}
	if response := h.api(http.MethodPost, "/packages/tool/versions/1.0.0:seal", h.writer, `{"files":[{"path":"a","sha256":"`+sha256Hex("nope")+`"}]}`); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected list mismatch=%d", response.Code)
	}
	if response := h.api(http.MethodPost, "/packages/tool/versions/1.0.0:seal", h.writer, `{"unknown":1}`); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field=%d", response.Code)
	}
	if response := h.api(http.MethodGet, "/packages/tool/versions/2.0.0", h.reader, ""); !strings.Contains(response.Body.String(), `"state":"open"`) {
		t.Fatalf("a failed seal changed the version: %s", response.Body.String())
	}
	if response := h.api(http.MethodPut, "/packages/empty/versions/1.0.0", h.writer, ""); response.Code != http.StatusCreated {
		t.Fatalf("create empty=%d %s", response.Code, response.Body.String())
	}
	if response := h.api(http.MethodPost, "/packages/empty/versions/1.0.0:seal", h.writer, ""); response.Code != http.StatusUnprocessableEntity || problemCodeOf(t, response) != "version_empty" {
		t.Fatalf("empty seal=%d %s", response.Code, response.Body.String())
	}

	if response := h.api(http.MethodPut, "/packages/tool/versions/1.0.0/properties", h.writer, `{"properties":{"k":"v"}}`); response.Code != http.StatusOK {
		t.Fatalf("properties=%d %s", response.Code, response.Body.String())
	}
	if response := h.api(http.MethodPut, "/packages/tool/versions/1.0.0/properties", h.writer, `{"properties":{"Bad Key":"v"}}`); response.Code != http.StatusUnprocessableEntity || problemCodeOf(t, response) != "invalid_properties" {
		t.Fatalf("bad key=%d %s", response.Code, response.Body.String())
	}
	if response := h.api(http.MethodPut, "/packages/tool/versions/1.0.0/properties", h.writer, `{}`); response.Code != http.StatusBadRequest {
		t.Fatalf("missing properties=%d", response.Code)
	}
	long := strings.Repeat("v", 257)
	if response := h.api(http.MethodPut, "/packages/tool/versions/1.0.0/properties", h.writer, `{"properties":{"k":"`+long+`"}}`); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("long value=%d", response.Code)
	}
	if response := h.api(http.MethodPost, "/packages/tool/versions/1.0.0:seal", h.writer, `{"properties":{"final":"yes"}}`); response.Code != http.StatusOK {
		t.Fatalf("seal=%d %s", response.Code, response.Body.String())
	}
	if response := h.api(http.MethodPut, "/packages/tool/versions/1.0.0/properties", h.writer, `{"properties":{"k":"late"}}`); response.Code != http.StatusConflict || problemCodeOf(t, response) != "version_sealed" {
		t.Fatalf("properties after seal=%d %s", response.Code, response.Body.String())
	}
	if response := h.api(http.MethodPut, "/packages/tool/versions/1.0.0", h.writer, ""); response.Code != http.StatusOK {
		t.Fatalf("a create that repeats a sealed version must be idempotent=%d", response.Code)
	}
	if response := h.api(http.MethodPut, "/packages/tool/versions/1.0.0", h.writer, `{"properties":{"final":"yes"}}`); response.Code != http.StatusOK {
		t.Fatalf("a create with the sealed properties=%d", response.Code)
	}
	if response := h.api(http.MethodPut, "/packages/tool/versions/1.0.0", h.writer, `{"properties":{"final":"no"}}`); response.Code != http.StatusConflict || problemCodeOf(t, response) != "version_sealed" {
		t.Fatalf("a create that would change a sealed version=%d", response.Code)
	}
	if response := h.api(http.MethodPut, "/packages/Bad_Name/versions/1.0.0", h.writer, ""); response.Code != http.StatusUnprocessableEntity || problemCodeOf(t, response) != "invalid_generic_path" {
		t.Fatalf("invalid name=%d %s", response.Code, response.Body.String())
	}
	created := h.api(http.MethodPut, "/packages/tool/versions/4.0.0", h.writer, `{"properties":{"a":"1"}}`)
	replay := h.api(http.MethodPut, "/packages/tool/versions/4.0.0", h.writer, `{"properties":{"a":"1"}}`)
	if created.Code != http.StatusCreated || replay.Code != http.StatusOK {
		t.Fatalf("create=%d replay=%d", created.Code, replay.Code)
	}
}

func TestVersionListsFilterAndPage(t *testing.T) {
	h := newRawVersionHarness(t, nil)
	for _, version := range []string{"1.0.0", "1.1.0", "2.0.0", "3.0.0-rc1"} {
		h.upload("tool/"+version+"/f", version)
		channel := "stable"
		if strings.Contains(version, "rc") {
			channel = "beta"
		}
		h.api(http.MethodPut, "/packages/tool/versions/"+version+"/properties", "admin-secret", fmt.Sprintf(`{"properties":{"channel":"%s"}}`, channel))
		if version != "3.0.0-rc1" {
			if response := h.api(http.MethodPost, "/packages/tool/versions/"+version+":seal", "admin-secret", ""); response.Code != http.StatusOK {
				t.Fatalf("seal %s=%d", version, response.Code)
			}
		}
	}
	names := func(page adminopenapi.RawVersionPage) []string {
		out := []string{}
		for _, item := range page.Items {
			out = append(out, item.Version)
		}
		return out
	}
	var collected []string
	token := ""
	for {
		suffix := "/packages/tool/versions?pageSize=2"
		if token != "" {
			suffix += "&pageToken=" + token
		}
		response := h.api(http.MethodGet, suffix, h.reader, "")
		if response.Code != http.StatusOK {
			t.Fatalf("page=%d %s", response.Code, response.Body.String())
		}
		page := decodeJSON[adminopenapi.RawVersionPage](t, response)
		collected = append(collected, names(page)...)
		if page.NextPageToken == nil {
			break
		}
		token = *page.NextPageToken
	}
	if strings.Join(collected, ",") != "2.0.0,1.1.0,1.0.0,3.0.0-rc1" {
		t.Fatalf("keyset order: %v", collected)
	}
	filtered := decodeJSON[adminopenapi.RawVersionPage](t, h.api(http.MethodGet, "/packages/tool/versions?property=channel:stable&state=sealed", h.reader, ""))
	if len(filtered.Items) != 3 {
		t.Fatalf("property filter: %v", names(filtered))
	}
	open := decodeJSON[adminopenapi.RawVersionPage](t, h.api(http.MethodGet, "/packages/tool/versions?state=open&olderThanDays=1", h.reader, ""))
	if len(open.Items) != 0 {
		t.Fatalf("nothing is a day old: %v", names(open))
	}
	for name, suffix := range map[string]string{
		"olderThanDays needs open": "/packages/tool/versions?olderThanDays=3",
		"bad property":             "/packages/tool/versions?property=nocolon",
		"bad state":                "/packages/tool/versions?state=tombstoned",
		"bad page size":            "/packages/tool/versions?pageSize=0",
		"bad token":                "/packages/tool/versions?pageToken=nope",
	} {
		if response := h.api(http.MethodGet, suffix, h.reader, ""); response.Code != http.StatusBadRequest {
			t.Errorf("%s=%d", name, response.Code)
		}
	}
	// A token is bound to the query that issued it.
	first := decodeJSON[adminopenapi.RawVersionPage](t, h.api(http.MethodGet, "/packages/tool/versions?pageSize=1", h.reader, ""))
	if response := h.api(http.MethodGet, "/packages/tool/versions?pageSize=1&state=open&pageToken="+*first.NextPageToken, h.reader, ""); response.Code != http.StatusBadRequest {
		t.Fatalf("a token reused with another filter=%d", response.Code)
	}
	if response := h.api(http.MethodGet, "/packages?pageSize=1", h.reader, ""); response.Code != http.StatusOK {
		t.Fatalf("packages=%d", response.Code)
	}
}

func TestVersionListRejectsAForgedCursor(t *testing.T) {
	h := newRawVersionHarness(t, nil)
	h.upload("tool/1.0.0/f", "x")
	h.upload("tool/2.0.0/f", "y")
	first := decodeJSON[adminopenapi.RawVersionPage](t, h.api(http.MethodGet, "/packages/tool/versions?pageSize=1", h.reader, ""))
	if first.NextPageToken == nil {
		t.Fatal("expected a second page")
	}
	filter := strings.Join([]string{"raw-versions", h.repoID, "tool", ""}, "\x00")
	for name, keys := range map[string][]string{"not a uuid": {"", "not-a-uuid"}, "bad time": {"yesterday", uuid.NewString()}} {
		token := repository.EncodeKeysetToken(filter, keys...)
		if response := h.api(http.MethodGet, "/packages/tool/versions?pageSize=1&pageToken="+token, h.reader, ""); response.Code != http.StatusBadRequest || problemCodeOf(t, response) != "invalid_page_token" {
			t.Errorf("%s: %d %s", name, response.Code, response.Body.String())
		}
	}
}

func TestVersionedRepositoriesAreOutsidePathRetentionAndPathTombstones(t *testing.T) {
	h := newRawVersionHarness(t, nil)
	h.upload("tool/1.0.0/f", "x")
	h.api(http.MethodPost, "/packages/tool/versions/1.0.0:seal", "admin-secret", "")
	for _, path := range []string{"/tombstones", "/restore"} {
		response := h.api(http.MethodPost, path, "admin-secret", `{"coordinate":"tool/1.0.0/f"}`)
		if response.Code != http.StatusUnprocessableEntity || problemCodeOf(t, response) != "layout_mismatch" {
			t.Errorf("%s: %d %s", path, response.Code, response.Body.String())
		}
	}
	if _, err := h.store.GetRawAsset(context.Background(), h.repoID, "tool/1.0.0/f"); err != nil {
		t.Fatalf("a refused tombstone must leave the file: %v", err)
	}
	retentionPolicyForTest(t, h.store, h.repoID, repository.RepositoryRetentionPolicy{Enabled: true, KeepDays: 1})
	retention := NativeRepositoryRetention{Store: h.store, Now: func() time.Time { return time.Now().Add(365 * 24 * time.Hour) }}
	candidates, err := retention.PlanRepositoryDetailed(context.Background(), h.repoID, repository.FormatRaw)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("path retention must not plan files of a versioned repository: %#v %v", candidates, err)
	}
}

func TestVersionAPIPermissionsFollowRepositoryRoles(t *testing.T) {
	h := newRawVersionHarness(t, nil)
	h.upload("tool/1.0.0/f", "x")
	h.api(http.MethodPost, "/packages/tool/versions/1.0.0:seal", "admin-secret", "")
	outsider := h.auth.IssueToken("someone-else")
	reads := []string{"/packages", "/packages/tool/versions", "/packages/tool/versions/1.0.0", "/packages/tool/versions/1.0.0/manifest", "/packages/tool/versions/1.0.0/checksums"}
	for _, suffix := range reads {
		if response := h.api(http.MethodGet, suffix, "", ""); response.Code != http.StatusUnauthorized {
			t.Errorf("anonymous GET %s=%d", suffix, response.Code)
		}
		if response := h.api(http.MethodGet, suffix, h.reader, ""); response.Code != http.StatusOK {
			t.Errorf("reader GET %s=%d", suffix, response.Code)
		}
		if response := h.api(http.MethodGet, suffix, outsider, ""); response.Code != http.StatusForbidden {
			t.Errorf("outsider GET %s=%d", suffix, response.Code)
		}
	}
	writes := []struct{ method, suffix, body string }{
		{http.MethodPut, "/packages/tool/versions/9.0.0", `{}`},
		{http.MethodPut, "/packages/tool/versions/9.0.0/properties", `{"properties":{}}`},
		{http.MethodPost, "/packages/tool/versions/9.0.0:seal", ``},
	}
	for _, write := range writes {
		for token, want := range map[string]int{h.reader: http.StatusForbidden, outsider: http.StatusForbidden, "": http.StatusUnauthorized} {
			if response := h.api(write.method, write.suffix, token, write.body); response.Code != want {
				t.Errorf("%s %s as %q=%d, want %d", write.method, write.suffix, token, response.Code, want)
			}
		}
	}
	if response := h.api(http.MethodPut, "/packages/tool/versions/9.0.0", h.writer, `{}`); response.Code != http.StatusCreated {
		t.Fatalf("writer create=%d %s", response.Code, response.Body.String())
	}
	audits, err := h.store.ListAudits(context.Background(), repository.AuditQuery{Operation: "raw.version.seal"})
	if err != nil || len(audits) != 1 || audits[0].Resource != "tool@1.0.0" || audits[0].Evidence["manifestDigest"] == "" {
		t.Fatalf("seal audit: %#v %v", audits, err)
	}
	if audits, err := h.store.ListAudits(context.Background(), repository.AuditQuery{Operation: "raw.version.create"}); err != nil || len(audits) != 1 {
		t.Fatalf("create audit: %#v %v", audits, err)
	}
}

func TestVersionAPIRejectsPathRepositories(t *testing.T) {
	h := newRawVersionHarness(t, func(repo *repository.HostedRepository) { repo.RawLayout = repository.RawLayoutPath })
	for _, suffix := range []string{"/packages", "/packages/tool/versions", "/packages/tool/versions/1.0.0"} {
		if response := h.api(http.MethodGet, suffix, "admin-secret", ""); response.Code != http.StatusUnprocessableEntity || problemCodeOf(t, response) != "layout_mismatch" {
			t.Errorf("%s=%d %s", suffix, response.Code, response.Body.String())
		}
	}
	if response := h.api(http.MethodPost, "/packages/tool/versions/1.0.0:seal", "admin-secret", ""); response.Code != http.StatusUnprocessableEntity {
		t.Errorf("seal=%d", response.Code)
	}
}

func TestSHA256SumsParsing(t *testing.T) {
	good := strings.Repeat("a", 64)
	entries, ok := parseSHA256Sums(strings.NewReader("# comment\n\n" + good + "  ./dir/file name\r\n" + strings.ToUpper(good) + " *bin\n"))
	if !ok || entries["dir/file name"] != good || entries["bin"] != good || len(entries) != 2 {
		t.Fatalf("entries=%#v ok=%v", entries, ok)
	}
	for _, bad := range []string{"", "# only a comment\n", good + " file\n", good[:63] + "  file\n", "zz" + good[2:] + "  file\n", good + "  a\n" + strings.Repeat("b", 64) + "  a\n"} {
		if _, ok := parseSHA256Sums(strings.NewReader(bad)); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}
