package authorization

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/repository"
)

type groupFixture struct {
	store         *repository.MemoryStore
	authenticator Authenticator
	repo          repository.HostedRepository
	user          repository.User
}

// newGroupFixture builds a managed repository, an active member account "ada" and an authenticator that resolves
// groups from the same store.
func newAuthGroupFixture(t *testing.T, role string) *groupFixture {
	t.Helper()
	store := repository.NewMemoryStore()
	ctx := context.Background()
	repo, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "releases", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(ctx, repository.User{ID: uuid.NewString(), Name: "ada", Role: role})
	if err != nil {
		t.Fatal(err)
	}
	return &groupFixture{
		store: store, repo: repo, user: user,
		authenticator: Authenticator{AdminToken: "admin-secret", ResolverToken: "resolver-secret", Users: store, UserGroups: store},
	}
}

func (f *groupFixture) group(t *testing.T, group repository.UserGroup, members ...repository.User) repository.UserGroup {
	t.Helper()
	created, err := f.store.CreateUserGroup(context.Background(), group)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		if err := f.store.AddUserGroupMember(context.Background(), created.ID, member.ID, repository.UserGroupSourceLocal); err != nil {
			t.Fatal(err)
		}
	}
	return created
}

func (f *groupFixture) grants(t *testing.T, grants ...repository.RepositoryGrant) {
	t.Helper()
	if _, err := f.store.ReplaceRepositoryGrants(context.Background(), f.repo.ID, grants, "1"); err != nil {
		t.Fatal(err)
	}
}

func (f *groupFixture) principal(t *testing.T) Principal {
	t.Helper()
	principal, ok := f.authenticator.Authenticate("Bearer " + f.authenticator.IssueUserSession(f.user.ID))
	if !ok {
		t.Fatal("user session did not authenticate")
	}
	return principal
}

func (f *groupFixture) decide(principal Principal, operation RepositoryOperation) AuthorizationDecision {
	return RepositoryAuthorizer{Grants: f.store}.Authorize(context.Background(), principal, f.repo, operation)
}

func TestGroupGrantsUnionWithTheUsersOwnGrants(t *testing.T) {
	f := newAuthGroupFixture(t, "member")
	readers := f.group(t, repository.UserGroup{DisplayName: "readers", Source: repository.UserGroupSourceLocal}, f.user)
	writers := f.group(t, repository.UserGroup{DisplayName: "writers", Source: repository.UserGroupSourceLocal})
	other := f.group(t, repository.UserGroup{DisplayName: "other", Source: repository.UserGroupSourceLocal})
	f.grants(t,
		repository.RepositoryGrant{Principal: "user:ada", Scopes: []string{"repositories:intelligence"}},
		repository.RepositoryGrant{Principal: GroupPrincipal(readers.ID), Scopes: []string{"repositories:read"}},
		repository.RepositoryGrant{Principal: GroupPrincipal(writers.ID), Scopes: []string{"repositories:write"}},
		repository.RepositoryGrant{Principal: GroupPrincipal(other.ID), Scopes: []string{"repositories:admin"}},
	)

	principal := f.principal(t)
	if len(principal.GroupIDs) != 1 || principal.GroupIDs[0] != readers.ID {
		t.Fatalf("group ids = %v, want only %s", principal.GroupIDs, readers.ID)
	}
	for operation, want := range map[RepositoryOperation]bool{
		RepositoryRead: true, RepositoryIntelligence: true, // one from the group, one from the user's own entry
		RepositoryWrite: false, RepositoryAdmin: false, // granted only to groups the user is not in
	} {
		if got := f.decide(principal, operation); got.Allowed != want {
			t.Errorf("%s allowed=%t, want %t (%+v)", operation, got.Allowed, want, got)
		}
	}

	// A second membership unions the scopes.
	if err := f.store.AddUserGroupMember(context.Background(), writers.ID, f.user.ID, repository.UserGroupSourceLocal); err != nil {
		t.Fatal(err)
	}
	principal = f.principal(t)
	if got := f.decide(principal, RepositoryWrite); !got.Allowed || got.Source != "repository_grants" {
		t.Fatalf("write after joining the writers group: %+v", got)
	}
}

