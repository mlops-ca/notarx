//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mlops-ca/notarx/internal/capacity"
)

// counterEquality is the per-format equality test of ADR 0023 section 3: it drives the
// format's store through publish, replace, delete and re-publish and calls check after
// every step, which asserts that the usage counter equals the reconciler's aggregation
// for the repository. A format whose registry entry is covered must have one.
type counterEquality struct {
	repoFormat Format
	run        func(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, check func(step string))
	// concurrent publishes distinct objects of size bytes at the quota edge, when the
	// format supports it; it returns how many publishes succeeded and were rejected.
	edge func(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, size int64, workers int) (accepted, rejected int64)
}

// counterEqualityCases lists the covered formats' tests; add a case when a format's
// registry entry becomes covered.
var counterEqualityCases = map[string]counterEquality{
	"raw":  {repoFormat: FormatRaw, run: rawCounterEquality, edge: rawCounterEdge},
	"go":   {repoFormat: FormatGo, run: goCounterEquality, edge: goCounterEdge},
	"pypi": {repoFormat: FormatPyPI, run: pypiCounterEquality, edge: pypiCounterEdge},
}

// assertCounterEqualsUsage fails when the repository's counter differs from the
// aggregation the reconciler recomputes.
func assertCounterEqualsUsage(t *testing.T, store *PostgresStore, repo HostedRepository, step string) {
	t.Helper()
	query, ok := capacity.UsageQuery(string(repo.Format))
	if !ok {
		t.Fatalf("no usage query for %s", repo.Format)
	}
	var usedBytes, usedObjects int64
	if err := store.db.QueryRow(query, repo.ID).Scan(&usedBytes, &usedObjects); err != nil {
		t.Fatalf("%s: aggregate usage: %v", step, err)
	}
	var bytes, objects int64
	// A repository that never reserved has no counter row; it must have no usage either.
	if err := store.db.QueryRow(`SELECT bytes,objects FROM repository_usage_counters WHERE repository_id=$1::uuid`, repo.ID).Scan(&bytes, &objects); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("%s: counter row: %v", step, err)
	}
	if bytes != usedBytes || objects != usedObjects {
		t.Fatalf("%s: counter bytes=%d objects=%d, aggregation bytes=%d objects=%d", step, bytes, objects, usedBytes, usedObjects)
	}
}

func TestCapacityCountersEqualAggregationForEveryCoveredFormat(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	for _, entry := range capacity.Formats() {
		if !entry.Covered {
			t.Logf("%s: pending, no counter equality test required yet", entry.Format)
			continue
		}
		testCase, ok := counterEqualityCases[entry.Format]
		if !ok {
			t.Errorf("%s is covered in the capacity registry but has no counter equality test", entry.Format)
			continue
		}
		t.Run(entry.Format, func(t *testing.T) {
			repo := counterTestRepository(t, store, testCase.repoFormat)
			testCase.run(t, ctx, store, repo, func(step string) { assertCounterEqualsUsage(t, store, repo, step) })
		})
	}
	for format := range counterEqualityCases {
		if !capacity.Covered(format) {
			t.Errorf("%s has an equality test but is not marked covered in the capacity registry", format)
		}
	}
}

func TestCapacityConcurrentPublishesRejectExactlyTheOverflowPerFormat(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	for _, entry := range capacity.Formats() {
		testCase, ok := counterEqualityCases[entry.Format]
		if !ok || !entry.Covered || testCase.edge == nil {
			continue
		}
		t.Run(entry.Format, func(t *testing.T) {
			repo := counterTestRepository(t, store, testCase.repoFormat)
			const size, workers, fit = 100, 24, 10
			if _, err := store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, size*fit); err != nil {
				t.Fatal(err)
			}
			accepted, rejected := testCase.edge(t, ctx, store, repo, size, workers)
			if accepted != fit || rejected != workers-fit {
				t.Fatalf("accepted %d rejected %d, want %d and %d", accepted, rejected, fit, workers-fit)
			}
			assertCounterEqualsUsage(t, store, repo, "after the quota edge")
		})
	}
}

