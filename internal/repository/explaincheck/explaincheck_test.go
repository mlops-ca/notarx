package explaincheck

import (
	"slices"
	"testing"
)

const sample = `[{"Plan": {"Node Type": "Limit", "Plans": [
 {"Node Type": "Nested Loop", "Plans": [
  {"Node Type": "Index Scan", "Relation Name": "a", "Index Name": "a_idx"},
  {"Node Type": "Seq Scan", "Relation Name": "b"}]}]}}]`

func TestParseAndInspectPlan(t *testing.T) {
	plan, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.UsesIndex("a_idx") || plan.UsesIndex("b_idx") {
		t.Errorf("indexes = %v", plan.Indexes())
	}
	if got := plan.SequentialScans(); !slices.Equal(got, []string{"b"}) {
		t.Errorf("sequential scans = %v", got)
	}
	if !plan.UsesAnyIndexContaining("_idx") || plan.UsesAnyIndexContaining("zzz") {
		t.Errorf("substring match wrong for %v", plan.Indexes())
	}
	for _, bad := range []string{"", "[]", "{", "[{},{}]"} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%q) succeeded", bad)
		}
	}
}
