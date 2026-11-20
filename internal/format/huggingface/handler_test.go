package huggingface

import (
	"net/http/httptest"
	"testing"
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
