package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

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
