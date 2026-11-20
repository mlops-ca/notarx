//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/testsupport"
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
		// ordered asserts the keyset order comes from the index: no Sort node anywhere in the plan.
		ordered bool
	}{
		{"lifecycle_jobs by repository", "lifecycle_jobs", listLifecycleJobsQuery, []any{id, 50}, true},
		{"lifecycle_jobs by repository after a cursor", "lifecycle_jobs", listLifecycleJobsAfterQuery, []any{id, time.Now().UTC(), id, 50}, true},
		{"lifecycle_jobs across repositories", "lifecycle_jobs", listAllLifecycleJobsQuery, []any{nil, 50}, true},
		{"lifecycle_jobs across repositories after a cursor", "lifecycle_jobs", listAllLifecycleJobsAfterQuery, []any{nil, time.Now().UTC(), id, 50}, true},
		{"lifecycle_jobs across administered repositories", "lifecycle_jobs", listAllLifecycleJobsQuery, []any{[]string{id}, 50}, false},
		{"lifecycle_jobs by repository and id", "lifecycle_jobs", getLifecycleJobQuery, []any{id, id}, false},
		{"user_sessions by id and user", "user_sessions", getUserSessionQuery, []any{id, id}, false},
		{"hosted_repositories by id", "hosted_repositories", getHostedRepositoryQuery, []any{id}, false},
		{"hosted_repositories first page", "hosted_repositories", listHostedRepositoriesQuery, []any{"", 50}, true},
		{"hosted_repositories after a cursor", "hosted_repositories", listHostedRepositoriesQuery, []any{id, 50}, true},
		{"hosted_groups first page", "hosted_groups", listHostedGroupsQuery, []any{"", 50}, true},
		{"hosted_groups after a cursor", "hosted_groups", listHostedGroupsQuery, []any{id, 50}, true},
		{"hosted_group_members of a page", "hosted_group_members", listHostedGroupMembersQuery, []any{[]string{id, uuid.NewString()}}, true},
		{"repository_grants of a principal", "repository_grants", listRepositoryIDsGrantedToPrincipalQuery, []any{"alice", "repositories:admin"}, true},
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
			// On a small table a bitmap scan plus a sort can cost less than the ordered index scan. With both
			// discouraged, the planner reads the index in order whenever one serves the ORDER BY, so a Sort
			// left in the plan means no index does, whatever the table statistics are.
			if tc.ordered {
				if _, err = tx.Exec("SET LOCAL enable_bitmapscan = off; SET LOCAL enable_sort = off"); err != nil {
					t.Fatal(err)
				}
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
			indexed, sequential, sorted := false, false, false
			plans[0].Plan.walk(func(n explainNode) {
				sorted = sorted || strings.Contains(n.NodeType, "Sort")
				if n.Relation != tc.relation {
					return
				}
				indexed = indexed || strings.Contains(n.NodeType, "Index") || strings.Contains(n.NodeType, "Bitmap")
				sequential = sequential || n.NodeType == "Seq Scan"
			})
			if tc.ordered && sorted {
				t.Fatalf("%s sorts instead of reading the keyset order from an index: %s", tc.name, raw)
			}
			if !indexed || sequential {
				t.Fatalf("%s does not use an index on %s: %s", tc.name, tc.relation, raw)
			}
		})
	}
}

// TestPostgresIDKeysetListingsReadTheKeyInOrder plans the id-keyset listings. Each selects id::text,
// so a bare ORDER BY id would sort that text output column and add a Sort node on every page; the
// listings qualify the column and read the primary key in order instead (ADR 0023 sections 4 and 8).
func TestPostgresIDKeysetListingsReadTheKeyInOrder(t *testing.T) {
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
	for _, tc := range []struct {
		name, relation, query string
		args                  []any
	}{
		{"workload issuers", "workload_issuers", listWorkloadIssuersQuery, []any{id, 50}},
		{"workload trust policies", "workload_trust_policies", listWorkloadPoliciesQuery, []any{id, 50}},
		{"service accounts", "service_accounts", listServiceAccountsQuery, []any{id, 50}},
		{"service account API keys", "api_keys", listServiceAccountKeysQuery, []any{id, id, 50}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testsupport.RequireOrderedByIndex(t, store.db, tc.relation, tc.query, tc.args...)
		})
	}
}
