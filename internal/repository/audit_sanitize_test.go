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
	if clean.Evidence["x\uFFFD\uFFFD"] != "x\uFFFD\uFFFD" || len(clean.Evidence) != 1 {
		t.Fatalf("evidence not sanitised: %q", clean.Evidence)
	}
	if strings.Contains(hostile, "\uFFFD") || record.Evidence[hostile] != hostile {
		t.Fatal("the original record must not change")
	}
}
