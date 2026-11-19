package repository

import (
	"strings"
	"testing"
)

func TestHotQueryRegistryIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, query := range HotQueries {
		if query.Name == "" || strings.TrimSpace(query.SQL) == "" || query.Args == nil || len(query.NoSeqScan) == 0 {
			t.Errorf("incomplete hot query %+v", query.Name)
		}
		if seen[query.Name] {
			t.Errorf("duplicate hot query %q", query.Name)
		}
		seen[query.Name] = true
		if upper := strings.ToUpper(query.SQL); strings.Contains(upper, "OFFSET") || strings.Contains(upper, "COUNT(*)") {
			t.Errorf("hot query %q uses OFFSET or COUNT(*) (ADR 0023 section 8)", query.Name)
		}
		if got := strings.Count(query.SQL, "$"); got != len(query.Args(HotQueryFixture{})) {
			t.Errorf("hot query %q has %d placeholders but %d arguments", query.Name, got, len(query.Args(HotQueryFixture{})))
		}
	}
}
