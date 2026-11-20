package repository

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The memory store keeps the guard of the PostgreSQL store, including the order of its checks: a tombstoned version
// refuses every write before the replay of identical bytes can be admitted.
func TestMemoryRawVersionTombstoneRefusesEverything(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	repo, err := store.CreateHostedRepository(ctx, HostedRepository{ID: "repo", Name: "versions", Format: FormatRaw, RawLayout: RawLayoutVersioned, SealMode: RawSealIdle})
	if err != nil {
		t.Fatal(err)
	}
	asset := RawAsset{RepositoryID: repo.ID, Path: "tool/1.0.0/a", Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000001", ObjectKey: "k", Size: 1}
	if _, err = store.PutRawAssetIf(ctx, asset, RawWriteCondition{RequireAbsent: true}); err != nil {
		t.Fatal(err)
	}
	key := rawVersionKey(repo.ID, "tool", "1.0.0")
	tombstoned := store.rawVersions[key]
	tombstoned.State = RawVersionTombstoned
	store.rawVersions[key] = tombstoned

	if _, err = store.PutRawAssetIf(ctx, asset, RawWriteCondition{RequireAbsent: true}); !errors.Is(err, ErrVersionTombstoned) {
		t.Fatalf("identical bytes into a tombstoned version: %v", err)
	}
	if _, _, err = store.CreateRawVersion(ctx, RawVersion{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0"}); !errors.Is(err, ErrVersionTombstoned) {
		t.Fatalf("create: %v", err)
	}
	if _, err = store.SealRawVersion(ctx, SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0"}); !errors.Is(err, ErrVersionTombstoned) {
		t.Fatalf("seal: %v", err)
	}
	if _, err = store.SetRawVersionProperties(ctx, repo.ID, "tool", "1.0.0", nil); !errors.Is(err, ErrVersionTombstoned) {
		t.Fatalf("properties: %v", err)
	}
	if _, err = store.GetRawVersion(ctx, repo.ID, "tool", "1.0.0"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read: %v", err)
	}
	if err = store.DeleteRawAsset(ctx, repo.ID, "tool/1.0.0/a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete: %v", err)
	}
	if packages, _ := store.ListRawPackages(ctx, repo.ID, "", 10); len(packages) != 0 {
		t.Fatalf("a tombstoned version is not listed: %#v", packages)
	}
	// The sweep never seals a tombstoned version, nor a version of a repository that is no longer active.
	if sealed, err := store.SealIdleRawVersions(ctx, time.Now().Add(time.Hour), 10); err != nil || len(sealed) != 0 {
		t.Fatalf("sweep: %#v %v", sealed, err)
	}
}

func TestMemoryRawVersionSweepSkipsInactiveRepositories(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	repo, _ := store.CreateHostedRepository(ctx, HostedRepository{ID: "repo", Name: "versions", Format: FormatRaw, RawLayout: RawLayoutVersioned, SealMode: RawSealIdle})
	asset := RawAsset{RepositoryID: repo.ID, Path: "tool/1.0.0/a", Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000002", ObjectKey: "k2", Size: 1}
	if _, err := store.PutRawAssetIf(ctx, asset, RawWriteCondition{RequireAbsent: true}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	deleting := store.hostedRepositories[repo.ID]
	deleting.State = RepositoryDeleting
	store.hostedRepositories[repo.ID] = deleting
	store.mu.Unlock()
	if sealed, err := store.SealIdleRawVersions(ctx, time.Now().Add(time.Hour), 10); err != nil || len(sealed) != 0 {
		t.Fatalf("a repository that is being deleted is not swept: %#v %v", sealed, err)
	}
}
