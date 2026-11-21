package authorization

import (
	"context"
	"testing"

	"github.com/mlops-ca/notarx/internal/repository"
)

// importStubPrincipal is the stand-in for the service account a peer's trust policy maps to (ADR 0021, Remote targets),
// used by tests until ADR 0019's trusted-issuer authenticator can mint it: a plain principal whose only authority is
// the grants written for its actor.
func importStubPrincipal(actor string) Principal { return Principal{Actor: actor, Role: RoleMember} }

func TestOnlyTheImportAndAdminScopesAllowImportWrite(t *testing.T) {
	scopes := []string{"repositories:read", "repositories:write", "repositories:intelligence", GrantScopeImport, GrantScopeAdmin, "import", "repositories:", ""}
	var allowing []string
	for _, scope := range scopes {
		if grantAllows([]string{scope}, RepositoryImportWrite) {
			allowing = append(allowing, scope)
		}
	}
	if len(allowing) != 2 || allowing[0] != GrantScopeImport || allowing[1] != GrantScopeAdmin {
		t.Fatalf("scopes allowing ImportWrite = %v, want %q and %q", allowing, GrantScopeImport, GrantScopeAdmin)
	}
	// The import scope allows nothing else: a peer's service account cannot read, publish or administer.
	for _, operation := range []RepositoryOperation{RepositoryRead, RepositoryWrite, RepositoryAdmin, RepositoryIntelligence} {
		if grantAllows([]string{GrantScopeImport}, operation) {
			t.Fatalf("the import scope allows %s", operation)
		}
	}
}

func TestImportWriteIsGrantedAndEnforcedByTheAuthorizer(t *testing.T) {
	target := repository.HostedRepository{ID: "repo-id", Name: "team/releases"}
	grant := func(principal string, scopes ...string) RepositoryAuthorizer {
		return RepositoryAuthorizer{Grants: grantStoreStub{set: repository.RepositoryGrantSet{Version: "2", Grants: []repository.RepositoryGrant{{Principal: principal, Scopes: scopes}}}}}
	}
	peer := importStubPrincipal("service-account:peer")
	if decision := grant(peer.Actor, GrantScopeImport).Authorize(context.Background(), peer, target, RepositoryImportWrite); !decision.Allowed {
		t.Fatalf("a principal granted the import scope was denied: %+v", decision)
	}
	for _, scopes := range [][]string{{"repositories:read"}, {"repositories:write"}, {"repositories:intelligence"}} {
		if decision := grant(peer.Actor, scopes...).Authorize(context.Background(), peer, target, RepositoryImportWrite); decision.Allowed {
			t.Fatalf("scopes %v allow ImportWrite: %+v", scopes, decision)
		}
	}
	if decision := grant("service-account:someone-else", GrantScopeImport).Authorize(context.Background(), peer, target, RepositoryImportWrite); decision.Allowed {
		t.Fatalf("another principal's grant allowed ImportWrite: %+v", decision)
	}
	if decision := grant(peer.Actor, GrantScopeAdmin).Authorize(context.Background(), peer, target, RepositoryImportWrite); !decision.Allowed {
		t.Fatalf("the admin scope must allow ImportWrite: %+v", decision)
	}
	// A principal holding only the import scope cannot read what it imports, so the missing-blob answer of an import
	// never depends on repository content it cannot see.
	if decision := grant(peer.Actor, GrantScopeImport).Authorize(context.Background(), peer, target, RepositoryRead); decision.Allowed {
		t.Fatalf("the import scope allowed a read: %+v", decision)
	}
}
