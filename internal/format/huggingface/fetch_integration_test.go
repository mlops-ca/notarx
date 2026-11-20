//go:build integration

package huggingface

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/capacity"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/spool"
)

type fetchEnv struct {
	f       *fixture
	hub     *fakeHub
	sha     string
	repo    Repo
	commit  Commit
	objects *objectstore.MemoryStore
	fetcher *Fetcher
	server  *httptest.Server
	tmp     string
	metrics *recordingMetrics
}

type recordingMetrics struct {
	mu       sync.Mutex
	outcomes map[string]int
}

func (m *recordingMetrics) RecordHuggingFaceFetch(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.outcomes == nil {
		m.outcomes = map[string]int{}
	}
	m.outcomes[outcome]++
}

func (m *recordingMetrics) count(outcome string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.outcomes[outcome]
}

// newFetchEnv ingests the tree of the fake hub commit and serves objects through a real HTTP
// server, so the tests see what a client sees: streaming, ranges and aborted bodies.
func newFetchEnv(t *testing.T, files map[string]*hubFile) *fetchEnv {
	t.Helper()
	f := newFixture(t)
	f.requirePostgres()
	hub := newFakeHub(t)
	sha := commitID(0x77)
	hub.addCommit(sha, "main", files)
	ingest, repo, _ := newIngest(t, f, hub)
	resolved, err := ingest.ResolveRevision(context.Background(), repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := ingest.EnsureTree(context.Background(), repo, resolved.Commit)
	if err != nil {
		t.Fatal(err)
	}
	env := &fetchEnv{f: f, hub: hub, sha: sha, repo: repo, commit: commit, objects: objectstore.NewMemoryStore(), tmp: t.TempDir(), metrics: &recordingMetrics{}}
	t.Setenv("TMPDIR", env.tmp)
	env.fetcher = NewFetcher(f.store, env.objects, DefaultConfig())
	env.fetcher.HoldBack = 16
	env.fetcher.Metrics = env.metrics
	upstream := hub.upstream()
	env.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("p")
		file, err := f.store.GetFile(r.Context(), commit.ID, path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		object := Object{RepositoryID: f.repositoryID, HFRepoID: repo.ID, CommitID: commit.ID, Path: path, Size: file.Size,
			Open: func(ctx context.Context) (*http.Response, error) {
				return upstream.Open(ctx, RepoTypeModel, hubModel, sha, path, nil)
			}}
		etag := file.SHA256
		if etag == "" {
			etag = file.GitBlobID
		}
		written, err := env.fetcher.Serve(w, r, object, http.Header{"ETag": {`"` + etag + `"`}, "Content-Type": {"application/octet-stream"}})
		if err != nil {
			if written {
				panic(http.ErrAbortHandler)
			}
			switch {
			case errors.Is(err, ErrFetchCapacity), errors.Is(err, ErrFetchInProgress):
				w.Header().Set("Retry-After", "5")
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
			case errors.Is(err, capacity.ErrQuotaExceeded):
				http.Error(w, err.Error(), http.StatusInsufficientStorage)
			case errors.Is(err, ErrObjectTooLarge):
				http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			default:
				http.Error(w, err.Error(), http.StatusBadGateway)
			}
		}
	}))
	t.Cleanup(env.server.Close)
	return env
}

func (e *fetchEnv) get(t *testing.T, path string, header http.Header) (*http.Response, []byte, error) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, e.server.URL+"/?p="+path, nil)
	for name, values := range header {
		request.Header[name] = values
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	return response, body, err
}

func (e *fetchEnv) requireNoStaging(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, _ := os.ReadDir(e.tmp)
		var left []string
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "notarx-hf-") {
				left = append(left, entry.Name())
			}
		}
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("staging files left behind: %v", left)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func weights(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte('a' + i%26)
	}
	return out
}

