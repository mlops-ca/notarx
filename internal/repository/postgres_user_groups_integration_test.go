//go:build integration

package repository

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/repository/explaincheck"
)

func userGroupTestStore(t *testing.T) *PostgresStore {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func userGroupTestUser(t *testing.T, store *PostgresStore) User {
	t.Helper()
	user, err := store.CreateUser(context.Background(), User{ID: uuid.NewString(), Name: "group-user-" + uuid.NewString()[:8], Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, user.ID)
	})
	return user
}

func TestPostgresUserGroupLifecycleAndRules(t *testing.T) {
	store := userGroupTestStore(t)
	ctx := context.Background()
	user := userGroupTestUser(t, store)
	name := "ops-" + uuid.NewString()[:8]

	group, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: name, Source: UserGroupSourceLocal})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DeleteUserGroup(context.Background(), group.ID) })
	if group.Version != "1" || group.GlobalRole != UserGroupRoleNone || group.Source != UserGroupSourceLocal || group.ProviderID != "" {
		t.Fatalf("group = %#v", group)
	}
	if _, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: " " + name + " ", Source: UserGroupSourceLocal}); !errors.Is(err, ErrUserGroupNameExists) {
		t.Fatalf("duplicate local name err=%v", err)
	}
	if _, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "x", Source: UserGroupSourceSCIM, ProviderID: uuid.NewString(), ExternalID: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a group of an unknown provider err=%v", err)
	}

	renamed, err := store.RenameUserGroup(ctx, group.ID, name+"-renamed", group.Version)
	if err != nil || renamed.Version != "2" || renamed.DisplayName != name+"-renamed" {
		t.Fatalf("rename = %#v, %v", renamed, err)
	}
	if _, err := store.RenameUserGroup(ctx, group.ID, name, "1"); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale rename err=%v", err)
	}
	if _, err := store.RenameUserGroup(ctx, group.ID, name, "not-a-number"); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("garbled version err=%v", err)
	}
	flagged, err := store.SetUserGroupGlobalRole(ctx, group.ID, UserGroupRoleAdmin, renamed.Version)
	if err != nil || flagged.GlobalRole != UserGroupRoleAdmin || flagged.Version != "3" {
		t.Fatalf("flag = %#v, %v", flagged, err)
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
	if err != nil || len(memberships) != 1 || memberships[0].GroupID != group.ID || memberships[0].GlobalRole != UserGroupRoleAdmin || memberships[0].GroupSource != UserGroupSourceLocal {
		t.Fatalf("memberships = %#v, %v", memberships, err)
	}
	members, next, err := store.ListUserGroupMembers(ctx, group.ID, 10, "")
	if err != nil || next != "" || len(members) != 1 || members[0].UserID != user.ID || members[0].UserName != user.Name {
		t.Fatalf("members = %#v next=%q err=%v", members, next, err)
	}
	listed, _, err := store.ListUserGroups(ctx, 200, "")
	if err != nil || !slices.ContainsFunc(listed, func(g UserGroup) bool { return g.ID == group.ID }) {
		t.Fatalf("list = %d groups, %v", len(listed), err)
	}

	// Deleting the group removes its memberships, and a recreated group starts again at global role none.
	if err := store.DeleteUserGroup(ctx, group.ID); err != nil {
		t.Fatal(err)
	}
	if memberships, _ := store.ListUserGroupMemberships(ctx, user.ID); len(memberships) != 0 {
		t.Fatalf("memberships after the group was deleted: %#v", memberships)
	}
	if err := store.DeleteUserGroup(ctx, group.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err=%v", err)
	}
	recreated, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: name, Source: UserGroupSourceLocal})
	if err != nil || recreated.GlobalRole != UserGroupRoleNone {
		t.Fatalf("recreated = %#v, %v", recreated, err)
	}
	_ = store.DeleteUserGroup(ctx, recreated.ID)
}

