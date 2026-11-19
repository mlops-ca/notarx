package repository

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var placeholder = regexp.MustCompile(`\$([0-9]+)`)

func TestHotQueryRegistryIsWellFormed(t *testing.T) {
	fixture := hotQueryFixture{repositoryID: "r", repositoryName: "n", packageName: "p", version: "1.0.0", actor: "a"}
	seen := map[string]bool{}
	for _, query := range hotQueries {
		if query.name == "" || query.build == nil || len(query.noSeqScan) == 0 {
			t.Errorf("incomplete hot query %q", query.name)
			continue
		}
		if seen[query.name] {
			t.Errorf("duplicate hot query %q", query.name)
		}
		seen[query.name] = true
		sql, args := query.build(fixture)
		if strings.TrimSpace(sql) == "" {
			t.Errorf("hot query %q has no SQL", query.name)
		}
		if upper := strings.ToUpper(sql); strings.Contains(upper, "OFFSET") || strings.Contains(upper, "COUNT(*)") {
			t.Errorf("hot query %q uses OFFSET or COUNT(*) (ADR 0023 section 8)", query.name)
		}
		highest := 0
		for _, match := range placeholder.FindAllStringSubmatch(sql, -1) {
			n, _ := strconv.Atoi(match[1])
			highest = max(highest, n)
		}
		if highest != len(args) {
			t.Errorf("hot query %q uses placeholders up to $%d but has %d arguments", query.name, highest, len(args))
		}
	}
}

func TestSeqScanOnListedTableOrItsPartition(t *testing.T) {
	for relation, want := range map[string]bool{
		"resolver_audit_log": true, "resolver_audit_log_2026_11": true, "resolver_audit_log_default": true,
		"resolver_audit_logs": false, "other": false,
	} {
		if got := onListedTable(relation, []string{"resolver_audit_log"}); got != want {
			t.Errorf("onListedTable(%q) = %v, want %v", relation, got, want)
		}
	}
}

// onListedTable reports whether relation is one of tables or a partition of one
// (partitions are named after their parent plus a suffix).
func onListedTable(relation string, tables []string) bool {
	for _, table := range tables {
		if relation == table || strings.HasPrefix(relation, table+"_") {
			return true
		}
	}
	return false
}