func TestColdFetchVerifiesCommitsAndThenServesFromTheStore(t *testing.T) {
	content := weights(5000)
	env := newFetchEnv(t, map[string]*hubFile{"model.safetensors": {Content: content, LFS: true}, "config.json": {Content: []byte(`{"k":1}`)}})
	response, body, err := env.get(t, "model.safetensors", nil)
	if err != nil || response.StatusCode != 200 || string(body) != string(content) || response.Header.Get("Accept-Ranges") != "bytes" || response.ContentLength != 5000 {
		t.Fatalf("cold GET = %v %v len %d err %v", response.StatusCode, response.Header, len(body), err)
	}
	file, _ := env.f.store.GetFile(context.Background(), env.commit.ID, "model.safetensors")
	if !file.Cached || file.SHA256 == "" {
		t.Fatalf("the file is not cached after the fetch: %+v", file)
	}
	if _, err := env.objects.Stat(context.Background(), BlobKey(file.SHA256)); err != nil {
		t.Fatalf("object missing: %v", err)
	}
	if bytes, objects := env.f.counter(); bytes != 5000 || objects != 1 {
		t.Fatalf("counter = %d/%d, want 5000/1", bytes, objects)
	}
	for range 3 {
		if _, again, err := env.get(t, "model.safetensors", nil); err != nil || string(again) != string(content) {
			t.Fatalf("warm GET: %v", err)
		}
	}
	if env.hub.count("GET /cdn/") != 1 {
		t.Fatalf("CDN requests = %d, want exactly one for four client requests", env.hub.count("GET /cdn/"))
	}
	if env.metrics.count(FetchFetched) != 1 || env.metrics.count(FetchHit) != 3 {
		t.Fatalf("outcomes = %v", env.metrics.outcomes)
	}
	// A regular file is verified by its git blob id and cached under its computed sha256.
	if _, body, err := env.get(t, "config.json", nil); err != nil || string(body) != `{"k":1}` {
		t.Fatalf("regular file: %q, %v", body, err)
	}
	regular, _ := env.f.store.GetFile(context.Background(), env.commit.ID, "config.json")
	if !regular.Cached || regular.SHA256 == "" {
		t.Fatalf("regular file = %+v", regular)
	}
	env.requireNoStaging(t)
}

func TestTeeServesTheFirstClientWhileTheObjectIsStillStaging(t *testing.T) {
	content := weights(2000)
	env := newFetchEnv(t, map[string]*hubFile{"model.safetensors": {Content: content, LFS: true}})
	gate := make(chan struct{})
	env.hub.mu.Lock()
	env.hub.cdnSlow = gate
	env.hub.mu.Unlock()
	request, _ := http.NewRequest(http.MethodGet, env.server.URL+"/?p=model.safetensors", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	// The CDN sent half and is blocked: the client already has bytes, but not the withheld tail.
	got := make([]byte, 900)
	if _, err := io.ReadFull(response.Body, got); err != nil {
		t.Fatalf("no streaming before the fetch finished: %v", err)
	}
	if string(got) != string(content[:900]) {
		t.Fatal("streamed bytes differ")
	}
	if file, _ := env.f.store.GetFile(context.Background(), env.commit.ID, "model.safetensors"); file.Cached {
		t.Fatal("the object was committed before the digest was verified")
	}
	close(gate)
	rest, err := io.ReadAll(response.Body)
	if err != nil || string(got)+string(rest) != string(content) {
		t.Fatalf("rest = %d bytes, %v", len(rest), err)
	}
}

func TestRangeOnAnInFlightObjectIsServedFromStaging(t *testing.T) {
	content := weights(2000)
	env := newFetchEnv(t, map[string]*hubFile{"model.safetensors": {Content: content, LFS: true}})
	gate := make(chan struct{})
	env.hub.mu.Lock()
	env.hub.cdnSlow = gate
	env.hub.mu.Unlock()
	leader, _ := http.NewRequest(http.MethodGet, env.server.URL+"/?p=model.safetensors", nil)
	leaderResponse, err := http.DefaultClient.Do(leader)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = leaderResponse.Body.Close() }()
	ranged, body, err := env.get(t, "model.safetensors", http.Header{"Range": {"bytes=100-199"}})
	if err != nil || ranged.StatusCode != http.StatusPartialContent || string(body) != string(content[100:200]) || ranged.Header.Get("Content-Range") != "bytes 100-199/2000" {
		t.Fatalf("range = %v %v %q %v", ranged.StatusCode, ranged.Header, body, err)
	}
	if env.hub.count("GET /cdn/") != 1 {
		t.Fatalf("CDN requests = %d: a range on an in-flight object must join the fetch", env.hub.count("GET /cdn/"))
	}
	close(gate)
	if _, err := io.Copy(io.Discard, leaderResponse.Body); err != nil {
		t.Fatal(err)
	}
}

