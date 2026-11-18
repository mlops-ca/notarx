// Package explaincheck plans a statement with EXPLAIN and reports which indexes
// the plan uses, so tests can assert that an index added for a query is the one the
// planner picks (ADR 0023 section 5).
package explaincheck

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Node is one node of an EXPLAIN (FORMAT JSON) plan.
type Node struct {
	NodeType  string `json:"Node Type"`
	Relation  string `json:"Relation Name"`
	IndexName string `json:"Index Name"`
	Plans     []Node `json:"Plans"`
}

// Walk calls visit on the node and every descendant.
func (n Node) Walk(visit func(Node)) {
	visit(n)
	for _, child := range n.Plans {
		child.Walk(visit)
	}
}

// Indexes returns the names of the indexes the plan scans.
func (n Node) Indexes() []string {
	var names []string
	n.Walk(func(node Node) {
		if node.IndexName != "" {
			names = append(names, node.IndexName)
		}
	})
	return names
}

// UsesIndex reports whether the plan scans the named index.
func (n Node) UsesIndex(name string) bool {
	for _, used := range n.Indexes() {
		if used == name {
			return true
		}
	}
	return false
}

// SequentialScans returns the relations the plan reads with a sequential scan.
func (n Node) SequentialScans() []string {
	var relations []string
	n.Walk(func(node Node) {
		if node.NodeType == "Seq Scan" {
			relations = append(relations, node.Relation)
		}
	})
	return relations
}

// Parse decodes the JSON that EXPLAIN (FORMAT JSON) returns.
func Parse(raw []byte) (Node, error) {
	var plans []struct {
		Plan Node `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &plans); err != nil {
		return Node{}, fmt.Errorf("explaincheck: decode plan: %w", err)
	}
	if len(plans) != 1 {
		return Node{}, fmt.Errorf("explaincheck: want one plan, got %d", len(plans))
	}
	return plans[0].Plan, nil
}

// Plan explains query with args inside tx with sequential scans discouraged, so the
// plan shows whether an index can serve the statement at all, whatever the table
// size. The caller rolls tx back.
func Plan(ctx context.Context, tx *sql.Tx, query string, args ...any) (Node, error) {
	if _, err := tx.ExecContext(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		return Node{}, err
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, "EXPLAIN (FORMAT JSON) "+query, args...).Scan(&raw); err != nil {
		return Node{}, err
	}
	return Parse(raw)
}