func TestRevokedMembershipWithdrawsGroupAuthorityOnTheNextRequest(t *testing.T) {
	f := newAuthGroupFixture(t, "member")
	team := f.group(t, repository.UserGroup{DisplayName: "team", Source: repository.UserGroupSourceLocal}, f.user)
	f.grants(t, repository.RepositoryGrant{Principal: GroupPrincipal(team.ID), Scopes: []string{"repositories:write"}})

	session := f.authenticator.IssueUserSession(f.user.ID)
	principal, _ := f.authenticator.Authenticate("Bearer " + session)
	if !f.decide(principal, RepositoryWrite).Allowed {
		t.Fatal("member of the group was denied")
	}
	if err := f.store.RemoveUserGroupMember(context.Background(), team.ID, f.user.ID); err != nil {
		t.Fatal(err)
	}
	// The same session token, one request later.
	principal, ok := f.authenticator.Authenticate("Bearer " + session)
	if !ok || len(principal.GroupIDs) != 0 {
		t.Fatalf("principal = %#v ok=%v", principal, ok)
	}
	if got := f.decide(principal, RepositoryWrite); got.Allowed || got.Reason != "scope_not_granted" {
		t.Fatalf("revoked member decision = %+v", got)
	}
}

func TestPendingAccountStaysBlockedWhateverItsGroupsAreGranted(t *testing.T) {
	f := newAuthGroupFixture(t, "none")
	team := f.group(t, repository.UserGroup{DisplayName: "team", Source: repository.UserGroupSourceLocal}, f.user) // role none
	f.grants(t, repository.RepositoryGrant{Principal: GroupPrincipal(team.ID), Scopes: []string{"repositories:admin"}})

	principal := f.principal(t)
	if principal.Role != RoleNone || principal.Admin {
		t.Fatalf("a role-none group changed the level: %#v", principal)
	}
	for _, operation := range []RepositoryOperation{RepositoryRead, RepositoryWrite, RepositoryAdmin} {
		if got := f.decide(principal, operation); got.Allowed || got.Reason != "authorization_pending" {
			t.Errorf("%s: pending account decision %+v, want the account-state block", operation, got)
		}
	}
}

func TestGroupGlobalRoleRaisesTheLevelAtAuthenticationTimeAndNeverLowersIt(t *testing.T) {
	cases := []struct {
		name      string
		userRole  string
		groupRole string
		wantRole  Role
		wantAdmin bool
	}{
		{"member in admin group", "member", repository.UserGroupRoleAdmin, RoleAdmin, true},
		{"pending in member group is approved", "none", repository.UserGroupRoleMember, RoleMember, false},
		{"pending in admin group", "none", repository.UserGroupRoleAdmin, RoleAdmin, true},
		{"admin stays admin in a none group", "admin", repository.UserGroupRoleNone, RoleAdmin, true},
		{"admin stays admin in a member group", "admin", repository.UserGroupRoleMember, RoleAdmin, true},
		{"member in a none group", "member", repository.UserGroupRoleNone, RoleMember, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthGroupFixture(t, tc.userRole)
			group := f.group(t, repository.UserGroup{DisplayName: "g", Source: repository.UserGroupSourceLocal, GlobalRole: tc.groupRole}, f.user)
			principal := f.principal(t)
			if principal.Role != tc.wantRole || principal.Admin != tc.wantAdmin {
				t.Fatalf("principal role=%q admin=%t, want %q %t", principal.Role, principal.Admin, tc.wantRole, tc.wantAdmin)
			}
			// The level is evaluated, not copied: dropping the flag or the membership takes it back at once.
			fetched, err := f.store.GetUserGroup(context.Background(), group.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.SetUserGroupGlobalRole(context.Background(), group.ID, repository.UserGroupRoleNone, fetched.Version); err != nil {
				t.Fatal(err)
			}
			if after := f.principal(t); after.Role != Role(tc.userRole) {
				t.Fatalf("after the flag was cleared role=%q, want the account's own %q", after.Role, tc.userRole)
			}
		})
	}
}

type fixedGroups []repository.UserGroupMembership

func (g fixedGroups) ListUserGroupMemberships(context.Context, string) ([]repository.UserGroupMembership, error) {
	return g, nil
}

type failingGroups struct{}

func (failingGroups) ListUserGroupMemberships(context.Context, string) ([]repository.UserGroupMembership, error) {
	return nil, context.DeadlineExceeded
}