func TestDigestMismatchNeverReachesTheCacheOrTheClient(t *testing.T) {
	content := weights(3000)
	env := newFetchEnv(t, map[string]*hubFile{"model.safetensors": {Content: content, LFS: true, Corrupt: true}, "config.json": {Content: weights(40), Corrupt: true}})
	for _, path := range []string{"model.safetensors", "config.json"} {
		response, body, err := env.get(t, path, nil)
		if err == nil && response.StatusCode == 200 && len(body) == int(response.ContentLength) {
			t.Fatalf("%s: a client got a complete body for corrupt bytes", path)
		}
		file, _ := env.f.store.GetFile(context.Background(), env.commit.ID, path)
		if file.Cached {
			t.Fatalf("%s: corrupt bytes were cached", path)
		}
	}
	if has, _ := env.f.store.HasBlob(context.Background(), env.repo.ID, sha256Of(content)); has {
		t.Fatal("a blob row exists for corrupt bytes")
	}
	if objects, _ := env.objects.List(context.Background(), "huggingface/"); len(objects) != 0 {
		t.Fatalf("objects = %v", objects)
	}
	if bytes, objects := env.f.counter(); bytes != 0 || objects != 0 {
		t.Fatalf("counter = %d/%d", bytes, objects)
	}
	if env.metrics.count(FetchDigestMismatch) < 2 {
		t.Fatalf("outcomes = %v", env.metrics.outcomes)
	}
	// The failure is not remembered: once upstream is right, the object is fetched and cached.
	env.hub.mu.Lock()
	env.hub.commits[env.sha]["model.safetensors"].Corrupt = false
	env.hub.mu.Unlock()
	if _, body, err := env.get(t, "model.safetensors", nil); err != nil || string(body) != string(content) {
		t.Fatalf("retry after the upstream was repaired: %v", err)
	}
	env.requireNoStaging(t)
}

func TestTruncatedUpstreamFailsWithoutCaching(t *testing.T) {
	env := newFetchEnv(t, map[string]*hubFile{"model.safetensors": {Content: weights(4000), LFS: true}})
	env.hub.mu.Lock()
	env.hub.failAfter = 1000
	env.hub.mu.Unlock()
	response, body, err := env.get(t, "model.safetensors", nil)
	if err == nil && response.StatusCode == 200 && len(body) == 4000 {
		t.Fatal("a truncated upstream produced a complete body")
	}
	if bytes, objects := env.f.counter(); bytes != 0 || objects != 0 {
		t.Fatalf("a failed fetch changed the counters: %d/%d", bytes, objects)
	}
	env.requireNoStaging(t)
}

