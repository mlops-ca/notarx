//go:build integration

package huggingface

import (
	"context"
	"testing"

	"github.com/mlops-ca/notarx/internal/capacity"
)

// TestHuggingFaceCounterEquality is the counter equality test of the capacity registry (ADR 0023
// section 3): after every step of a blob's life the repository counter equals the usage the
// reconciler recomputes from hf_blobs.
func TestHuggingFaceCounterEquality(t *testing.T) {
	f := newFixture(t)
	f.requirePostgres()
	ctx := context.Background()
	query, ok := capacity.UsageQuery("huggingface")
	if !ok {
		t.Fatal("no usage query for huggingface")
	}
	assertEqual := func(step string) {
		t.Helper()
		var usedBytes, usedObjects int64
		if err := f.db.QueryRowContext(ctx, query, f.repositoryID).Scan(&usedBytes, &usedObjects); err != nil {
			t.Fatalf("%s: aggregate usage: %v", step, err)
		}
		if bytes, objects := f.counter(); bytes != usedBytes || objects != usedObjects {
			t.Fatalf("%s: counter bytes=%d objects=%d, aggregation bytes=%d objects=%d", step, bytes, objects, usedBytes, usedObjects)
		}
	}
	assertEqual("empty repository")
	repoA, _ := f.store.EnsureRepo(ctx, f.repositoryID, RepoTypeModel, "acme/a")
	repoB, _ := f.store.EnsureRepo(ctx, f.repositoryID, RepoTypeDataset, "acme/b")
	commit := func(repo Repo, n int, files ...File) Commit {
		c, err := f.store.EnsureCommit(ctx, Commit{HFRepoID: repo.ID, SHA: commitID(n), Source: SourceUpstream})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.ReplaceTree(ctx, c.ID, files); err != nil {
			t.Fatal(err)
		}
		return c
	}
	cA := commit(repoA, 1, File{Path: "w.bin", Size: 100, SHA256: digest(1), LFS: true}, File{Path: "v.bin", Size: 40, SHA256: digest(2), LFS: true})
	cB := commit(repoB, 2, File{Path: "w.bin", Size: 100, SHA256: digest(1), LFS: true})

	// publish
	if err := f.store.CommitBlob(ctx, f.repositoryID, repoA.ID, cA.ID, "w.bin", digest(1), 100); err != nil {
		t.Fatal(err)
	}
	assertEqual("first blob")
	// the same digest in the same repository again (a re-publish): counted once
	if err := f.store.CommitBlob(ctx, f.repositoryID, repoA.ID, cA.ID, "w.bin", digest(1), 100); err != nil {
		t.Fatal(err)
	}
	assertEqual("re-publish of the same digest")
	// the same digest in a second repo of the same notarx repository: counted per hf repo
	if err := f.store.CommitBlob(ctx, f.repositoryID, repoB.ID, cB.ID, "w.bin", digest(1), 100); err != nil {
		t.Fatal(err)
	}
	assertEqual("same digest in another repo")
	if err := f.store.CommitBlob(ctx, f.repositoryID, repoA.ID, cA.ID, "v.bin", digest(2), 40); err != nil {
		t.Fatal(err)
	}
	assertEqual("second digest")
	// replace: the commit loses a file, its blob is collected
	if err := f.store.ReplaceTree(ctx, cA.ID, []File{{Path: "w.bin", Size: 100, SHA256: digest(1), LFS: true}}); err != nil {
		t.Fatal(err)
	}
	if deleted, _, err := f.store.DeleteBlob(ctx, f.repositoryID, repoA.ID, digest(2)); err != nil || !deleted {
		t.Fatalf("DeleteBlob = %v %v", deleted, err)
	}
	assertEqual("after collecting a replaced file")
	// delete: a blob whose object is gone is forgotten and its bytes released
	if err := f.store.ForgetBlob(ctx, f.repositoryID, repoB.ID, digest(1)); err != nil {
		t.Fatal(err)
	}
	assertEqual("after forgetting a blob")
	// re-publish after delete
	if err := f.store.CommitBlob(ctx, f.repositoryID, repoA.ID, cA.ID, "v.bin", digest(2), 40); err != nil {
		t.Fatal(err)
	}
	assertEqual("re-publish after delete")
}
