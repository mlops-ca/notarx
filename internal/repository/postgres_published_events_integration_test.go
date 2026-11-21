//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/events"
	"github.com/mlops-ca/notarx/internal/testsupport"
)

func publishedKey(repo HostedRepository, coordinate, digest string) string {
	return events.Published(repo.ID, string(repo.Format), coordinate, digest, "").DedupeKey()
}

// Every publish path that emits artifact.published does it in the publication's own transaction: a publish that
// fails after the event was written (here the quota check, which is the last statement) leaves no event, and the
// same publish that succeeds leaves exactly one. The publishes run with no subscription and no rule, so a publish
// still succeeds without any consumer.
func TestPostgresPublishedEmissionIsTransactionalPerFormat(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	// publish returns the coordinate it published; Maven takes a new coordinate per attempt because its first,
	// rejected attempt leaves an open publish session on the coordinate it used.
	attempt := 0
	cases := map[string]func(repo HostedRepository) (string, error){
		"raw": func(repo HostedRepository) (string, error) {
			asset := rawFixture(repo, "docs/readme.txt", strings.Repeat("x", 100)+repo.ID)
			_, err := store.PutRawAsset(ctx, asset)
			return asset.Path, err
		},
		"oci": func(repo HostedRepository) (string, error) {
			manifest := ociManifestFixture(repo, "app", 100)
			_, err := store.PutOCIManifest(ctx, manifest, "latest")
			return manifest.Name, err
		},
		"maven": func(repo HostedRepository) (string, error) {
			attempt++
			coordinate := fmt.Sprintf("org.example:app:1.%d", attempt)
			_, _, err := mavenCounterPublish(ctx, store, repo, coordinate, mavenFile{name: fmt.Sprintf("app-1.%d.pom", attempt), size: 100})
			return coordinate, err
		},
	}
	for format, publish := range cases {
		t.Run(format, func(t *testing.T) {
			repo := counterTestRepository(t, store, Format(format))
			if _, err := store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, 50); err != nil {
				t.Fatal(err)
			}
			if _, err := publish(repo); err == nil {
				t.Fatal("the publish above the quota succeeded")
			}
			var rolledBack int
			if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE type='artifact.published' AND repository_id=$1::uuid`, repo.ID).Scan(&rolledBack); err != nil {
				t.Fatal(err)
			}
			if rolledBack != 0 {
				t.Fatalf("a rolled-back publish left %d artifact.published events", rolledBack)
			}
			if _, err := store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, 10000); err != nil {
				t.Fatal(err)
			}
			published, err := publish(repo)
			if err != nil {
				t.Fatal(err)
			}
			var committed int
			var coordinate string
			if err := store.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(min(coordinate),'') FROM domain_events WHERE type='artifact.published' AND repository_id=$1::uuid AND format=$2`, repo.ID, format).Scan(&committed, &coordinate); err != nil {
				t.Fatal(err)
			}
			if committed != 1 || coordinate != published {
				t.Fatalf("committed publish wrote %d events for %q, want 1 for %q", committed, coordinate, published)
			}
		})
	}
}

// Writing the same digest to the same Raw path again publishes nothing new; a new digest at the path does.
func TestPostgresRawRewriteOfTheSameDigestEmitsOnce(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	repo := counterTestRepository(t, store, FormatRaw)
	first := rawFixture(repo, "a.txt", "one "+repo.ID)
	var updatedAt [2]time.Time
	for i := range updatedAt {
		if _, err := store.PutRawAsset(ctx, first); err != nil {
			t.Fatal(err)
		}
		if err := store.db.QueryRowContext(ctx, `SELECT updated_at FROM native_raw_assets WHERE repository_id=$1::uuid AND path=$2`, repo.ID, first.Path).Scan(&updatedAt[i]); err != nil {
			t.Fatal(err)
		}
	}
	if !updatedAt[0].Equal(updatedAt[1]) {
		t.Fatalf("an identical rewrite moved updated_at: %v -> %v", updatedAt[0], updatedAt[1])
	}
	if n := countDomainEvents(t, store, publishedKey(repo, first.Path, first.Digest)); n != 1 {
		t.Fatalf("rewriting the same digest left %d events, want 1", n)
	}
	second := rawFixture(repo, "a.txt", "two "+repo.ID)
	if _, err := store.PutRawAsset(ctx, second); err != nil {
		t.Fatal(err)
	}
	if n := countDomainEvents(t, store, publishedKey(repo, second.Path, second.Digest)); n != 1 {
		t.Fatalf("a new digest left %d events, want 1", n)
	}
}

