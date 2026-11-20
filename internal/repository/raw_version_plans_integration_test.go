//go:build integration

package repository

import (
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/repository/explaincheck"
)

// TestRawSweepQueriesAreIndexBacked plans the idle sweep's candidate query and the open-version statistics. On small
// tables the planner may pick either (state, ...) index of native_raw_versions, so the check accepts both.
func TestRawSweepQueriesAreIndexBacked(t *testing.T) {
	db := integrationDB(t)
	for name, query := range map[string]struct {
		sql  string
		args []any
	}{
		"idle seal candidates": {rawIdleCandidatesQuery, []any{time.Now(), 100}},
		"open version stats":   {rawOpenStatsQuery, []any{time.Now()}},
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			for _, statement := range []string{"ANALYZE native_raw_versions", "SET LOCAL enable_seqscan = off"} {
				if _, err := tx.ExecContext(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := explaincheck.Plan(t.Context(), tx, query.sql, query.args...)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.UsesAnyIndexContaining("native_raw_versions_state_") {
				t.Fatalf("plan scans no (state, ...) index of native_raw_versions; it uses %v", plan.Indexes())
			}
			if scans := plan.SequentialScans(); len(scans) > 0 {
				t.Fatalf("sequential scans on %v", scans)
			}
		})
	}
}

// TestRawVersionFilesAreAPathRangeScan seeds a repository with thousands of files inside the transaction and checks
// that the file read of one version is served by the byte-ordered path index and not by a scan of the repository
// (ADR 0028, ADR 0023 section 11). The plan follows the data, so the rows are seeded and analysed first.
func TestRawVersionFilesAreAPathRangeScan(t *testing.T) {
	db := integrationDB(t)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	const repositoryID = "00000000-0000-7000-8000-0000000000b1"
	for _, statement := range []string{
		`INSERT INTO hosted_repositories (id,name,format) VALUES ('` + repositoryID + `'::uuid,'plan-seed-raw','raw')`,
		`INSERT INTO native_raw_objects (digest,repository_id,object_key,size) SELECT 'sha256:'||md5(g::text)||md5((g+1)::text), '` + repositoryID + `'::uuid, 'plan-seed/'||g, 1 FROM generate_series(1,6000) g`,
		`INSERT INTO native_raw_assets (repository_id,path,digest,content_type) SELECT '` + repositoryID + `'::uuid, 'pkg'||(g%300)||'/1.0.'||(g%7)||'/file'||g, 'sha256:'||md5(g::text)||md5((g+1)::text), 'x' FROM generate_series(1,6000) g`,
		"ANALYZE native_raw_assets",
		"ANALYZE native_raw_objects",
		"SET LOCAL enable_seqscan = off",
	} {
		if _, err := tx.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	from, to := RawVersionPathRange("pkg7", "1.0.0")
	plan, err := explaincheck.Plan(t.Context(), tx, rawVersionFilesQuery, repositoryID, from, to, rawVersionFilesHardLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.UsesIndex("native_raw_assets_repository_path_pattern_idx") {
		t.Fatalf("the version's files are not read through the path pattern index; the plan uses %v", plan.Indexes())
	}
}

// TestRawVersionListingsAreIndexBacked plans the production statements of the version listing, including the stale
// open filter of olderThanDays, and asserts an index of native_raw_versions serves them (ADR 0028 slice 3a,
// ADR 0023 section 11). The table is analysed inside the transaction and sequential scans are switched off, so the
// check is which index serves the lookup and not whether the table is large enough to need one.
func TestRawVersionListingsAreIndexBacked(t *testing.T) {
	db := integrationDB(t)
	const id = "00000000-0000-7000-8000-000000000001"
	cursor := &RawVersionCursor{SealedAt: time.Now(), ID: id}
	cases := []struct {
		name  string
		query RawVersionQuery
	}{
		{"stale open versions", RawVersionQuery{RepositoryID: id, Name: "tool", State: RawVersionOpen, OpenCreatedBefore: time.Now().Add(-7 * 24 * time.Hour), Limit: 50}},
		{"keyset page", RawVersionQuery{RepositoryID: id, Name: "tool", After: cursor, Limit: 50}},
		{"unsealed keyset page", RawVersionQuery{RepositoryID: id, Name: "tool", After: &RawVersionCursor{ID: id}, Limit: 50}},
		{"property filter", RawVersionQuery{RepositoryID: id, Name: "tool", Properties: []RawVersionProperty{{Key: "channel", Value: "stable"}}, Limit: 50}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			for _, statement := range []string{"ANALYZE native_raw_versions", "ANALYZE native_raw_version_properties", "SET LOCAL enable_seqscan = off"} {
				if _, err := tx.ExecContext(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
			query, args := rawVersionListStatement(tc.query)
			plan, err := explaincheck.Plan(t.Context(), tx, query, args...)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.UsesAnyIndexContaining("native_raw_versions_") {
				t.Fatalf("plan scans no native_raw_versions index; it uses %v", plan.Indexes())
			}
			if scans := plan.SequentialScans(); len(scans) > 0 {
				t.Fatalf("sequential scans on %v", scans)
			}
		})
	}
}
