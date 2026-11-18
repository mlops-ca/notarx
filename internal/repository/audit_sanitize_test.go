package repository

import (
	"strings"
	"testing"
)

func TestSanitizeAuditText(t *testing.T) {
	for in, want := range map[string]string{
		"clean": "clean", "": "", "a\x00b": "a\uFFFDb", "bad\xff\xfeutf8": "bad\uFFFDutf8", "ok-é-✓": "ok-é-✓",
	} {
		if got := SanitizeAuditText(in); got != want {
			t.Errorf("SanitizeAuditText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAuditRecordSanitizedCoversEveryTextField(t *testing.T) {
	hostile := "x\x00\xff"
	record := AuditRecord{GroupName: hostile, Repository: hostile, MemberName: hostile, Outcome: AuditOutcome(hostile), Actor: hostile,
		Format: hostile, Resource: hostile, Representation: hostile, MemberType: hostile, UpstreamHost: hostile, Operation: hostile,
		CacheDisposition: hostile, AuthorizationSource: hostile, AuthorizationReason: hostile, RequestID: hostile, TraceID: hostile,
		Evidence: map[string]string{hostile: hostile}}
	clean := record.Sanitized()
	for _, value := range []string{clean.GroupName, clean.Repository, clean.MemberName, string(clean.Outcome), clean.Actor, clean.Format,
		clean.Resource, clean.Representation, clean.MemberType, clean.UpstreamHost, clean.Operation, clean.CacheDisposition,
		clean.AuthorizationSource, clean.AuthorizationReason, clean.RequestID, clean.TraceID} {
		if value != "x\uFFFD\uFFFD" {
			t.Fatalf("field not sanitised: %q", value)
		}
	}
	if clean.Evidence["x\uFFFD\uFFFD"] != "x\uFFFD\uFFFD" || len(clean.Evidence) != 2 {
		t.Fatalf("evidence not sanitised: %q", clean.Evidence)
	}
	if strings.Contains(hostile, "\uFFFD") || record.Evidence[hostile] != hostile {
		t.Fatal("the original record must not change")
	}
}

func TestSanitizedEvidenceKeepsCollidingKeysDeterministically(t *testing.T) {
	record := AuditRecord{Evidence: map[string]string{"k\x00": "a", "k\xff": "b", "plain": "c"}}
	first := record.Sanitized()
	for i := 0; i < 50; i++ {
		again := record.Sanitized()
		for key, value := range first.Evidence {
			if again.Evidence[key] != value {
				t.Fatalf("run %d: %q = %q, first run %q", i, key, again.Evidence[key], value)
			}
		}
	}
	if len(first.Evidence) != 4 || first.Evidence["k\uFFFD"] != "a" || first.Evidence["k\uFFFD~2"] != "b" || first.Evidence[AuditEvidenceSanitized] != "true" {
		t.Fatalf("no entry may be lost: %q", first.Evidence)
	}
}

func TestSanitizedChangedFlagsOnlyRewrittenRecords(t *testing.T) {
	clean := AuditRecord{Resource: "ok", Evidence: map[string]string{"k": "v"}}
	got, changed := clean.SanitizedChanged()
	if changed || len(got.Evidence) != 1 {
		t.Fatalf("a clean record must be untouched: %v %q", changed, got.Evidence)
	}
	if allocations := testing.AllocsPerRun(100, func() { _, _ = clean.SanitizedChanged() }); allocations != 0 {
		t.Fatalf("a clean record allocated %v times", allocations)
	}
	got, changed = AuditRecord{Resource: "a\x00"}.SanitizedChanged()
	if !changed || got.Evidence[AuditEvidenceSanitized] != "true" {
		t.Fatalf("a rewritten record must be flagged: %v %q", changed, got.Evidence)
	}
	again, changedAgain := got.SanitizedChanged()
	if changedAgain || again.Resource != got.Resource {
		t.Fatal("sanitising is idempotent")
	}
}

func TestSanitizedUsageMergesKeysThatCollapse(t *testing.T) {
	merged := sanitizedUsage([]UsageDelta{
		{Repository: "r", Format: "raw", Resource: "a\x00b", Downloads: 1, Bytes: 5},
		{Repository: "r", Format: "raw", Resource: "a\xffb", Downloads: 2, Bytes: 7},
	})
	if len(merged) != 1 || merged[0].Resource != "a\uFFFDb" || merged[0].Downloads != 3 || merged[0].Bytes != 12 {
		t.Fatalf("keys equal after sanitising must merge: %+v", merged)
	}
	rollups := sanitizedRollups([]RollupDelta{{Repository: "r\x00", Requests: 1}, {Repository: "r\xff", Requests: 2}})
	if len(rollups) != 1 || rollups[0].Requests != 3 {
		t.Fatalf("rollups must merge: %+v", rollups)
	}
}
