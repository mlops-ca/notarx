//go:build integration

package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// nativePublication is the coordinate and digest the artifact.published event of a publication must carry.
type nativePublication struct {
	coordinate, digest string
}

// aptTryPublish stages one package and publishes a snapshot of it, returning the publication error instead of failing the test.
func aptTryPublish(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, staged aptStaged, sequence int64) error {
	t.Helper()
	snapshot, err := store.CreateAPTRepositorySnapshot(ctx, APTRepositorySnapshot{ID: uuid.NewString(), RepositoryID: repo.ID, Suite: "stable", Sequence: sequence, State: APTRepositorySnapshotBuilding},
		[]APTSnapshotPackage{{PublicationSessionID: staged.SessionID, PackageRevisionID: staged.Revision.ID, Component: "main", Architecture: "amd64"}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, assets, release := aptSnapshotFixture(snapshot, uuid.NewString(), []APTPackageRevision{staged.Revision})
	_, err = store.PublishAPTRepositorySnapshotWithAudit(WithWriteActor(ctx, "release-operator"), snapshot, assets, release, AuditRecord{Repository: repo.Name, Actor: "release-operator", Outcome: AuditResolved, Operation: "apt.repository_snapshot.publish", Status: 200})
	if err != nil {
		if failErr := store.FailAPTRepositorySnapshot(ctx, snapshot.ID); failErr != nil {
			t.Fatal(failErr)
		}
	}
	return err
}

func countPublished(t *testing.T, store *PostgresStore, repo HostedRepository) (count int, coordinate string) {
	t.Helper()
	if err := store.db.QueryRowContext(context.Background(), `SELECT count(*),COALESCE(min(coordinate),'') FROM domain_events WHERE type='artifact.published' AND repository_id=$1::uuid`, repo.ID).Scan(&count, &coordinate); err != nil {
		t.Fatal(err)
	}
	return count, coordinate
}

// Every publish path of npm, PyPI, Go, Cargo, Conan and APT writes artifact.published in the publication's own
// transaction: a publish that fails after the event was written (the quota check, which is the last statement of each)
// leaves no event row and no delivery, and the publish that succeeds leaves exactly one.
func TestPostgresNativeFormatPublishedEmissionIsTransactional(t *testing.T) {
	store := counterTestStore(t)
	ctx := WithWriteActor(context.Background(), "ci")
	var npm NPMVersion
	var pypi PyPIFile
	var goModule GoModulePublication
	// Each case attempts the publication and returns the coordinate and digest the event must carry.
	cases := map[Format]func(repo HostedRepository) (nativePublication, error){
		FormatNPM: func(repo HostedRepository) (nativePublication, error) {
			if npm.RepositoryID != repo.ID {
				npm = npmFixture(repo, "widget", "1.0.0", 100)
			}
			_, err := store.PublishNPMVersion(ctx, npm, map[string]string{"latest": npm.Version})
			return nativePublication{"widget@1.0.0", npm.Digest}, err
		},
		FormatPyPI: func(repo HostedRepository) (nativePublication, error) {
			if pypi.RepositoryID != repo.ID {
				pypi = pypiFixture(repo, "widget", "1.0", "widget-1.0-py3-none-any.whl", 100)
			}
			_, err := store.PublishPyPIFile(ctx, pypi)
			return nativePublication{"widget@1.0", pypi.Digest}, err
		},
		FormatGo: func(repo HostedRepository) (nativePublication, error) {
			if len(goModule.Assets) == 0 || goModule.Version.RepositoryID != repo.ID {
				goModule = goPublicationFixture(repo, "example.com/team/widget", "v1.0.0", [3]int64{40, 40, 40})
			}
			_, _, err := store.PublishGoModule(ctx, goModule)
			return nativePublication{"example.com/team/widget@v1.0.0", goModule.Assets[2].Digest}, err
		},
		FormatCargo: func(repo HostedRepository) (nativePublication, error) {
			claim := CargoIdentityClaim{RepositoryID: repo.ID, Name: "demo-crate", Version: "1.0.0", Digest: "sha256:" + strings.Repeat("a", 64), MetadataDigest: "sha256:" + strings.Repeat("c", 64)}
			reservation, _, err := store.ReserveCargoIdentity(ctx, claim)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = store.CommitCargoPublication(ctx, cargoTestPublication(t, claim, reservation, 100))
			return nativePublication{"demo-crate@1.0.0", claim.Digest}, err
		},
		FormatConan: func(repo HostedRepository) (nativePublication, error) {
			recipe, _, err := conanCounterRecipe(t, ctx, store, repo, "pkg/1.0/user/stable", "rrev1", 100)
			return nativePublication{"pkg/1.0/user/stable#rrev1", recipe.Digest}, err
		},
	}
	for format, publish := range cases {
		t.Run(string(format), func(t *testing.T) {
			repo := counterTestRepository(t, store, format)
			if _, err := store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, 50); err != nil {
				t.Fatal(err)
			}
			if _, err := publish(repo); err == nil {
				t.Fatal("the publish above the quota succeeded")
			}
			if n, _ := countPublished(t, store, repo); n != 0 {
				t.Fatalf("a rolled-back publish left %d artifact.published events", n)
			}
			if _, err := store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, 10000); err != nil {
				t.Fatal(err)
			}
			published, err := publish(repo)
			if err != nil {
				t.Fatal(err)
			}
			n, coordinate := countPublished(t, store, repo)
			if n != 1 || coordinate != published.coordinate {
				t.Fatalf("committed publish wrote %d events for %q, want 1 for %q", n, coordinate, published.coordinate)
			}
			if got := countDomainEvents(t, store, publishedKey(repo, published.coordinate, published.digest)); got != 1 {
				t.Fatalf("the event carries another digest: %d rows for %s", got, published.digest)
			}
			// A replay of the publish, accepted or rejected as a conflict, is not a new publication.
			_, _ = publish(repo)
			if n, _ = countPublished(t, store, repo); n != 1 {
				t.Fatalf("a replay wrote another event: %d", n)
			}
		})
	}
	t.Run("apt", func(t *testing.T) {
		repo := counterTestRepository(t, store, FormatAPT)
		if _, err := store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, 10000); err != nil {
			t.Fatal(err)
		}
		staged, err := aptPackage(t, ctx, store, repo, "widget", 80)
		if err != nil {
			t.Fatal(err)
		}
		used, _, _ := counterRow(t, store.db, repo.ID)
		if _, err = store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, used+1); err != nil {
			t.Fatal(err)
		}
		if err = aptTryPublish(t, ctx, store, repo, staged, 1); err == nil {
			t.Fatal("the snapshot above the quota published")
		}
		if n, _ := countPublished(t, store, repo); n != 0 {
			t.Fatalf("a rolled-back snapshot left %d artifact.published events", n)
		}
		if _, err = store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, 10000); err != nil {
			t.Fatal(err)
		}
		if err = aptTryPublish(t, ctx, store, repo, staged, 2); err != nil {
			t.Fatal(err)
		}
		pool := APTPoolPath("main", "widget", staged.Revision.ObjectName)
		if n, coordinate := countPublished(t, store, repo); n != 1 || coordinate != pool {
			t.Fatalf("committed snapshot wrote %d events for %q, want 1 for %q", n, coordinate, pool)
		}
		// A later snapshot that carries the same pool path again publishes nothing new.
		if err = aptTryPublish(t, ctx, store, repo, staged, 3); err != nil {
			t.Fatal(err)
		}
		if n, _ := countPublished(t, store, repo); n != 1 {
			t.Fatalf("a snapshot that only carries the pool path again wrote %d events", n)
		}
	})
}

