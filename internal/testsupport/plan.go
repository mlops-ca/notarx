package testsupport

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

type planNode struct {
	NodeType string     `json:"Node Type"`
	Relation string     `json:"Relation Name"`
	Plans    []planNode `json:"Plans"`
}

func (n planNode) walk(visit func(planNode)) {
	visit(n)
	for _, child := range n.Plans {
		child.walk(visit)
	}
}

// RequireOrderedByIndex plans query with sequential scans discouraged and fails unless the plan
// reads relation through an index and contains no Sort node: the rows come out of the index in the
// order the statement asks for. A bare ORDER BY id over a select list holding id::text sorts the
// text output column instead and adds a Sort node (ADR 0023 section 4).
func RequireOrderedByIndex(t *testing.T, db *sql.DB, relation, query string, args ...any) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	// On a small table a bitmap scan plus a sort can cost less than the ordered index scan. With both
	// discouraged, the planner reads an index in order whenever one serves the ORDER BY, so a Sort left
	// in the plan means no index does, whatever the table statistics are.
	for _, setting := range []string{"enable_seqscan", "enable_bitmapscan", "enable_sort"} {
		if _, err = tx.Exec("SET LOCAL " + setting + " = off"); err != nil {
			t.Fatal(err)
		}
	}
	var raw []byte
	if err = tx.QueryRow("EXPLAIN (FORMAT JSON) "+query, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []struct {
		Plan planNode `json:"Plan"`
	}
	if err = json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("explain output: %s %v", raw, err)
	}
	indexed, sorted := false, false
	plans[0].Plan.walk(func(n planNode) {
		sorted = sorted || strings.Contains(n.NodeType, "Sort")
		if n.Relation == relation {
			indexed = indexed || strings.Contains(n.NodeType, "Index")
		}
	})
	if !indexed || sorted {
		t.Fatalf("the statement must read %s in index order without a Sort node (indexed=%v sorted=%v): %s", relation, indexed, sorted, raw)
	}
}
