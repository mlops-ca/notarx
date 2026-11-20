//go:build integration

package repository

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func rawVersionIntegrationStore(t *testing.T) *PostgresStore {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestHostedRepositoriesRawLayoutShapeCheck drives the constraint with raw SQL, bypassing the Go validation, so the
// database itself rejects a layout, an idle seal mode or the strict flag on repositories that cannot have them.
func TestHostedRepositoriesRawLayoutShapeCheck(t *testing.T) {
	store := rawVersionIntegrationStore(t)
	ctx := context.Background()
	insert := func(format, repoType, endpoint, layout, sealMode string, strict bool) error {
		_, err := store.db.ExecContext(ctx, `INSERT INTO hosted_repositories (id,name,format,repo_type,endpoint,raw_layout,seal_mode,raw_strict) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			uuid.NewString(), "shape-"+strings.ReplaceAll(uuid.NewString(), "-", "")[:20], format, repoType, endpoint, layout, sealMode, strict)
		return err
	}
	for name, err := range map[string]error{
		"versioned raw":        insert("raw", "hosted", "", "versioned", "explicit", false),
		"versioned idle":       insert("raw", "hosted", "", "versioned", "idle", false),
		"versioned strict":     insert("raw", "hosted", "", "versioned", "explicit", true),
		"path defaults on npm": insert("npm", "hosted", "", "path", "explicit", false),
	} {
		if err != nil {
			t.Errorf("%s must be accepted: %v", name, err)
		}
	}
	for name, err := range map[string]error{
		"versioned on npm":   insert("npm", "hosted", "", "versioned", "explicit", false),
		"versioned on proxy": insert("raw", "proxy", "https://example.test", "versioned", "explicit", false),
		"strict on path":     insert("raw", "hosted", "", "path", "explicit", true),
		"idle on path":       insert("raw", "hosted", "", "path", "idle", false),
		"strict on npm":      insert("npm", "hosted", "", "path", "explicit", true),
		"unknown layout":     insert("raw", "hosted", "", "tree", "explicit", false),
		"unknown seal mode":  insert("raw", "hosted", "", "versioned", "never", false),
	} {
		if err == nil {
			t.Errorf("%s must be rejected by the shape check", name)
		}
	}
}

// TestPostgresIdleSealSkipsAVersionSealedInTheMeantime seals a version between the candidate read and the row lock of the
// sweep and checks that the sweep does not claim the seal.
func TestPostgresIdleSealSkipsAVersionSealedInTheMeantime(t *testing.T) {
	store := rawVersionIntegrationStore(t)
	ctx := context.Background()
	repo, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "raw-idle-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12], Format: FormatRaw, RawLayout: RawLayoutVersioned, SealMode: RawSealIdle})
	if err != nil {
		t.Fatal(err)
	}
	asset := RawAsset{RepositoryID: repo.ID, Path: "tool/1.0.0/a", Digest: "sha256:" + strings.Repeat("d", 63) + "2", ObjectKey: "native/raw/sha256/d2-idle", Size: 1, ContentType: "text/plain"}
	if _, err = store.PutRawAssetIf(ctx, asset, RawWriteCondition{RequireAbsent: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SealRawVersion(ctx, SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.sealRawVersion(ctx, SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0"}, time.Now().Add(time.Hour)); !errors.Is(err, errRawSealSkipped) {
		t.Fatalf("the sweep claimed a seal it did not make: %v", err)
	}
}

// TestPostgresIdleSweepMovesAVersionThatCannotBeSealedBehindTheOthers makes a candidate whose seal fails (it counts a file
// it no longer has) and checks that the sweep restarts its idle clock, so it neither heads every later scan nor blocks
// the versions behind it.
func TestPostgresIdleSweepMovesAVersionThatCannotBeSealedBehindTheOthers(t *testing.T) {
	store := rawVersionIntegrationStore(t)
	ctx := context.Background()
	repo, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "raw-poison-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:10], Format: FormatRaw, RawLayout: RawLayoutVersioned, SealMode: RawSealIdle})
	if err != nil {
		t.Fatal(err)
	}
	// The broken version stays a sweep candidate for every later suite of the shared database, so it is removed.
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM native_raw_versions WHERE repository_id=$1::uuid`, repo.ID)
	})
	for index, path := range []string{"tool/1.0.0/a", "tool/2.0.0/a"} {
		digest := "sha256:" + strings.Repeat("e", 63) + string(rune('1'+index))
		asset := RawAsset{RepositoryID: repo.ID, Path: path, Digest: digest, ObjectKey: "native/raw/" + digest, Size: 1, ContentType: "text/plain"}
		if _, err = store.PutRawAssetIf(ctx, asset, RawWriteCondition{RequireAbsent: true}); err != nil {
			t.Fatal(err)
		}
	}
	// 1.0.0 is the older write and loses its only file behind the version row's back.
	if _, err = store.db.ExecContext(ctx, `UPDATE native_raw_versions SET updated_at=now()-interval '1 hour' WHERE repository_id=$1::uuid AND version='1.0.0'`, repo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(ctx, `DELETE FROM native_raw_assets WHERE repository_id=$1::uuid AND path='tool/1.0.0/a'`, repo.ID); err != nil {
		t.Fatal(err)
	}
	sealed, err := store.SealIdleRawVersions(ctx, time.Now().Add(time.Minute), 10)
	if err != nil || len(sealed) != 1 || sealed[0].Version != "2.0.0" {
		t.Fatalf("the healthy version behind the broken one must be sealed: %#v %v", sealed, err)
	}
	var restarted bool
	if err = store.db.QueryRowContext(ctx, `SELECT updated_at > now()-interval '5 minutes' FROM native_raw_versions WHERE repository_id=$1::uuid AND version='1.0.0'`, repo.ID).Scan(&restarted); err != nil || !restarted {
		t.Fatalf("the version that could not be sealed restarts its idle clock: %v %v", restarted, err)
	}
	if sealed, err = store.SealIdleRawVersions(ctx, time.Now().Add(-time.Minute), 10); err != nil || len(sealed) != 0 {
		t.Fatalf("it is no longer a candidate until its next idle period: %#v %v", sealed, err)
	}
}

// TestPostgresTombstonedVersionBurnsItsIdentifier sets the tombstoned state directly (the tombstone operation is
// ADR 0028 slice 4) and checks that the write guard refuses the identifier and the reads hide the version.
func TestPostgresTombstonedVersionBurnsItsIdentifier(t *testing.T) {
	store := rawVersionIntegrationStore(t)
	ctx := context.Background()
	repo, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "raw-burn-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12], Format: FormatRaw, RawLayout: RawLayoutVersioned})
	if err != nil {
		t.Fatal(err)
	}
	asset := RawAsset{RepositoryID: repo.ID, Path: "tool/1.0.0/a", Digest: "sha256:" + strings.Repeat("c", 63) + "1", ObjectKey: "native/raw/sha256/c1", Size: 1, ContentType: "text/plain"}
	if _, err = store.PutRawAssetIf(ctx, asset, RawWriteCondition{RequireAbsent: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(ctx, `UPDATE native_raw_versions SET state='tombstoned' WHERE repository_id=$1::uuid`, repo.ID); err != nil {
		t.Fatal(err)
	}
	asset.Path = "tool/1.0.0/b"
	if _, err = store.PutRawAssetIf(ctx, asset, RawWriteCondition{RequireAbsent: true}); !errors.Is(err, ErrVersionTombstoned) {
		t.Fatalf("write to a tombstoned version: %v", err)
	}
	if _, _, err = store.CreateRawVersion(ctx, RawVersion{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0"}); !errors.Is(err, ErrVersionTombstoned) {
		t.Fatalf("create of a tombstoned version: %v", err)
	}
	if _, err = store.SealRawVersion(ctx, SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0"}); !errors.Is(err, ErrVersionTombstoned) {
		t.Fatalf("seal of a tombstoned version: %v", err)
	}
	if _, err = store.SetRawVersionProperties(ctx, repo.ID, "tool", "1.0.0", nil); !errors.Is(err, ErrVersionTombstoned) {
		t.Fatalf("properties of a tombstoned version: %v", err)
	}
	if _, err = store.GetRawVersion(ctx, repo.ID, "tool", "1.0.0"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read of a tombstoned version: %v", err)
	}
	if err = store.DeleteRawAsset(ctx, repo.ID, "tool/1.0.0/a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete in a tombstoned version: %v", err)
	}
	if packages, err := store.ListRawPackages(ctx, repo.ID, "", 10); err != nil || len(packages) != 0 {
		t.Fatalf("a tombstoned version is not listed: %#v %v", packages, err)
	}
}
