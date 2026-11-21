package durableactor

import (
	"testing"

	"github.com/google/uuid"
)

func TestKindsAreClassified(t *testing.T) {
	if len(Kinds()) != len(classes) {
		t.Fatalf("Kinds lists %d kinds, classes has %d", len(Kinds()), len(classes))
	}
	seen := map[Class]bool{}
	for _, k := range Kinds() {
		c := ClassOf(k)
		if c == ClassNone || !Valid(k) {
			t.Fatalf("kind %q is not classified", k)
		}
		seen[c] = true
	}
	for _, c := range Classes() {
		if !seen[c] {
			t.Fatalf("class %d has no kind", c)
		}
	}
	if Valid("") || Valid("unknown") || ClassOf("unknown") != ClassNone {
		t.Fatal("unknown kinds must be refused")
	}
}

func TestCredentialAndActorShape(t *testing.T) {
	id := uuid.NewString()
	for _, tc := range []struct {
		kind, actor, credential string
		wantActor, wantCred     bool
	}{
		{StaticAdmin, "admin", "", true, true},
		{StaticResolver, "bad\x00actor", "", false, true},
		{StaticAdmin, "admin", id, true, false},
		{LocalSession, "user:a", "", true, true},
		{OIDC, "a", "", false, true},
		{APIKey, "api-key:" + id, "", true, true},
		{APIKey, "user:a", "", false, true},
		{ServiceAccountCredential, "service-account:" + id, id, true, true},
		{ServiceAccountCredential, "service-account:" + id, "", true, false},
		{WorkloadFederation, "service-account:" + id, "not-a-uuid", true, false},
		{WorkloadFederation, "user:a", id, false, true},
		{"unknown", "x", "", false, false},
	} {
		if got := ActorShapeValid(tc.kind, tc.actor); got != tc.wantActor {
			t.Errorf("ActorShapeValid(%q,%q) = %v", tc.kind, tc.actor, got)
		}
		if got := ValidCredential(tc.kind, tc.credential); got != tc.wantCred {
			t.Errorf("ValidCredential(%q,%q) = %v", tc.kind, tc.credential, got)
		}
	}
	if !CredentialRequired(WorkloadFederation) || !CredentialRequired(ServiceAccountCredential) || CredentialRequired(APIKey) {
		t.Fatal("credential requirement mismatch")
	}
}