// A Cargo publication writes artifact.published once and no Cargo-specific event: the triggers only name the promotion and
// replication outcomes, so the code path and the triggers cannot announce the same publication twice.
func TestPostgresCargoPublicationWritesOneFormatNeutralEvent(t *testing.T) {
	store := counterTestStore(t)
	ctx := WithWriteActor(context.Background(), "ci")
	repo := counterTestRepository(t, store, FormatCargo)
	publication, err := cargoCommit(t, ctx, store, repo, "demo-crate", "1.0.0", 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CommitCargoPublication(ctx, publication); err != nil {
		t.Fatal(err)
	}
	var total int
	if err = store.db.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE repository_id=$1::uuid`, repo.ID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 || countDomainEvents(t, store, publishedKey(repo, "demo-crate@1.0.0", publication.Digest)) != 1 {
		t.Fatalf("a Cargo publication wrote %d events, want one artifact.published", total)
	}
}

// The Conan session commit writes the events of the recipe and the package with the committing actor.
func TestPostgresConanSessionCommitEmitsWithTheCommitActor(t *testing.T) {
	store := counterTestStore(t)
	fixture := newConanManagedContractFixture(t, store, "package")
	t.Cleanup(func() { _, _ = store.db.Exec(`DELETE FROM hosted_repositories WHERE id=$1::uuid`, fixture.repo.ID) })
	fixture.stageAll(t)
	fixture.commit(t, "package-committer")
	var actor sql.NullString
	if err := store.db.QueryRow(`SELECT payload->>'actor' FROM domain_events WHERE type='artifact.published' AND repository_id=$1::uuid AND coordinate LIKE '%#%/%#%'`, fixture.repo.ID).Scan(&actor); err != nil || actor.String != "package-committer" {
		t.Fatalf("package event actor = %q (%v)", actor.String, err)
	}
}

// The repair path covers npm, PyPI, Go, Cargo, Conan and APT: with their events gone it writes one per visible hosted
// publication, once.
func TestPostgresReconcilePublishedEventsCoversTheNativeFormats(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	npmRepo := counterTestRepository(t, store, FormatNPM)
	pypiRepo := counterTestRepository(t, store, FormatPyPI)
	goRepo := counterTestRepository(t, store, FormatGo)
	cargoRepo := counterTestRepository(t, store, FormatCargo)
	conanRepo := counterTestRepository(t, store, FormatConan)
	aptRepo := counterTestRepository(t, store, FormatAPT)
	npm := npmFixture(npmRepo, "widget", "1.0.0", 10)
	if _, err := store.PublishNPMVersion(ctx, npm, map[string]string{"latest": npm.Version}); err != nil {
		t.Fatal(err)
	}
	pypi := pypiFixture(pypiRepo, "widget", "1.0", "widget-1.0-py3-none-any.whl", 10)
	if _, err := store.PublishPyPIFile(ctx, pypi); err != nil {
		t.Fatal(err)
	}
	goModule := goPublicationFixture(goRepo, "example.com/team/widget", "v1.0.0", [3]int64{1, 2, 3})
	if _, _, err := store.PublishGoModule(ctx, goModule); err != nil {
		t.Fatal(err)
	}
	crate, err := cargoCommit(t, ctx, store, cargoRepo, "demo-crate", "1.0.0", 10)
	if err != nil {
		t.Fatal(err)
	}
	recipe, _, err := conanCounterRecipe(t, ctx, store, conanRepo, "pkg/1.0/user/stable", "rrev1", 10)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := aptPackage(t, ctx, store, aptRepo, "widget", 80)
	if err != nil {
		t.Fatal(err)
	}
	aptPublishSnapshot(t, ctx, store, aptRepo, 1, "one", []aptStaged{staged})
	keys := []string{
		publishedKey(npmRepo, "widget@1.0.0", npm.Digest),
		publishedKey(pypiRepo, "widget@1.0", pypi.Digest),
		publishedKey(goRepo, "example.com/team/widget@v1.0.0", goModule.Assets[2].Digest),
		publishedKey(cargoRepo, "demo-crate@1.0.0", crate.Digest),
		publishedKey(conanRepo, "pkg/1.0/user/stable#rrev1", recipe.Digest),
		publishedKey(aptRepo, APTPoolPath("main", "widget", staged.Revision.ObjectName), staged.Revision.Digest),
	}
	for _, key := range keys {
		if n := countDomainEvents(t, store, key); n != 1 {
			t.Fatalf("%s: %d events after the publish, want 1", key, n)
		}
		if _, err = store.db.ExecContext(ctx, `DELETE FROM domain_events WHERE dedupe_key=$1`, key); err != nil {
			t.Fatal(err)
		}
	}
	window := time.Now().Add(-time.Hour)
	for {
		written, reconcileErr := store.ReconcilePublishedEvents(ctx, window, 1000)
		if reconcileErr != nil {
			t.Fatal(reconcileErr)
		}
		if written == 0 {
			break
		}
	}
	for _, key := range keys {
		if n := countDomainEvents(t, store, key); n != 1 {
			t.Fatalf("%s: %d events after the reconcile, want 1", key, n)
		}
	}
}
