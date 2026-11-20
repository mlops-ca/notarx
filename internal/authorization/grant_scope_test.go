package authorization

import "testing"

// The aggregate lifecycle job listing finds a principal's candidate repositories through the grant index of
// GrantScopeAdmin and lets the authorizer decide on each. That is complete only while this scope is the one
// scope that allows RepositoryAdmin.
func TestOnlyTheAdminScopeAllowsRepositoryAdmin(t *testing.T) {
	scopes := []string{"repositories:read", "repositories:write", "repositories:intelligence", "repositories:admin", "admin", "repositories:", ""}
	var allowing []string
	for _, scope := range scopes {
		if grantAllows([]string{scope}, RepositoryAdmin) {
			allowing = append(allowing, scope)
		}
	}
	if len(allowing) != 1 || allowing[0] != GrantScopeAdmin {
		t.Fatalf("scopes allowing RepositoryAdmin = %v, want only %q", allowing, GrantScopeAdmin)
	}
	if !grantAllows([]string{"repositories:read", GrantScopeAdmin}, RepositoryAdmin) {
		t.Fatal("a grant that lists the admin scope among others must allow RepositoryAdmin")
	}
}
