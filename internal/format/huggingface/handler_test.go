package huggingface

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

func TestParseRoute(t *testing.T) {
	good := []struct {
		path string
		want route
	}{
		{"/hf/hub/acme/widget/resolve/main/config.json", route{repository: "hub", repoType: RepoTypeModel, repoName: "acme/widget", rev: "main", path: "config.json"}},
		{"/hf/hub/datasets/acme/data/resolve/" + commitSHA + "/train/part-0.parquet", route{repository: "hub", repoType: RepoTypeDataset, repoName: "acme/data", rev: commitSHA, path: "train/part-0.parquet"}},
		{"/hf/hub/acme/widget/resolve/refs%2Fpr%2F1/a%20b.txt", route{repository: "hub", repoType: RepoTypeModel, repoName: "acme/widget", rev: "refs/pr/1", path: "a b.txt"}},
		{"/hf/hub/acme/widget/resolve/v1.0/deep/er/file.bin", route{repository: "hub", repoType: RepoTypeModel, repoName: "acme/widget", rev: "v1.0", path: "deep/er/file.bin"}},
	}
	for _, tc := range good {
		got, code, ok := parseRoute(tc.path)
		if !ok || code != "" || got != tc.want {
			t.Errorf("%s = %+v %q %v, want %+v", tc.path, got, code, ok, tc.want)
		}
	}
	bad := []struct{ path, code string }{
		{"/other/hub/acme/widget/resolve/main/x", codeNotFound},
		{"/hf/", codeNotFound},
		{"/hf/hub", codeNotFound},
		{"/hf//acme/widget/resolve/main/x", codeNotFound},
		{"/hf/hub/spaces/acme/app/resolve/main/app.py", codeUnsupportedType},
		{"/hf/hub/api/spaces/acme/app/revision/main", codeUnsupportedType},
		{"/hf/hub/api/models/acme/widget/xet-read-token/main", codeXetUnsupported},
		{"/hf/hub/api/models/acme/widget", codeNotFound},
		{"/hf/hub/acme/widget/blob/main/x", codeNotFound},
		{"/hf/hub/acme/widget/resolve/main", codeNotFound},
		{"/hf/hub/acme/widget/resolve//x", codeNotFound},
		{"/hf/hub/acme/widget/resolve/main/%2e%2e/x", codeNotFound},
		{"/hf/hub/acme/widget/resolve/main/a%00b", codeNotFound},
		{"/hf/hub/acme/widget/resolve/main/a%zz", codeNotFound},
		{"/hf/hub/acme/widget/resolve/main/a%5Cb", codeNotFound},
		{"/hf/hub/-acme/widget/resolve/main/x", codeNotFound},
		{"/hf/hub/acme/resolve/main/x/y", codeNotFound},
	}
	for _, tc := range bad {
		if got, code, ok := parseRoute(tc.path); ok || code != tc.code {
			t.Errorf("%s = %+v %q %v, want rejection %q", tc.path, got, code, ok, tc.code)
		}
	}
}

const digestSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestFileHeadersNameTheCommitAndNeverCopyUpstreamHeaders(t *testing.T) {
	commit := Commit{SHA: commitSHA}
	lfs := fileHeaders(commit, File{LFS: true, SHA256: digestSHA, GitBlobID: commitSHA, Size: 9})
	if lfs.Get("X-Repo-Commit") != commitSHA || lfs.Get("ETag") != `"`+digestSHA+`"` || lfs.Get("X-Linked-Etag") != lfs.Get("ETag") || lfs.Get("X-Linked-Size") != "9" {
		t.Fatalf("lfs headers = %v", lfs)
	}
	regular := fileHeaders(commit, File{GitBlobID: commitSHA, Size: 3})
	if regular.Get("ETag") != `"`+commitSHA+`"` || regular.Get("X-Linked-Etag") != "" || regular.Get("X-Linked-Size") != "" {
		t.Fatalf("regular headers = %v", regular)
	}
	for name := range lfs {
		if len(name) >= 5 && name[:5] == "X-Xet" {
			t.Fatalf("Xet header %s", name)
		}
	}
}