func TestRangesAreServedFromTheStoreOnceCached(t *testing.T) {
	content := weights(3000)
	env := newFetchEnv(t, map[string]*hubFile{"model.safetensors": {Content: content, LFS: true}})
	if _, _, err := env.get(t, "model.safetensors", nil); err != nil {
		t.Fatal(err)
	}
	file, _ := env.f.store.GetFile(context.Background(), env.commit.ID, "model.safetensors")
	etag := `"` + file.SHA256 + `"`
	cases := []struct {
		name   string
		header http.Header
		status int
		body   string
		rng    string
	}{
		{"single", http.Header{"Range": {"bytes=0-9"}}, 206, string(content[:10]), "bytes 0-9/3000"},
		{"suffix", http.Header{"Range": {"bytes=-5"}}, 206, string(content[2995:]), "bytes 2995-2999/3000"},
		{"open ended", http.Header{"Range": {"bytes=2990-"}}, 206, string(content[2990:]), "bytes 2990-2999/3000"},
		{"if-range match", http.Header{"Range": {"bytes=1-2"}, "If-Range": {etag}}, 206, string(content[1:3]), "bytes 1-2/3000"},
		{"if-range mismatch serves everything", http.Header{"Range": {"bytes=1-2"}, "If-Range": {`"other"`}}, 200, string(content), ""},
		{"unsatisfiable", http.Header{"Range": {"bytes=5000-6000"}}, 416, "", "bytes */3000"},
		{"garbage", http.Header{"Range": {"bytes=a-b"}}, 416, "", "bytes */3000"},
	}
	for _, tc := range cases {
		response, body, err := env.get(t, "model.safetensors", tc.header)
		if err != nil || response.StatusCode != tc.status || (tc.status != 416 && string(body) != tc.body) || response.Header.Get("Content-Range") != tc.rng {
			t.Errorf("%s: %d %q %v", tc.name, response.StatusCode, response.Header.Get("Content-Range"), err)
		}
	}
	response, body, err := env.get(t, "model.safetensors", http.Header{"Range": {"bytes=0-3,10-13"}})
	if err != nil || response.StatusCode != 206 || !strings.HasPrefix(response.Header.Get("Content-Type"), "multipart/byteranges") || !strings.Contains(string(body), string(content[:4])) || !strings.Contains(string(body), string(content[10:14])) {
		t.Fatalf("multi range = %d %v", response.StatusCode, err)
	}
	if env.hub.count("GET /cdn/") != 1 {
		t.Fatal("ranges went upstream")
	}
}

func TestConcurrentFetchesAtTheQuotaEdgeRejectExactlyTheOverflow(t *testing.T) {
	files := map[string]*hubFile{}
	for i := 0; i < 8; i++ {
		files[fmt.Sprintf("f%d.bin", i)] = &hubFile{Content: []byte(fmt.Sprintf("payload-%02d", i)), LFS: true} // 10 bytes each
	}
	env := newFetchEnv(t, files)
	env.fetcher.Slots = spool.NewSlots(16) // the slot limit has its own test; here every fetch must run
	if err := capacity.SetQuota(context.Background(), env.f.db, env.f.repositoryID, 50); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, body, err := env.get(t, fmt.Sprintf("f%d.bin", i), nil)
			if err != nil || response.StatusCode != 200 || len(body) != 10 {
				t.Logf("f%d: %v %v %q", i, err, response, body)
			}
			results <- err == nil && response.StatusCode == 200 && len(body) == 10
		}()
	}
	wg.Wait()
	close(results)
	ok := 0
	for success := range results {
		if success {
			ok++
		}
	}
	if ok != 5 {
		t.Fatalf("%d clients succeeded, want exactly 5 at a quota of 50 bytes", ok)
	}
	if bytes, objects := env.f.counter(); bytes != 50 || objects != 5 {
		t.Fatalf("counter = %d/%d, want 50/5", bytes, objects)
	}
	stored, _ := env.objects.List(context.Background(), "huggingface/")
	if len(stored) != 5 {
		t.Fatalf("%d objects stored, want 5: an over-quota object must be removed again", len(stored))
	}
	env.requireNoStaging(t)
}

func TestQuotaIsCheckedBeforeTheFetchStarts(t *testing.T) {
	env := newFetchEnv(t, map[string]*hubFile{"big.bin": {Content: weights(100), LFS: true}})
	if err := capacity.SetQuota(context.Background(), env.f.db, env.f.repositoryID, 50); err != nil {
		t.Fatal(err)
	}
	response, _, _ := env.get(t, "big.bin", nil)
	if response.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want the standard quota error", response.StatusCode)
	}
	if env.hub.count("GET /cdn/") != 0 {
		t.Fatal("an object that cannot fit was fetched")
	}
}

