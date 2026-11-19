//go:build integration

package repository

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/mlops-ca/notarx/internal/repository/explaincheck"
	"github.com/mlops-ca/notarx/tools/datagen"
)

// TestHotQueriesKeepTheirPlans seeds a datagen profile inside a transaction, runs
// ANALYZE and asserts every registered hot query still plans on its index with no
// sequential scan on the listed tables (ADR 0023 section 11, make explain-check).
// NOTARX_EXPLAIN_PROFILE picks the profile (default tiny) and NOTARX_EXPLAIN_SCALE
// divides it; make integration-test scales it down, make explain-check runs it whole.
// Everything rolls back, so the shared test database is untouched.
func TestHotQueriesKeepTheirPlans(t *testing.T) {
	db := integrationDB(t)
	profileName := os.Getenv("NOTARX_EXPLAIN_PROFILE")
	if profileName == "" {
		profileName = "tiny"
	}
	profile, err := datagen.ParseProfile(profileName)
	if err != nil {
		t.Fatal(err)
	}
	if scale := os.Getenv("NOTARX_EXPLAIN_SCALE"); scale != "" {
		divisor, err := strconv.Atoi(scale)
		if err != nil || divisor < 1 {
			t.Fatalf("NOTARX_EXPLAIN_SCALE=%q", scale)
		}
		profile = profile.Scaled(divisor)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	if err := conn.Raw(func(driverConn any) error {
		pgConn := driverConn.(*stdlib.Conn).Conn()
		_, err := datagen.Seed(ctx, pgConn, profile, 1, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"hosted_repositories", "native_npm_packages", "native_npm_versions", "resolver_audit_log", "lifecycle_jobs"} {
		if _, err := conn.ExecContext(ctx, "ANALYZE "+table); err != nil {
			t.Fatal(err)
		}
	}

	var fixture HotQueryFixture
	fixture.RepositoryName = "perf-npm-0000"
	if err := conn.QueryRowContext(ctx, `
		SELECT r.id::text, v.package_name, v.version
		  FROM hosted_repositories r
		  JOIN native_npm_versions v ON v.repository_id = r.id
		 WHERE r.name = $1 ORDER BY v.package_name, v.version LIMIT 1`, fixture.RepositoryName).
		Scan(&fixture.RepositoryID, &fixture.PackageName, &fixture.Version); err != nil {
		t.Fatalf("seeded fixture: %v", err)
	}
	fixture.Actor = "perf-actor-000"

	for _, query := range HotQueries {
		t.Run(query.Name, func(t *testing.T) {
			plan, err := explaincheck.Plan(ctx, conn, query.SQL, query.Args(fixture)...)
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range plan.SequentialScans() {
				for _, listed := range query.NoSeqScan {
					if table == listed {
						t.Errorf("Seq Scan on %s", table)
					}
				}
			}
			if query.IndexContains != "" && !plan.UsesAnyIndexContaining(query.IndexContains) {
				t.Errorf("plan uses indexes %v, want one containing %q", plan.Indexes(), query.IndexContains)
			}
		})
	}
}
