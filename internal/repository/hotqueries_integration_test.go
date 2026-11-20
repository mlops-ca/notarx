//go:build integration

package repository

import (
	"context"
	"os"
	"slices"
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

	ctx := t.Context()
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
	for _, table := range []string{"hosted_repositories", "native_npm_packages", "native_npm_versions", "resolver_audit_log", "lifecycle_jobs", "security_audit_log", "artifact_catalog"} {
		if _, err := conn.ExecContext(ctx, "ANALYZE "+table); err != nil {
			t.Fatal(err)
		}
	}

	fixture := hotQueryFixture{actor: "perf-actor-000"}
	if err := conn.QueryRowContext(ctx, `
		SELECT r.id::text, r.name, v.package_name, v.version
		  FROM native_npm_versions v
		  JOIN hosted_repositories r ON r.id = v.repository_id
		 WHERE r.name LIKE 'perf-npm-%' ORDER BY v.created_at DESC LIMIT 1`).
		Scan(&fixture.repositoryID, &fixture.repositoryName, &fixture.packageName, &fixture.version); err != nil {
		t.Fatalf("seeded fixture: %v", err)
	}

	for _, query := range hotQueries {
		t.Run(query.name, func(t *testing.T) {
			sql, args := query.build(fixture)
			plan, err := explaincheck.Plan(ctx, conn, sql, args...)
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range plan.SequentialScans() {
				if onListedTable(table, query.noSeqScan) {
					t.Errorf("Seq Scan on %s", table)
				}
			}
			if len(plan.Indexes()) == 0 {
				t.Errorf("plan scans no index")
			}
			if len(query.indexContains) > 0 && !slices.ContainsFunc(query.indexContains, plan.UsesAnyIndexContaining) {
				t.Errorf("plan uses indexes %v, want one containing any of %q", plan.Indexes(), query.indexContains)
			}
		})
	}
}
