package huggingface

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func hubWithFiles(t *testing.T) (*fakeHub, string) {
	t.Helper()
	hub := newFakeHub(t)
	sha := commitID(0xabc)
	hub.addCommit(sha, "main", map[string]*hubFile{
		"config.json":       {Content: []byte(`{"a":1}`)},
		"model.safetensors": {Content: []byte(strings.Repeat("w", 1000)), LFS: true},
		"nested/deep/x.bin": {Content: []byte("x"), LFS: true},
	})
	return hub, sha
}

func TestRevisionResolvesRefsAndSendsTheCredentialToTheHubOnly(t *testing.T) {
	hub, sha := hubWithFiles(t)
	upstream := hub.upstream()
	revision, err := upstream.Revision(context.Background(), RepoTypeModel, hubModel, "main")
	if err != nil || revision.SHA != sha || revision.LastModified.Year() != 2026 {
		t.Fatalf("Revision = %+v, %v", revision, err)
	}
	if seen := hub.authSeen("/api/"); len(seen) != 1 || seen[0] != "Bearer upstream-secret" {
		t.Fatalf("hub credential = %v", seen)
	}
	if _, err := upstream.Revision(context.Background(), RepoTypeModel, hubModel, "nope"); !errors.Is(err, &UpstreamError{Code: CodeUpstreamNotFound}) {
		t.Fatalf("unknown revision = %v", err)
	}
	if _, err := upstream.Revision(context.Background(), "space", hubModel, "main"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("space = %v", err)
	}
	if _, err := upstream.Revision(context.Background(), RepoTypeModel, "bad name", "main"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad repo = %v", err)
	}
}

func TestUpstreamStatusMapping(t *testing.T) {
	hub, _ := hubWithFiles(t)
	upstream := hub.upstream()
	hub.setDown(true)
	_, err := upstream.Revision(context.Background(), RepoTypeModel, hubModel, "main")
	var failure *UpstreamError
	if !errors.As(err, &failure) || failure.Code != CodeUpstreamUnavailable || failure.Status != http.StatusServiceUnavailable {
		t.Fatalf("503 = %v", err)
	}
	for status, want := range map[int]string{401: CodeUpstreamGated, 403: CodeUpstreamGated, 404: CodeUpstreamNotFound, 410: CodeUpstreamNotFound, 500: CodeUpstreamUnavailable} {
		if got := upstreamStatusError(status); got.Code != want {
			t.Errorf("status %d = %s, want %s", status, got.Code, want)
		}
	}
	if upstreamStatusError(403).Status != 403 {
		t.Error("a gated response must keep its status so it can be relayed")
	}
	if got := (&UpstreamError{Code: CodeXetOnly}).Error(); got != CodeXetOnly {
		t.Errorf("Error() = %q", got)
	}
}