func TestClaimsGroupsNeverRaiseTheLevelAndAFailedLookupNeverWidensAccess(t *testing.T) {
	f := newAuthGroupFixture(t, "member")
	f.authenticator.UserGroups = fixedGroups{
		// A claims-sourced row that somehow carries a role still contributes none.
		{GroupID: uuid.NewString(), GroupSource: repository.UserGroupSourceClaims, GlobalRole: repository.UserGroupRoleAdmin, MemberSource: repository.UserGroupSourceClaims},
	}
	if principal := f.principal(t); principal.Admin || principal.Role != RoleMember || len(principal.GroupIDs) != 1 {
		t.Fatalf("claims group principal = %#v", principal)
	}
	f.authenticator.UserGroups = failingGroups{}
	if principal := f.principal(t); principal.Admin || principal.Role != RoleMember || len(principal.GroupIDs) != 0 {
		t.Fatalf("principal after a failed lookup = %#v", principal)
	}
}

func TestAnExternalSubjectSpellingAGroupPrincipalDoesNotTakeOverTheGroupGrants(t *testing.T) {
	f := newAuthGroupFixture(t, "member")
	team := f.group(t, repository.UserGroup{DisplayName: "team", Source: repository.UserGroupSourceLocal})
	f.grants(t, repository.RepositoryGrant{Principal: GroupPrincipal(team.ID), Scopes: []string{"repositories:admin"}})

	// An OIDC subject with no linked account becomes the actor string as the IdP wrote it.
	spoofed := Principal{Actor: GroupPrincipal(team.ID), AuthenticationKind: AuthenticationOIDC}
	if spoofed.NamesGrantPrincipal(GroupPrincipal(team.ID)) {
		t.Fatal("an actor string matched a group principal")
	}
	if got := f.decide(spoofed, RepositoryAdmin); got.Allowed {
		t.Fatalf("spoofed actor decision = %+v", got)
	}
	member := Principal{Actor: "user:ada", GroupIDs: []string{team.ID}}
	if !member.NamesGrantPrincipal(GroupPrincipal(team.ID)) || !member.NamesGrantPrincipal("user:ada") || member.NamesGrantPrincipal(GroupPrincipal(uuid.NewString())) {
		t.Fatal("NamesGrantPrincipal does not match exactly the actor and the groups of the principal")
	}
	if got := member.GrantPrincipals(); len(got) != 2 || got[0] != "user:ada" || got[1] != GroupPrincipal(team.ID) {
		t.Fatalf("grant principals = %v", got)
	}
}

func TestPrincipalTokensCarryNoGroupIDsAndGroupsAreResolvedAgainEachRequest(t *testing.T) {
	f := newAuthGroupFixture(t, "member")
	team := f.group(t, repository.UserGroup{DisplayName: "team", Source: repository.UserGroupSourceLocal, GlobalRole: repository.UserGroupRoleMember}, f.user)
	f.grants(t, repository.RepositoryGrant{Principal: GroupPrincipal(team.ID), Scopes: []string{"repositories:read"}})

	principal := f.principal(t)
	if len(principal.GroupIDs) != 1 {
		t.Fatalf("principal = %#v", principal)
	}
	for name, token := range map[string]string{
		"principal token": f.authenticator.IssuePrincipalToken(principal),
		"actor token":     f.authenticator.IssueToken(principal.Actor),
		"web session":     f.authenticator.IssueWebSession(Principal{Actor: principal.Actor, GroupIDs: principal.GroupIDs, AuthenticationKind: AuthenticationOIDC}),
	} {
		parts := strings.Split(token, ".")
		for _, part := range parts {
			if raw, err := base64.RawURLEncoding.DecodeString(part); err == nil && strings.Contains(string(raw), team.ID) {
				t.Fatalf("%s carries a group id: %s", name, raw)
			}
		}
		if strings.Contains(token, team.ID) {
			t.Fatalf("%s carries a group id", name)
		}
	}
	token := f.authenticator.IssuePrincipalToken(principal)
	fromToken, ok := f.authenticator.Authenticate("Bearer " + token)
	if !ok || len(fromToken.GroupIDs) != 1 || !f.decide(fromToken, RepositoryRead).Allowed {
		t.Fatalf("token principal = %#v ok=%v", fromToken, ok)
	}
	// A protocol token names the account but is not a session: code that tells a static credential from a user
	// session (the Nexus migration authority) relies on the token principal carrying no UserID.
	if fromToken.UserID != "" {
		t.Fatalf("a protocol token principal carries the user id %q", fromToken.UserID)
	}
	if err := f.store.RemoveUserGroupMember(context.Background(), team.ID, f.user.ID); err != nil {
		t.Fatal(err)
	}
	fromToken, ok = f.authenticator.Authenticate("Bearer " + token)
	if !ok || len(fromToken.GroupIDs) != 0 || f.decide(fromToken, RepositoryRead).Allowed {
		t.Fatalf("token principal after revocation = %#v ok=%v", fromToken, ok)
	}
}

