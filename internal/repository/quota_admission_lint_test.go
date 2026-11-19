package repository

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// quotaAdmissionExceptions lists the files allowed to decide quota admission with a SUM over asset or
// session tables under the quota row lock instead of capacity.Reserve (ADR 0023 section 3). It is
// empty: every Postgres store admits through the counter. Do not add to it; the test fails on a
// stale entry.
var quotaAdmissionExceptions = map[string]string{}

var (
	quotaRowLock = regexp.MustCompile(`(?is)repository_capacity_quotas[^\x60]*FOR\s+UPDATE`)
	assetSum     = regexp.MustCompile(`(?i)\bsum\s*\(`)
)

// TestQuotaAdmissionUsesCountersNotAssetSums enforces ADR 0023 section 3: a Postgres store query
// never takes FOR UPDATE on repository_capacity_quotas, and a file that rejects with
// ErrQuotaExceeded never sums a repository's asset tables to decide it. Admission is
// capacity.Reserve on the counter row.
func TestQuotaAdmissionUsesCountersNotAssetSums(t *testing.T) {
	files, err := filepath.Glob("postgres*.go")
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		key := name
		locks := quotaRowLock.MatchString(source)
		sums := strings.Contains(source, "ErrQuotaExceeded") && assetSum.MatchString(source)
		if !locks && !sums {
			continue
		}
		if _, ok := quotaAdmissionExceptions[key]; ok {
			used[key] = true
			continue
		}
		t.Errorf("%s locks the quota row or sums rows and rejects with ErrQuotaExceeded; admit with capacity.Reserve instead", key)
	}
	for key := range quotaAdmissionExceptions {
		if !used[key] {
			t.Errorf("quotaAdmissionExceptions entry %s no longer matches; delete it", key)
		}
	}
}
