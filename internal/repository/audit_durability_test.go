package repository

import "testing"

func TestAuditDurabilityClasses(t *testing.T) {
	for name, tc := range map[string]struct {
		record AuditRecord
		want   AuditClass
	}{
		"download":               {AuditRecord{Repository: "r", Format: "raw", Operation: "get", Outcome: AuditResolved, Actor: "a", Status: 200}, AuditClassAsync},
		"head":                   {AuditRecord{Repository: "r", Format: "oci", Operation: "head", Outcome: AuditResolved, Actor: "a"}, AuditClassAsync},
		"group resolution":       {AuditRecord{GroupName: "g", Repository: "g", MemberName: "m", Outcome: AuditInternalPreferred, Actor: "a"}, AuditClassAsync},
		"cache not found":        {AuditRecord{Repository: "r", Format: "npm", Operation: "get", Outcome: AuditNotFound, Actor: "a"}, AuditClassAsync},
		"oci catalog read":       {AuditRecord{Format: "oci", Operation: "oci.catalog.read", Outcome: AuditResolved, Actor: "a"}, AuditClassAsync},
		"anonymous 403":          {AuditRecord{Repository: "r", Format: "raw", Operation: "get", Outcome: AuditAccessDenied, Actor: AnonymousActor, Status: 403}, AuditClassAsync},
		"anonymous 401":          {AuditRecord{Repository: "r", Format: "raw", Operation: "get", Outcome: AuditAccessDenied, Status: 401}, AuditClassAsync},
		"authenticated denial":   {AuditRecord{Repository: "r", Format: "raw", Operation: "get", Outcome: AuditAccessDenied, Actor: "alice", Status: 403}, AuditClassSync},
		"proxy denial":           {AuditRecord{Repository: "r", Format: "raw", Operation: "get", Outcome: AuditProxyDenied, Actor: "alice"}, AuditClassSync},
		"publish":                {AuditRecord{Repository: "r", Format: "npm", Operation: "put", Outcome: AuditResolved, Actor: "ci", Status: 201}, AuditClassSync},
		"delete":                 {AuditRecord{Repository: "r", Format: "raw", Operation: "delete", Outcome: AuditResolved, Actor: "ci"}, AuditClassSync},
		"management mutation":    {AuditRecord{Format: "management", Operation: "repo update", Outcome: AuditResolved, Actor: "admin"}, AuditClassSync},
		"management read":        {AuditRecord{Format: "management", Operation: "get", Outcome: AuditResolved, Actor: "admin"}, AuditClassSync},
		"promotion":              {AuditRecord{Repository: "r", Format: "management", Operation: "promote", Outcome: AuditResolved, Actor: "admin"}, AuditClassSync},
		"named operation":        {AuditRecord{Repository: "r", Format: "maven", Operation: "maven.snapshot.import", Outcome: AuditResolved, Actor: "admin"}, AuditClassSync},
		"explicit class kept":    {AuditRecord{Class: AuditClassSync, Operation: "get", Outcome: AuditResolved}, AuditClassSync},
		"explicit async kept":    {AuditRecord{Class: AuditClassAsync, Format: "management", Operation: "repo update"}, AuditClassAsync},
		"unknown operation sync": {AuditRecord{Repository: "r", Format: "raw", Operation: "frobnicate", Outcome: AuditResolved, Actor: "a"}, AuditClassSync},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.record.Durability(); got != tc.want {
				t.Fatalf("Durability()=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestRequestOutcomeClassification(t *testing.T) {
	for name, tc := range map[string]struct {
		record AuditRecord
		want   RequestOutcome
	}{
		"allowed":         {AuditRecord{Outcome: AuditResolved, Status: 200}, OutcomeAllowed},
		"not found":       {AuditRecord{Outcome: AuditNotFound, Status: 404}, OutcomeAllowed},
		"unauthenticated": {AuditRecord{Outcome: AuditAccessDenied, Status: 401}, OutcomeUnauthenticated},
		"forbidden":       {AuditRecord{Outcome: AuditAccessDenied, Status: 403}, OutcomeForbidden},
		"quarantined":     {AuditRecord{Outcome: AuditAccessDenied, Status: 403, AuthorizationReason: ArtifactQuarantinedReason}, OutcomeQuarantined},
		"rate limited":    {AuditRecord{Outcome: AuditAccessDenied, Status: 429}, OutcomeRateLimited},
		"quota":           {AuditRecord{Outcome: AuditAccessDenied, Status: 413}, OutcomeQuota},
		"other":           {AuditRecord{Outcome: AuditProxyDenied}, OutcomeOther},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.record.RequestOutcome(); got != tc.want {
				t.Fatalf("RequestOutcome()=%s, want %s", got, tc.want)
			}
		})
	}
	if !(AuditRecord{Repository: "r", Format: "raw"}).CountsAsRequest() || (AuditRecord{Repository: "r", Format: "management"}).CountsAsRequest() || (AuditRecord{Format: "raw"}).CountsAsRequest() {
		t.Fatal("only audited protocol requests that name a repository count")
	}
	if OutcomeForbidden.String() != "forbidden" || RequestOutcome(99).String() != "other" {
		t.Fatal("outcome names")
	}
}
