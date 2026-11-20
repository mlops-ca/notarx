//go:build integration

package huggingface

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/capacity"
	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/testsupport"
)

type fixture struct {
	t            *testing.T
	store        *Store
	db           *sql.DB
	repositoryID string
	name         string
}

// newFixture opens the selected engine and creates one huggingface repository row. The
// schema is shared with other suites, so every fixture uses its own repository id.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	driver, url := testsupport.Database(t)
	db, err := database.Open(driver, url, database.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	dialect := Dialect(driver)
	f := &fixture{t: t, db: db, store: NewStore(db, dialect), repositoryID: ids.New()}
	f.name = "hf-" + strings.ReplaceAll(f.repositoryID, "-", "")[:24]
	name := f.name
	f.exec("INSERT INTO hosted_repositories (id, name, format) VALUES (?, ?, 'huggingface')", f.repositoryID, name)
	t.Cleanup(func() {
		ctx := context.Background()
		f.exec("DELETE FROM hf_repos WHERE repository_id = ?", f.repositoryID)
		f.exec("DELETE FROM repository_usage_counters WHERE repository_id = ?", f.repositoryID)
		f.exec("DELETE FROM hosted_repositories WHERE id = ?", f.repositoryID)
		_ = ctx
	})
	return f
}

func (f *fixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.db.ExecContext(context.Background(), f.store.rebind(query), args...); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
}

func (f *fixture) counter() (bytes, objects int64) {
	f.t.Helper()
	err := f.db.QueryRowContext(context.Background(), f.store.rebind("SELECT bytes, objects FROM repository_usage_counters WHERE repository_id = ?"), f.repositoryID).Scan(&bytes, &objects)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return bytes, objects
}

func (f *fixture) requirePostgres() {
	f.t.Helper()
	if f.store.Dialect != Postgres {
		f.t.Skip("capacity.Reserve is PostgreSQL SQL until the counters are ported to MySQL")
	}
}

const sha1a = "1111111111111111111111111111111111111111"
const sha1b = "2222222222222222222222222222222222222222"

func digest(n int) string { return fmt.Sprintf("%064x", n) }

func TestRepoAndCommitAreIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repo, err := f.store.EnsureRepo(ctx, f.repositoryID, RepoTypeModel, "acme/widget")
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.store.EnsureRepo(ctx, f.repositoryID, RepoTypeModel, "acme/widget")
	if err != nil || again.ID != repo.ID || !ids.Valid(repo.ID) {
		t.Fatalf("EnsureRepo = %+v, %v; want the same row %s", again, err, repo.ID)
	}
	if _, err := f.store.EnsureRepo(ctx, f.repositoryID, "space", "acme/widget"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("space repo = %v, want ErrInvalid", err)
	}
	if _, err := f.store.GetRepo(ctx, f.repositoryID, RepoTypeDataset, "acme/widget"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("dataset with the same name = %v, want ErrNotFound", err)
	}
	commit, err := f.store.EnsureCommit(ctx, Commit{HFRepoID: repo.ID, SHA: sha1a, Source: SourceUpstream, Message: "first", Author: "ada", CommittedAt: time.Unix(1700000000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if commit.TreeIngested() || commit.Message != "first" || commit.ParentSHA != "" || !ids.Valid(commit.ID) {
		t.Fatalf("commit = %+v", commit)
	}
	same, err := f.store.EnsureCommit(ctx, Commit{HFRepoID: repo.ID, SHA: sha1a, Source: SourceUpstream, Message: "rewritten"})
	if err != nil || same.ID != commit.ID || same.Message != "first" {
		t.Fatalf("a commit is immutable: got %+v, %v", same, err)
	}
	if _, err := f.store.EnsureCommit(ctx, Commit{HFRepoID: repo.ID, SHA: "main", Source: SourceUpstream}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("branch name as sha = %v", err)
	}
	if _, err := f.store.EnsureCommit(ctx, Commit{HFRepoID: repo.ID, SHA: sha1b, Source: "mirror"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown source = %v", err)
	}
	before := commit.LastReadAt
	time.Sleep(5 * time.Millisecond)
	if err := f.store.TouchCommit(ctx, commit.ID); err != nil {
		t.Fatal(err)
	}
	touched, _ := f.store.GetCommit(ctx, repo.ID, sha1a)
	if !touched.LastReadAt.After(before) {
		t.Fatalf("last_read_at %v not after %v", touched.LastReadAt, before)
	}
}

func TestTreeRoundTripAndKeysetListing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repo, _ := f.store.EnsureRepo(ctx, f.repositoryID, RepoTypeModel, "acme/big")
	commit, _ := f.store.EnsureCommit(ctx, Commit{HFRepoID: repo.ID, SHA: sha1a, Source: SourceUpstream})
	var files []File
	var total int64
	for i := 0; i < insertBatch+37; i++ {
		files = append(files, File{Path: fmt.Sprintf("dir/file-%05d.bin", i), Size: int64(i), LFS: i%2 == 0, SHA256: digest(i)})
		total += int64(i)
	}
	files = append(files, File{Path: "config.json", Size: 12, GitBlobID: sha1b})
	total += 12
	if err := f.store.ReplaceTree(ctx, commit.ID, files); err != nil {
		t.Fatal(err)
	}
	got, _ := f.store.GetCommit(ctx, repo.ID, sha1a)
	if !got.TreeIngested() || *got.FileCount != len(files) || *got.TotalBytes != total {
		t.Fatalf("commit counts = %v %v, want %d %d", got.FileCount, got.TotalBytes, len(files), total)
	}
	var listed []string
	after := ""
	for {
		page, err := f.store.ListFiles(ctx, commit.ID, after, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range page {
			listed = append(listed, file.Path)
		}
		if len(page) < 100 {
			break
		}
		after = page[len(page)-1].Path
	}
	if len(listed) != len(files) || listed[0] != "config.json" {
		t.Fatalf("listed %d files, first %q", len(listed), listed[0])
	}
	file, err := f.store.GetFile(ctx, commit.ID, "config.json")
	if err != nil || file.GitBlobID != sha1b || file.SHA256 != "" || file.LFS || file.Cached {
		t.Fatalf("config.json = %+v, %v", file, err)
	}
	if _, err := f.store.GetFile(ctx, commit.ID, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file = %v", err)
	}
	// A second ingest replaces the first; a one-file tree is a valid, ingested, tree.
	if err := f.store.ReplaceTree(ctx, commit.ID, []File{{Path: "only.txt", Size: 1}}); err != nil {
		t.Fatal(err)
	}
	if page, _ := f.store.ListFiles(ctx, commit.ID, "", 10); len(page) != 1 {
		t.Fatalf("replace left %d files", len(page))
	}
	if err := f.store.ReplaceTree(ctx, commit.ID, nil); err != nil {
		t.Fatal(err)
	}
	if empty, _ := f.store.GetCommit(ctx, repo.ID, sha1a); !empty.TreeIngested() || *empty.FileCount != 0 {
		t.Fatalf("an empty tree must be told apart from an unread one: %+v", empty)
	}
}

func TestTreeRejectsBadFiles(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repo, _ := f.store.EnsureRepo(ctx, f.repositoryID, RepoTypeModel, "acme/bad")
	commit, _ := f.store.EnsureCommit(ctx, Commit{HFRepoID: repo.ID, SHA: sha1a, Source: SourceUpstream})
	long := strings.Repeat("a", MaxPathLength+1)
	if err := f.store.ReplaceTree(ctx, commit.ID, []File{{Path: long}}); !errors.Is(err, ErrPathTooLong) {
		t.Fatalf("long path = %v", err)
	}
	if err := f.store.ReplaceTree(ctx, commit.ID, []File{{Path: strings.Repeat("a", MaxPathLength)}}); err != nil {
		t.Fatalf("a path at the limit must be stored: %v", err)
	}
	for _, bad := range [][]File{{{Path: "../x"}}, {{Path: "a"}, {Path: "a"}}, {{Path: "a", Size: -1}}, {{Path: "a", SHA256: "xyz"}}} {
		if err := f.store.ReplaceTree(ctx, commit.ID, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v = %v, want ErrInvalid", bad, err)
		}
	}
	if err := f.store.ReplaceTree(ctx, ids.New(), []File{{Path: "a"}}); err == nil {
		t.Fatal("a tree for an unknown commit was stored")
	}
}

func TestRefUpsert(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repo, _ := f.store.EnsureRepo(ctx, f.repositoryID, RepoTypeModel, "acme/refs")
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := f.store.GetRef(ctx, repo.ID, RefBranch, "main"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent ref = %v", err)
	}
	if err := f.store.PutRef(ctx, Ref{HFRepoID: repo.ID, Kind: RefBranch, Name: "main", CommitSHA: sha1a, CheckedAt: now, ExpiresAt: now.Add(5 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PutRef(ctx, Ref{HFRepoID: repo.ID, Kind: RefBranch, Name: "main", CommitSHA: sha1b, CheckedAt: now.Add(time.Minute), ExpiresAt: now.Add(6 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	ref, err := f.store.GetRef(ctx, repo.ID, RefBranch, "main")
	if err != nil || ref.CommitSHA != sha1b || !ref.ExpiresAt.Equal(now.Add(6*time.Minute)) {
		t.Fatalf("ref = %+v, %v", ref, err)
	}
	if err := f.store.PutRef(ctx, Ref{HFRepoID: repo.ID, Kind: RefTag, Name: "main", CommitSHA: sha1a, CheckedAt: now, ExpiresAt: now}); err != nil {
		t.Fatalf("a tag may share a branch name: %v", err)
	}
	if err := f.store.PutRef(ctx, Ref{HFRepoID: repo.ID, Kind: "head", Name: "x", CommitSHA: sha1a}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad kind = %v", err)
	}
}

func TestCommitBlobCountsADigestOncePerRepositoryAndHonoursTheQuota(t *testing.T) {
	f := newFixture(t)
	f.requirePostgres()
	ctx := context.Background()
	repo, _ := f.store.EnsureRepo(ctx, f.repositoryID, RepoTypeModel, "acme/blobs")
	c1, _ := f.store.EnsureCommit(ctx, Commit{HFRepoID: repo.ID, SHA: sha1a, Source: SourceUpstream})
	c2, _ := f.store.EnsureCommit(ctx, Commit{HFRepoID: repo.ID, SHA: sha1b, Source: SourceUpstream})
	for _, c := range []Commit{c1, c2} {
		if err := f.store.ReplaceTree(ctx, c.ID, []File{{Path: "w.bin", Size: 100, SHA256: digest(7), LFS: true}, {Path: "v.bin", Size: 50, SHA256: digest(8), LFS: true}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := capacity.SetQuota(ctx, f.db, f.repositoryID, 120); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CommitBlob(ctx, f.repositoryID, repo.ID, c1.ID, "w.bin", digest(7), 100); err != nil {
		t.Fatal(err)
	}
	if bytes, objects := f.counter(); bytes != 100 || objects != 1 {
		t.Fatalf("counter = %d/%d, want 100/1", bytes, objects)
	}
	// The same digest in another commit of the repo is one object: nothing more is reserved
	// and the file of the other commit is marked cached as well.
	if err := f.store.CommitBlob(ctx, f.repositoryID, repo.ID, c2.ID, "w.bin", digest(7), 100); err != nil {
		t.Fatal(err)
	}
	if bytes, objects := f.counter(); bytes != 100 || objects != 1 {
		t.Fatalf("a repeated digest reserved again: %d/%d", bytes, objects)
	}
	for _, c := range []Commit{c1, c2} {
		if file, _ := f.store.GetFile(ctx, c.ID, "w.bin"); !file.Cached {
			t.Fatalf("w.bin of %s is not cached", c.SHA)
		}
	}
	// 100 + 50 exceeds the quota of 120: the whole transaction rolls back.
	err := f.store.CommitBlob(ctx, f.repositoryID, repo.ID, c1.ID, "v.bin", digest(8), 50)
	if !errors.Is(err, capacity.ErrQuotaExceeded) {
		t.Fatalf("over quota = %v", err)
	}
	if bytes, objects := f.counter(); bytes != 100 || objects != 1 {
		t.Fatalf("a rejected reservation changed the counter: %d/%d", bytes, objects)
	}
	if has, _ := f.store.HasBlob(ctx, repo.ID, digest(8)); has {
		t.Fatal("a rejected blob row was kept")
	}
	if file, _ := f.store.GetFile(ctx, c1.ID, "v.bin"); file.Cached {
		t.Fatal("a rejected blob marked its file cached")
	}
	// The recomputed usage (the reconciler's definition) equals the counter.
	query, _ := capacity.UsageQuery("huggingface")
	var usedBytes, usedObjects int64
	if err := f.db.QueryRowContext(ctx, query, f.repositoryID).Scan(&usedBytes, &usedObjects); err != nil {
		t.Fatal(err)
	}
	if bytes, objects := f.counter(); usedBytes != bytes || usedObjects != objects {
		t.Fatalf("usage query %d/%d disagrees with the counter %d/%d", usedBytes, usedObjects, bytes, objects)
	}
}

func TestConcurrentCommitBlobRejectsExactlyTheOverflow(t *testing.T) {
	f := newFixture(t)
	f.requirePostgres()
	ctx := context.Background()
	repo, _ := f.store.EnsureRepo(ctx, f.repositoryID, RepoTypeModel, "acme/edge")
	commit, _ := f.store.EnsureCommit(ctx, Commit{HFRepoID: repo.ID, SHA: sha1a, Source: SourceUpstream})
	const workers = 8
	var files []File
	for i := 0; i < workers; i++ {
		files = append(files, File{Path: fmt.Sprintf("f%d.bin", i), Size: 10, SHA256: digest(100 + i), LFS: true})
	}
	if err := f.store.ReplaceTree(ctx, commit.ID, files); err != nil {
		t.Fatal(err)
	}
	if err := capacity.SetQuota(ctx, f.db, f.repositoryID, 50); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			results <- f.store.CommitBlob(ctx, f.repositoryID, repo.ID, commit.ID, files[i].Path, files[i].SHA256, 10)
		}()
	}
	accepted, rejected := 0, 0
	for i := 0; i < workers; i++ {
		switch err := <-results; {
		case err == nil:
			accepted++
		case errors.Is(err, capacity.ErrQuotaExceeded):
			rejected++
		default:
			t.Fatal(err)
		}
	}
	if accepted != 5 || rejected != 3 {
		t.Fatalf("accepted %d rejected %d, want 5 and 3 at the quota edge", accepted, rejected)
	}
	if bytes, objects := f.counter(); bytes != 50 || objects != 5 {
		t.Fatalf("counter = %d/%d, want 50/5", bytes, objects)
	}
}

func TestBlobGarbageCollectionKeepsReferencedDigests(t *testing.T) {
	f := newFixture(t)
	f.requirePostgres()
	ctx := context.Background()
	repo, _ := f.store.EnsureRepo(ctx, f.repositoryID, RepoTypeModel, "acme/gc")
	commit, _ := f.store.EnsureCommit(ctx, Commit{HFRepoID: repo.ID, SHA: sha1a, Source: SourceUpstream})
	if err := f.store.ReplaceTree(ctx, commit.ID, []File{{Path: "keep.bin", Size: 10, SHA256: digest(1), LFS: true}, {Path: "drop.bin", Size: 20, SHA256: digest(2), LFS: true}}); err != nil {
		t.Fatal(err)
	}
	for i, size := range []int64{10, 20} {
		if err := f.store.CommitBlob(ctx, f.repositoryID, repo.ID, commit.ID, []string{"keep.bin", "drop.bin"}[i], digest(i+1), size); err != nil {
			t.Fatal(err)
		}
	}
	if orphans, _ := f.store.UnreferencedBlobs(ctx, repo.ID, 10); len(orphans) != 0 {
		t.Fatalf("referenced digests listed as orphans: %v", orphans)
	}
	// Dropping a file leaves its digest unreferenced; the kept one stays.
	if err := f.store.ReplaceTree(ctx, commit.ID, []File{{Path: "keep.bin", Size: 10, SHA256: digest(1), LFS: true}}); err != nil {
		t.Fatal(err)
	}
	orphans, err := f.store.UnreferencedBlobs(ctx, repo.ID, 10)
	if err != nil || len(orphans) != 1 || orphans[0] != digest(2) {
		t.Fatalf("orphans = %v, %v", orphans, err)
	}
	if deleted, _, err := f.store.DeleteBlob(ctx, f.repositoryID, repo.ID, digest(1)); err != nil || deleted {
		t.Fatalf("a referenced digest was deleted: %v %v", deleted, err)
	}
	deleted, others, err := f.store.DeleteBlob(ctx, f.repositoryID, repo.ID, digest(2))
	if err != nil || !deleted || others {
		t.Fatalf("DeleteBlob = %v %v %v", deleted, others, err)
	}
	if bytes, objects := f.counter(); bytes != 10 || objects != 1 {
		t.Fatalf("counter after GC = %d/%d, want 10/1", bytes, objects)
	}
	if deleted, _, _ := f.store.DeleteBlob(ctx, f.repositoryID, repo.ID, digest(2)); deleted {
		t.Fatal("a missing blob reported deleted")
	}
}

func TestSearchCommitsAndModuleBrowse(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	module, err := New(Options{Store: f.store})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"acme/alpha", "acme/beta", "other/gamma"} {
		repo, _ := f.store.EnsureRepo(ctx, f.repositoryID, RepoTypeModel, name)
		for _, sha := range []string{sha1a, sha1b} {
			commit, err := f.store.EnsureCommit(ctx, Commit{HFRepoID: repo.ID, SHA: sha, Source: SourceUpstream, CommittedAt: time.Unix(1700000000, 0)})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.store.ReplaceTree(ctx, commit.ID, []File{{Path: "w.bin", Size: 5}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	all, err := module.Search(ctx, f.repositoryID, repository.ArtifactSearchQuery{}, 100, "")
	if err != nil || len(all) != 6 {
		t.Fatalf("Search = %d items, %v", len(all), err)
	}
	if all[0].Coordinate != "model/acme/alpha@"+sha1a || all[0].Size != 5 {
		t.Fatalf("first = %+v", all[0])
	}
	acme, _ := module.Search(ctx, f.repositoryID, repository.ArtifactSearchQuery{Mode: repository.ArtifactSearchByCoordinate, Value: "model/acme/"}, 100, "")
	if len(acme) != 4 {
		t.Fatalf("prefix search = %d items", len(acme))
	}
	page, _ := module.Search(ctx, f.repositoryID, repository.ArtifactSearchQuery{}, 2, all[1].Coordinate)
	if len(page) != 2 || page[0].Coordinate != all[2].Coordinate {
		t.Fatalf("keyset page = %+v", page)
	}
	if _, err := module.Search(ctx, f.repositoryID, repository.ArtifactSearchQuery{}, 2, "not-a-coordinate"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad cursor = %v", err)
	}
	wild, _ := module.Search(ctx, f.repositoryID, repository.ArtifactSearchQuery{Mode: repository.ArtifactSearchByCoordinate, Value: "model/%"}, 100, "")
	if len(wild) != 0 {
		t.Fatalf("a LIKE wildcard in the prefix matched %d items", len(wild))
	}
}