func TestDisabledAccountIsBlockedRegardlessOfGroups(t *testing.T) {
	f := newAuthGroupFixture(t, "member")
	f.group(t, repository.UserGroup{DisplayName: "admins", Source: repository.UserGroupSourceLocal, GlobalRole: repository.UserGroupRoleAdmin}, f.user)
	session := f.authenticator.IssueUserSession(f.user.ID)
	if principal, ok := f.authenticator.Authenticate("Bearer " + session); !ok || !principal.Admin {
		t.Fatalf("principal = %#v ok=%v", principal, ok)
	}
	state := repository.UserDisabled
	if _, err := f.store.UpdateUser(context.Background(), repository.UserUpdate{ID: f.user.ID, State: &state}, f.user.Version); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.authenticator.Authenticate("Bearer " + session); ok {
		t.Fatal("a disabled account authenticated through its admin group")
	}
}

func TestMustChangePasswordPrincipalIgnoresGroups(t *testing.T) {
	f := newAuthGroupFixture(t, "member")
	pending, err := f.store.CreateUser(context.Background(), repository.User{ID: uuid.NewString(), Name: "grace", Role: "member", SecretHash: "local-hash", MustChangePassword: true})
	if err != nil {
		t.Fatal(err)
	}
	f.group(t, repository.UserGroup{DisplayName: "admins", Source: repository.UserGroupSourceLocal, GlobalRole: repository.UserGroupRoleAdmin}, pending)
	principal, ok := f.authenticator.Authenticate("Bearer " + f.authenticator.IssueUserSession(pending.ID))
	if !ok || !principal.MustChangePassword || principal.Admin || principal.Role != "" || len(principal.GroupIDs) != 0 {
		t.Fatalf("principal = %#v ok=%v", principal, ok)
	}
}

func TestAnAccountInMoreGroupsThanTheBoundLosesAuthorityNeverGainsIt(t *testing.T) {
	f := newAuthGroupFixture(t, "member")
	memberships := make(fixedGroups, 0, repository.MaxUserGroupMemberships+10)
	for range repository.MaxUserGroupMemberships + 10 {
		memberships = append(memberships, repository.UserGroupMembership{GroupID: uuid.NewString(), GroupSource: repository.UserGroupSourceLocal, GlobalRole: repository.UserGroupRoleNone})
	}
	f.authenticator.UserGroups = memberships
	if principal := f.principal(t); len(principal.GroupIDs) != repository.MaxUserGroupMemberships {
		t.Fatalf("group ids = %d, want the bound", len(principal.GroupIDs))
	}
}

// The ADR asks for under 1 ms added at 100 groups. The lookup itself is one indexed statement (see the PostgreSQL
// plan test); this measures the work the authenticator adds on top of it.
func TestResolvingOneHundredGroupsAddsUnderOneMillisecond(t *testing.T) {
	f := newAuthGroupFixture(t, "member")
	memberships := make(fixedGroups, 0, 100)
	for range 100 {
		memberships = append(memberships, repository.UserGroupMembership{GroupID: uuid.NewString(), GroupSource: repository.UserGroupSourceLocal, GlobalRole: repository.UserGroupRoleNone})
	}
	f.authenticator.UserGroups = memberships
	base := Principal{Actor: "user:ada", Role: RoleMember, UserID: f.user.ID}
	grantSet := repository.RepositoryGrantSet{Version: "2"}
	for _, membership := range memberships {
		grantSet.Grants = append(grantSet.Grants, repository.RepositoryGrant{Principal: GroupPrincipal(membership.GroupID), Scopes: []string{"repositories:read"}})
	}
	authorizer := RepositoryAuthorizer{Grants: grantStoreStub{set: grantSet}}
	const rounds = 200
	start := time.Now()
	for range rounds {
		principal := f.authenticator.WithUserGroups(context.Background(), base)
		_ = authorizer.Authorize(context.Background(), principal, f.repo, RepositoryWrite)
	}
	if perRequest := time.Since(start) / rounds; perRequest >= time.Millisecond {
		t.Fatalf("resolving 100 groups and deciding against 100 group grants took %v per request, want under 1ms", perRequest)
	}
}