func TestPostgresClaimsGroupsMatchOnlyTheLoginProvidersClaimsGroups(t *testing.T) {
	store := userGroupTestStore(t)
	ctx := context.Background()
	user := userGroupTestUser(t, store)
	providerA, providerB := userGroupTestProvider(t, store), userGroupTestProvider(t, store)
	create := func(group UserGroup) UserGroup {
		t.Helper()
		created, err := store.CreateUserGroup(ctx, group)
		if err != nil {
			t.Fatalf("create %#v: %v", group, err)
		}
		t.Cleanup(func() { _ = store.DeleteUserGroup(context.Background(), created.ID) })
		return created
	}
	own := create(UserGroup{DisplayName: "Eng", Source: UserGroupSourceClaims, ProviderID: providerA, ExternalID: "eng"})
	create(UserGroup{DisplayName: "Eng", Source: UserGroupSourceClaims, ProviderID: providerB, ExternalID: "eng"})
	scim := create(UserGroup{DisplayName: "Eng", Source: UserGroupSourceSCIM, ProviderID: providerA, ExternalID: "eng-scim"})
	if _, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "Eng again", Source: UserGroupSourceClaims, ProviderID: providerA, ExternalID: "eng"}); !errors.Is(err, ErrUserGroupExternalID) {
		t.Fatalf("duplicate external id err=%v", err)
	}
	// Two provisioned groups without an external id never collide (the unique index is partial).
	create(UserGroup{DisplayName: "No ext 1", Source: UserGroupSourceSCIM, ProviderID: providerA})
	create(UserGroup{DisplayName: "No ext 2", Source: UserGroupSourceSCIM, ProviderID: providerA})

	matched, err := store.SyncClaimsUserGroups(ctx, ClaimsGroupSync{ProviderID: providerA, UserID: user.ID, ExternalIDs: []string{"eng", "eng-scim", scim.ID, "Eng"}})
	if err != nil || matched != 1 {
		t.Fatalf("matched=%d err=%v", matched, err)
	}
	memberships, _ := store.ListUserGroupMemberships(ctx, user.ID)
	if len(memberships) != 1 || memberships[0].GroupID != own.ID || memberships[0].MemberSource != UserGroupSourceClaims {
		t.Fatalf("memberships = %#v", memberships)
	}
	if err := store.AddUserGroupMember(ctx, scim.ID, user.ID, UserGroupSourceSCIM); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SyncClaimsUserGroups(ctx, ClaimsGroupSync{ProviderID: providerA, UserID: user.ID}); err != nil {
		t.Fatal(err)
	}
	memberships, _ = store.ListUserGroupMemberships(ctx, user.ID)
	if len(memberships) != 1 || memberships[0].GroupID != scim.ID {
		t.Fatalf("a withdrawn claim must remove only the claims membership: %#v", memberships)
	}
}

func userGroupTestProvider(t *testing.T, store *PostgresStore) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := store.db.ExecContext(context.Background(), `INSERT INTO identity_providers (id,slug,kind,display_name) VALUES ($1::uuid,$2,'oidc','Group test')`, id, "grp-"+id[:12]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM identity_providers WHERE id=$1::uuid`, id)
	})
	return id
}

// The per-request lookup must be served by user_group_members_user_idx (ADR 0023 section 5): the primary key leads
// with group_id and cannot serve it.
func TestPostgresMembershipLookupUsesTheUserIndex(t *testing.T) {
	store := userGroupTestStore(t)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	plan, err := explaincheck.Plan(ctx, tx, userGroupMembershipsQuery, uuid.NewString(), MaxUserGroupMemberships)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.UsesIndex("user_group_members_user_idx") {
		t.Fatalf("the membership lookup does not use user_group_members_user_idx: %v", plan.Indexes())
	}
	for _, relation := range plan.SequentialScans() {
		if relation == "user_group_members" {
			t.Fatalf("the membership lookup scans user_group_members sequentially")
		}
	}
}

func TestPostgresGroupChangesAnnounceTheUserOrTheScope(t *testing.T) {
	store := userGroupTestStore(t)
	ctx := context.Background()
	user := userGroupTestUser(t, store)
	group, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: "notify-" + uuid.NewString()[:8], Source: UserGroupSourceLocal})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DeleteUserGroup(context.Background(), group.ID) })
	listener := listenCacheChannel(t, os.Getenv("TEST_DATABASE_URL"))
	listener.drain(300 * time.Millisecond)

	if err := store.AddUserGroupMember(ctx, group.ID, user.ID, UserGroupSourceLocal); err != nil {
		t.Fatal(err)
	}
	if got := listener.drain(500 * time.Millisecond); !slices.Equal(got, []string{"user_group:" + user.ID}) {
		t.Fatalf("a membership insert announced %v, want the user", got)
	}
	if err := store.RemoveUserGroupMember(ctx, group.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	if got := listener.drain(500 * time.Millisecond); !slices.Equal(got, []string{"user_group:" + user.ID}) {
		t.Fatalf("a membership removal announced %v, want the user", got)
	}
	if _, err := store.SetUserGroupGlobalRole(ctx, group.ID, UserGroupRoleMember, group.Version); err != nil {
		t.Fatal(err)
	}
	if got := listener.drain(500 * time.Millisecond); !slices.Equal(got, []string{"user_group:"}) {
		t.Fatalf("a group change announced %v, want the scope", got)
	}
}