func TestObjectSizeLimitsApplyPerDeploymentAndPerRepository(t *testing.T) {
	env := newFetchEnv(t, map[string]*hubFile{"a.bin": {Content: weights(100), LFS: true}})
	env.fetcher.Config.MaxObjectBytes = 99
	if response, _, _ := env.get(t, "a.bin", nil); response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("deployment limit: %d", response.StatusCode)
	}
	env.fetcher.Config.MaxObjectBytes = 1 << 30
	if err := env.f.store.SetMaxObjectBytes(context.Background(), env.f.repositoryID, 50); err != nil {
		t.Fatal(err)
	}
	if limit, _ := env.f.store.MaxObjectBytes(context.Background(), env.f.repositoryID); limit != 50 {
		t.Fatalf("stored limit = %d", limit)
	}
	if response, _, _ := env.get(t, "a.bin", nil); response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("repository limit: %d", response.StatusCode)
	}
	if err := env.f.store.SetMaxObjectBytes(context.Background(), env.f.repositoryID, 0); err != nil {
		t.Fatal(err)
	}
	if response, _, err := env.get(t, "a.bin", nil); err != nil || response.StatusCode != 200 {
		t.Fatalf("a cleared repository limit: %v %v", response, err)
	}
	if env.hub.count("GET /cdn/") != 1 {
		t.Fatal("a refused object was fetched")
	}
	if err := env.f.store.SetMaxObjectBytes(context.Background(), "not-a-uuid", 5); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad repository id = %v", err)
	}
}

func TestColdFetchesAreBoundedPerProcess(t *testing.T) {
	env := newFetchEnv(t, map[string]*hubFile{"a.bin": {Content: weights(100), LFS: true}, "b.bin": {Content: weights(101), LFS: true}})
	env.fetcher.Slots = nil
	env.fetcher.Config.MaxConcurrentFetches = 1
	gate := make(chan struct{})
	env.hub.mu.Lock()
	env.hub.cdnSlow = gate
	env.hub.mu.Unlock()
	first, err := http.Get(env.server.URL + "/?p=a.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Body.Close() }()
	second, _, _ := env.get(t, "b.bin", nil)
	if second.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second cold fetch = %d, want 503 while the only slot is busy", second.StatusCode)
	}
	close(gate)
	if _, err := io.Copy(io.Discard, first.Body); err != nil {
		t.Fatal(err)
	}
	if env.metrics.count(FetchCapacity) != 1 {
		t.Fatalf("outcomes = %v", env.metrics.outcomes)
	}
}

func TestUpstreamFailureBeforeAnyByteIsAnOrdinaryError(t *testing.T) {
	env := newFetchEnv(t, map[string]*hubFile{"a.bin": {Content: weights(100), LFS: true}})
	env.hub.setDown(true)
	response, _, _ := env.get(t, "a.bin", nil)
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", response.StatusCode)
	}
	env.requireNoStaging(t)
}

func sha256Of(content []byte) string {
	return (&hubFile{Content: content}).sha256()
}

type failingObjects struct {
	objectstore.Store
	err error
}

func (f failingObjects) OpenRange(context.Context, string, int64, int64) (io.ReadCloser, int64, error) {
	return nil, 0, f.err
}

func TestACachedFileWhoseObjectIsGoneIsFetchedAgain(t *testing.T) {
	content := weights(2000)
	env := newFetchEnv(t, map[string]*hubFile{"model.safetensors": {Content: content, LFS: true}})
	if _, body, err := env.get(t, "model.safetensors", nil); err != nil || string(body) != string(content) {
		t.Fatalf("first fetch: %v", err)
	}
	file, _ := env.f.store.GetFile(context.Background(), env.commit.ID, "model.safetensors")
	if err := env.objects.Delete(context.Background(), BlobKey(file.SHA256)); err != nil {
		t.Fatal(err)
	}
	response, body, err := env.get(t, "model.safetensors", nil)
	if err != nil || response.StatusCode != 200 || string(body) != string(content) {
		t.Fatalf("a lost object must be repaired, not a permanent 500: %v %v", response, err)
	}
	if env.hub.count("GET /cdn/") != 2 {
		t.Fatalf("CDN requests = %d, want the repair to fetch once more", env.hub.count("GET /cdn/"))
	}
	if bytes, objects := env.f.counter(); bytes != 2000 || objects != 1 {
		t.Fatalf("counter = %d/%d: the forgotten blob must be released before it is counted again", bytes, objects)
	}
	if _, err := env.objects.Stat(context.Background(), BlobKey(file.SHA256)); err != nil {
		t.Fatalf("object not restored: %v", err)
	}
}