func TestHubErrorShape(t *testing.T) {
	recorder := newRecorder()
	if status := hubError(recorder, 404, "EntryNotFound", "Entry Not Found"); status != 404 {
		t.Fatalf("status = %d", status)
	}
	if recorder.Code != 404 || recorder.Header().Get("X-Error-Code") != "EntryNotFound" || recorder.Body.String() != "{\"error\":\"Entry Not Found\"}\n" {
		t.Fatalf("response = %d %v %q", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestAuditOutcomeClassifiesStatuses(t *testing.T) {
	for status, want := range map[int]repository.AuditOutcome{
		200: repository.AuditResolved, 206: repository.AuditResolved, 416: repository.AuditResolved,
		400: repository.AuditProxyDenied, 405: repository.AuditProxyDenied, 413: repository.AuditProxyDenied,
		401: repository.AuditAccessDenied, 403: repository.AuditAccessDenied,
		404: repository.AuditNotFound,
		500: repository.AuditStorageError,
		502: repository.AuditUpstreamError, 503: repository.AuditUpstreamError, 504: repository.AuditUpstreamError, 507: repository.AuditUpstreamError,
	} {
		if got := auditOutcome(status); got != want {
			t.Errorf("auditOutcome(%d) = %q, want %q", status, got, want)
		}
	}
}

func TestMissCacheIsBoundedAndExpires(t *testing.T) {
	var cache missCache
	if cache.has("a") {
		t.Fatal("an empty cache reported a miss")
	}
	cache.add("a")
	if !cache.has("a") || cache.has("b") {
		t.Fatal("a recorded miss must be found and only that one")
	}
	cache.mu.Lock()
	cache.entries["a"] = time.Now().Add(-time.Second)
	cache.mu.Unlock()
	if cache.has("a") {
		t.Fatal("an expired miss was reported")
	}
	for i := 0; i < missCapacity+10; i++ {
		cache.add(string(rune('a'+i%26)) + time.Duration(i).String())
	}
	cache.mu.Lock()
	size := len(cache.entries)
	cache.mu.Unlock()
	if size > missCapacity {
		t.Fatalf("the cache grew to %d entries, above its bound %d", size, missCapacity)
	}
}

func TestReadProtocolRequiresPostgreSQL(t *testing.T) {
	_, err := newReader(ReadOptions{}, NewStore(nil, MySQL), nil)
	if !errors.Is(err, ErrDialectUnsupported) {
		t.Fatalf("err = %v, want ErrDialectUnsupported", err)
	}
	module := &Module{store: NewStore(nil, MySQL)}
	if err := module.EnableRead(ReadOptions{}); !errors.Is(err, ErrDialectUnsupported) {
		t.Fatalf("EnableRead = %v", err)
	}
	if routes := module.Routes(); len(routes) != 0 {
		t.Fatalf("a module whose read protocol is off mounted %d routes", len(routes))
	}
}

func TestFlightKeyKeepsRepositoriesApart(t *testing.T) {
	lfs := File{SHA256: digestSHA}
	a := flightKey(Object{RepositoryID: "repo-a", CommitID: "c1", Path: "w.bin"}, lfs)
	b := flightKey(Object{RepositoryID: "repo-b", CommitID: "c1", Path: "w.bin"}, lfs)
	same := flightKey(Object{RepositoryID: "repo-a", CommitID: "c2", Path: "other.bin"}, lfs)
	if a == b {
		t.Fatal("two repositories share a flight: the joiner would inherit the other's quota and credential")
	}
	if a != same {
		t.Fatal("one repository must share a flight per digest across commits and paths")
	}
	regular := File{}
	if flightKey(Object{RepositoryID: "repo-a", CommitID: "c1", Path: "a"}, regular) == flightKey(Object{RepositoryID: "repo-a", CommitID: "c1", Path: "b"}, regular) {
		t.Fatal("regular files without a digest must be keyed by their path")
	}
}

func TestHeaderWriterAppliesHeadersOnTheFirstWriteOnly(t *testing.T) {
	recorder := httptest.NewRecorder()
	applied := 0
	writer := &headerWriter{ResponseWriter: recorder, apply: func() { applied++; recorder.Header().Set("X-Repo-Commit", "abc") }}
	if recorder.Header().Get("X-Repo-Commit") != "" {
		t.Fatal("headers applied before a write")
	}
	writer.WriteHeader(206)
	_, _ = writer.Write([]byte("x"))
	if applied != 1 || recorder.Header().Get("X-Repo-Commit") != "abc" || recorder.Code != 206 {
		t.Fatalf("applied %d times, code %d, headers %v", applied, recorder.Code, recorder.Header())
	}
	if writer.Unwrap() != recorder {
		t.Fatal("Unwrap must reach the underlying writer for http.ResponseController")
	}
}

func TestFailMapsErrorsToStableResponses(t *testing.T) {
	rd := &reader{}
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{ErrFetchInProgress, 503, "fetch_in_progress"},
		{ErrFetchCapacity, 503, "fetch_capacity"},
		{ErrUnverifiable, 502, "upstream_invalid_response"},
		{ErrDigestMismatch, 502, "upstream_digest_mismatch"},
		{ErrObjectTooLarge, 413, "object_too_large"},
		{ErrXetOnly, 502, "upstream_xet_only"},
		{ErrTreeTooLarge, 502, "upstream_tree_too_large"},
		{&UpstreamError{Code: CodeUpstreamGated, Status: 403}, 403, "upstream_gated"},
		{&UpstreamError{Code: CodeUpstreamUnavailable}, 504, "upstream_unavailable"},
		{errors.New("boom"), 500, "internal_error"},
	}
	for _, tc := range cases {
		recorder := httptest.NewRecorder()
		if status := rd.fail(recorder, tc.err); status != tc.status || recorder.Code != tc.status || recorder.Header().Get("X-Error-Code") != tc.code {
			t.Errorf("%v = %d %q, want %d %q", tc.err, status, recorder.Header().Get("X-Error-Code"), tc.status, tc.code)
		}
	}
	recorder := httptest.NewRecorder()
	rd.fail(recorder, ErrFetchInProgress)
	if recorder.Header().Get("Retry-After") == "" {
		t.Error("a busy answer must tell the client when to retry")
	}
}

func TestFailWritesNothingForAClientThatWentAway(t *testing.T) {
	recorder := httptest.NewRecorder()
	if status := (&reader{}).fail(recorder, context.Canceled); status != statusClientClosed {
		t.Fatalf("status = %d", status)
	}
	if recorder.Body.Len() != 0 || recorder.Header().Get("X-Error-Code") != "" {
		t.Fatal("an error body was written to a client that is gone")
	}
	if auditOutcome(statusClientClosed) != repository.AuditResolved {
		t.Fatal("an abandoned download is not an upstream error")
	}
}