func BenchmarkResolveAndDecideAtOneHundredGroups(b *testing.B) {
	memberships := make(fixedGroups, 0, 100)
	grantSet := repository.RepositoryGrantSet{Version: "2"}
	for i := range 100 {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		memberships = append(memberships, repository.UserGroupMembership{GroupID: id, GroupSource: repository.UserGroupSourceLocal, GlobalRole: repository.UserGroupRoleNone})
		grantSet.Grants = append(grantSet.Grants, repository.RepositoryGrant{Principal: GroupPrincipal(id), Scopes: []string{"repositories:read"}})
	}
	authenticator := Authenticator{UserGroups: memberships}
	authorizer := RepositoryAuthorizer{Grants: grantStoreStub{set: grantSet}}
	base := Principal{Actor: "user:ada", Role: RoleMember, UserID: "u"}
	target := repository.HostedRepository{ID: "r", Name: "r"}
	b.ResetTimer()
	for range b.N {
		principal := authenticator.WithUserGroups(context.Background(), base)
		_ = authorizer.Authorize(context.Background(), principal, target, RepositoryWrite)
	}
}

func TestClaimGroupsPresentTellsAnAbsentClaimFromAnEmptyOne(t *testing.T) {
	for name, tc := range map[string]struct {
		claims  map[string]any
		present bool
	}{
		"absent":      {map[string]any{}, false},
		"empty list":  {map[string]any{"groups": []any{}}, true},
		"overage":     {map[string]any{"hasgroups": true}, true},
		"odd shape":   {map[string]any{"groups": 7}, true},
		"other claim": {map[string]any{"roles": []any{"x"}}, false},
	} {
		if _, _, present := claimGroupsPresent(tc.claims, ""); present != tc.present {
			t.Errorf("%s: present=%t, want %t", name, present, tc.present)
		}
	}
}

func TestTolerantStringsNeverRejectsAToken(t *testing.T) {
	for raw, want := range map[string]int{"": 1} {
		_ = raw
		_ = want
	}
	cases := []struct {
		raw  any
		want []string
	}{
		{"eng", []string{"eng"}},
		{[]any{"a", 7, "b"}, []string{"a", "b"}},
		{7, nil},
		{nil, nil},
		{map[string]any{"a": "b"}, nil},
	}
	for _, tc := range cases {
		if got := tolerantStrings(tc.raw); strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("tolerantStrings(%#v) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestClaimGroupsReadsTheGroupsClaimAndTreatsOverageAsNoGroups(t *testing.T) {
	cases := []struct {
		name        string
		claims      map[string]any
		claim       string
		wantGroups  []string
		wantOverage bool
	}{
		{"plain list", map[string]any{"groups": []any{"eng", " ops ", "eng", ""}}, "", []string{"eng", "ops"}, false},
		{"configured claim", map[string]any{"memberOf": []any{"a"}, "groups": []any{"b"}}, "memberOf", []string{"a"}, false},
		{"no claim", map[string]any{}, "", nil, false},
		{"entra overage via claim names", map[string]any{"_claim_names": map[string]any{"groups": "src1"}, "groups": []any{"partial"}}, "", nil, true},
		{"entra overage via hasgroups", map[string]any{"hasgroups": true}, "", nil, true},
		{"single string claim", map[string]any{"groups": "eng"}, "", []string{"eng"}, false},
		{"non-list claim", map[string]any{"groups": 7}, "", nil, false},
		{"object claim", map[string]any{"groups": map[string]any{"a": "b"}}, "", nil, false},
		{"non-string member", map[string]any{"groups": []any{"eng", 7}}, "", nil, false},
		{"over the bound", map[string]any{"groups": make([]any, repository.MaxClaimsGroups+1)}, "", nil, true},
		{"unrelated claim names", map[string]any{"_claim_names": map[string]any{"other": "src1"}, "groups": []any{"eng"}}, "", []string{"eng"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			groups, overage := claimGroups(tc.claims, tc.claim)
			if overage != tc.wantOverage || strings.Join(groups, ",") != strings.Join(tc.wantGroups, ",") {
				t.Fatalf("groups=%v overage=%t, want %v %t", groups, overage, tc.wantGroups, tc.wantOverage)
			}
		})
	}
}