func TestOpenFailureLeavesACleanErrorResponse(t *testing.T) {
	env := newFetchEnv(t, map[string]*hubFile{"model.safetensors": {Content: weights(2000), LFS: true}})
	if _, _, err := env.get(t, "model.safetensors", nil); err != nil {
		t.Fatal(err)
	}
	env.fetcher.Objects = failingObjects{Store: env.objects, err: errors.New("object store unavailable")}
	response, body, err := env.get(t, "model.safetensors", nil)
	if err != nil || response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %v err = %v", response, err)
	}
	if response.Header.Get("ETag") != "" || response.Header.Get("Accept-Ranges") != "" || response.Header.Get("Content-Range") != "" || int(response.ContentLength) != len(body) {
		t.Fatalf("success headers leaked onto the error response: %v (body %d bytes)", response.Header, len(body))
	}
}

func TestTheSameDigestInANewCommitNeedsNoFetchAndNoSecondReservation(t *testing.T) {
	content := weights(3000)
	env := newFetchEnv(t, map[string]*hubFile{"model.safetensors": {Content: content, LFS: true}})
	if _, _, err := env.get(t, "model.safetensors", nil); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	file, _ := env.f.store.GetFile(ctx, env.commit.ID, "model.safetensors")
	second, err := env.f.store.EnsureCommit(ctx, Commit{HFRepoID: env.repo.ID, SHA: commitID(0x78), Source: SourceUpstream})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.f.store.ReplaceTree(ctx, second.ID, []File{{Path: "model.safetensors", Size: file.Size, SHA256: file.SHA256, LFS: true, GitBlobID: file.GitBlobID}}); err != nil {
		t.Fatal(err)
	}
	inherited, _ := env.f.store.GetFile(ctx, second.ID, "model.safetensors")
	if !inherited.Cached {
		t.Fatal("a digest the repository already holds must be cached in the new commit's row")
	}
	// A quota that only fits the first copy: the second commit must not need headroom.
	if err := capacity.SetQuota(ctx, env.f.db, env.f.repositoryID, 3000); err != nil {
		t.Fatal(err)
	}
	object := Object{RepositoryID: env.f.repositoryID, HFRepoID: env.repo.ID, CommitID: second.ID, Path: "model.safetensors", Size: file.Size,
		Open: func(context.Context) (*http.Response, error) { return nil, errors.New("must not go upstream") }}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if written, err := env.fetcher.Serve(recorder, request, object, http.Header{"ETag": {`"x"`}}); err != nil || !written || recorder.Body.Len() != 3000 {
		t.Fatalf("Serve = %v %v, %d bytes", written, err, recorder.Body.Len())
	}
	if env.hub.count("GET /cdn/") != 1 {
		t.Fatal("the second commit was fetched again")
	}
	if bytes, objects := env.f.counter(); bytes != 3000 || objects != 1 {
		t.Fatalf("counter = %d/%d", bytes, objects)
	}
}

func TestAFinishedFlightIsRetiredBeforeReadersSeeItDone(t *testing.T) {
	env := newFetchEnv(t, map[string]*hubFile{"model.safetensors": {Content: weights(2000), LFS: true}})
	for range 20 {
		if _, _, err := env.get(t, "model.safetensors", nil); err != nil {
			t.Fatal(err)
		}
		env.fetcher.mu.Lock()
		left := len(env.fetcher.flights)
		env.fetcher.mu.Unlock()
		if left != 0 {
			t.Fatalf("%d flights remain after the response completed: a late joiner would attach to a closed staging file", left)
		}
		// Make the next round cold again.
		file, _ := env.f.store.GetFile(context.Background(), env.commit.ID, "model.safetensors")
		_ = env.f.store.ForgetBlob(context.Background(), env.f.repositoryID, env.repo.ID, file.SHA256)
		_ = env.objects.Delete(context.Background(), BlobKey(file.SHA256))
	}
	env.requireNoStaging(t)
}

type heldCoordinator struct {
	mu    sync.Mutex
	held  bool
	tries int
	owner string
}