func rawFixture(repo HostedRepository, path, content string) RawAsset {
	sum := sha256.Sum256([]byte(content))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	return RawAsset{RepositoryID: repo.ID, Path: path, Digest: digest, ObjectKey: "raw/" + digest, Size: int64(len(content)), ContentType: "application/octet-stream"}
}

func rawCounterEquality(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, check func(string)) {
	put := func(path, content string) {
		t.Helper()
		if _, err := store.PutRawAsset(ctx, rawFixture(repo, path, content)); err != nil {
			t.Fatalf("put %s: %v", path, err)
		}
	}
	put("a.bin", "0123456789")
	check("publish a")
	put("a.bin", "0123456789012345678901234")
	check("replace a with a larger object")
	put("a.bin", "tiny")
	check("replace a with a smaller object")
	put("a.bin", "tiny")
	check("replace a with the same object")
	put("b.bin", "tiny") // a second path referencing the same object
	check("publish b sharing a's object")
	put("c.bin", "another object")
	check("publish c")
	if err := store.DeleteRawAsset(ctx, repo.ID, "a.bin"); err != nil {
		t.Fatal(err)
	}
	check("delete a")
	if _, err := store.RestoreRawAsset(ctx, repo.ID, "a.bin"); err != nil {
		t.Fatal(err)
	}
	check("restore a")
	if err := store.DeleteRawAsset(ctx, repo.ID, "a.bin"); err != nil {
		t.Fatal(err)
	}
	put("a.bin", "published again")
	check("re-publish a after delete")
	// A conditional write that fails its condition changes nothing.
	if _, err := store.PutRawAssetIf(ctx, rawFixture(repo, "a.bin", "never stored"), RawWriteCondition{RequireAbsent: true}); !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("conditional put = %v, want ErrPreconditionFailed", err)
	}
	check("failed conditional put")
	// Completing an upload counts like a put.
	upload, err := store.CreateRawUpload(ctx, RawUpload{ID: uuid.NewString(), RepositoryID: repo.ID, Path: "d.bin", ObjectKey: "raw/upload-" + uuid.NewString(), State: "open", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompleteRawUpload(ctx, upload.ID, rawFixture(repo, "d.bin", "uploaded content")); err != nil {
		t.Fatal(err)
	}
	check("complete upload d")
	// Over the quota: rejected, counter unchanged.
	var used int64
	if err = store.db.QueryRow(`SELECT bytes FROM repository_usage_counters WHERE repository_id=$1::uuid`, repo.ID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, used); err != nil {
		t.Fatal(err)
	}
	if _, err = store.PutRawAsset(ctx, rawFixture(repo, "e.bin", "does not fit any more")); !IsQuotaExceeded(err) {
		t.Fatalf("put over quota = %v, want quota exceeded", err)
	}
	check("rejected put over quota")
}

func rawCounterEdge(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, size int64, workers int) (accepted, rejected int64) {
	var acc, rej atomic.Int64
	var wg sync.WaitGroup
	for index := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			content := make([]byte, size)
			for position := range content {
				content[position] = byte('a' + index%26)
			}
			asset := rawFixture(repo, "edge/"+string(rune('a'+index))+".bin", string(content)+string(rune('A'+index)))
			asset.Size = size
			switch _, err := store.PutRawAsset(ctx, asset); {
			case err == nil:
				acc.Add(1)
			case IsQuotaExceeded(err):
				rej.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	return acc.Load(), rej.Load()
}

// uniqueDigest returns a sha256 digest no other fixture shares.
func uniqueDigest() string {
	sum := sha256.Sum256([]byte(uuid.NewString()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func goPublicationFixture(repo HostedRepository, module, version string, sizes [3]int64) GoModulePublication {
	publication := GoModulePublication{Version: GoModuleVersion{RepositoryID: repo.ID, Module: module, Version: version, Publisher: "builder", PublishedAt: time.Now().UTC()}}
	for index, kind := range []string{"info", "mod", "zip"} {
		publication.Assets = append(publication.Assets, GoModuleAsset{RepositoryID: repo.ID, Module: module, Version: version, Kind: kind, Digest: uniqueDigest(), ObjectKey: "native/go/" + uuid.NewString(), Size: sizes[index]})
	}
	return publication
}

func goCounterEquality(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, check func(string)) {
	first := goPublicationFixture(repo, "example.com/team/widget", "v1.0.0", [3]int64{10, 20, 30})
	if _, _, err := store.PublishGoModule(ctx, first); err != nil {
		t.Fatal(err)
	}
	check("publish v1.0.0")
	if _, _, err := store.PublishGoModule(ctx, goPublicationFixture(repo, "example.com/team/widget", "v1.1.0", [3]int64{1, 2, 3})); err != nil {
		t.Fatal(err)
	}
	check("publish v1.1.0")
	if _, existed, err := store.PublishGoModule(ctx, first); err != nil || !existed {
		t.Fatalf("idempotent republish existed=%v err=%v", existed, err)
	}
	check("idempotent republish of v1.0.0")
	cached := GoModuleAsset{RepositoryID: repo.ID, Module: "example.com/cached", Version: "v0.1.0", Kind: "zip", Digest: uniqueDigest(), ObjectKey: "native/go/" + uuid.NewString(), Size: 70}
	if _, err := store.PutGoModuleVersion(ctx, GoModuleVersion{RepositoryID: repo.ID, Module: cached.Module, Version: cached.Version, Publisher: "proxy", PublishedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"cache an asset", "cache the same asset again"} {
		if _, err := store.CacheGoModuleAsset(ctx, cached); err != nil {
			t.Fatal(err)
		}
		check(step)
	}
	if _, err := store.TombstoneGoModuleVersion(ctx, repo.ID, "example.com/team/widget", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	check("tombstone v1.0.0 (bytes stay until collected)")
	for _, asset := range first.Assets {
		if err := store.MarkGoModuleObjectCollecting(ctx, asset.ObjectKey); err != nil {
			t.Fatal(err)
		}
		check("mark collecting " + asset.Kind)
		if err := store.MarkGoModuleObjectCollected(ctx, asset.ObjectKey); err != nil {
			t.Fatal(err)
		}
		check("collect " + asset.Kind)
	}
	if _, _, err := store.PublishGoModule(ctx, goPublicationFixture(repo, "example.com/team/widget", "v1.2.0", [3]int64{5, 6, 7})); err != nil {
		t.Fatal(err)
	}
	check("publish v1.2.0 after collection")
	var used int64
	if err := store.db.QueryRow(`SELECT bytes FROM repository_usage_counters WHERE repository_id=$1::uuid`, repo.ID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, used); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PublishGoModule(ctx, goPublicationFixture(repo, "example.com/team/widget", "v1.3.0", [3]int64{1, 1, 1})); !IsQuotaExceeded(err) {
		t.Fatalf("publish over quota = %v, want quota exceeded", err)
	}
	check("rejected publish over quota")
}

func goCounterEdge(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, size int64, workers int) (accepted, rejected int64) {
	var acc, rej atomic.Int64
	var wg sync.WaitGroup
	for index := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Every version is size bytes: one byte each for info and mod, the rest the zip.
			switch _, _, err := store.PublishGoModule(ctx, goPublicationFixture(repo, "example.com/edge", "v1.0."+string(rune('a'+index)), [3]int64{1, 1, size - 2})); {
			case err == nil:
				acc.Add(1)
			case IsQuotaExceeded(err):
				rej.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	return acc.Load(), rej.Load()
}

func pypiFixture(repo HostedRepository, project, version, filename string, size int64) PyPIFile {
	return PyPIFile{RepositoryID: repo.ID, Project: project, Version: version, Filename: filename, FileType: "bdist_wheel", PythonVersion: "py3", Digest: uniqueDigest(), ObjectKey: "native/pypi/" + uuid.NewString(), Size: size, Publisher: "builder"}
}

func pypiCounterEquality(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, check func(string)) {
	one := pypiFixture(repo, "widget", "1.0", "widget-1.0-py3-none-any.whl", 10)
	if _, err := store.PublishPyPIFile(ctx, one); err != nil {
		t.Fatal(err)
	}
	check("publish a file")
	if _, err := store.PublishPyPIFile(ctx, one); err != nil {
		t.Fatalf("idempotent publish: %v", err)
	}
	check("publish the same file again")
	version := []PyPIFile{
		pypiFixture(repo, "widget", "2.0", "widget-2.0-py3-none-any.whl", 20),
		pypiFixture(repo, "widget", "2.0", "widget-2.0.tar.gz", 30),
	}
	version[1].FileType = "sdist"
	if _, err := store.PublishPyPIVersion(ctx, version); err != nil {
		t.Fatal(err)
	}
	check("publish a version of two files")
	if _, err := store.PublishPyPIVersion(ctx, version); err != nil {
		t.Fatal(err)
	}
	check("publish the same version again")
	if _, err := store.TombstonePyPIVersion(ctx, repo.ID, "widget", "2.0"); err != nil {
		t.Fatal(err)
	}
	check("tombstone version 2.0")
	if _, err := store.RestorePyPIVersion(ctx, repo.ID, "widget", "2.0"); err != nil {
		t.Fatal(err)
	}
	check("restore version 2.0")
	if _, err := store.TombstonePyPIVersion(ctx, repo.ID, "widget", "1.0"); err != nil {
		t.Fatal(err)
	}
	check("tombstone version 1.0")
	if _, err := store.PublishPyPIFile(ctx, pypiFixture(repo, "widget", "3.0", "widget-3.0-py3-none-any.whl", 7)); err != nil {
		t.Fatal(err)
	}
	check("publish after tombstone")
	// A proxy repository caches bytes after listing metadata: only the cached file counts.
	proxy, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "pypi-proxy-counter-" + uuid.NewString(), Format: FormatPyPI, Type: RepositoryTypeProxy, Endpoint: "https://proxy.example"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = store.db.Exec(`DELETE FROM hosted_repositories WHERE id=$1`, proxy.ID) })
	listed := PyPIFile{RepositoryID: proxy.ID, SourceVersion: proxy.Version, Project: "widget", Version: "1.0", Filename: "widget-1.0-py3-none-any.whl", FileType: "bdist_wheel", PythonVersion: "py3", Digest: uniqueDigest(), Publisher: "upstream", SourceURL: "https://files.example/widget-1.0-py3-none-any.whl"}
	if err = store.SyncPyPIProxyFiles(ctx, proxy.ID, "widget", []PyPIFile{listed}); err != nil {
		t.Fatal(err)
	}
	assertCounterEqualsUsage(t, store, proxy, "sync proxy metadata")
	listed.ObjectKey, listed.Size, listed.CachedAt = "native/pypi/"+uuid.NewString(), 40, time.Now().UTC()
	for _, step := range []string{"cache a proxy file", "cache the proxy file again"} {
		if _, err = store.CachePyPIProxyFile(ctx, listed); err != nil {
			t.Fatal(err)
		}
		assertCounterEqualsUsage(t, store, proxy, step)
	}
	var used int64
	if err = store.db.QueryRow(`SELECT bytes FROM repository_usage_counters WHERE repository_id=$1::uuid`, repo.ID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, used); err != nil {
		t.Fatal(err)
	}
	if _, err = store.PublishPyPIFile(ctx, pypiFixture(repo, "widget", "4.0", "widget-4.0-py3-none-any.whl", 1)); !IsQuotaExceeded(err) {
		t.Fatalf("publish over quota = %v, want quota exceeded", err)
	}
	if _, err = store.PublishPyPIVersion(ctx, []PyPIFile{pypiFixture(repo, "widget", "5.0", "widget-5.0-py3-none-any.whl", 1)}); !IsQuotaExceeded(err) {
		t.Fatalf("publish version over quota = %v, want quota exceeded", err)
	}
	check("rejected publishes over quota")
}

func pypiCounterEdge(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, size int64, workers int) (accepted, rejected int64) {
	var acc, rej atomic.Int64
	var wg sync.WaitGroup
	for index := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := "edge" + string(rune('a'+index))
			switch _, err := store.PublishPyPIFile(ctx, pypiFixture(repo, name, "1.0", name+"-1.0-py3-none-any.whl", size)); {
			case err == nil:
				acc.Add(1)
			case IsQuotaExceeded(err):
				rej.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	return acc.Load(), rej.Load()
}
