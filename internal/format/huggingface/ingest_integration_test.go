//go:build integration

package huggingface

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/retention"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newIngest(t *testing.T, f *fixture, hub *fakeHub) (*Ingest, Repo, *clock) {
	t.Helper()
	clk := &clock{now: time.Now().UTC().Truncate(time.Second)}
	repo, err := f.store.EnsureRepo(context.Background(), f.repositoryID, RepoTypeModel, hubModel)
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	return &Ingest{Store: f.store, Hub: hub.upstream(), Config: cfg, Now: clk.Now}, repo, clk
}

func TestRefMovingUpstreamIsRevalidatedAfterTheTTL(t *testing.T) {
	f := newFixture(t)
	hub, shaA := hubWithFiles(t)
	shaB := commitID(0xdef)
	hub.addCommit(shaB, "", map[string]*hubFile{"config.json": {Content: []byte("v2")}})
	ingest, repo, clk := newIngest(t, f, hub)
	ctx := context.Background()

	first, err := ingest.ResolveRevision(ctx, repo, "main")
	if err != nil || first.Commit.SHA != shaA || first.Cached || first.Stale {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if first.Commit.TreeIngested() {
		t.Fatal("a ref resolution must not ingest the tree")
	}
	hub.setRef("main", shaB)
	again, err := ingest.ResolveRevision(ctx, repo, "main")
	if err != nil || again.Commit.SHA != shaA || !again.Cached {
		t.Fatalf("a fresh ref must answer from the store: %+v, %v", again, err)
	}
	if hub.count("GET /api/models/"+hubModel+"/revision/") != 1 {
		t.Fatalf("revision requests = %d, want 1 within the TTL", hub.count("GET /api/models/"+hubModel+"/revision/"))
	}
	clk.advance(6 * time.Minute)
	moved, err := ingest.ResolveRevision(ctx, repo, "main")
	if err != nil || moved.Commit.SHA != shaB || moved.Cached || moved.Stale {
		t.Fatalf("after the TTL the ref must be revalidated: %+v, %v", moved, err)
	}
	ref, err := f.store.GetRef(ctx, repo.ID, RefBranch, "main")
	if err != nil || ref.CommitSHA != shaB || !ref.ExpiresAt.Equal(clk.Now().Add(5*time.Minute)) {
		t.Fatalf("ref = %+v, %v", ref, err)
	}
	if _, err := f.store.GetCommit(ctx, repo.ID, shaA); err != nil {
		t.Fatalf("the old commit must stay readable by sha: %v", err)
	}
}

func TestStaleRefIsServedWhileUpstreamIsDown(t *testing.T) {
	f := newFixture(t)
	hub, shaA := hubWithFiles(t)
	ingest, repo, clk := newIngest(t, f, hub)
	ctx := context.Background()
	if _, err := ingest.ResolveRevision(ctx, repo, "main"); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Hour)
	hub.setDown(true)
	stale, err := ingest.ResolveRevision(ctx, repo, "main")
	if err != nil || !stale.Stale || stale.Commit.SHA != shaA {
		t.Fatalf("stale = %+v, %v", stale, err)
	}
	ingest.Config.ServeStale = false
	if _, err := ingest.ResolveRevision(ctx, repo, "main"); !errors.Is(err, &UpstreamError{Code: CodeUpstreamUnavailable}) {
		t.Fatalf("with NOTARX_HF_SERVE_STALE=false an expired ref must fail: %v", err)
	}
	ingest.Config.ServeStale = true
	// Nothing cached: metadata is never fabricated.
	if _, err := ingest.ResolveRevision(ctx, repo, "dev"); !errors.Is(err, &UpstreamError{Code: CodeUpstreamUnavailable}) {
		t.Fatalf("an unknown ref with upstream down = %v", err)
	}
	// An upstream 404 is not an outage: the stale answer must not hide a deleted branch.
	hub.setDown(false)
	if _, err := ingest.ResolveRevision(ctx, repo, "gone"); !errors.Is(err, &UpstreamError{Code: CodeUpstreamNotFound}) {
		t.Fatalf("unknown branch = %v", err)
	}
}