func (c *heldCoordinator) Acquire(context.Context, string, time.Duration) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tries++
	if c.held {
		return "", false, nil
	}
	c.held = true
	return "me", true, nil
}
func (c *heldCoordinator) Renew(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}
func (c *heldCoordinator) Release(context.Context, string, string) error {
	c.mu.Lock()
	c.held = false
	c.mu.Unlock()
	return nil
}
func (*heldCoordinator) CircuitOpen(context.Context, string) (bool, error)        { return false, nil }
func (*heldCoordinator) OpenCircuit(context.Context, string, time.Duration) error { return nil }
func (*heldCoordinator) CloseCircuit(context.Context, string) error               { return nil }

func TestAnotherNodeFetchingTheObjectFailsFastInsteadOfHangingTheClient(t *testing.T) {
	content := weights(2000)
	env := newFetchEnv(t, map[string]*hubFile{"model.safetensors": {Content: content, LFS: true}})
	coordinator := &heldCoordinator{held: true}
	env.fetcher.Locker = spool.Locker{Coordinator: coordinator, Lease: time.Minute, RenewEvery: time.Minute, Prefix: "t:"}
	response, body, err := env.get(t, "model.safetensors", nil)
	if err != nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %v err = %v: a cold object another node is fetching must answer 503, not wait", response, err)
	}
	if response.Header.Get("Retry-After") == "" || !strings.Contains(string(body), "another node") {
		t.Fatalf("headers = %v body = %q", response.Header, body)
	}
	if env.hub.count("GET /cdn/") != 0 {
		t.Fatal("the node fetched an object another node holds")
	}
	// The other node finishes: the retry is served from the store without a second transfer.
	coordinator.mu.Lock()
	coordinator.held = false
	coordinator.mu.Unlock()
	if _, got, err := env.get(t, "model.safetensors", nil); err != nil || string(got) != string(content) {
		t.Fatalf("retry after the other node: %v", err)
	}
	env.requireNoStaging(t)
}

func TestAStalledUpstreamIsCancelledAndFreesItsSlot(t *testing.T) {
	env := newFetchEnv(t, map[string]*hubFile{"a.bin": {Content: weights(2000), LFS: true}, "b.bin": {Content: weights(2001), LFS: true}})
	env.fetcher.IdleTimeout = 300 * time.Millisecond
	env.fetcher.Slots = spool.NewSlots(1)
	gate := make(chan struct{}) // never opened: the CDN delivers half the body and goes silent
	env.hub.mu.Lock()
	env.hub.cdnSlow = gate
	env.hub.mu.Unlock()
	t.Cleanup(func() { close(gate) })
	start := time.Now()
	response, body, err := env.get(t, "a.bin", nil)
	if err == nil && response.StatusCode == 200 && len(body) == 2000 {
		t.Fatal("a stalled transfer produced a complete body")
	}
	if time.Since(start) > 20*time.Second {
		t.Fatalf("the stalled fetch took %s: the idle timeout did not fire", time.Since(start))
	}
	// The slot is free again: a different object starts a fetch instead of getting 503 fetch_capacity.
	env.hub.mu.Lock()
	env.hub.cdnSlow = nil
	env.hub.mu.Unlock()
	if second, got, err := env.get(t, "b.bin", nil); err != nil || second.StatusCode != 200 || len(got) != 2001 {
		t.Fatalf("second fetch after the stall: %v %v", second, err)
	}
	if env.metrics.count(FetchUpstreamError) == 0 {
		t.Fatalf("outcomes = %v", env.metrics.outcomes)
	}
	env.requireNoStaging(t)
}

func TestAFileWithNoDigestIsNeverCached(t *testing.T) {
	env := newFetchEnv(t, map[string]*hubFile{"a.bin": {Content: weights(100), LFS: true}})
	ctx := context.Background()
	if err := env.f.store.ReplaceTree(ctx, env.commit.ID, []File{{Path: "nodigest.bin", Size: 100}}); err != nil {
		t.Fatal(err)
	}
	response, _, _ := env.get(t, "nodigest.bin", nil)
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d: bytes with nothing to verify them against must not be cached", response.StatusCode)
	}
	if env.hub.count("GET /cdn/") != 0 {
		t.Fatal("an unverifiable file was fetched")
	}
	if objects, _ := env.objects.List(ctx, "huggingface/"); len(objects) != 0 {
		t.Fatalf("objects = %v", objects)
	}
}

