package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	groupProviderA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	groupProviderB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func newGroupFixture(t *testing.T) (*MemoryStore, User) {
	t.Helper()
	store := NewMemoryStore()
	user, err := store.CreateUser(context.Background(), User{ID: uuid.NewString(), Name: "ada", Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	return store, user
}

func TestUserGroupNamesAreUniqueCaseInsensitivelyAmongLocalGroups(t *testing.T) {
	store, _ := newGroupFixture(t)
	ctx := context.Background()
	if _, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "Platform", Source: UserGroupSourceLocal}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: " platform ", Source: UserGroupSourceLocal}); !errors.Is(err, ErrUserGroupNameExists) {
		t.Fatalf("duplicate local name err=%v", err)
	}
	// Two providers may both have an "Admins" group, and a local group may share the name.
	for _, provider := range []string{groupProviderA, groupProviderB} {
		if _, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "Platform", Source: UserGroupSourceSCIM, ProviderID: provider, ExternalID: "ext-" + provider}); err != nil {
			t.Fatalf("provisioned group of %s: %v", provider, err)
		}
	}
	if _, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "Other", Source: UserGroupSourceSCIM, ProviderID: groupProviderA, ExternalID: "ext-" + groupProviderA}); !errors.Is(err, ErrUserGroupExternalID) {
		t.Fatalf("duplicate external id err=%v", err)
	}
}

func TestUserGroupRoleRules(t *testing.T) {
	store, _ := newGroupFixture(t)
	ctx := context.Background()
	for name, group := range map[string]UserGroup{
		"scim group created with admin":    {DisplayName: "g", Source: UserGroupSourceSCIM, ProviderID: groupProviderA, GlobalRole: UserGroupRoleAdmin},
		"scim group created with member":   {DisplayName: "g", Source: UserGroupSourceSCIM, ProviderID: groupProviderA, GlobalRole: UserGroupRoleMember},
		"claims group with a role":         {DisplayName: "g", Source: UserGroupSourceClaims, ProviderID: groupProviderA, ExternalID: "x", GlobalRole: UserGroupRoleMember},
		"claims group without external id": {DisplayName: "g", Source: UserGroupSourceClaims, ProviderID: groupProviderA},
		"local group with a provider":      {DisplayName: "g", Source: UserGroupSourceLocal, ProviderID: groupProviderA},
		"unknown source":                   {DisplayName: "g", Source: "ldap"},
		"empty name":                       {DisplayName: "  ", Source: UserGroupSourceLocal},
		"unknown role":                     {DisplayName: "g", Source: UserGroupSourceLocal, GlobalRole: "owner"},
	} {
		if _, err := store.CreateUserGroup(ctx, group); !errors.Is(err, ErrUserGroupInvalid) {
			t.Errorf("%s: err=%v, want ErrUserGroupInvalid", name, err)
		}
	}

	local, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "ops", Source: UserGroupSourceLocal, GlobalRole: UserGroupRoleAdmin})
	if err != nil || local.GlobalRole != UserGroupRoleAdmin {
		t.Fatalf("local admin group = %#v, %v", local, err)
	}
	// An administrator may flag an existing SCIM group; the SCIM-facing rename cannot reach the role.
	scim, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "scim-team", Source: UserGroupSourceSCIM, ProviderID: groupProviderA, ExternalID: "scim-1"})
	if err != nil || scim.GlobalRole != UserGroupRoleNone {
		t.Fatalf("scim group = %#v, %v", scim, err)
	}
	renamed, err := store.RenameUserGroup(ctx, scim.ID, "scim-team-2", scim.Version)
	if err != nil || renamed.GlobalRole != UserGroupRoleNone || renamed.DisplayName != "scim-team-2" {
		t.Fatalf("rename = %#v, %v", renamed, err)
	}
	flagged, err := store.SetUserGroupGlobalRole(ctx, scim.ID, UserGroupRoleAdmin, renamed.Version)
	if err != nil || flagged.GlobalRole != UserGroupRoleAdmin {
		t.Fatalf("flag = %#v, %v", flagged, err)
	}
	if _, err := store.SetUserGroupGlobalRole(ctx, scim.ID, UserGroupRoleNone, renamed.Version); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale version err=%v", err)
	}
	// The flag is cleared when the group is deleted: a recreated group starts at none.
	if err := store.DeleteUserGroup(ctx, scim.ID); err != nil {
		t.Fatal(err)
	}
	recreated, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "scim-team", Source: UserGroupSourceSCIM, ProviderID: groupProviderA, ExternalID: "scim-1"})
	if err != nil || recreated.GlobalRole != UserGroupRoleNone || recreated.ID == scim.ID {
		t.Fatalf("recreated = %#v, %v", recreated, err)
	}
	// A claims-sourced group can never be flagged.
	claims, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "claims-team", Source: UserGroupSourceClaims, ProviderID: groupProviderA, ExternalID: "claims-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetUserGroupGlobalRole(ctx, claims.ID, UserGroupRoleMember, claims.Version); !errors.Is(err, ErrUserGroupInvalid) {
		t.Fatalf("flagging a claims group err=%v", err)
	}
}

