//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/mlops-ca/notarx/internal/testsupport"
)

// catalogDifferences returns the coordinates of the repository whose artifact_catalog row is
// missing, extra or different from what the backfill query computes from the per-format tables.
func catalogDifferences(t *testing.T, db *sql.DB, format Format, repositoryID string) []string {
	t.Helper()
	expected, err := catalogExpectedSQL(format)
	if err != nil {
		t.Fatal(err)
	}
	const columns = "repository_id, coordinate, latest_version, latest_digest, latest_published_at, publisher, version_count, size_bytes"
	rows, err := db.Query(`WITH expected AS (`+expected+`),
		want AS (SELECT `+columns+` FROM expected WHERE repository_id=$1::uuid),
		have AS (SELECT `+columns+` FROM artifact_catalog WHERE repository_id=$1::uuid AND format=$2)
		SELECT 'missing or different: ' || coordinate FROM (SELECT * FROM want EXCEPT SELECT * FROM have) m
		UNION ALL
		SELECT 'extra or different: ' || coordinate FROM (SELECT * FROM have EXCEPT SELECT * FROM want) x`, repositoryID, string(format))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var differences []string
	for rows.Next() {
		var difference string
		if err := rows.Scan(&difference); err != nil {
			t.Fatal(err)
		}
		differences = append(differences, difference)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return differences
}

func assertCatalogEqualsProjection(t *testing.T, store *PostgresStore, repo HostedRepository, step string) {
	t.Helper()
	if differences := catalogDifferences(t, store.db, repo.Format, repo.ID); len(differences) > 0 {
		t.Fatalf("%s: artifact_catalog differs from the projection: %s", step, strings.Join(differences, "; "))
	}
}

func catalogRowCount(t *testing.T, db *sql.DB, format Format, repositoryID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM artifact_catalog WHERE repository_id=$1::uuid AND format=$2`, repositoryID, string(format)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestArtifactCatalogEqualsTheProjectionAfterEveryStep drives each covered format through the
// publish, replace, yank, tombstone, restore and delete sequences of the capacity equality
// scenarios and asserts after every step that the catalogue equals what the backfill query
// computes from the per-format tables (ADR 0023 section 8). This is also the per-format
// yank and tombstone recompute test.
func TestArtifactCatalogEqualsTheProjectionAfterEveryStep(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	for _, entry := range artifactCatalogRegistry {
		if !entry.Covered {
			continue
		}
		testCase, ok := counterEqualityCases[string(entry.Format)]
		if !ok {
			t.Errorf("%s is covered by the catalogue but has no scenario in counterEqualityCases", entry.Format)
			continue
		}
		t.Run(string(entry.Format), func(t *testing.T) {
			repo := counterTestRepository(t, store, testCase.repoFormat)
			steps := 0
			testCase.run(t, ctx, store, repo, func(step string) {
				steps++
				assertCatalogEqualsProjection(t, store, repo, step)
			})
			if steps == 0 {
				t.Fatal("the scenario made no checks")
			}
		})
	}
}

// TestArtifactCatalogBackfillEqualsTheProjectionAndResumes empties the catalogue after the
// scenarios, as if the rows predated the migration, interrupts the backfill after one batch,
// resumes it, and runs it a third time: the catalogue equals the projection and the second
// and third passes change nothing.
func TestArtifactCatalogBackfillEqualsTheProjectionAndResumes(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `DELETE FROM artifact_catalog_backfill`); err != nil {
		t.Fatal(err)
	}
	repos := map[Format]HostedRepository{}
	for _, entry := range artifactCatalogRegistry {
		testCase, ok := counterEqualityCases[string(entry.Format)]
		if !entry.Covered || !ok {
			continue
		}
		repo := counterTestRepository(t, store, testCase.repoFormat)
		testCase.run(t, ctx, store, repo, func(string) {})
		repos[entry.Format] = repo
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM artifact_catalog WHERE repository_id = ANY($1::uuid[])`, repositoryIDs(repos)); err != nil {
		t.Fatal(err)
	}

	// One batch of one coordinate per format, then stop: the interrupted run.
	for format := range repos {
		if _, _, busy, err := store.backfillArtifactCatalogBatch(ctx, format, 1); err != nil || busy {
			t.Fatalf("%s: first batch: busy=%v err=%v", format, busy, err)
		}
	}
	var cursors int
	if err := store.db.QueryRow(`SELECT count(*) FROM artifact_catalog_backfill WHERE cursor_coordinate <> ''`).Scan(&cursors); err != nil {
		t.Fatal(err)
	}
	if cursors == 0 {
		t.Fatal("the interrupted backfill stored no cursor")
	}

	for pass := 1; pass <= 3; pass++ {
		progress, err := store.BackfillArtifactCatalog(ctx, 2)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if len(progress) != coveredCatalogFormats() {
			t.Fatalf("pass %d: progress for %d formats, want %d", pass, len(progress), coveredCatalogFormats())
		}
		for _, p := range progress {
			if !p.Completed {
				t.Errorf("pass %d: %s did not complete", pass, p.Format)
			}
			if pass > 1 && p.Refreshed != 0 {
				t.Errorf("pass %d: %s refreshed %d coordinates after completing", pass, p.Format, p.Refreshed)
			}
		}
		for format, repo := range repos {
			assertCatalogEqualsProjection(t, store, repo, fmt.Sprintf("pass %d (%s)", pass, format))
		}
	}
	var total int
	for format, repo := range repos {
		total += catalogRowCount(t, store.db, format, repo.ID)
	}
	if total == 0 {
		t.Fatal("the backfill produced no catalogue rows from the scenarios")
	}
}

