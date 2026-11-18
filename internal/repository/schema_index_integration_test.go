//go:build integration

package repository

import (
	"context"
	"database/sql"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/repository/explaincheck"
)

// foreignKeyIndexAllowlist is the reviewed list of foreign keys that have no index
// whose leading columns are the key columns (ADR 0023 section 5). Each entry names the
// reason; add a key here only when the child table is small and its parent rows are
// deleted rarely, otherwise add an index in a migration.
var foreignKeyIndexAllowlist = map[string]string{
	"nexus_migration_schedules(active_batch_id)": "operator-sized table (tens of rows); batches are deleted only with their schedule",
	"nexus_migration_schedules(last_batch_id)":   "operator-sized table (tens of rows); batches are deleted only with their schedule",
	"nexus_migration_batches(schedule_id)":       "operator-sized table; schedules are deleted rarely and by an operator",
}

// foreignKeysWithoutIndex lists table(columns) for every foreign key without a valid,
// non-partial index whose leading columns are exactly the key columns in any order.
const foreignKeysWithoutIndex = `
SELECT c.conrelid::regclass::text || '(' || (
         SELECT string_agg(a.attname, ',' ORDER BY k.ord)
           FROM unnest(c.conkey) WITH ORDINALITY k(attnum, ord)
           JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum) || ')'
  FROM pg_constraint c
 WHERE c.contype = 'f'
   AND NOT EXISTS (
         SELECT 1 FROM pg_index i
          WHERE i.indrelid = c.conrelid AND i.indisvalid AND i.indpred IS NULL
            AND (SELECT array_agg(x ORDER BY x) FROM unnest((i.indkey::int2[])[0:array_length(c.conkey, 1) - 1]) x)
              = (SELECT array_agg(x ORDER BY x) FROM unnest(c.conkey) x))
 ORDER BY 1`

func integrationDB(t *testing.T) *sql.DB {
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
	return store.db
}