func TestCommitShaIsServedFromTheStoreAndVerifiedOnce(t *testing.T) {
	f := newFixture(t)
	hub, shaA := hubWithFiles(t)
	ingest, repo, _ := newIngest(t, f, hub)
	ctx := context.Background()
	first, err := ingest.ResolveRevision(ctx, repo, shaA)
	if err != nil || first.Commit.SHA != shaA || first.Cached {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := ingest.ResolveRevision(ctx, repo, shaA)
	if err != nil || !second.Cached {
		t.Fatalf("second = %+v, %v", second, err)
	}
	if hub.count("GET /api/models/"+hubModel+"/revision/") != 1 {
		t.Fatal("a known commit sha must never go upstream again")
	}
	hub.setDown(true)
	if offline, err := ingest.ResolveRevision(ctx, repo, shaA); err != nil || offline.Stale {
		t.Fatalf("a commit is served forever once known: %+v, %v", offline, err)
	}
	if _, err := ingest.ResolveRevision(ctx, repo, commitID(0x999)); err == nil {
		t.Fatal("an unknown commit with upstream down was fabricated")
	}
	hub.setDown(false)
	if _, err := ingest.ResolveRevision(ctx, repo, commitID(0x998)); !errors.Is(err, &UpstreamError{Code: CodeUpstreamNotFound}) {
		t.Fatalf("an unknown commit = %v", err)
	}
	if _, err := ingest.ResolveRevision(ctx, repo, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty revision = %v", err)
	}
}

func TestTreeIsIngestedLazilyOnceAndSharedByConcurrentRequests(t *testing.T) {
	f := newFixture(t)
	hub, shaA := hubWithFiles(t)
	ingest, repo, _ := newIngest(t, f, hub)
	ctx := context.Background()
	resolved, _ := ingest.ResolveRevision(ctx, repo, shaA)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			commit, err := ingest.EnsureTree(ctx, repo, resolved.Commit)
			if err == nil && (!commit.TreeIngested() || *commit.FileCount != 3) {
				err = fmt.Errorf("commit = %+v", commit)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := hub.count("GET /api/models/" + hubModel + "/tree/"); got != 1 {
		t.Fatalf("tree requests = %d, want one shared fetch", got)
	}
	cached, err := ingest.EnsureTree(ctx, repo, resolved.Commit)
	if err != nil || !cached.TreeIngested() || hub.count("GET /api/models/"+hubModel+"/tree/") != 1 {
		t.Fatalf("a second request must not refetch: %+v, %v", cached, err)
	}
	files, _ := f.store.ListFiles(ctx, resolved.Commit.ID, "", 10)
	if len(files) != 3 {
		t.Fatalf("files = %d", len(files))
	}
}

func TestOversizedTreeIsRefusedAndStoresNothing(t *testing.T) {
	f := newFixture(t)
	hub, shaA := hubWithFiles(t)
	ingest, repo, _ := newIngest(t, f, hub)
	ingest.Config.MaxFilesPerCommit = 2
	ctx := context.Background()
	resolved, _ := ingest.ResolveRevision(ctx, repo, shaA)
	_, err := ingest.EnsureTree(ctx, repo, resolved.Commit)
	if !errors.Is(err, ErrTreeTooLarge) {
		t.Fatalf("err = %v, want upstream_tree_too_large", err)
	}
	commit, _ := f.store.GetCommit(ctx, repo.ID, shaA)
	if commit.TreeIngested() {
		t.Fatal("a refused tree left file counts")
	}
	if files, _ := f.store.ListFiles(ctx, commit.ID, "", 10); len(files) != 0 {
		t.Fatalf("a refused tree stored %d file rows", len(files))
	}
}

func TestRevalidatingARefNeverCreatesFileRows(t *testing.T) {
	f := newFixture(t)
	hub, _ := hubWithFiles(t)
	ingest, repo, clk := newIngest(t, f, hub)
	ctx := context.Background()
	for range 3 {
		clk.advance(10 * time.Minute)
		if _, err := ingest.ResolveRevision(ctx, repo, "main"); err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	if err := f.db.QueryRowContext(ctx, f.store.rebind("SELECT COUNT(*) FROM hf_files WHERE commit_id IN (SELECT id FROM hf_commits WHERE hf_repo_id = ?)"), repo.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("file rows = %d, %v", rows, err)
	}
	if hub.count("GET /api/models/"+hubModel+"/tree/") != 0 {
		t.Fatal("the tree was fetched by a ref resolution")
	}
}

func newJanitor(t *testing.T, f *fixture) *retention.Janitor {
	t.Helper()
	var entries []retention.Entry
	for _, entry := range retention.DefaultRegistry() {
		if entry.Table == "hf_refs" || entry.Table == "hf_commits" {
			entries = append(entries, entry)
		}
	}
	if len(entries) != 2 {
		t.Fatalf("hf retention entries = %d", len(entries))
	}
	janitor, err := retention.New(f.db, retention.DefaultConfig(), retention.Options{
		Owner: "hf-test", Registry: entries, Dialect: retention.Dialect(f.store.Dialect),
		Sleep: func(context.Context, time.Duration) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	return janitor
}

func TestJanitorDeletesOnlyUnreferencedUnreadCommitsAndKeepsTheNewestThree(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repo, _ := f.store.EnsureRepo(ctx, f.repositoryID, RepoTypeModel, hubModel)
	base := time.Now().UTC().Add(-90 * 24 * time.Hour)
	commits := map[string]Commit{}
	add := func(name string, n int, source string) {
		commit, err := f.store.EnsureCommit(ctx, Commit{HFRepoID: repo.ID, SHA: commitID(n), Source: source, CommittedAt: base.Add(time.Duration(n) * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.ReplaceTree(ctx, commit.ID, []File{{Path: "w.bin", Size: 1}}); err != nil {
			t.Fatal(err)
		}
		commits[name] = commit
	}
	add("hosted", 1, SourceHosted)
	add("referenced", 2, SourceUpstream)
	add("old-a", 3, SourceUpstream)
	add("old-b", 4, SourceUpstream)
	add("fresh-read", 5, SourceUpstream)
	add("newest-1", 6, SourceUpstream)
	add("newest-2", 7, SourceUpstream)
	add("newest-3", 8, SourceUpstream)
	old := time.Now().UTC().Add(-60 * 24 * time.Hour)
	for name, commit := range commits {
		if name == "fresh-read" {
			continue
		}
		f.exec("UPDATE hf_commits SET last_read_at = ? WHERE id = ?", old, commit.ID)
	}
	if err := f.store.PutRef(ctx, Ref{HFRepoID: repo.ID, Kind: RefBranch, Name: "main", CommitSHA: commitID(2), CheckedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := newJanitor(t, f).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	gone := map[string]bool{"old-a": true, "old-b": true}
	for name, commit := range commits {
		_, err := f.store.GetCommit(ctx, repo.ID, commit.SHA)
		switch {
		case gone[name] && !errors.Is(err, ErrNotFound):
			t.Errorf("%s survived: %v", name, err)
		case !gone[name] && err != nil:
			t.Errorf("%s was deleted: %v", name, err)
		}
	}
	if files, _ := f.store.ListFiles(ctx, commits["old-a"].ID, "", 10); len(files) != 0 {
		t.Errorf("the file rows of a deleted commit survived: %d", len(files))
	}
	if files, _ := f.store.ListFiles(ctx, commits["referenced"].ID, "", 10); len(files) != 1 {
		t.Errorf("the referenced commit lost its files")
	}
	if _, err := f.store.GetRef(ctx, repo.ID, RefBranch, "main"); err != nil {
		t.Fatalf("the ref of a protected commit was deleted: %v", err)
	}
}

func TestJanitorDeletesRefsPastExpiryPlusKeep(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repo, _ := f.store.EnsureRepo(ctx, f.repositoryID, RepoTypeModel, hubModel)
	long := time.Now().UTC().Add(-45 * 24 * time.Hour)
	recent := time.Now().UTC().Add(-2 * 24 * time.Hour)
	for name, expires := range map[string]time.Time{"stale": long, "recent": recent} {
		if err := f.store.PutRef(ctx, Ref{HFRepoID: repo.ID, Kind: RefBranch, Name: name, CommitSHA: commitID(1), CheckedAt: expires, ExpiresAt: expires}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := newJanitor(t, f).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.GetRef(ctx, repo.ID, RefBranch, "stale"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a ref past expires_at + keep survived: %v", err)
	}
	if _, err := f.store.GetRef(ctx, repo.ID, RefBranch, "recent"); err != nil {
		t.Fatalf("a recently expired ref was deleted: %v", err)
	}
}

func TestBlobGarbageCollectionDeletesObjectsOnlyWhenNoRepositoryHoldsThem(t *testing.T) {
	f := newFixture(t)
	f.requirePostgres()
	ctx := context.Background()
	objects := objectstore.NewMemoryStore()
	module, err := New(Options{Store: f.store, Objects: objects, GCPause: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	repoA, _ := f.store.EnsureRepo(ctx, f.repositoryID, RepoTypeModel, "acme/a")
	repoB, _ := f.store.EnsureRepo(ctx, f.repositoryID, RepoTypeModel, "acme/b")
	for _, repo := range []Repo{repoA, repoB} {
		commit, _ := f.store.EnsureCommit(ctx, Commit{HFRepoID: repo.ID, SHA: commitID(1), Source: SourceUpstream})
		if err := f.store.ReplaceTree(ctx, commit.ID, []File{{Path: "shared.bin", Size: 10, SHA256: digest(1), LFS: true}, {Path: "own.bin", Size: 20, SHA256: digest(2 + int(repo.RepoName[len(repo.RepoName)-1])), LFS: true}}); err != nil {
			t.Fatal(err)
		}
		for _, file := range []File{{Path: "shared.bin", SHA256: digest(1), Size: 10}, {Path: "own.bin", SHA256: digest(2 + int(repo.RepoName[len(repo.RepoName)-1])), Size: 20}} {
			if err := objects.Put(ctx, BlobKey(file.SHA256), make([]byte, file.Size)); err != nil {
				t.Fatal(err)
			}
			if err := f.store.CommitBlob(ctx, f.repositoryID, repo.ID, commit.ID, file.Path, file.SHA256, file.Size); err != nil {
				t.Fatal(err)
			}
		}
	}
	if bytes, objectsCount := f.counter(); bytes != 60 || objectsCount != 4 {
		t.Fatalf("counter = %d/%d, want 60/4 (a digest counts once per repository)", bytes, objectsCount)
	}
	// Repo A loses both its files (its commit is gone); repo B keeps everything.
	f.exec("DELETE FROM hf_commits WHERE hf_repo_id = ?", repoA.ID)
	if err := module.CollectCache(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := objects.Stat(ctx, BlobKey(digest(1))); err != nil {
		t.Fatalf("a digest another repository holds was deleted: %v", err)
	}
	if _, err := objects.Stat(ctx, BlobKey(digest(2+int('a')))); err == nil {
		t.Fatal("an orphaned object was kept")
	}
	if _, err := objects.Stat(ctx, BlobKey(digest(2+int('b')))); err != nil {
		t.Fatalf("a referenced object was deleted: %v", err)
	}
	if bytes, objectsCount := f.counter(); bytes != 30 || objectsCount != 2 {
		t.Fatalf("counter after GC = %d/%d, want 30/2", bytes, objectsCount)
	}
	// Nothing is left to collect: a second pass changes nothing.
	if err := module.CollectCache(ctx); err != nil {
		t.Fatal(err)
	}
	if bytes, _ := f.counter(); bytes != 30 {
		t.Fatalf("a second pass changed the counter to %d", bytes)
	}
	noObjects, _ := New(Options{Store: f.store})
	if err := noObjects.CollectCache(ctx); err != nil {
		t.Fatalf("a module without an object store must not collect: %v", err)
	}
}