func TestUserGroupMembershipLifecycle(t *testing.T) {
	store, user := newGroupFixture(t)
	ctx := context.Background()
	group, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "ops", Source: UserGroupSourceLocal, GlobalRole: UserGroupRoleMember})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddUserGroupMember(ctx, group.ID, user.ID, UserGroupSourceLocal); err != nil {
		t.Fatal(err)
	}
	if err := store.AddUserGroupMember(ctx, group.ID, user.ID, UserGroupSourceLocal); !errors.Is(err, ErrUserGroupMemberExists) {
		t.Fatalf("duplicate member err=%v", err)
	}
	if err := store.AddUserGroupMember(ctx, group.ID, uuid.NewString(), UserGroupSourceLocal); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user err=%v", err)
	}
	memberships, err := store.ListUserGroupMemberships(ctx, user.ID)
	if err != nil || len(memberships) != 1 || memberships[0].GroupID != group.ID || memberships[0].GlobalRole != UserGroupRoleMember {
		t.Fatalf("memberships = %#v, %v", memberships, err)
	}
	members, next, err := store.ListUserGroupMembers(ctx, group.ID, 10, "")
	if err != nil || next != "" || len(members) != 1 || members[0].UserName != "ada" {
		t.Fatalf("members = %#v next=%q err=%v", members, next, err)
	}
	if err := store.RemoveUserGroupMember(ctx, group.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveUserGroupMember(ctx, group.ID, user.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second removal err=%v", err)
	}
	if memberships, _ := store.ListUserGroupMemberships(ctx, user.ID); len(memberships) != 0 {
		t.Fatalf("revoked membership still listed: %#v", memberships)
	}
	if err := store.AddUserGroupMember(ctx, group.ID, user.ID, UserGroupSourceLocal); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteUserGroup(ctx, group.ID); err != nil {
		t.Fatal(err)
	}
	if memberships, _ := store.ListUserGroupMemberships(ctx, user.ID); len(memberships) != 0 {
		t.Fatalf("a deleted group's membership still listed: %#v", memberships)
	}
}

func TestMembershipsAreBoundedAndPagesAreKeyset(t *testing.T) {
	store, user := newGroupFixture(t)
	ctx := context.Background()
	for i := range MaxUserGroupMemberships + 8 {
		group, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: fmt.Sprintf("group-%04d", i), Source: UserGroupSourceLocal})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddUserGroupMember(ctx, group.ID, user.ID, UserGroupSourceLocal); err != nil {
			t.Fatal(err)
		}
	}
	memberships, err := store.ListUserGroupMemberships(ctx, user.ID)
	if err != nil || len(memberships) != MaxUserGroupMemberships {
		t.Fatalf("memberships = %d, %v, want the bound %d", len(memberships), err, MaxUserGroupMemberships)
	}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		items, next, err := store.ListUserGroups(ctx, 100, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if seen[item.ID] {
				t.Fatalf("group %s listed twice", item.ID)
			}
			seen[item.ID] = true
		}
		if next == "" {
			break
		}
		if pages > 10 {
			t.Fatal("paging does not end")
		}
		cursor = next
	}
	if len(seen) != MaxUserGroupMemberships+8 {
		t.Fatalf("paged %d groups, want %d", len(seen), MaxUserGroupMemberships+8)
	}
	if _, _, err := store.ListUserGroups(ctx, 10, "not-a-cursor"); !errors.Is(err, ErrUserGroupInvalid) {
		t.Fatalf("bad cursor err=%v", err)
	}
}