// The catch-up reconciler emits the events a publication lacks, idempotently, with several reconcilers racing, and
// leaves a publication outside the window alone.
func TestPostgresReconcilePublishedEvents(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	reconcileAll := func(floor time.Time) int {
		total := 0
		for {
			written, err := store.ReconcilePublishedEvents(ctx, floor, 1000)
			if err != nil {
				t.Fatal(err)
			}
			total += written
			if written == 0 {
				return total
			}
		}
	}
	rawRepo := counterTestRepository(t, store, FormatRaw)
	ociRepo := counterTestRepository(t, store, FormatOCI)
	mavenRepo := counterTestRepository(t, store, FormatMaven)
	asset := rawFixture(rawRepo, "r.txt", "raw "+rawRepo.ID)
	if _, err := store.PutRawAsset(ctx, asset); err != nil {
		t.Fatal(err)
	}
	manifest := ociManifestFixture(ociRepo, "app", 10)
	if _, err := store.PutOCIManifest(ctx, manifest, "latest"); err != nil {
		t.Fatal(err)
	}
	artifact, _, err := mavenCounterPublish(ctx, store, mavenRepo, "org.example:lib:1.0", mavenFile{name: "lib-1.0.pom", size: 10})
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{publishedKey(rawRepo, asset.Path, asset.Digest), publishedKey(ociRepo, manifest.Name, manifest.Digest), publishedKey(mavenRepo, artifact.Coordinate, artifact.Digest)}
	remove := func() {
		for _, key := range keys {
			if _, err := store.db.ExecContext(ctx, `DELETE FROM domain_events WHERE dedupe_key=$1`, key); err != nil {
				t.Fatal(err)
			}
		}
	}
	remove() // the publications are visible and have no event, as a path that does not emit leaves them
	window := time.Now().Add(-time.Hour)
	if reconcileAll(time.Now().Add(time.Hour)) != 0 {
		t.Fatal("a publication outside the window was reconciled")
	}
	for _, key := range keys {
		if n := countDomainEvents(t, store, key); n != 0 {
			t.Fatalf("%s: %d events outside the window", key, n)
		}
	}
	if reconcileAll(window) < len(keys) {
		t.Fatal("the reconciler wrote fewer events than the missing publications")
	}
	for _, key := range keys {
		if n := countDomainEvents(t, store, key); n != 1 {
			t.Fatalf("%s: %d events after the first reconcile, want 1", key, n)
		}
	}
	if reconcileAll(window) != 0 {
		t.Fatal("a second reconcile wrote events again")
	}

	// Several reconcilers racing for the same missing event write one.
	remove()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.ReconcilePublishedEvents(ctx, window, 1000); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for _, key := range keys {
		if n := countDomainEvents(t, store, key); n != 1 {
			t.Fatalf("%s: %d events after concurrent reconcilers, want exactly 1", key, n)
		}
	}
}

// EmitOnce ignores an existing row older than the retention floor (its partition is dropped or about to be) and
// counts one at or after it.
func TestPostgresEmitOnceHonoursTheRetentionFloor(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	repo := counterTestRepository(t, store, FormatRaw)
	event := events.Published(repo.ID, "raw", "floor.txt", "sha256:"+uniqueDigest()[7:], "")
	emit := func(floor time.Time) bool {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		result, err := events.EmitOnce(ctx, tx, event, floor)
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return result.Written
	}
	past := time.Now().Add(-time.Hour)
	if !emit(past) || emit(past) {
		t.Fatal("the first EmitOnce must write and the second must not")
	}
	if !emit(time.Now().Add(time.Hour)) {
		t.Fatal("an event older than the floor must not block a new one")
	}
	if n := countDomainEvents(t, store, event.DedupeKey()); n != 2 {
		t.Fatalf("%d events, want 2", n)
	}
}

