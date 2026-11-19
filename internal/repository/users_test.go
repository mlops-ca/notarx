package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestMemoryUserProfileFilteringAndAuthenticationLifecycle(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	created, err := store.CreateUser(ctx, User{
		ID:          "alice-id",
		Name:        "alice",
		DisplayName: "Alice Example",
		Email:       "alice@example.test",
		Description: "build account",
		SecretHash:  "hash",
		Role:        "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.SessionVersion != 1 || created.PasswordChangedAt == nil {
		t.Fatalf("created user lifecycle fields=%+v", created)
	}
	caseInsensitive, err := store.GetUserByName(ctx, "ALICE")
	if err != nil || caseInsensitive.ID != created.ID {
		t.Fatalf("case-insensitive lookup=%+v err=%v", caseInsensitive, err)
	}
	if _, err := store.CreateUser(ctx, User{ID: "bob-id", Name: "bob", DisplayName: "Bob", Role: "none"}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListUsers(ctx, UserListQuery{Search: "alice", Role: "member", State: UserActive, Limit: 10})
	if err != nil || page.Total.Value != 1 || len(page.Items) != 1 || page.Items[0].Email != "alice@example.test" {
		t.Fatalf("filtered users page=%+v err=%v", page, err)
	}

	displayName, email, description := "Alice Builder", "alice@new.example", "updated"
	updated, err := store.UpdateUser(ctx, UserUpdate{ID: created.ID, DisplayName: &displayName, Email: &email, Description: &description}, created.Version)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != "Alice Builder" || updated.Email != "alice@new.example" || updated.Description != "updated" {
		t.Fatalf("profile was not updated: %+v", updated)
	}

	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	for attempt := 1; attempt <= 3; attempt++ {
		updated, err = store.RecordUserLoginFailure(ctx, created.ID, now, 3, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if updated.FailedLoginAttempts != attempt {
			t.Fatalf("attempt %d state=%+v", attempt, updated)
		}
	}
	if updated.LockedUntil == nil || !updated.LockedUntil.After(now) {
		t.Fatalf("user was not locked after failed attempts: %+v", updated)
	}
	if _, err := store.RecordUserLoginSuccess(ctx, created.ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	updated, err = store.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.FailedLoginAttempts != 0 || updated.LockedUntil != nil || updated.LastLoginAt == nil {
		t.Fatalf("successful login did not clear lock state: %+v", updated)
	}
	updated, err = store.RecordUserLoginFailure(ctx, created.ID, now.Add(2*time.Second), 1, time.Minute)
	if err != nil || updated.LockedUntil == nil {
		t.Fatalf("failed login before reset=%+v err=%v", updated, err)
	}

	passwordUpdated, err := store.UpdateUserPassword(ctx, created.ID, "new-hash", updated.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	if passwordUpdated.SecretHash != "new-hash" || !passwordUpdated.MustChangePassword || passwordUpdated.SessionVersion != 2 || passwordUpdated.PasswordChangedAt == nil || passwordUpdated.FailedLoginAttempts != 0 || passwordUpdated.LockedUntil != nil {
		t.Fatalf("password lifecycle fields=%+v", passwordUpdated)
	}
	revoked, err := store.RevokeUserSessions(ctx, created.ID, passwordUpdated.Version)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.SessionVersion != 3 {
		t.Fatalf("session version=%d want=3", revoked.SessionVersion)
	}
}

func TestMemoryUserStoreProtectsLastActiveAdministrator(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	first, err := store.CreateUser(ctx, User{ID: "admin-1", Name: "admin-one", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	member := "member"
	if _, err = store.UpdateUser(ctx, UserUpdate{ID: first.ID, Role: &member}, first.Version); !errors.Is(err, ErrLastActiveAdmin) {
		t.Fatalf("demote last admin error=%v want=%v", err, ErrLastActiveAdmin)
	}
	if err = store.DeleteUser(ctx, first.ID); !errors.Is(err, ErrLastActiveAdmin) {
		t.Fatalf("delete last admin error=%v want=%v", err, ErrLastActiveAdmin)
	}
	second, err := store.CreateUser(ctx, User{ID: "admin-2", Name: "admin-two", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.UpdateUser(ctx, UserUpdate{ID: first.ID, Role: &member}, first.Version); err != nil {
		t.Fatalf("demote with second admin: %v", err)
	}
	if err = store.DeleteUser(ctx, second.ID); !errors.Is(err, ErrLastActiveAdmin) {
		t.Fatalf("delete remaining admin error=%v want=%v", err, ErrLastActiveAdmin)
	}
}

func TestMemoryListUsersWalksKeysetPages(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	for i := 0; i < 7; i++ {
		if _, err := store.CreateUser(ctx, User{ID: fmt.Sprintf("walker-id-%d", i), Name: fmt.Sprintf("walker-%d", i), Role: "member"}); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	token := ""
	for pages := 0; pages < 10; pages++ {
		page, err := store.ListUsers(ctx, UserListQuery{Search: "walker", Limit: 3, PageToken: token})
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != (ListTotal{Value: 7, Relation: TotalExact}) {
			t.Fatalf("total=%+v", page.Total)
		}
		for _, user := range page.Items {
			seen = append(seen, user.Name)
		}
		if token = page.NextPageToken; token == "" {
			break
		}
	}
	unique := map[string]bool{}
	for _, name := range seen {
		unique[name] = true
	}
	if len(seen) != 7 || len(unique) != 7 {
		t.Fatalf("a keyset walk must see each of 7 users once: %v", seen)
	}
	first, err := store.ListUsers(ctx, UserListQuery{Search: "walker", Limit: 3})
	if err != nil || first.NextPageToken == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if _, err = store.ListUsers(ctx, UserListQuery{Search: "other", Limit: 3, PageToken: first.NextPageToken}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("a token for other filters was accepted: %v", err)
	}
	if _, err = store.ListUsers(ctx, UserListQuery{PageToken: "!"}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("a malformed token was accepted: %v", err)
	}
}

func TestBoundedListTotal(t *testing.T) {
	if got := BoundedListTotal(ListTotalCap); got != (ListTotal{Value: ListTotalCap, Relation: TotalExact}) {
		t.Fatalf("at the cap: %+v", got)
	}
	if got := BoundedListTotal(ListTotalCap + 1); got != (ListTotal{Value: ListTotalCap + 1, Relation: TotalAtLeast}) {
		t.Fatalf("above the cap: %+v", got)
	}
	if got := boundedCountSQL("users WHERE true"); got != "SELECT count(*) FROM (SELECT 1 FROM users WHERE true LIMIT 10001) bounded" {
		t.Fatalf("bounded count SQL: %s", got)
	}
}