func TestClaimsGroupsMatchOnlyClaimsGroupsOfTheLoginProviderByExternalID(t *testing.T) {
	store, user := newGroupFixture(t)
	ctx := context.Background()
	create := func(group UserGroup) UserGroup {
		t.Helper()
		created, err := store.CreateUserGroup(ctx, group)
		if err != nil {
			t.Fatalf("create %#v: %v", group, err)
		}
		return created
	}
	own := create(UserGroup{DisplayName: "Engineers", Source: UserGroupSourceClaims, ProviderID: groupProviderA, ExternalID: "eng"})
	otherProvider := create(UserGroup{DisplayName: "Engineers", Source: UserGroupSourceClaims, ProviderID: groupProviderB, ExternalID: "eng"})
	scim := create(UserGroup{DisplayName: "Engineers SCIM", Source: UserGroupSourceSCIM, ProviderID: groupProviderA, ExternalID: "eng-scim"})
	local := create(UserGroup{DisplayName: "eng-local", Source: UserGroupSourceLocal, GlobalRole: UserGroupRoleAdmin})
	byName := create(UserGroup{DisplayName: "display-only", Source: UserGroupSourceClaims, ProviderID: groupProviderA, ExternalID: "different"})

	// The token asserts every one of these strings, including the ids and display names of the other groups.
	asserted := []string{"eng", "eng-scim", local.ID, "eng-local", "display-only-name", scim.ID, otherProvider.ID, "Engineers"}
	matched, err := store.SyncClaimsUserGroups(ctx, ClaimsGroupSync{ProviderID: groupProviderA, UserID: user.ID, ExternalIDs: asserted})
	if err != nil || matched != 1 {
		t.Fatalf("matched=%d err=%v, want only the claims group of this provider with that external id", matched, err)
	}
	memberships, _ := store.ListUserGroupMemberships(ctx, user.ID)
	if len(memberships) != 1 || memberships[0].GroupID != own.ID || memberships[0].MemberSource != UserGroupSourceClaims || memberships[0].GlobalRole != UserGroupRoleNone {
		t.Fatalf("memberships = %#v, want only %s", memberships, own.ID)
	}
	if byName.ID == "" {
		t.Fatal("fixture")
	}

	// A local and a SCIM membership are not touched by a login, and a withdrawn claim removes only claims rows.
	if err := store.AddUserGroupMember(ctx, local.ID, user.ID, UserGroupSourceLocal); err != nil {
		t.Fatal(err)
	}
	if err := store.AddUserGroupMember(ctx, scim.ID, user.ID, UserGroupSourceSCIM); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SyncClaimsUserGroups(ctx, ClaimsGroupSync{ProviderID: groupProviderA, UserID: user.ID}); err != nil {
		t.Fatal(err)
	}
	memberships, _ = store.ListUserGroupMemberships(ctx, user.ID)
	var ids []string
	for _, membership := range memberships {
		ids = append(ids, membership.GroupID)
	}
	slices.Sort(ids)
	want := []string{local.ID, scim.ID}
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("after the claim was withdrawn memberships = %v, want %v", ids, want)
	}

	if _, err := store.SyncClaimsUserGroups(ctx, ClaimsGroupSync{ProviderID: groupProviderA, UserID: user.ID, ExternalIDs: make([]string, MaxClaimsGroups+1)}); !errors.Is(err, ErrUserGroupInvalid) {
		t.Fatalf("oversized claim err=%v", err)
	}
}