func TestTreeFollowsPaginationAndSkipsDirectories(t *testing.T) {
	hub, sha := hubWithFiles(t)
	hub.pageSize = 1
	files, err := hub.upstream().Tree(context.Background(), RepoTypeModel, hubModel, sha, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || hub.count("GET /api/models/"+hubModel+"/tree/") != 3 {
		t.Fatalf("files = %d, pages = %d", len(files), hub.count("GET /api/models/"))
	}
	byPath := map[string]File{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	lfs := byPath["model.safetensors"]
	file := hub.commits[sha]["model.safetensors"]
	if !lfs.LFS || lfs.SHA256 != file.sha256() || lfs.Size != 1000 || lfs.GitBlobID != file.gitBlob() {
		t.Fatalf("lfs file = %+v", lfs)
	}
	regular := byPath["config.json"]
	if regular.LFS || regular.SHA256 != "" || regular.GitBlobID == "" || regular.Size != 7 {
		t.Fatalf("regular file = %+v", regular)
	}
	if _, ok := byPath["dir"]; ok {
		t.Fatal("a directory entry became a file")
	}
}

func TestTreeRefusesAnOversizedTreeBeforeReadingTheRest(t *testing.T) {
	hub, sha := hubWithFiles(t)
	hub.pageSize = 1
	_, err := hub.upstream().Tree(context.Background(), RepoTypeModel, hubModel, sha, 2)
	if !errors.Is(err, ErrTreeTooLarge) {
		t.Fatalf("err = %v, want ErrTreeTooLarge", err)
	}
	var failure *UpstreamError
	if !errors.As(err, &failure) || failure.Code != "upstream_tree_too_large" {
		t.Fatalf("code = %v", err)
	}
	if pages := hub.count("GET /api/models/" + hubModel + "/tree/"); pages != 3 {
		t.Fatalf("read %d pages; the third entry is the first over the limit, so a fourth page must not be read", pages)
	}
	if _, err := hub.upstream().Tree(context.Background(), RepoTypeModel, hubModel, "main", 10); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a branch name is not a tree revision: %v", err)
	}
}

func TestTreeRejectsLongPathsAndForeignPaginationLinks(t *testing.T) {
	hub := newFakeHub(t)
	sha := commitID(7)
	hub.addCommit(sha, "main", map[string]*hubFile{strings.Repeat("a", MaxPathLength+1): {Content: []byte("x")}})
	_, err := hub.upstream().Tree(context.Background(), RepoTypeModel, hubModel, sha, 10)
	var failure *UpstreamError
	if !errors.As(err, &failure) || failure.Code != CodePathTooLong {
		t.Fatalf("long path = %v", err)
	}
	origin, _ := url.Parse("https://example.com")
	header := http.Header{"Link": {`<https://evil.example/next>; rel="next"`}}
	if _, err := nextLink(header, origin); err == nil {
		t.Fatal("a pagination link to another origin was followed with the upstream credential")
	}
	if next, err := nextLink(http.Header{"Link": {`</a?x=1>; rel="prev", </b>; rel="next"`}}, origin); err != nil || next != "https://example.com/b" {
		t.Fatalf("relative next = %q, %v", next, err)
	}
	if next, _ := nextLink(http.Header{}, origin); next != "" {
		t.Fatalf("no link = %q", next)
	}
}

func TestHeadCapturesMetadataWithoutFollowingTheRedirect(t *testing.T) {
	hub, sha := hubWithFiles(t)
	meta, err := hub.upstream().Head(context.Background(), RepoTypeModel, hubModel, "main", "model.safetensors")
	if err != nil {
		t.Fatal(err)
	}
	file := hub.commits[sha]["model.safetensors"]
	if meta.Status != 302 || meta.Commit != sha || meta.LinkedETag != file.sha256() || meta.LinkedSize != 1000 || meta.XetOnly || !strings.HasPrefix(meta.Location, "https://example.com/cdn/") {
		t.Fatalf("meta = %+v", meta)
	}
	if hub.count("HEAD /cdn/") != 0 {
		t.Fatal("HEAD followed the redirect")
	}
	regular, err := hub.upstream().Head(context.Background(), RepoTypeModel, hubModel, sha, "config.json")
	if err != nil || regular.Status != 200 || regular.ETag != hub.commits[sha]["config.json"].gitBlob() || regular.Size != 7 {
		t.Fatalf("regular = %+v, %v", regular, err)
	}
	if _, err := hub.upstream().Head(context.Background(), RepoTypeModel, hubModel, sha, "missing.bin"); !errors.Is(err, &UpstreamError{Code: CodeUpstreamNotFound}) {
		t.Fatalf("missing = %v", err)
	}
	if _, err := hub.upstream().Head(context.Background(), RepoTypeModel, hubModel, sha, "../etc/passwd"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("path traversal = %v", err)
	}
}

func TestHeadDetectsAXetOnlyFile(t *testing.T) {
	hub := newFakeHub(t)
	sha := commitID(9)
	hub.addCommit(sha, "main", map[string]*hubFile{
		"bridged.bin": {Content: []byte("bridge"), LFS: true, Xet: true},
		"xet.bin":     {Content: []byte("xet"), LFS: true, Xet: true, XetOnly: true},
	})
	bridged, err := hub.upstream().Head(context.Background(), RepoTypeModel, hubModel, sha, "bridged.bin")
	if err != nil || bridged.XetOnly || bridged.Location == "" {
		t.Fatalf("a Xet file with a plain redirect is served over the bridge: %+v, %v", bridged, err)
	}
	only, err := hub.upstream().Head(context.Background(), RepoTypeModel, hubModel, sha, "xet.bin")
	if err != nil || !only.XetOnly {
		t.Fatalf("a Xet file without a plain route must be flagged: %+v, %v", only, err)
	}
}

func TestOpenFollowsOneRedirectWithoutTheCredential(t *testing.T) {
	hub, sha := hubWithFiles(t)
	response, err := hub.upstream().Open(context.Background(), RepoTypeModel, hubModel, sha, "model.safetensors", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != strings.Repeat("w", 1000) {
		t.Fatalf("body = %d bytes", len(body))
	}
	if seen := hub.authSeen("/example.com"); len(seen) != 0 {
		t.Fatalf("unexpected %v", seen)
	}
	for _, value := range hub.authSeen("/cdn/") {
		if value != "" {
			t.Fatalf("the upstream credential reached the CDN: %q", value)
		}
	}
	if seen := hub.authSeen("/" + hubModel + "/resolve/"); len(seen) == 0 || seen[0] != "Bearer upstream-secret" {
		t.Fatalf("the hub did not get the credential: %v", seen)
	}
	regular, err := hub.upstream().Open(context.Background(), RepoTypeModel, hubModel, sha, "config.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	regular.Body.Close()
}

func TestOpenRejectsADisallowedHostAndASecondRedirect(t *testing.T) {
	hub, sha := hubWithFiles(t)
	upstream := hub.upstream()
	upstream.CDNAllowlist = []string{"cdn.other.example"}
	_, err := upstream.Open(context.Background(), RepoTypeModel, hubModel, sha, "model.safetensors", nil)
	var failure *UpstreamError
	if !errors.As(err, &failure) || failure.Code != CodeRedirectRejected {
		t.Fatalf("allowlist miss = %v", err)
	}
	upstream.CDNAllowlist = nil
	if _, err := upstream.Open(context.Background(), RepoTypeModel, hubModel, sha, "model.safetensors", nil); !errors.As(err, &failure) || failure.Code != CodeRedirectRejected {
		t.Fatalf("an overridden upstream with no allowlist must follow no redirect: %v", err)
	}
	if hub.count("GET /cdn/") != 0 {
		t.Fatal("a rejected redirect was fetched")
	}
}

func TestOpenMapsUpstreamStatuses(t *testing.T) {
	hub, sha := hubWithFiles(t)
	if _, err := hub.upstream().Open(context.Background(), RepoTypeModel, hubModel, sha, "gone.bin", nil); !errors.Is(err, &UpstreamError{Code: CodeUpstreamNotFound}) {
		t.Fatalf("missing = %v", err)
	}
	hub.setDown(true)
	if _, err := hub.upstream().Open(context.Background(), RepoTypeModel, hubModel, sha, "config.json", nil); !errors.Is(err, &UpstreamError{Code: CodeUpstreamUnavailable}) {
		t.Fatalf("down = %v", err)
	}
}

func TestResolvePathEscapesAndSelectsTheDatasetPrefix(t *testing.T) {
	path, err := resolvePath(RepoTypeDataset, "acme/data", "main", "a b/c#d.txt")
	if err != nil || path != "/datasets/acme/data/resolve/main/a%20b/c%23d.txt" {
		t.Fatalf("path = %q, %v", path, err)
	}
	if path, _ := resolvePath(RepoTypeModel, "acme/w", "v1.0", "x"); path != "/acme/w/resolve/v1.0/x" {
		t.Fatalf("model path = %q", path)
	}
	for _, rev := range []string{"", "a?b", "a#b"} {
		if _, err := resolvePath(RepoTypeModel, "acme/w", rev, "x"); err == nil {
			t.Errorf("revision %q accepted", rev)
		}
	}
	if _, err := resolvePath("space", "acme/w", "main", "x"); err == nil {
		t.Error("space accepted")
	}
}

func TestUpstreamRefusesAPrivateEndpoint(t *testing.T) {
	hub, _ := hubWithFiles(t)
	upstream := hub.upstream()
	upstream.Hooks.LookupIP = func(context.Context, string, string) ([]net.IP, error) { return []net.IP{net.ParseIP("10.0.0.1")}, nil }
	_, err := upstream.Revision(context.Background(), RepoTypeModel, hubModel, "main")
	if err == nil || !strings.Contains(fmt.Sprint(err), "private address") {
		t.Fatalf("private upstream = %v", err)
	}
}
