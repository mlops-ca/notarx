//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/testsupport"

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
	"raw":   {repoFormat: FormatRaw, run: rawCounterEquality, edge: rawCounterEdge},
	"go":    {repoFormat: FormatGo, run: goCounterEquality, edge: goCounterEdge},
	"oci":   {repoFormat: FormatOCI, run: ociCounterEquality, edge: ociCounterEdge},
	"helm":  {repoFormat: FormatHelm, run: helmCounterEquality, edge: helmCounterEdge},
	"nuget": {repoFormat: FormatNuGet, run: nugetCounterEquality, edge: nugetCounterEdge},
	"apt":   {repoFormat: FormatAPT, run: aptCounterEquality, edge: aptCounterEdge},
	"cargo": {repoFormat: FormatCargo, run: cargoCounterEquality, edge: cargoCounterEdge},
	"npm":   {repoFormat: FormatNPM, run: npmCounterEquality, edge: npmCounterEdge},
	"pypi":  {repoFormat: FormatPyPI, run: pypiCounterEquality, edge: pypiCounterEdge},
	"maven": {repoFormat: FormatMaven, run: mavenCounterEquality, edge: mavenCounterEdge},
	"conan": {repoFormat: FormatConan, run: conanCounterEquality, edge: conanCounterEdge},
}