func TestDeletingAGroupRemovesTheGrantsThatNameIt(t *testing.T) {
	store, user := newGroupFixture(t)
	ctx := context.Background()
	repo, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "releases", Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "team", Source: UserGroupSourceLocal})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddUserGroupMember(ctx, group.ID, user.ID, UserGroupSourceLocal); err != nil {
		t.Fatal(err)
	}
	principal := "group:" + group.ID
	before, err := store.ReplaceRepositoryGrants(ctx, repo.ID, []RepositoryGrant{
		{Principal: principal, Scopes: []string{"repositories:write"}},
		{Principal: principal, Scopes: []string{"repositories:read"}, ResourcePrefix: "releases/"},
		{Principal: "user:ada", Scopes: []string{"repositories:read"}},
	}, "1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteUserGroup(ctx, group.ID); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetRepositoryGrants(ctx, repo.ID)
	if err != nil || len(after.Grants) != 1 || after.Grants[0].Principal != "user:ada" || after.Version == before.Version {
		t.Fatalf("grants after the group was deleted = %#v (was version %s), %v", after, before.Version, err)
	}
}

func TestProvisionedGroupsMayShareANameAndOrphansStayEditable(t *testing.T) {
	store, _ := newGroupFixture(t)
	ctx := context.Background()
	if _, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "Admins", Source: UserGroupSourceLocal}); err != nil {
		t.Fatal(err)
	}
	// Provisioned groups do not compete with local names, in one provider or across providers.
	first, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "Admins", Source: UserGroupSourceSCIM, ProviderID: groupProviderA, ExternalID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "admins", Source: UserGroupSourceSCIM, ProviderID: groupProviderA, ExternalID: "2"}); err != nil {
		t.Fatal(err)
	}
	// A group of a deleted provider has no provider id any more; it stays a provisioned group and can be renamed.
	store.userGroupState.groups[first.ID] = func() UserGroup { g := store.userGroupState.groups[first.ID]; g.ProviderID = ""; return g }()
	if _, err := store.RenameUserGroup(ctx, first.ID, "Former admins", first.Version); err != nil {
		t.Fatalf("renaming an orphaned provisioned group: %v", err)
	}
	// Renaming a local group onto another local name is refused.
	other, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "Ops", Source: UserGroupSourceLocal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RenameUserGroup(ctx, other.ID, "ADMINS", other.Version); !errors.Is(err, ErrUserGroupNameExists) {
		t.Fatalf("rename onto a taken local name err=%v", err)
	}
}

func TestPageSizesAreClamped(t *testing.T) {
	for in, want := range map[int]int{-1: 50, 0: 50, 1: 1, 200: 200, 201: 200, 5000: 200} {
		if got := ClampUserGroupPageSize(in); got != want {
			t.Errorf("ClampUserGroupPageSize(%d) = %d, want %d", in, got, want)
		}
	}
	store, _ := newGroupFixture(t)
	for i := range 205 {
		if _, err := store.CreateUserGroup(context.Background(), UserGroup{DisplayName: fmt.Sprintf("g-%03d", i), Source: UserGroupSourceLocal}); err != nil {
			t.Fatal(err)
		}
	}
	items, next, err := store.ListUserGroups(context.Background(), 500, "")
	if err != nil || len(items) != 200 || next == "" {
		t.Fatalf("an oversized limit returned %d items next=%q err=%v, want the clamp of 200", len(items), next, err)
	}
}

func TestGrantIndexListsRepositoriesOfAnyOfSeveralPrincipalsOnce(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	var repos []HostedRepository
	for _, name := range []string{"a", "b", "c"} {
		repo, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: name, Format: FormatRaw})
		if err != nil {
			t.Fatal(err)
		}
		repos = append(repos, repo)
	}
	grant := func(repo HostedRepository, principals ...string) {
		var grants []RepositoryGrant
		for _, principal := range principals {
			grants = append(grants, RepositoryGrant{Principal: principal, Scopes: []string{"repositories:admin"}})
		}
		if _, err := store.ReplaceRepositoryGrants(ctx, repo.ID, grants, "1"); err != nil {
			t.Fatal(err)
		}
	}
	grant(repos[0], "user:ada", "group:g1") // both name the caller: listed once
	grant(repos[1], "group:g2")
	grant(repos[2], "user:other")
	got, err := store.ListRepositoriesGrantedToPrincipals(ctx, []string{"user:ada", "group:g1", "group:g2"}, "repositories:admin")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d repositories, %v, want 2", len(got), err)
	}
	if none, err := store.ListRepositoriesGrantedToPrincipals(ctx, nil, "repositories:admin"); err != nil || len(none) != 0 {
		t.Fatalf("no principals = %d, %v", len(none), err)
	}
}

