package raw_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/repository"
)

func versionedFixture(t *testing.T, strict bool) (*repository.MemoryStore, http.Handler) {
	t.Helper()
	store, _, mux := fixture(t)
	if _, err := store.CreateHostedRepository(context.Background(), repository.HostedRepository{ID: "releases", Name: "releases", Format: repository.FormatRaw, RawLayout: repository.RawLayoutVersioned, RawStrict: strict}); err != nil {
		t.Fatal(err)
	}
	return store, mux
}

func problemCode(t *testing.T, body string) string {
	t.Helper()
	var problem struct {
		Code   string `json:"code"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal([]byte(body), &problem); err != nil {
		t.Fatalf("not a problem document: %q", body)
	}
	return problem.Code
}

func TestVersionedRawFilesAreCreateOnly(t *testing.T) {
	store, mux := versionedFixture(t, false)
	const path = "/raw/releases/tool/1.0.0/bin/tool"
	if put := request(mux, http.MethodPut, path, "first", nil); put.Code != http.StatusCreated {
		t.Fatalf("first put=%d %s", put.Code, put.Body.String())
	}
	if replay := request(mux, http.MethodPut, path, "first", nil); replay.Code != http.StatusOK {
		t.Fatalf("a same-digest replay must be 200, got %d %s", replay.Code, replay.Body.String())
	}
	if replay := request(mux, http.MethodPut, path, "first", map[string]string{"If-None-Match": "*"}); replay.Code != http.StatusOK {
		t.Fatalf("If-None-Match is not needed and must not break a replay: %d", replay.Code)
	}
	conflict := request(mux, http.MethodPut, path, "second", nil)
	if conflict.Code != http.StatusConflict || problemCode(t, conflict.Body.String()) != "version_file_exists" || !strings.HasPrefix(conflict.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("overwrite=%d %s", conflict.Code, conflict.Body.String())
	}
	if read := request(mux, http.MethodGet, path, "", nil); read.Body.String() != "first" {
		t.Fatalf("a refused overwrite mutated the file: %q", read.Body.String())
	}
	if refused := request(mux, http.MethodPut, path, "first", map[string]string{"If-Match": "*"}); refused.Code != http.StatusBadRequest {
		t.Fatalf("If-Match=%d", refused.Code)
	}
	audits, err := store.ListAudits(context.Background(), repository.AuditQuery{Operation: "raw.version.file.overwrite"})
	if err != nil || len(audits) != 1 || audits[0].Resource != "tool/1.0.0/bin/tool" || audits[0].Repository != "releases" {
		t.Fatalf("overwrite audit: %#v %v", audits, err)
	}
	version, err := store.GetRawVersion(context.Background(), "releases", "tool", "1.0.0")
	if err != nil || version.FileCount != 1 || version.State != repository.RawVersionOpen {
		t.Fatalf("version row: %#v %v", version, err)
	}
}

func TestVersionedRawRejectsPathsOutsideTheLayout(t *testing.T) {
	_, mux := versionedFixture(t, false)
	for _, path := range []string{
		"tool", "tool/1.0.0", "Tool/1.0.0/file", "tool/1.0.0/.notarx/x", "tool/ver%20sion/file", "tool/1.0.0/a/b/c/d/e/f/g/h/i",
		"tool/%C3%A9/file",
	} {
		response := request(mux, http.MethodPut, "/raw/releases/"+path, "x", nil)
		if response.Code != http.StatusUnprocessableEntity || problemCode(t, response.Body.String()) != "invalid_generic_path" {
			t.Errorf("%s: %d %s", path, response.Code, response.Body.String())
		}
		started := request(mux, http.MethodPost, "/raw/releases/"+path+"?resumable=1", "", nil)
		if started.Code != http.StatusUnprocessableEntity {
			t.Errorf("resumable %s: %d", path, started.Code)
		}
	}
	for _, path := range []string{"tool/1.0.0/%E2%80%AEevil", "tool/1.0.0/../2.0.0/file", "tool/1.0.0/%2e%2e/file", "tool//1.0.0/file", "tool/1.0.0/a%2Fb"} {
		if response := request(mux, http.MethodPut, "/raw/releases/"+path, "x", nil); response.Code == http.StatusOK || response.Code == http.StatusCreated {
			t.Errorf("traversal %s accepted: %d", path, response.Code)
		}
	}
}

func TestVersionedRawDeleteRepairsOnlyOpenNonStrictVersions(t *testing.T) {
	store, mux := versionedFixture(t, false)
	const path = "/raw/releases/tool/1.0.0/a"
	request(mux, http.MethodPut, path, "one", nil)
	if response := request(mux, http.MethodDelete, path, "", nil); response.Code != http.StatusNoContent {
		t.Fatalf("delete of an open version=%d", response.Code)
	}
	if put := request(mux, http.MethodPut, path, "repaired", nil); put.Code != http.StatusCreated {
		t.Fatalf("re-upload=%d %s", put.Code, put.Body.String())
	}
	if _, err := store.SealRawVersion(context.Background(), repository.SealRawVersionInput{RepositoryID: "releases", Name: "tool", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, body, code string
	}{
		{http.MethodDelete, path, "", "version_sealed"},
		{http.MethodPut, "/raw/releases/tool/1.0.0/b", "late", "version_sealed"},
		{http.MethodPut, path, "changed", "version_file_exists"},
	} {
		response := request(mux, tc.method, tc.path, tc.body, nil)
		if response.Code != http.StatusConflict || problemCode(t, response.Body.String()) != tc.code {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, response.Code, response.Body.String())
		}
	}
	if replay := request(mux, http.MethodPut, path, "repaired", nil); replay.Code != http.StatusOK {
		t.Fatalf("a replay after seal=%d", replay.Code)
	}
}

func TestVersionedRawStrictRepositoryRefusesFileDelete(t *testing.T) {
	store, mux := versionedFixture(t, true)
	const path = "/raw/releases/tool/1.0.0/a"
	request(mux, http.MethodPut, path, "one", nil)
	response := request(mux, http.MethodDelete, path, "", nil)
	if response.Code != http.StatusConflict || problemCode(t, response.Body.String()) != "version_file_immutable" {
		t.Fatalf("strict delete=%d %s", response.Code, response.Body.String())
	}
	if _, err := store.GetRawAsset(context.Background(), "releases", "tool/1.0.0/a"); err != nil {
		t.Fatalf("the file must survive: %v", err)
	}
}

func TestVersionedRawResumableCompletionObeysCreateOnly(t *testing.T) {
	store, mux := versionedFixture(t, false)
	const path = "/raw/releases/tool/1.0.0/big"
	upload := func(content string) *httptest.ResponseRecorder {
		started := request(mux, http.MethodPost, path+"?resumable=1", "", nil)
		if started.Code != http.StatusCreated {
			t.Fatalf("start=%d %s", started.Code, started.Body.String())
		}
		location := started.Header().Get("Location")
		request(mux, http.MethodPatch, location, content, map[string]string{"Upload-Offset": "0"})
		sum := sha256.Sum256([]byte(content))
		return request(mux, http.MethodPut, location+"&complete=1", "", map[string]string{"Digest": "sha-256=" + base64.StdEncoding.EncodeToString(sum[:])})
	}
	if done := upload("payload"); done.Code != http.StatusCreated {
		t.Fatalf("first completion=%d %s", done.Code, done.Body.String())
	}
	if done := upload("payload"); done.Code != http.StatusOK {
		t.Fatalf("replayed completion=%d %s", done.Code, done.Body.String())
	}
	conflict := upload("other payload")
	if conflict.Code != http.StatusConflict || problemCode(t, conflict.Body.String()) != "version_file_exists" {
		t.Fatalf("conflicting completion=%d %s", conflict.Code, conflict.Body.String())
	}
	// The refusal is final, so the upload is closed and no longer holds the version open against the idle sweep.
	if uploads, err := store.ListUncollectedRawUploads(context.Background(), 100); err != nil {
		t.Fatal(err)
	} else {
		for _, upload := range uploads {
			if upload.State == "open" {
				t.Errorf("an upload refused at completion is still open: %#v", upload)
			}
		}
	}
	if read := request(mux, http.MethodGet, path, "", nil); read.Body.String() != "payload" {
		t.Fatalf("a refused completion mutated the file: %q", read.Body.String())
	}
}

// trackedBody records whether the handler read the request body.
type trackedBody struct {
	reads int
}

func (b *trackedBody) Read([]byte) (int, error) {
	b.reads++
	return 0, io.EOF
}

func TestVersionedRawRefusesBeforeReadingTheBodyWhenTheOutcomeIsKnown(t *testing.T) {
	store, mux := versionedFixture(t, false)
	request(mux, http.MethodPut, "/raw/releases/tool/1.0.0/a", "stored", nil)
	if _, err := store.SealRawVersion(context.Background(), repository.SealRawVersionInput{RepositoryID: "releases", Name: "tool", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	other := sha256.Sum256([]byte("other bytes"))
	cases := []struct {
		name, path, digest, code string
	}{
		{"a new file in a sealed version", "tool/1.0.0/late", "", "version_sealed"},
		{"another digest than the stored file", "tool/1.0.0/a", "sha-256=" + base64.StdEncoding.EncodeToString(other[:]), "version_file_exists"},
	}
	for _, tc := range cases {
		body := &trackedBody{}
		req := httptest.NewRequest(http.MethodPut, "/raw/releases/"+tc.path, body)
		req.Header.Set("Authorization", "Bearer fixture-admin")
		if tc.digest != "" {
			req.Header.Set("Digest", tc.digest)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		if response.Code != http.StatusConflict || problemCode(t, response.Body.String()) != tc.code {
			t.Errorf("%s: %d %s", tc.name, response.Code, response.Body.String())
		}
		if body.reads != 0 {
			t.Errorf("%s: the body was read %d times before the refusal", tc.name, body.reads)
		}
	}
	// A declared digest that matches the stored file is not refused early: the replay is answered after the read.
	same := sha256.Sum256([]byte("stored"))
	if replay := request(mux, http.MethodPut, "/raw/releases/tool/1.0.0/a", "stored", map[string]string{"Digest": "sha-256=" + base64.StdEncoding.EncodeToString(same[:])}); replay.Code != http.StatusOK {
		t.Fatalf("replay=%d %s", replay.Code, replay.Body.String())
	}
}

func TestVersionedRawStoresChecksumNamedFilesAsOrdinaryFiles(t *testing.T) {
	_, mux := versionedFixture(t, false)
	for path, content := range map[string]string{"tool/1.0.0/tool.tar.gz": "archive", "tool/1.0.0/tool.tar.gz.sha256": "abcd  tool.tar.gz\n", "tool/1.0.0/tool.tar.gz.sha512": "ef01  tool.tar.gz\n"} {
		if put := request(mux, http.MethodPut, "/raw/releases/"+path, content, nil); put.Code != http.StatusCreated {
			t.Fatalf("put %s=%d %s", path, put.Code, put.Body.String())
		}
		if got := request(mux, http.MethodGet, "/raw/releases/"+path, "", nil); got.Code != http.StatusOK || got.Body.String() != content {
			t.Fatalf("a file named %s must be served as stored: %d %q", path, got.Code, got.Body.String())
		}
	}
}

func TestPathRepositoriesStillOverwriteByDefault(t *testing.T) {
	_, _, mux := fixture(t)
	for _, content := range []string{"one", "two"} {
		if put := request(mux, http.MethodPut, "/raw/downloads/Any/Path", content, nil); put.Code != http.StatusCreated {
			t.Fatalf("put=%d", put.Code)
		}
	}
	if read := request(mux, http.MethodGet, "/raw/downloads/Any/Path", "", nil); read.Body.String() != "two" {
		t.Fatalf("overwrite lost: %q", read.Body.String())
	}
}
