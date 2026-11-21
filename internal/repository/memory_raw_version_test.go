package repository

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A versioned Raw repository announces a version once, when it is sealed, as name@version under the manifest digest:
// writing files into an open version publishes nothing, and a repeated seal publishes nothing more.
func TestMemoryRawVersionEmitsPublishedAtSealNotPerFile(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	repo, err := store.CreateHostedRepository(ctx, HostedRepository{ID: "repo", Name: "versions", Format: FormatRaw, RawLayout: RawLayoutVersioned, SealMode: RawSealExplicit})
	if err != nil {
		t.Fatal(err)
	}
	for i, path := range []string{"tool/1.0.0/a", "tool/1.0.0/b"} {
		digest := "sha256:000000000000000000000000000000000000000000000000000000000000000" + string(rune('1'+i))
		if _, err = store.PutRawAssetIf(ctx, RawAsset{RepositoryID: repo.ID, Path: path, Digest: digest, ObjectKey: "k" + path, Size: 1}, RawWriteCondition{RequireAbsent: true}); err != nil {
			t.Fatal(err)
		}
	}
	if keys := publishedKeys(store); len(keys) != 0 {
		t.Fatalf("files of an open version published %v", keys)
	}
	input := SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0"}
	sealed, err := store.SealRawVersion(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SealRawVersion(ctx, input); err != nil {
		t.Fatal(err)
	}
	want := "artifact.published|repo|tool@1.0.0|" + sealed.ManifestDigest + "|"
	if keys := publishedKeys(store); len(keys) != 1 || keys[0] != want {
		t.Fatalf("keys = %v, want [%s]", keys, want)
	}
	// The catch-up reconciler agrees with the seal: it finds the sealed version already announced and skips the files.
	if n, err := store.ReconcilePublishedEvents(ctx, time.Now().Add(-time.Hour), 100); err != nil || n != 0 {
		t.Fatalf("reconcile wrote %d, %v", n, err)
	}
}

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