func TestGroupsOfADeletedProviderAreInertAndTemplatesLoseTheDeletedGroup(t *testing.T) {
	store, user := newGroupFixture(t)
	ctx := context.Background()
	orphan, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "scim-team", Source: UserGroupSourceSCIM, ProviderID: groupProviderA, ExternalID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	local, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "local-team", Source: UserGroupSourceLocal})
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []UserGroup{orphan, local} {
		if err := store.AddUserGroupMember(ctx, group.ID, user.ID, UserGroupSourceLocal); err != nil {
			t.Fatal(err)
		}
	}
	// What deleting the provider does to its groups (ON DELETE SET NULL).
	store.userGroupState.groups[orphan.ID] = func() UserGroup { g := orphan; g.ProviderID = ""; return g }()
	memberships, err := store.ListUserGroupMemberships(ctx, user.ID)
	if err != nil || len(memberships) != 1 || memberships[0].GroupID != local.ID {
		t.Fatalf("memberships = %#v, %v, want only the local group: a provider-less provisioned group grants nothing", memberships, err)
	}

	principal := "group:" + local.ID
	template, err := store.CreateAuthorizationTemplate(ctx, AuthorizationTemplate{Name: "ops", Grants: []RepositoryGrant{
		{Principal: principal, Scopes: []string{"repositories:read"}},
		{Principal: "user:keeper", Scopes: []string{"repositories:read"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteUserGroup(ctx, local.ID); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetAuthorizationTemplate(ctx, template.ID)
	if err != nil || len(after.Grants) != 1 || after.Grants[0].Principal != "user:keeper" || after.Version == template.Version {
		t.Fatalf("template after the group was deleted = %#v, %v", after, err)
	}
}

func TestBoundClaimsGroupsAndTheOverageAudit(t *testing.T) {
	if got, overage := BoundClaimsGroups([]string{"a"}); overage || len(got) != 1 {
		t.Fatalf("got=%v overage=%t", got, overage)
	}
	if got, overage := BoundClaimsGroups(make([]string, MaxClaimsGroups+1)); !overage || got != nil {
		t.Fatalf("got %d groups overage=%t", len(got), overage)
	}
	record := GroupsOverageAudit("saml", "Corp", time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), 302, "req-1")
	if record.Actor != "saml:corp" || record.Resource != "auth/saml/corp" || record.Operation != "auth.saml.groups_overage" || record.AuthorizationReason != "groups_overage" || record.Status != 302 || record.RequestID != "req-1" {
		t.Fatalf("record = %#v", record)
	}
}

func TestPageTokensMustCarryAnID(t *testing.T) {
	store, _ := newGroupFixture(t)
	bad := encodeUserGroupCursor(time.Now(), "not-a-uuid")
	if _, _, err := store.ListUserGroups(context.Background(), 10, bad); !errors.Is(err, ErrUserGroupInvalid) {
		t.Fatalf("a token with a malformed id err=%v", err)
	}
}

func TestClaimsMembershipNeverContributesAGlobalRole(t *testing.T) {
	for _, source := range []UserGroupSource{UserGroupSourceLocal, UserGroupSourceSCIM} {
		membership := UserGroupMembership{GroupSource: source, GlobalRole: UserGroupRoleAdmin}
		if membership.EffectiveGroupRole() != UserGroupRoleAdmin {
			t.Fatalf("%s group role was dropped", source)
		}
	}
	if role := (UserGroupMembership{GroupSource: UserGroupSourceClaims, GlobalRole: UserGroupRoleAdmin}).EffectiveGroupRole(); role != UserGroupRoleNone {
		t.Fatalf("a claims group contributed role %q", role)
	}
}
