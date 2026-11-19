package repository

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ADR 0023 section 8 and slice 14: listings page by keyset and report a bounded total, so no production
// SQL may use OFFSET, and no COUNT(*) may scan a whole listed table. A count is bounded when its statement
// carries a LIMIT (see boundedCountSQL). The few aggregates that are not listing totals are named below
// with the reason, so a new unbounded count has to be argued for in review rather than slipping in.

// listedTables are the tables the management API lists or pages.
var listedTables = []string{
	"users", "artifact_usage_stats", "lifecycle_jobs", "webhook_deliveries", "webhook_subscriptions",
	"resolver_audit_log", "security_audit_log", "replication_plans", "hosted_repositories",
}

// unboundedCountAllowlist maps "file: table" to why that aggregate is not an unbounded listing total.
var unboundedCountAllowlist = map[string]string{
	"postgres_lifecycle.go: lifecycle_jobs":            "the running-set probe of the claim; a partial index keeps it the size of the worker count",
	"postgres_artifact_usage.go: artifact_usage_stats": "whole-repository lifetime totals next to SUM aggregates over the same rows; the artifact catalogue rollup (slice 15) replaces the scan",
	"postgres_queue_stats.go: lifecycle_jobs":          "queue gauge grouped over the actionable and failed states only; not a listing total",
	"postgres_queue_stats.go: replication_plans":       "queue gauge grouped over the actionable and failed states only; not a listing total",
	"postgres_users.go: users":                         "last-administrator invariant over active administrators, not a listing total",
}

var (
	offsetClause = regexp.MustCompile(`(?i)\bOFFSET\s+(\$\d+|\d+)`)
	countClause  = regexp.MustCompile(`(?is)count\(\*\)(.{0,600})`)
)

func TestNoOffsetOrUnboundedCountOnListedTables(t *testing.T) {
	root := filepath.Join("..", "..", "internal")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "generated") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(raw)
		if offsetClause.MatchString(text) {
			t.Errorf("%s: OFFSET paging is forbidden; page by keyset (ADR 0023 section 8)", path)
		}
		for _, match := range countClause.FindAllStringSubmatch(text, -1) {
			statement := match[1]
			if end := strings.IndexAny(statement, "`"); end >= 0 {
				statement = statement[:end]
			}
			if strings.Contains(strings.ToUpper(statement), "LIMIT") {
				continue
			}
			for _, table := range listedTables {
				if !regexp.MustCompile(`(?i)\bFROM\s+` + table + `\b`).MatchString(statement) {
					continue
				}
				if _, ok := unboundedCountAllowlist[filepath.Base(path)+": "+table]; !ok {
					t.Errorf("%s: COUNT(*) over %s without a LIMIT is an unbounded listing total; use a bounded count (boundedTotal) or allowlist it with a reason", path, table)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestListingLintAllowlistEntriesStillMatch(t *testing.T) {
	for entry := range unboundedCountAllowlist {
		file, table, _ := strings.Cut(entry, ": ")
		found := false
		_ = filepath.WalkDir(filepath.Join("..", ".."), func(path string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || filepath.Base(path) != file {
				return nil
			}
			raw, readErr := os.ReadFile(path)
			if readErr == nil && regexp.MustCompile(`(?is)count\(\*\).{0,600}?FROM\s+`+table+`\b`).Match(raw) {
				found = true
			}
			return nil
		})
		if !found {
			t.Errorf("allowlist entry %q no longer matches a COUNT(*); remove it", entry)
		}
	}
}