func TestMultiRangeOfAMissingObjectIsRepaired(t *testing.T) {
	content := weights(3000)
	env := newFetchEnv(t, map[string]*hubFile{"model.safetensors": {Content: content, LFS: true}})
	if _, _, err := env.get(t, "model.safetensors", nil); err != nil {
		t.Fatal(err)
	}
	file, _ := env.f.store.GetFile(context.Background(), env.commit.ID, "model.safetensors")
	if err := env.objects.Delete(context.Background(), BlobKey(file.SHA256)); err != nil {
		t.Fatal(err)
	}
	response, body, err := env.get(t, "model.safetensors", http.Header{"Range": {"bytes=0-3,100-103"}})
	if err != nil || response.StatusCode != http.StatusPartialContent || !strings.Contains(string(body), string(content[100:104])) {
		t.Fatalf("a multi range of a missing object must be repaired like a single one: %v %v", response, err)
	}
	if env.hub.count("GET /cdn/") != 2 {
		t.Fatalf("CDN requests = %d", env.hub.count("GET /cdn/"))
	}
}

// slowFinish is an object store whose uploads are observable: it records how many bytes the upload had
// read when the CDN was still holding back the second half of the object.
type observedObjects struct {
	objectstore.Store
	mu   sync.Mutex
	read int64
}

func (o *observedObjects) PutVerifiedReader(ctx context.Context, key string, r io.Reader, size int64, digest string) error {
	counting := &countingReader{r: r, onRead: func(n int64) {
		o.mu.Lock()
		o.read += n
		o.mu.Unlock()
	}}
	return o.Store.PutVerifiedReader(ctx, key, counting, size, digest)
}

func (o *observedObjects) bytesRead() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.read
}

type countingReader struct {
	r      io.Reader
	onRead func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.onRead(int64(n))
	return n, err
}

func TestTheObjectStoreUploadRunsWhileTheBytesArrive(t *testing.T) {
	content := weights(4000)
	env := newFetchEnv(t, map[string]*hubFile{"model.safetensors": {Content: content, LFS: true}})
	observed := &observedObjects{Store: env.objects}
	env.fetcher.Objects = observed
	gate := make(chan struct{})
	env.hub.mu.Lock()
	env.hub.cdnSlow = gate
	env.hub.mu.Unlock()
	request, _ := http.NewRequest(http.MethodGet, env.server.URL+"/?p=model.safetensors", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	first := make([]byte, 1000)
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatal(err)
	}
	// The CDN holds back the second half: if the upload only started after the last byte, nothing was read yet.
	deadline := time.Now().Add(5 * time.Second)
	for observed.bytesRead() < 1000 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if observed.bytesRead() < 1000 {
		t.Fatalf("the upload had read %d bytes while the transfer was still running: readers would wait for a full copy after the last byte", observed.bytesRead())
	}
	close(gate)
	if rest, err := io.ReadAll(response.Body); err != nil || string(first)+string(rest) != string(content) {
		t.Fatalf("body: %v", err)
	}
	file, _ := env.f.store.GetFile(context.Background(), env.commit.ID, "model.safetensors")
	if !file.Cached {
		t.Fatal("not cached after the stream")
	}
}

func TestAFailureBeforeTheFirstReadableByteIsAProperErrorResponse(t *testing.T) {
	// 10 bytes are below the hold-back (16): nothing is readable until the digest verified, so headers
	// are not committed and the mismatch can still be a clean 502.
	env := newFetchEnv(t, map[string]*hubFile{"tiny.bin": {Content: weights(10), Corrupt: true}})
	response, body, err := env.get(t, "tiny.bin", nil)
	if err != nil || response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %v err = %v, want a clean 502 rather than a dropped connection", response, err)
	}
	if !strings.Contains(string(body), "digest") && !strings.Contains(string(body), "match") {
		t.Fatalf("body = %q", body)
	}
}