func repositoryIDs(repos map[Format]HostedRepository) []string {
	ids := make([]string, 0, len(repos))
	for _, repo := range repos {
		ids = append(ids, repo.ID)
	}
	return ids
}

func coveredCatalogFormats() int {
	count := 0
	for _, entry := range artifactCatalogRegistry {
		if entry.Covered {
			count++
		}
	}
	return count
}

// TestArtifactCatalogBackfillCountsEqualPerFormat checks, per covered format, that the number
// of catalogue rows equals the number of distinct coordinates the projection holds for the
// repositories the scenarios created, after a backfill from an empty catalogue.
func TestArtifactCatalogBackfillCountsEqualPerFormat(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	for _, entry := range artifactCatalogRegistry {
		testCase, ok := counterEqualityCases[string(entry.Format)]
		if !entry.Covered || !ok {
			continue
		}
		t.Run(string(entry.Format), func(t *testing.T) {
			repo := counterTestRepository(t, store, testCase.repoFormat)
			testCase.run(t, ctx, store, repo, func(string) {})
			if _, err := store.db.ExecContext(ctx, `DELETE FROM artifact_catalog WHERE repository_id=$1::uuid`, repo.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `DELETE FROM artifact_catalog_backfill WHERE format=$1`, string(entry.Format)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.BackfillArtifactCatalog(ctx, CatalogBackfillBatchSize); err != nil {
				t.Fatal(err)
			}
			expected, err := catalogExpectedSQL(entry.Format)
			if err != nil {
				t.Fatal(err)
			}
			var want int
			if err := store.db.QueryRow(`SELECT count(*) FROM (`+expected+`) e WHERE repository_id=$1::uuid`, repo.ID).Scan(&want); err != nil {
				t.Fatal(err)
			}
			if got := catalogRowCount(t, store.db, entry.Format, repo.ID); got != want {
				t.Fatalf("catalogue has %d rows, the projection has %d coordinates", got, want)
			}
		})
	}
}

func catalogProxyRepository(t *testing.T, store *PostgresStore, format Format) HostedRepository {
	t.Helper()
	repo, err := store.CreateHostedRepository(context.Background(), HostedRepository{
		ID: uuid.NewString(), Name: "catalog-proxy-" + string(format) + "-" + uuid.NewString()[:8], Format: format, Type: RepositoryTypeProxy, Endpoint: "https://proxy.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testsupport.PurgeRepository(t, store.db, repo.ID) })
	return repo
}

// TestArtifactCatalogFollowsTheProxyCaches covers the writers the format scenarios do not:
// metadata sync, negative entries and tarball or file caching of the proxy repositories.
func TestArtifactCatalogFollowsTheProxyCaches(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	digest := "sha256:" + strings.Repeat("c", 64)

	t.Run("npm", func(t *testing.T) {
		repo := catalogProxyRepository(t, store, FormatNPM)
		version := NPMVersion{Version: "1.0.0", UpstreamTarball: "https://registry.example/widget.tgz", TarballName: "widget.tgz", Manifest: []byte(`{"name":"widget","version":"1.0.0"}`)}
		pkg := NPMPackage{RepositoryID: repo.ID, SourceVersion: repo.Version, Name: "widget", SourceEndpoint: repo.Endpoint, Versions: []NPMVersion{version}, DistTags: map[string]string{"latest": "1.0.0"}}
		if _, err := store.SyncNPMProxyPackage(ctx, pkg); err != nil {
			t.Fatal(err)
		}
		assertCatalogEqualsProjection(t, store, repo, "metadata sync")
		if err := store.StoreNPMProxyNegative(ctx, NPMPackage{RepositoryID: repo.ID, SourceVersion: repo.Version, Name: "widget", SourceEndpoint: repo.Endpoint}); err != nil {
			t.Fatal(err)
		}
		assertCatalogEqualsProjection(t, store, repo, "negative entry hides the package")
		if catalogRowCount(t, store.db, FormatNPM, repo.ID) != 0 {
			t.Fatal("a negative package kept its catalogue row")
		}
		if _, err := store.SyncNPMProxyPackage(ctx, pkg); err != nil {
			t.Fatal(err)
		}
		assertCatalogEqualsProjection(t, store, repo, "metadata sync after the negative entry")
		cached, err := store.GetNPMVersion(ctx, repo.ID, "widget", "1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		cached.Digest, cached.ObjectKey, cached.Size, cached.SourceVersion = digest, "npm/widget", 10, repo.Version
		if _, err = store.CacheNPMProxyTarball(ctx, cached); err != nil {
			t.Fatal(err)
		}
		assertCatalogEqualsProjection(t, store, repo, "tarball cached")
	})

	t.Run("pypi", func(t *testing.T) {
		repo := catalogProxyRepository(t, store, FormatPyPI)
		file := PyPIFile{SourceVersion: repo.Version, Version: "1.0", Filename: "widget-1.0.whl", Digest: digest, SourceURL: "https://pypi.example/widget.whl"}
		if err := store.SyncPyPIProxyFiles(ctx, repo.ID, "widget", []PyPIFile{file}); err != nil {
			t.Fatal(err)
		}
		assertCatalogEqualsProjection(t, store, repo, "metadata sync")
		if _, err := store.CachePyPIProxyFile(ctx, PyPIFile{RepositoryID: repo.ID, SourceVersion: repo.Version, Filename: file.Filename, Digest: digest, SourceURL: file.SourceURL, ObjectKey: "pypi/widget", Size: 20}); err != nil {
			t.Fatal(err)
		}
		assertCatalogEqualsProjection(t, store, repo, "file cached")
	})

	t.Run("go", func(t *testing.T) {
		repo := catalogProxyRepository(t, store, FormatGo)
		if err := store.SyncGoProxyVersions(ctx, repo.ID, "example.com/widget", []GoModuleVersion{{Version: "v1.0.0"}, {Version: "v1.1.0"}}); err != nil {
			t.Fatal(err)
		}
		assertCatalogEqualsProjection(t, store, repo, "versions synced")
		if _, err := store.PutGoModuleVersion(ctx, GoModuleVersion{RepositoryID: repo.ID, Module: "example.com/widget", Version: "v1.2.0"}); err != nil {
			t.Fatal(err)
		}
		assertCatalogEqualsProjection(t, store, repo, "version put")
		asset := GoModuleAsset{RepositoryID: repo.ID, Module: "example.com/widget", Version: "v1.2.0", Kind: "zip", Digest: digest, ObjectKey: "go/widget.zip", Size: 30, SourceURL: "https://proxy.golang.example/widget.zip"}
		if _, err := store.CacheGoModuleAsset(ctx, asset); err != nil {
			t.Fatal(err)
		}
		assertCatalogEqualsProjection(t, store, repo, "asset cached")
	})

	t.Run("apt", func(t *testing.T) {
		repo := catalogProxyRepository(t, store, FormatAPT)
		asset := APTAsset{RepositoryID: repo.ID, SourceVersion: repo.Version, Path: "pool/main/w/widget/widget_1.0_amd64.deb", Digest: digest, ObjectKey: "apt/widget", Size: 40, ContentType: "application/vnd.debian.binary-package", SourceURL: "https://deb.example/widget.deb"}
		if _, err := store.CacheAPTAsset(ctx, asset); err != nil {
			t.Fatal(err)
		}
		assertCatalogEqualsProjection(t, store, repo, "asset cached")
		if catalogRowCount(t, store.db, FormatAPT, repo.ID) != 1 {
			t.Fatal("the cached asset has no catalogue row")
		}
	})
}

// TestArtifactCatalogSurvivesConcurrentPublishesOfOneCoordinate publishes many versions of one
// Go module at once. The transactions do not share a lock of their own, so only the catalogue
// row lock and the recompute that follows it keep the version count exact.
func TestArtifactCatalogSurvivesConcurrentPublishesOfOneCoordinate(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	repo := counterTestRepository(t, store, FormatGo)
	const versions = 12
	var wg sync.WaitGroup
	for i := range versions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			publication := goPublicationFixture(repo, "example.com/team/busy", fmt.Sprintf("v1.0.%d", i), [3]int64{1, 2, 3})
			if _, _, err := store.PublishGoModule(ctx, publication); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	assertCatalogEqualsProjection(t, store, repo, "after concurrent publishes")
	var count int
	if err := store.db.QueryRow(`SELECT version_count FROM artifact_catalog WHERE repository_id=$1::uuid AND format='go' AND coordinate='example.com/team/busy'`, repo.ID).Scan(&count); err != nil || count != versions {
		t.Fatalf("version_count = %d, %v; want %d", count, err, versions)
	}
}