// counterEqualityElsewhere names the equality tests of covered formats whose store lives in
// another package (it imports this one, so its test cannot be listed above).
var counterEqualityElsewhere = map[string]struct{ file, test string }{
	"rubygems": {file: "../rubygemspublication/pgstore/capacity_integration_test.go", test: "TestRubyGemsCounterEquality"},
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
		if elsewhere, found := counterEqualityElsewhere[entry.Format]; found && !ok {
			source, err := os.ReadFile(elsewhere.file)
			if err != nil || !strings.Contains(string(source), "func "+elsewhere.test+"(") {
				t.Errorf("%s names its equality test %s in %s, which does not exist: %v", entry.Format, elsewhere.test, elsewhere.file, err)
			}
			continue
		}
		if !ok {
			t.Errorf("%s is covered in the capacity registry but has no counter equality test", entry.Format)
			continue
		}
		t.Run(entry.Format, func(t *testing.T) {
			repo := counterTestRepository(t, store, testCase.repoFormat)
			testCase.run(t, ctx, store, repo, func(step string) { assertCounterEqualsUsage(t, store, repo, step) })
		})
	}
	for format := range counterEqualityElsewhere {
		if !capacity.Covered(format) {
			t.Errorf("%s has an equality test but is not marked covered in the capacity registry", format)
		}
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
	helmSessionLifecycleEquality(t, ctx, store, repo, check)
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
	t.Cleanup(func() { testsupport.PurgeRepository(t, store.db, proxy.ID) })
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

func npmFixture(repo HostedRepository, name, version string, size int64) NPMVersion {
	return NPMVersion{RepositoryID: repo.ID, PackageName: name, Version: version, Digest: uniqueDigest(), ObjectKey: "npm/" + uuid.NewString(), TarballName: name + "-" + version + ".tgz", Size: size, Manifest: []byte(`{"name":"` + name + `","version":"` + version + `"}`)}
}

func npmCounterEquality(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, check func(string)) {
	for _, version := range []NPMVersion{npmFixture(repo, "widget", "1.0.0", 10), npmFixture(repo, "widget", "2.0.0", 20)} {
		if _, err := store.PublishNPMVersion(ctx, version, map[string]string{"latest": version.Version}); err != nil {
			t.Fatal(err)
		}
		check("publish " + version.Version)
	}
	if _, err := store.PublishNPMVersion(ctx, npmFixture(repo, "widget", "1.0.0", 99), map[string]string{}); !errors.Is(err, ErrNameExists) {
		t.Fatalf("republish an existing version = %v, want ErrNameExists", err)
	}
	check("rejected republish of 1.0.0")
	if _, err := store.TombstoneNPMVersion(ctx, repo.ID, "widget", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	check("tombstone 1.0.0 (bytes stay until the row goes)")
	if _, err := store.RestoreNPMVersion(ctx, NPMRestoreRequest{RepositoryID: repo.ID, PackageName: "widget", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	check("restore 1.0.0")
	if _, err := store.PublishNPMVersion(ctx, npmFixture(repo, "widget", "3.0.0", 5), map[string]string{}); err != nil {
		t.Fatal(err)
	}
	check("publish after restore")
	proxy, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "npm-proxy-counter-" + uuid.NewString(), Format: FormatNPM, Type: RepositoryTypeProxy, Endpoint: "https://proxy.example"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testsupport.PurgeRepository(t, store.db, proxy.ID) })
	listed := NPMVersion{Version: "1.0.0", UpstreamTarball: "https://registry.example/widget.tgz", TarballName: "widget.tgz", Manifest: []byte(`{"name":"widget","version":"1.0.0"}`)}
	if _, err = store.SyncNPMProxyPackage(ctx, NPMPackage{RepositoryID: proxy.ID, SourceVersion: proxy.Version, Name: "widget", SourceEndpoint: proxy.Endpoint, Versions: []NPMVersion{listed}, DistTags: map[string]string{"latest": "1.0.0"}}); err != nil {
		t.Fatal(err)
	}
	assertCounterEqualsUsage(t, store, proxy, "sync proxy metadata")
	cached, err := store.GetNPMVersion(ctx, proxy.ID, "widget", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	cached.Digest, cached.ObjectKey, cached.Size, cached.SourceVersion = uniqueDigest(), "npm/"+uuid.NewString(), 40, proxy.Version
	for _, step := range []string{"cache a proxy tarball", "cache the proxy tarball again"} {
		if _, err = store.CacheNPMProxyTarball(ctx, cached); err != nil {
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
	if _, err = store.PublishNPMVersion(ctx, npmFixture(repo, "widget", "4.0.0", 1), map[string]string{}); !IsQuotaExceeded(err) {
		t.Fatalf("publish over quota = %v, want quota exceeded", err)
	}
	check("rejected publish over quota")
}

func npmCounterEdge(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, size int64, workers int) (accepted, rejected int64) {
	var acc, rej atomic.Int64
	var wg sync.WaitGroup
	for index := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch _, err := store.PublishNPMVersion(ctx, npmFixture(repo, "edge", "1.0."+string(rune('a'+index)), size), map[string]string{}); {
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

// cargoCommit publishes one crate version and returns the publication it committed.
func cargoCommit(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, name, version string, size int64) (CargoPublication, error) {
	t.Helper()
	claim := CargoIdentityClaim{RepositoryID: repo.ID, Name: name, Version: version, Digest: uniqueDigest(), MetadataDigest: uniqueDigest()}
	reservation, _, err := store.ReserveCargoIdentity(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	publication := cargoTestPublication(t, claim, reservation, size)
	_, _, err = store.CommitCargoPublication(ctx, publication)
	return publication, err
}

func cargoCounterEquality(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, check func(string)) {
	first, err := cargoCommit(t, ctx, store, repo, "demo-crate", "1.0.0", 10)
	if err != nil {
		t.Fatal(err)
	}
	check("publish 1.0.0")
	if _, err = cargoCommit(t, ctx, store, repo, "demo-crate", "1.1.0", 20); err != nil {
		t.Fatal(err)
	}
	check("publish 1.1.0")
	if _, _, err = store.CommitCargoPublication(ctx, first); err != nil {
		t.Fatalf("replay of 1.0.0: %v", err)
	}
	check("replay 1.0.0")
	if _, _, err = store.SetCargoYanked(ctx, repo.ID, "demo-crate", "1.1.0", true); err != nil {
		t.Fatal(err)
	}
	check("yank 1.1.0")
	if _, err = store.TombstoneCargoPublication(ctx, repo.ID, "demo-crate", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	check("tombstone 1.0.0 (bytes stay until collected)")
	if err = store.MarkCargoObjectCollecting(ctx, first.ObjectKey); err != nil {
		t.Fatal(err)
	}
	if err = store.MarkCargoObjectCollected(ctx, first.ObjectKey); err != nil {
		t.Fatal(err)
	}
	check("collect 1.0.0")
	if _, err = cargoCommit(t, ctx, store, repo, "demo-crate", "2.0.0", 5); err != nil {
		t.Fatal(err)
	}
	check("publish 2.0.0 after collection")
	var used int64
	if err = store.db.QueryRow(`SELECT bytes FROM repository_usage_counters WHERE repository_id=$1::uuid`, repo.ID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, used); err != nil {
		t.Fatal(err)
	}
	if _, err = cargoCommit(t, ctx, store, repo, "demo-crate", "3.0.0", 1); !IsQuotaExceeded(err) {
		t.Fatalf("publish over quota = %v, want quota exceeded", err)
	}
	check("rejected publish over quota")
}

func cargoCounterEdge(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, size int64, workers int) (accepted, rejected int64) {
	var acc, rej atomic.Int64
	var wg sync.WaitGroup
	for index := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch _, err := cargoCommit(t, ctx, store, repo, "edge-crate", "1.0."+strconv.Itoa(index), size); {
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

// aptPackage stages one package revision through a publication session.
type aptStaged struct {
	Revision  APTPackageRevision
	SessionID string
}

func aptPackage(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, name string, size int64) (aptStaged, error) {
	t.Helper()
	session, err := aptOpenSession(ctx, store, repo, name, size, time.Hour)
	if err != nil {
		return aptStaged{}, err
	}
	objectKey := "native/apt/sha256/" + strings.TrimPrefix(session.DeclaredDigest, "sha256:")
	if err := store.BeginAPTPackageUpload(ctx, session.ID, objectKey); err != nil {
		return aptStaged{}, err
	}
	revision, err := store.CompleteAPTPackageUpload(ctx, session.ID, APTPackageRevision{
		ID: uuid.NewString(), RepositoryID: repo.ID, Package: name, Version: "1.0-1", Architecture: "amd64",
		CanonicalIdentity: name + "@1.0-1#amd64", Digest: session.DeclaredDigest, ObjectKey: objectKey, Size: size, ObjectName: session.ObjectName, Publisher: "ci",
	})
	return aptStaged{Revision: revision, SessionID: session.ID}, err
}

// aptOpenSession admits one publication session of the declared size; admission reserves the
// bytes in the usage counter until the session completes or expires.
func aptOpenSession(ctx context.Context, store *PostgresStore, repo HostedRepository, name string, size int64, ttl time.Duration) (APTPublicationSession, error) {
	session := APTPublicationSession{
		ID: uuid.NewString(), RepositoryID: repo.ID, Suite: "stable", Component: "main", Publisher: "ci",
		ObjectName: name + "_1.0-1_amd64.deb", DeclaredDigest: uniqueDigest(), DeclaredSize: size,
		State: APTPublicationSessionOpen, ExpiresAt: time.Now().Add(ttl),
	}
	_, _, err := store.CreateAPTPublicationSessionIdempotently(ctx, session, "ci", "apt/"+repo.ID, "key-"+session.ID, "payload-"+session.ID)
	return session, err
}

// aptPublishSnapshot publishes a visible snapshot of the suite holding the given
// revisions; marker changes the generated dists/ objects, so each snapshot has its own.
func aptPublishSnapshot(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, sequence int64, marker string, staged []aptStaged) {
	t.Helper()
	packages := make([]APTSnapshotPackage, 0, len(staged))
	revisions := make([]APTPackageRevision, 0, len(staged))
	for _, item := range staged {
		revisions = append(revisions, item.Revision)
		packages = append(packages, APTSnapshotPackage{PublicationSessionID: item.SessionID, PackageRevisionID: item.Revision.ID, Component: "main", Architecture: "amd64"})
	}
	snapshot, err := store.CreateAPTRepositorySnapshot(ctx, APTRepositorySnapshot{ID: uuid.NewString(), RepositoryID: repo.ID, Suite: "stable", Sequence: sequence, State: APTRepositorySnapshotBuilding}, packages)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, assets, releaseBody := aptSnapshotFixture(snapshot, marker, revisions)
	if _, err = store.PublishAPTRepositorySnapshotWithAudit(ctx, snapshot, assets, releaseBody, AuditRecord{Repository: repo.Name, Actor: "release-operator", Outcome: AuditResolved, Operation: "apt.repository_snapshot.publish", Status: 200}); err != nil {
		t.Fatal(err)
	}
}

// aptSnapshotFixture returns the visible snapshot, its assets and the Release body of a snapshot
// of the given revisions: generated dists/ objects named by marker plus one pool object per revision.
func aptSnapshotFixture(snapshot APTRepositorySnapshot, marker string, revisions []APTPackageRevision) (APTRepositorySnapshot, []APTSnapshotAsset, []byte) {
	repoID, suite := snapshot.RepositoryID, snapshot.Suite
	indexBody := []byte(marker + ":Packages")
	indexSum := sha256.Sum256(indexBody)
	indexDigest := "sha256:" + hex.EncodeToString(indexSum[:])
	releaseBody := []byte(fmt.Sprintf("Suite: %s\nAcquire-By-Hash: yes\nSHA256:\n %s %16d main/binary-amd64/Packages\n", suite, strings.TrimPrefix(indexDigest, "sha256:"), len(indexBody)))
	bodies := map[string][]byte{
		"dists/" + suite + "/Release": releaseBody, "dists/" + suite + "/InRelease": []byte(marker + ":InRelease"),
		"dists/" + suite + "/Release.gpg": []byte(marker + ":Release.gpg"), "dists/" + suite + "/main/binary-amd64/Packages": indexBody,
		"dists/" + suite + "/main/binary-amd64/by-hash/SHA256/" + strings.TrimPrefix(indexDigest, "sha256:"): indexBody,
	}
	assets := make([]APTSnapshotAsset, 0, len(bodies)+len(revisions))
	for path, body := range bodies {
		sum := sha256.Sum256(body)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		assets = append(assets, APTSnapshotAsset{SnapshotID: snapshot.ID, RepositoryID: repoID, Path: path, Digest: digest, ObjectKey: "native/apt/sha256/" + strings.TrimPrefix(digest, "sha256:"), Size: int64(len(body)), ContentType: "text/plain"})
	}
	for _, revision := range revisions {
		assets = append(assets, APTSnapshotAsset{SnapshotID: snapshot.ID, RepositoryID: repoID, Path: APTPoolPath("main", revision.Package, revision.ObjectName), Digest: revision.Digest, ObjectKey: revision.ObjectKey, Size: revision.Size, ContentType: "application/vnd.debian.binary-package"})
	}
	snapshot.State = APTRepositorySnapshotVisible
	for _, asset := range assets {
		switch asset.Path {
		case "dists/" + suite + "/Release":
			snapshot.ReleaseDigest = asset.Digest
		case "dists/" + suite + "/InRelease":
			snapshot.InReleaseDigest = asset.Digest
		}
	}
	snapshot.SignerIdentity, snapshot.KeyFingerprint, snapshot.SignatureAlgorithm = "integration-signer", strings.Repeat("b", 40), "integration-fixture"
	return snapshot, assets, releaseBody
}

func aptCounterEquality(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, check func(string)) {
	first, err := aptPackage(t, ctx, store, repo, "widget", 80)
	if err != nil {
		t.Fatal(err)
	}
	check("stage a package revision")
	second, err := aptPackage(t, ctx, store, repo, "gadget", 120)
	if err != nil {
		t.Fatal(err)
	}
	check("stage a second package revision")
	aptPublishSnapshot(t, ctx, store, repo, 1, "one", []aptStaged{first})
	check("publish the first snapshot (generated objects become visible)")
	aptPublishSnapshot(t, ctx, store, repo, 2, "two", []aptStaged{first, second})
	check("publish a second snapshot that retires the first")
	aptPublishSnapshot(t, ctx, store, repo, 3, "two", []aptStaged{first, second})
	check("publish a third snapshot with the same generated objects")
	aptSessionAndRestoreEquality(t, ctx, store, repo, check)
	// A proxy repository caches upstream objects; refreshed index files replace their bytes.
	proxy, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "apt-proxy-counter-" + uuid.NewString(), Format: FormatAPT, Type: RepositoryTypeProxy, Endpoint: "https://deb.example"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testsupport.PurgeRepository(t, store.db, proxy.ID) })
	cache := func(path string, size int64, step string) {
		t.Helper()
		digest := uniqueDigest()
		if _, err := store.CacheAPTAsset(ctx, APTAsset{RepositoryID: proxy.ID, SourceVersion: proxy.Version, Path: path, Digest: digest, ObjectKey: "native/apt/" + uuid.NewString(), Size: size, ContentType: "text/plain", SourceURL: "https://deb.example/" + path}); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		assertCounterEqualsUsage(t, store, proxy, step)
	}
	cache("pool/main/h/hello/hello_1.deb", 50, "cache a package")
	cache("dists/bookworm/InRelease", 10, "cache an index")
	cache("dists/bookworm/InRelease", 25, "refresh the index with a larger object")
	cache("dists/bookworm/InRelease", 5, "refresh the index with a smaller object")
	var used int64
	if err = store.db.QueryRow(`SELECT bytes FROM repository_usage_counters WHERE repository_id=$1::uuid`, repo.ID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, used); err != nil {
		t.Fatal(err)
	}
	if _, err = aptPackage(t, ctx, store, repo, "overflow", 1); !IsQuotaExceeded(err) {
		t.Fatalf("stage over quota = %v, want quota exceeded", err)
	}
	check("rejected package over quota")
}

// aptSessionAndRestoreEquality drives the pending reservations of APT: an open session holds its
// declared bytes in the counter until it expires, and an archive restore holds its reservation until
// it fails or commits. The counter equals the reconciler aggregation after every step.
func aptSessionAndRestoreEquality(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, check func(string)) {
	t.Helper()
	before := counterBytes(t, store, repo)
	session, err := aptOpenSession(ctx, store, repo, "pending", 33, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	check("open a publication session")
	if got := counterBytes(t, store, repo); got != before+33 {
		t.Fatalf("open session counter = %d, want %d", got, before+33)
	}
	if err = store.BeginAPTPackageUpload(ctx, session.ID, "native/apt/sha256/"+strings.TrimPrefix(session.DeclaredDigest, "sha256:")); err != nil {
		t.Fatal(err)
	}
	check("begin the upload of the session")
	if _, err = store.ExpireAPTPublicationSessions(ctx, time.Now().Add(2*time.Minute), 100); err != nil {
		t.Fatal(err)
	}
	check("expire the abandoned session")
	if got := counterBytes(t, store, repo); got != before {
		t.Fatalf("expired session counter = %d, want %d", got, before)
	}

	// An archive restore of one new package into a suite of its own.
	now := time.Now().UTC().Truncate(time.Microsecond)
	revision := APTPackageRevision{ID: uuid.NewString(), RepositoryID: repo.ID, Package: "restored", Version: "1.0-1", Architecture: "amd64",
		CanonicalIdentity: "restored@1.0-1#amd64", Digest: uniqueDigest(), Size: 40, ObjectName: "restored_1.0-1_amd64.deb", Publisher: "ci", CreatedAt: now}
	revision.ObjectKey = "native/apt/sha256/" + strings.TrimPrefix(revision.Digest, "sha256:")
	snapshot := APTRepositorySnapshot{ID: uuid.NewString(), RepositoryID: repo.ID, Suite: "restored", Sequence: 1, State: APTRepositorySnapshotVisible, CreatedAt: now, PublishedAt: now}
	snapshot, assets, release := aptSnapshotFixture(snapshot, "restored", []APTPackageRevision{revision})
	plan := APTArchiveRestorePlan{ID: uuid.NewString(), ArchiveDigest: uniqueDigest(), Snapshot: snapshot, Packages: []APTArchivePackage{{Component: "main", Revision: revision}}, Assets: assets}
	if err = store.BeginAPTArchiveRestore(ctx, plan); err != nil {
		t.Fatal(err)
	}
	check("begin an archive restore")
	if got := counterBytes(t, store, repo); got <= before {
		t.Fatalf("archive restore reserved nothing: counter %d, before %d", got, before)
	}
	if err = store.FailAPTArchiveRestore(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	check("fail the archive restore")
	if got := counterBytes(t, store, repo); got != before {
		t.Fatalf("failed restore counter = %d, want %d", got, before)
	}
	// At the quota edge the restore is refused and reserves nothing.
	if _, err = store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, before); err != nil {
		t.Fatal(err)
	}
	plan.ID = uuid.NewString()
	if err = store.BeginAPTArchiveRestore(ctx, plan); !IsQuotaExceeded(err) {
		t.Fatalf("restore over quota = %v, want quota exceeded", err)
	}
	check("refuse an archive restore over quota")
	if _, err = store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, 1<<30); err != nil {
		t.Fatal(err)
	}
	if err = store.BeginAPTArchiveRestore(ctx, plan); err != nil {
		t.Fatal(err)
	}
	committed := plan
	committed.ID = uuid.NewString()
	if err = store.BeginAPTArchiveRestore(ctx, committed); err != nil {
		t.Fatal(err)
	}
	check("begin a second attempt that supersedes the first")
	// Committing releases the reservation and counts the restored objects once.
	if _, err = store.CommitAPTArchiveRestore(ctx, committed, release, AuditRecord{Repository: repo.Name, Actor: "release-operator", Outcome: AuditResolved, Operation: "apt.archive.restore", Status: 200}); err != nil {
		t.Fatal(err)
	}
	check("commit the archive restore")
}

func counterBytes(t *testing.T, store *PostgresStore, repo HostedRepository) int64 {
	t.Helper()
	var bytes int64
	if err := store.db.QueryRow(`SELECT bytes FROM repository_usage_counters WHERE repository_id=$1::uuid`, repo.ID).Scan(&bytes); err != nil {
		t.Fatal(err)
	}
	return bytes
}

func aptCounterEdge(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, size int64, workers int) (accepted, rejected int64) {
	var acc, rej atomic.Int64
	var wg sync.WaitGroup
	for index := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch _, err := aptPackage(t, ctx, store, repo, "edge"+string(rune('a'+index)), size); {
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

func helmPublish(ctx context.Context, store *PostgresStore, repo HostedRepository, name, version string, size, provenanceSize int64) error {
	session := HelmSession{ID: uuid.NewString(), RepositoryID: repo.ID, Name: name, Version: version, Digest: uniqueDigest(), Size: size, Actor: "publisher",
		MetadataJSON: []byte(`{"name":"` + name + `","version":"` + version + `"}`), ChartYAML: []byte("chart metadata"), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if provenanceSize > 0 {
		session.ProvenanceDigest, session.ProvenanceSize = uniqueDigest(), provenanceSize
	}
	reserved, _, err := store.ReserveHelmPublication(ctx, session)
	if err != nil {
		return err
	}
	_, _, err = store.CommitHelmPublication(ctx, reserved.ID, "publisher")
	return err
}

// helmOpen admits one pending Helm session; admission reserves its bytes in the usage counter.
func helmOpen(ctx context.Context, store *PostgresStore, repo HostedRepository, name, version string, size, provenanceSize int64) (HelmSession, error) {
	session := HelmSession{ID: uuid.NewString(), RepositoryID: repo.ID, Name: name, Version: version, Digest: uniqueDigest(), Size: size, Actor: "publisher",
		MetadataJSON: []byte(`{"name":"` + name + `","version":"` + version + `"}`), ChartYAML: []byte("chart metadata"), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if provenanceSize > 0 {
		session.ProvenanceDigest, session.ProvenanceSize = uniqueDigest(), provenanceSize
	}
	reserved, _, err := store.ReserveHelmPublication(ctx, session)
	return reserved, err
}

// helmSessionLifecycleEquality drives the pending reservation of Helm sessions: an open session holds
// its bytes (chart and provenance) in the counter until it commits or expires, lazily when a later
// reservation finds it past its deadline and through the janitor.
func helmSessionLifecycleEquality(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, check func(string)) {
	t.Helper()
	before := counterBytes(t, store, repo)
	if _, err := helmOpen(ctx, store, repo, "pending", "1.0.0", 11, 4); err != nil {
		t.Fatal(err)
	}
	check("open a session")
	if got := counterBytes(t, store, repo); got != before+15 {
		t.Fatalf("open session counter = %d, want %d", got, before+15)
	}
	stale, err := helmOpen(ctx, store, repo, "stale", "1.0.0", 9, 0)
	if err != nil {
		t.Fatal(err)
	}
	check("open a second session")
	if _, err = store.db.ExecContext(ctx, `UPDATE native_helm_publication_sessions SET expires_at=now()-interval '1 minute' WHERE id=$1::uuid`, stale.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = helmOpen(ctx, store, repo, "fresh", "1.0.0", 3, 0); err != nil {
		t.Fatal(err)
	}
	check("open a session after another passed its deadline (lazy expiry)")
	if got := counterBytes(t, store, repo); got != before+15+3 {
		t.Fatalf("counter after lazy expiry = %d, want %d", got, before+15+3)
	}
	if _, err = store.ExpireHelmSessions(ctx, time.Now().Add(2*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	check("expire the abandoned sessions")
	if got := counterBytes(t, store, repo); got != before {
		t.Fatalf("counter after the janitor = %d, want %d", got, before)
	}
	// Expiring again releases nothing twice.
	if _, err = store.ExpireHelmSessions(ctx, time.Now().Add(2*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	check("expire again")
}

func nugetOpen(ctx context.Context, store *PostgresStore, repo HostedRepository, packageID, version string, size int64) (NuGetSession, error) {
	session := NuGetSession{ID: uuid.NewString(), RepositoryID: repo.ID, PackageID: strings.ToLower(packageID), Version: version, Digest: uniqueDigest(), Size: size, Actor: "publisher", ExpiresAt: time.Now().UTC().Add(time.Hour)}
	reserved, _, err := store.ReserveNuGetPublication(ctx, session)
	return reserved, err
}

// nugetSessionLifecycleEquality is the NuGet counterpart: reserved and staged sessions hold their
// bytes until they commit or expire, lazily or through the janitor.
func nugetSessionLifecycleEquality(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, check func(string)) {
	t.Helper()
	before := counterBytes(t, store, repo)
	staged, err := nugetOpen(ctx, store, repo, "Pending", "1.0.0", 12)
	if err != nil {
		t.Fatal(err)
	}
	check("reserve a session")
	staged.Nuspec = []byte("nuspec")
	staged.MetadataJSON = []byte(`{"id":"pending","version":"1.0.0","dependencyGroups":[]}`)
	if _, err = store.StageNuGetPublication(ctx, staged); err != nil {
		t.Fatal(err)
	}
	check("stage the session")
	stale, err := nugetOpen(ctx, store, repo, "Stale", "1.0.0", 9)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(ctx, `UPDATE native_nuget_publication_sessions SET expires_at=now()-interval '1 minute' WHERE id=$1::uuid`, stale.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = nugetOpen(ctx, store, repo, "Fresh", "1.0.0", 3); err != nil {
		t.Fatal(err)
	}
	check("reserve a session after another passed its deadline (lazy expiry)")
	if got := counterBytes(t, store, repo); got != before+12+3 {
		t.Fatalf("counter after lazy expiry = %d, want %d", got, before+12+3)
	}
	if _, err = store.ExpireNuGetSessions(ctx, time.Now().Add(2*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	check("expire the abandoned sessions")
	if got := counterBytes(t, store, repo); got != before {
		t.Fatalf("counter after the janitor = %d, want %d", got, before)
	}
	if _, err = store.ExpireNuGetSessions(ctx, time.Now().Add(2*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	check("expire again")
}

func helmCounterEquality(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, check func(string)) {
	for _, step := range []struct {
		name, version string
		size, prov    int64
	}{{"chart", "1.0.0", 60, 10}, {"chart", "1.1.0", 25, 0}, {"other", "0.1.0", 5, 3}} {
		if err := helmPublish(ctx, store, repo, step.name, step.version, step.size, step.prov); err != nil {
			t.Fatal(err)
		}
		check("publish " + step.name + " " + step.version)
	}
	// Committing a session twice is a replay and counts once.
	session := HelmSession{ID: uuid.NewString(), RepositoryID: repo.ID, Name: "replay", Version: "1.0.0", Digest: uniqueDigest(), Size: 7, Actor: "publisher", MetadataJSON: []byte(`{"name":"replay","version":"1.0.0"}`), ChartYAML: []byte("chart metadata"), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	reserved, _, err := store.ReserveHelmPublication(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"commit a session", "commit the same session again"} {
		if _, _, err = store.CommitHelmPublication(ctx, reserved.ID, "publisher"); err != nil {
			t.Fatal(err)
		}
		check(step)
	}
	var used int64
	if err = store.db.QueryRow(`SELECT bytes FROM repository_usage_counters WHERE repository_id=$1::uuid`, repo.ID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, used); err != nil {
		t.Fatal(err)
	}
	if err = helmPublish(ctx, store, repo, "overflow", "1.0.0", 1, 0); !IsQuotaExceeded(err) {
		t.Fatalf("publish over quota = %v, want quota exceeded", err)
	}
	check("rejected publish over quota")
}

func helmCounterEdge(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, size int64, workers int) (accepted, rejected int64) {
	var acc, rej atomic.Int64
	var wg sync.WaitGroup
	for index := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := helmPublish(ctx, store, repo, "edge", "1.0."+strconv.Itoa(index), size, 0); {
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

func nugetPublish(ctx context.Context, store *PostgresStore, repo HostedRepository, packageID, version string, size int64) error {
	session := NuGetSession{ID: uuid.NewString(), RepositoryID: repo.ID, PackageID: strings.ToLower(packageID), Version: version, Digest: uniqueDigest(), Size: size, Actor: "publisher", ExpiresAt: time.Now().UTC().Add(time.Hour)}
	reserved, _, err := store.ReserveNuGetPublication(ctx, session)
	if err != nil {
		return err
	}
	reserved.Nuspec = []byte("nuspec")
	reserved.MetadataJSON = []byte(`{"id":"` + packageID + `","version":"` + version + `","dependencyGroups":[]}`)
	if _, err = store.StageNuGetPublication(ctx, reserved); err != nil {
		return err
	}
	_, _, err = store.CommitNuGetPublication(ctx, reserved.ID, "publisher")
	return err
}

func nugetCounterEquality(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, check func(string)) {
	for _, step := range []struct {
		id, version string
		size        int64
	}{{"Example", "1.0.0", 60}, {"Example", "1.1.0", 25}, {"Other", "0.1.0", 5}} {
		if err := nugetPublish(ctx, store, repo, step.id, step.version, step.size); err != nil {
			t.Fatal(err)
		}
		check("publish " + step.id + " " + step.version)
	}
	nugetSessionLifecycleEquality(t, ctx, store, repo, check)
	var used int64
	if err := store.db.QueryRow(`SELECT bytes FROM repository_usage_counters WHERE repository_id=$1::uuid`, repo.ID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, used); err != nil {
		t.Fatal(err)
	}
	if err := nugetPublish(ctx, store, repo, "Overflow", "1.0.0", 1); !IsQuotaExceeded(err) {
		t.Fatalf("publish over quota = %v, want quota exceeded", err)
	}
	check("rejected publish over quota")
}

func nugetCounterEdge(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, size int64, workers int) (accepted, rejected int64) {
	var acc, rej atomic.Int64
	var wg sync.WaitGroup
	for index := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := nugetPublish(ctx, store, repo, "Edge", "1.0."+strconv.Itoa(index), size); {
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

// ociUploadBlob uploads a new blob into the repository through an upload session.
func ociUploadBlob(ctx context.Context, store *PostgresStore, repo HostedRepository, size int64) (OCIBlob, error) {
	digest := uniqueDigest()
	upload, err := store.CreateOCIUpload(ctx, OCIUpload{ID: uuid.NewString(), RepositoryID: repo.ID, Name: "app", ObjectKey: "native/oci/uploads/" + uuid.NewString(), State: "open", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		return OCIBlob{}, err
	}
	return store.CompleteOCIUpload(ctx, upload.ID, OCIBlob{Digest: digest, ObjectKey: "native/oci/blobs/" + strings.TrimPrefix(digest, "sha256:"), Size: size})
}

func ociManifestFixture(repo HostedRepository, name string, size int64) OCIManifest {
	digest := uniqueDigest()
	return OCIManifest{RepositoryID: repo.ID, Name: name, Digest: digest, ObjectKey: "native/oci/manifests/" + strings.TrimPrefix(digest, "sha256:"), MediaType: "application/vnd.oci.image.manifest.v1+json", Size: size}
}

func ociCounterEquality(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, check func(string)) {
	other := counterTestRepository(t, store, FormatOCI)
	otherCheck := func(step string) { assertCounterEqualsUsage(t, store, other, step) }
	blob, err := ociUploadBlob(ctx, store, repo, 100)
	if err != nil {
		t.Fatal(err)
	}
	check("upload a blob")
	// Uploading the same blob again into the same repository is a re-reference and counts once.
	upload, err := store.CreateOCIUpload(ctx, OCIUpload{ID: uuid.NewString(), RepositoryID: repo.ID, Name: "app", ObjectKey: "native/oci/uploads/" + uuid.NewString(), State: "open", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompleteOCIUpload(ctx, upload.ID, blob); err != nil {
		t.Fatal(err)
	}
	check("upload the same blob again")
	if _, err = store.MountOCIBlobFrom(ctx, other.ID, repo.ID, blob.Digest); err != nil {
		t.Fatal(err)
	}
	otherCheck("mount the blob into a second repository")
	if _, err = store.MountOCIBlobFrom(ctx, other.ID, repo.ID, blob.Digest); err != nil {
		t.Fatal(err)
	}
	otherCheck("mount the blob again")
	if _, err = store.MountOCIBlob(ctx, repo.ID, blob.Digest); err != nil {
		t.Fatal(err)
	}
	check("mount an already referenced blob")
	manifest := ociManifestFixture(repo, "app", 30)
	for _, reference := range []string{"v1", "latest", manifest.Digest} {
		if _, err = store.PutOCIManifest(ctx, manifest, reference); err != nil {
			t.Fatal(err)
		}
		check("put the manifest as " + reference)
	}
	second := ociManifestFixture(repo, "app", 45)
	if _, err = store.PutOCIManifest(ctx, second, "v2"); err != nil {
		t.Fatal(err)
	}
	check("put a second manifest")
	if err = store.DeleteOCIManifest(ctx, repo.ID, "app", manifest.Digest); err != nil {
		t.Fatal(err)
	}
	check("delete the first manifest")
	if _, err = store.RestoreOCIManifest(ctx, repo.ID, "app", manifest.Digest); err != nil {
		t.Fatal(err)
	}
	check("restore the first manifest")
	// Replication copies the manifest and its blob references into another repository.
	target := counterTestRepository(t, store, FormatOCI)
	replicated := manifest
	replicated.RepositoryID = target.ID
	replicated.ObjectKey = "native/oci/manifests/" + uuid.NewString()
	if _, err = store.PublishReplicatedOCIManifest(ctx, OCIReplicationPublication{SourceRepositoryID: repo.ID, TargetRepositoryID: target.ID, Manifest: replicated, SourceObjectKey: manifest.ObjectKey, BlobDigests: []string{blob.Digest}}); err != nil {
		t.Fatal(err)
	}
	assertCounterEqualsUsage(t, store, target, "replicate a manifest and its blob")
	var used int64
	if err = store.db.QueryRow(`SELECT bytes FROM repository_usage_counters WHERE repository_id=$1::uuid`, repo.ID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, used); err != nil {
		t.Fatal(err)
	}
	if _, err = ociUploadBlob(ctx, store, repo, 1); !IsQuotaExceeded(err) {
		t.Fatalf("upload over quota = %v, want quota exceeded", err)
	}
	if _, err = store.PutOCIManifest(ctx, ociManifestFixture(repo, "app", 1), "v3"); !IsQuotaExceeded(err) {
		t.Fatalf("manifest over quota = %v, want quota exceeded", err)
	}
	check("rejected writes over quota")
}

func ociCounterEdge(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, size int64, workers int) (accepted, rejected int64) {
	var acc, rej atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch _, err := ociUploadBlob(ctx, store, repo, size); {
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