func TestEveryForeignKeyHasALeadingIndexOrAReviewedAllowance(t *testing.T) {
	db := integrationDB(t)
	rows, err := db.QueryContext(t.Context(), foreignKeysWithoutIndex)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	found := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		found[key] = true
		if _, ok := foreignKeyIndexAllowlist[key]; !ok {
			t.Errorf("foreign key %s has no index with its columns as the leading prefix; add an index in a migration or a reviewed allowance", key)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var stale []string
	for key := range foreignKeyIndexAllowlist {
		if !found[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("allowlist entries that no longer apply (remove them): %s", strings.Join(stale, ", "))
	}
}

// TestAddedIndexesServeTheirQueries plans the production shape of each lookup the
// ADR 0023 section 5 indexes were added for and asserts the planner scans the index.
func TestAddedIndexesServeTheirQueries(t *testing.T) {
	db := integrationDB(t)
	const id = "00000000-0000-7000-8000-000000000001"
	cases := []struct {
		name, index, query string
		args               []any
	}{
		{"maven committed publisher", "native_maven_publish_sessions_committed_idx",
			"SELECT publisher FROM native_maven_publish_sessions WHERE repository_id=$1::uuid AND coordinate=$2 AND state='committed' ORDER BY expires_at DESC LIMIT 1", []any{id, "g:a:1"}},
		{"conan committed publisher", "native_conan_publish_sessions_committed_idx",
			"SELECT publisher FROM native_conan_publish_sessions WHERE repository_id=$1::uuid AND reference=$2 AND state='committed' ORDER BY expires_at DESC LIMIT 1", []any{id, "pkg/1.0@u/c"}},
		{"lifecycle claim by kind", "lifecycle_jobs_kind_actionable_idx",
			"SELECT id FROM lifecycle_jobs WHERE state IN ('pending','retrying') AND kind=$1 AND next_attempt_at<=now() ORDER BY next_attempt_at LIMIT 10", []any{"scan"}},
		{"maven assets by digest", "native_maven_assets_digest_idx",
			"SELECT repository_id FROM native_maven_assets WHERE digest=$1", []any{"sha256:x"}},
		{"oci tags by digest", "native_oci_tags_repository_id_digest_name_fk_idx",
			"SELECT name FROM native_oci_tags WHERE repository_id=$1::uuid AND digest=$2", []any{id, "sha256:x"}},
		{"oci repository blobs by digest", "native_oci_repository_blobs_digest_fk_idx",
			"SELECT repository_id FROM native_oci_repository_blobs WHERE digest=$1", []any{"sha256:x"}},
		{"sessions of a user", "user_sessions_user_id_fk_idx",
			"SELECT id FROM user_sessions WHERE user_id=$1::uuid", []any{id}},
		{"raw uploads of a repository", "native_raw_uploads_repository_id_fk_idx",
			"SELECT 1 FROM native_raw_uploads WHERE repository_id=$1::uuid", []any{id}},
		{"replication plans by source", "replication_plans_source_repository_id_fk_idx",
			"SELECT id FROM replication_plans WHERE source_repository_id=$1::uuid", []any{id}},
		{"maven search publisher is a column", "native_maven_artifacts_visible_coordinate_trgm_idx",
			"SELECT publisher FROM native_maven_artifacts WHERE state='visible' AND coordinate ILIKE $1", []any{"%junit%"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			plan, err := explaincheck.Plan(context.Background(), tx, tc.query, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.UsesIndex(tc.index) {
				t.Fatalf("plan does not scan %s; it uses %v", tc.index, plan.Indexes())
			}
		})
	}
}

// TestSearchProjectionNoLongerJoinsPublishSessions proves the Maven and Conan branches
// read the persisted publisher instead of a LATERAL join over publish sessions.
func TestSearchProjectionNoLongerJoinsPublishSessions(t *testing.T) {
	db := integrationDB(t)
	var definition string
	if err := db.QueryRowContext(t.Context(), "SELECT pg_get_viewdef('artifact_search_projection'::regclass)").Scan(&definition); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"native_maven_publish_sessions", "native_conan_publish_sessions"} {
		if strings.Contains(definition, table) {
			t.Errorf("artifact_search_projection still reads %s", table)
		}
	}
}

// TestHotTablesCarryVacuumSettings checks the migration's storage parameters.
func TestHotTablesCarryVacuumSettings(t *testing.T) {
	db := integrationDB(t)
	for _, table := range []string{"lifecycle_jobs", "webhook_deliveries", "replication_plans", "cache_tasks", "artifact_usage_stats", "api_keys", "user_sessions", "runtime_nodes"} {
		var options sql.NullString
		if err := db.QueryRowContext(t.Context(), "SELECT array_to_string(reloptions, ',') FROM pg_class WHERE oid = $1::regclass", table).Scan(&options); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"fillfactor=80", "autovacuum_vacuum_scale_factor=0.02", "autovacuum_analyze_scale_factor=0.02"} {
			if !strings.Contains(options.String, want) {
				t.Errorf("%s: reloptions %q lack %s", table, options.String, want)
			}
		}
	}
	for _, index := range []string{"native_raw_assets_path_trgm_idx", "native_oci_manifests_name_trgm_idx"} {
		var options sql.NullString
		if err := db.QueryRowContext(t.Context(), "SELECT array_to_string(reloptions, ',') FROM pg_class WHERE oid = $1::regclass", index).Scan(&options); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(options.String, "gin_pending_list_limit=1024") || !strings.Contains(options.String, "fastupdate=on") {
			t.Errorf("%s: reloptions %q", index, options.String)
		}
	}
}

// TestPayloadShapeMigration pins the jsonb columns, the dropped CHECK constraints and
// the body reference columns of ADR 0023 section 9.
func TestPayloadShapeMigration(t *testing.T) {
	db := integrationDB(t)
	for _, table := range []string{"native_helm_publication_sessions", "native_helm_publications", "native_nuget_publication_sessions", "native_nuget_publications", "native_rubygems_sessions"} {
		var dataType string
		if err := db.QueryRowContext(t.Context(), "SELECT data_type FROM information_schema.columns WHERE table_name=$1 AND column_name='metadata_json'", table).Scan(&dataType); err != nil || dataType != "jsonb" {
			t.Errorf("%s.metadata_json is %q (%v), want jsonb", table, dataType, err)
		}
	}
	var leftovers []string
	rows, err := db.QueryContext(t.Context(), `SELECT conrelid::regclass::text || '.' || conname FROM pg_constraint
 WHERE contype = 'c' AND (pg_get_constraintdef(oid) ~ 'metadata_json' OR pg_get_constraintdef(oid) ~ 'octet_length\(\(payload\)::text\)') ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		leftovers = append(leftovers, name)
	}
	if len(leftovers) > 0 {
		t.Errorf("CHECK constraints that Go validation replaced are still present: %v", leftovers)
	}
	for _, table := range []string{"native_npm_versions", "native_helm_publications", "native_nuget_publications", "native_nuget_source_documents", "native_helm_source_documents", "native_cargo_proxy_indexes", "oci_discovery_snapshots"} {
		var n int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM information_schema.columns WHERE table_name=$1 AND column_name IN ('body_digest','body_size','body_key')", table).Scan(&n); err != nil || n != 3 {
			t.Errorf("%s has %d of the 3 body reference columns (%v)", table, n, err)
		}
	}
	// A partial reference is rejected by the *_body_ref_check constraint.
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), "SET LOCAL session_replication_role = replica"); err != nil {
		t.Skipf("replica role needs a superuser: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), "INSERT INTO oci_discovery_snapshots(id,owner,kind,payload,expires_at,body_key) VALUES (gen_random_uuid(),'o','k','{}',now(),'meta/sha256/aa')"); err == nil {
		t.Error("a body_key without digest and size was accepted")
	}
}
