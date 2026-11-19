package repository

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// quotaAdmissionExceptions lists the functions that still decide quota admission with a SUM over
// asset or session tables under the quota row lock instead of capacity.Reserve. Each one is a debt
// of ADR 0023 section 3 (see docs/distributed-deployment.md, "Quota counters"); delete the entry
// when the function moves to the counters. The test fails when an entry no longer matches.
var quotaAdmissionExceptions = map[string]string{
	"postgres_helm.go:helmQuota":   "locks the quota row and sums pending Helm sessions instead of reserving them",
	"postgres_nuget.go:nuGetQuota": "locks the quota row and sums pending NuGet sessions instead of reserving them",
}

var (
	quotaRowLock   = regexp.MustCompile("(?is)repository_capacity_quotas[^`]*FOR\\s+UPDATE")
	assetSum       = regexp.MustCompile("(?i)\\bsum\\s*\\(")
	functionHeader = regexp.MustCompile("(?m)^func (?:\\([^)]*\\) )?(\\w+)")
)

// TestQuotaAdmissionUsesCountersNotAssetSums enforces ADR 0023 section 3: a Postgres store query
// never takes FOR UPDATE on repository_capacity_quotas, and a function that rejects with
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
		headers := functionHeader.FindAllStringSubmatchIndex(source, -1)
		for i, header := range headers {
			end := len(source)
			if i+1 < len(headers) {
				end = headers[i+1][0]
			}
			function, key := source[header[0]:end], name+":"+source[header[2]:header[3]]
			locks := quotaRowLock.MatchString(function)
			sums := strings.Contains(function, "ErrQuotaExceeded") && assetSum.MatchString(function)
			if !locks && !sums {
				continue
			}
			if _, ok := quotaAdmissionExceptions[key]; ok {
				used[key] = true
				continue
			}
			t.Errorf("%s locks the quota row or sums rows to reject with ErrQuotaExceeded; admit with capacity.Reserve instead", key)
		}
	}
	for key := range quotaAdmissionExceptions {
		if !used[key] {
			t.Errorf("quotaAdmissionExceptions entry %s no longer matches; delete it", key)
		}
	}
}