// A sealed version of a versioned Raw repository is one publication addressed as name@version under the manifest digest,
// written in the seal transaction: files written into the open version emit nothing, a seal whose transaction fails
// leaves no event and no sealed version, a repeated seal adds none, and the reconciler leaves both the files and the
// announced version alone.
func TestPostgresRawVersionEmitsPublishedAtSealInItsTransaction(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	repo, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "raw-seal-" + uuid.NewString(), Format: FormatRaw, RawLayout: RawLayoutVersioned, SealMode: RawSealExplicit})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testsupport.PurgeRepository(t, store.db, repo.ID) })
	files := []RawAsset{rawFixture(repo, "tool/1.0.0/a", "a "+repo.ID), rawFixture(repo, "tool/1.0.0/b", "b "+repo.ID)}
	for _, file := range files {
		if _, err = store.PutRawAssetIf(ctx, file, RawWriteCondition{RequireAbsent: true}); err != nil {
			t.Fatal(err)
		}
		if n := countDomainEvents(t, store, publishedKey(repo, file.Path, file.Digest)); n != 0 {
			t.Fatalf("a file of an open version emitted %d events", n)
		}
	}
	input := SealRawVersionInput{RepositoryID: repo.ID, Name: "tool", Version: "1.0.0"}
	_, manifestDigest := RawVersionManifest("tool", "1.0.0", files, nil)
	key := publishedKey(repo, "tool@1.0.0", manifestDigest)

	// Fail the event insert of this version only: the seal must roll back with it.
	function, trigger := "reject_"+strings.ReplaceAll(uuid.NewString(), "-", ""), "reject_"+strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = store.db.ExecContext(ctx, `CREATE FUNCTION `+function+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'event rejected'; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS `+trigger+` ON domain_events`)
		_, _ = store.db.ExecContext(context.Background(), `DROP FUNCTION IF EXISTS `+function+`()`)
	})
	if _, err = store.db.ExecContext(ctx, `CREATE TRIGGER `+trigger+` BEFORE INSERT ON domain_events FOR EACH ROW WHEN (NEW.dedupe_key = '`+key+`') EXECUTE FUNCTION `+function+`()`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SealRawVersion(ctx, input); err == nil {
		t.Fatal("a seal whose event could not be written succeeded")
	}
	if version, getErr := store.GetRawVersion(ctx, repo.ID, "tool", "1.0.0"); getErr != nil || version.State != RawVersionOpen {
		t.Fatalf("version after the failed seal = %+v, %v; want open", version, getErr)
	}
	if _, err = store.db.ExecContext(ctx, `DROP TRIGGER `+trigger+` ON domain_events`); err != nil {
		t.Fatal(err)
	}

	sealed, err := store.SealRawVersion(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.ManifestDigest != manifestDigest {
		t.Fatalf("manifest digest = %s, want %s", sealed.ManifestDigest, manifestDigest)
	}
	if _, err = store.SealRawVersion(ctx, input); err != nil {
		t.Fatal(err)
	}
	if n := countDomainEvents(t, store, key); n != 1 {
		t.Fatalf("seal left %d events, want exactly 1", n)
	}
	if _, err = store.ReconcilePublishedEvents(ctx, time.Now().Add(-time.Hour), 10000); err != nil {
		t.Fatal(err)
	}
	if n := countDomainEvents(t, store, key); n != 1 {
		t.Fatalf("the reconciler changed the version's events to %d", n)
	}
	for _, file := range files {
		if n := countDomainEvents(t, store, publishedKey(repo, file.Path, file.Digest)); n != 0 {
			t.Fatalf("the reconciler announced a file of a versioned repository (%d events)", n)
		}
	}
}
