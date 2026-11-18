//go:build integration

package pgstore_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/capacity"
	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/ids"
	p "github.com/mlops-ca/notarx/internal/protocol/rubygems"
	"github.com/mlops-ca/notarx/internal/repository"
	d "github.com/mlops-ca/notarx/internal/rubygemspublication"
	"github.com/mlops-ca/notarx/internal/rubygemspublication/pgstore"
	f "github.com/mlops-ca/notarx/internal/testsupport/rubygemsfixture"
)

// gemSession builds a publication session for one distinct gem.
func gemSession(t *testing.T, repo, name, version string) d.Session {
	t.Helper()
	body := f.Build(t, f.YAML(name, version, "ruby", "fixture", f.Dependency("leaf", "runtime")), "lib/owned.rb")
	in, err := p.Inspect(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return d.Session{ID: ids.New(), RepositoryID: repo, Name: in.Metadata.Name, Version: in.Metadata.Version, Platform: in.Metadata.Platform, Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(body)), Actor: "operator", MetadataJSON: in.MetadataJSON, MetadataYAML: in.MetadataYAML, ExpiresAt: time.Now().UTC().Add(time.Hour)}
}

func assertRubyGemsCounter(t *testing.T, db *sql.DB, repositoryID, step string) {
	t.Helper()
	query, _ := capacity.UsageQuery("rubygems")
	var usedBytes, usedObjects, bytes, objects int64
	if err := db.QueryRow(query, repositoryID).Scan(&usedBytes, &usedObjects); err != nil {
		t.Fatalf("%s: aggregate: %v", step, err)
	}
	if err := db.QueryRow("SELECT bytes,objects FROM repository_usage_counters WHERE repository_id=$1::uuid", repositoryID).Scan(&bytes, &objects); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("%s: counter: %v", step, err)
	}
	if bytes != usedBytes || objects != usedObjects {
		t.Fatalf("%s: counter bytes=%d objects=%d, aggregation bytes=%d objects=%d", step, bytes, objects, usedBytes, usedObjects)
	}
}

// TestRubyGemsCounterEquality is the RubyGems equality test of ADR 0023 section 3:
// publish, replay, publish more, hit the quota; the counter equals the aggregation after every step.
func TestRubyGemsCounterEquality(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("owned PostgreSQL required")
	}
	db, err := database.OpenPostgres(dsn, database.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	locks, err := database.OpenPostgres(dsn, database.DefaultArtifactLockPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locks.Close() })
	repos, err := repository.NewPostgresStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repos.Close() })
	newRepo := func() repository.HostedRepository {
		repo, createErr := repos.CreateHostedRepository(t.Context(), repository.HostedRepository{ID: uuid.NewString(), Name: "ruby-counter-" + uuid.NewString(), Format: repository.FormatRubyGems, Type: repository.RepositoryTypeHosted})
		if createErr != nil {
			t.Fatal(createErr)
		}
		t.Cleanup(func() { _, _ = db.Exec("DELETE FROM hosted_repositories WHERE id=$1", repo.ID) })
		return repo
	}
	store, err := pgstore.New(db, locks)
	if err != nil {
		t.Fatal(err)
	}
	publish := func(repo repository.HostedRepository, name, version string) error {
		reserved, _, reserveErr := store.Reserve(t.Context(), gemSession(t, repo.ID, name, version))
		if reserveErr != nil {
			return reserveErr
		}
		_, _, commitErr := store.Commit(t.Context(), reserved.ID, "operator")
		return commitErr
	}

	repo := newRepo()
	for _, gem := range [][2]string{{"alpha", "1.0.0"}, {"alpha", "1.1.0"}, {"beta", "0.1.0"}} {
		if err = publish(repo, gem[0], gem[1]); err != nil {
			t.Fatal(err)
		}
		assertRubyGemsCounter(t, db, repo.ID, "publish "+gem[0]+" "+gem[1])
	}
	reserved, _, err := store.Reserve(t.Context(), gemSession(t, repo.ID, "gamma", "1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"commit a session", "commit the same session again"} {
		if _, _, err = store.Commit(t.Context(), reserved.ID, "operator"); err != nil {
			t.Fatal(err)
		}
		assertRubyGemsCounter(t, db, repo.ID, step)
	}
	var used int64
	if err = db.QueryRow("SELECT bytes FROM repository_usage_counters WHERE repository_id=$1::uuid", repo.ID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetQuota(t.Context(), repo.ID, "operator", used); err != nil {
		t.Fatal(err)
	}
	var quota sql.NullInt64
	if err = db.QueryRow("SELECT quota_bytes FROM repository_usage_counters WHERE repository_id=$1::uuid", repo.ID).Scan(&quota); err != nil || quota.Int64 != used {
		t.Fatalf("SetQuota did not write the counter quota: %v %v", quota, err)
	}
	if err = publish(repo, "overflow", "1.0.0"); !errors.Is(err, repository.ErrQuotaExceeded) {
		t.Fatalf("publish over quota = %v, want quota exceeded", err)
	}
	assertRubyGemsCounter(t, db, repo.ID, "rejected publish over quota")

	// Concurrent publishes at the quota edge reject exactly the overflow.
	edge := newRepo()
	one := gemSession(t, edge.ID, "edge0", "1.0.0")
	if _, err = store.SetQuota(t.Context(), edge.ID, "operator", one.Size*10); err != nil {
		t.Fatal(err)
	}
	var accepted, rejected atomic.Int64
	var wg sync.WaitGroup
	for index := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := "edge" + string(rune('a'+index))
			// Equal sizes need equal-length names.
			switch publishErr := publish(edge, name, "1.0.0"); {
			case publishErr == nil:
				accepted.Add(1)
			case errors.Is(publishErr, repository.ErrQuotaExceeded):
				rejected.Add(1)
			default:
				t.Error(publishErr)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 10 || rejected.Load() != 14 {
		t.Fatalf("accepted %d rejected %d, want 10 and 14", accepted.Load(), rejected.Load())
	}
	assertRubyGemsCounter(t, db, edge.ID, "after the quota edge")
}
