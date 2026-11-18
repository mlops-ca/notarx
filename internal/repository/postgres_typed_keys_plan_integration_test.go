//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestPostgresTypedKeyQueriesUseIndexes plans the production statements of the hot
// control-plane lookups with sequential scans discouraged. Comparing a uuid column with a
// text-cast parameter defeats the btree, so these plans fall back to a sequential scan
// (or filter on a cast) when the keys are not typed. ADR 0023 section 4.
func TestPostgresTypedKeyQueriesUseIndexes(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	id := uuid.NewString()
	cases := []struct {
		name     string
		relation string
		query    string
		args     []any
	}{
		{"lifecycle_jobs by repository", "lifecycle_jobs", listLifecycleJobsQuery, []any{id, 50}},
		{"lifecycle_jobs by repository and id", "lifecycle_jobs", getLifecycleJobQuery, []any{id, id}},
		{"user_sessions by id and user", "user_sessions", getUserSessionQuery, []any{id, id}},
		{"hosted_repositories by id", "hosted_repositories", getHostedRepositoryQuery, []any{id}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := store.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			if _, err = tx.Exec("SET LOCAL enable_seqscan = off"); err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if err = tx.QueryRow("EXPLAIN (FORMAT JSON) "+tc.query, tc.args...).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var plans []struct {
				Plan explainNode `json:"Plan"`
			}
			if err = json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
				t.Fatalf("explain output: %s %v", raw, err)
			}
			indexed, sequential := false, false
			plans[0].Plan.walk(func(n explainNode) {
				if n.Relation != tc.relation {
					return
				}
				indexed = indexed || strings.Contains(n.NodeType, "Index") || strings.Contains(n.NodeType, "Bitmap")
				sequential = sequential || n.NodeType == "Seq Scan"
			})
			if !indexed || sequential {
				t.Fatalf("%s does not use an index on %s: %s", tc.name, tc.relation, raw)
			}
		})
	}
}
