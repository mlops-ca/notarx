package repository

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

// scimContractStore is what the SCIM contract exercises: the memory store and the PostgreSQL store both run it.
type scimContractStore interface {
	SCIMStore
	SCIMTokenStore
	UserIdentityLinker
	UserStore
	UserSessionStore
	UserGroupStore
	UserIdentityStore
}

// scimContract runs the behaviour both stores must share. newProvider creates an identity provider and returns its id.
func scimContract(t *testing.T, store scimContractStore, newProvider func(t *testing.T) string) {
	t.Helper()
	ctx := context.Background()
	provider, other := newProvider(t), newProvider(t)
	suffix := ids.New()[28:]
	name := func(base string) string { return base + "-" + suffix }

	t.Run("tokens enforce the expiry cap and scope", func(t *testing.T) {
		now := time.Now().UTC()
		valid := SCIMToken{ProviderID: provider, Name: " Entra ", SecretHash: "hash-" + suffix, ExpiresAt: now.Add(30 * 24 * time.Hour)}
		for label, bad := range map[string]SCIMToken{
			"no expiry":     {ProviderID: provider, Name: "x", SecretHash: "h1" + suffix},
			"past expiry":   {ProviderID: provider, Name: "x", SecretHash: "h2" + suffix, ExpiresAt: now.Add(-time.Minute)},
			"over 365 days": {ProviderID: provider, Name: "x", SecretHash: "h3" + suffix, ExpiresAt: now.Add(MaxSCIMTokenLifetime + time.Hour)},
			"empty name":    {ProviderID: provider, Name: " ", SecretHash: "h4" + suffix, ExpiresAt: now.Add(time.Hour)},
			"no hash":       {ProviderID: provider, Name: "x", ExpiresAt: now.Add(time.Hour)},
		} {
			if _, err := store.CreateSCIMToken(ctx, bad); !errors.Is(err, ErrSCIMTokenInvalid) {
				t.Errorf("%s: err = %v, want ErrSCIMTokenInvalid", label, err)
			}
		}
		if _, err := store.CreateSCIMToken(ctx, SCIMToken{ProviderID: ids.New(), Name: "x", SecretHash: "h5" + suffix, ExpiresAt: now.Add(time.Hour)}); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown provider: err = %v, want ErrNotFound", err)
		}
		edge, err := store.CreateSCIMToken(ctx, SCIMToken{ProviderID: provider, Name: "edge", SecretHash: "edge-" + suffix, ExpiresAt: now.Add(MaxSCIMTokenLifetime - time.Minute)})
		if err != nil || edge.Name != "edge" {
			t.Fatalf("a token at the cap: %+v, %v", edge, err)
		}
		created, err := store.CreateSCIMToken(ctx, valid)
		if err != nil || created.Name != "Entra" || created.ID == "" || created.RevokedAt != nil {
			t.Fatalf("create = %+v, %v", created, err)
		}
		if _, err := store.CreateSCIMToken(ctx, SCIMToken{ProviderID: other, Name: "dup", SecretHash: valid.SecretHash, ExpiresAt: now.Add(time.Hour)}); !errors.Is(err, ErrSCIMTokenInvalid) {
			t.Errorf("a duplicate hash: err = %v", err)
		}
		found, err := store.FindActiveSCIMTokenByHash(ctx, valid.SecretHash)
		if err != nil || found.ID != created.ID || found.ProviderID != provider {
			t.Fatalf("find = %+v, %v", found, err)
		}
		if _, err := store.FindActiveSCIMTokenByHash(ctx, "unknown"); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown hash: %v", err)
		}
		if _, err := store.RevokeSCIMToken(ctx, other, created.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("revoke through another provider: %v", err)
		}
		revoked, err := store.RevokeSCIMToken(ctx, provider, created.ID)
		if err != nil || revoked.RevokedAt == nil {
			t.Fatalf("revoke = %+v, %v", revoked, err)
		}
		again, err := store.RevokeSCIMToken(ctx, provider, created.ID)
		if err != nil || again.RevokedAt == nil || !again.RevokedAt.Equal(*revoked.RevokedAt) {
			t.Errorf("a second revoke must keep the first time: %+v, %v", again, err)
		}
		if _, err := store.FindActiveSCIMTokenByHash(ctx, valid.SecretHash); !errors.Is(err, ErrNotFound) {
			t.Errorf("a revoked token authenticated: %v", err)
		}
		listed, err := store.ListSCIMTokens(ctx, provider)
		if err != nil || len(listed) < 2 || !slices.ContainsFunc(listed, func(token SCIMToken) bool { return token.ID == created.ID && token.RevokedAt != nil }) {
			t.Errorf("list = %+v, %v", listed, err)
		}
		if listed, _ := store.ListSCIMTokens(ctx, other); len(listed) != 0 {
			t.Errorf("another provider lists %d tokens", len(listed))
		}
	})

	t.Run("users: create, conflicts and scope", func(t *testing.T) {
		created, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: name("Alice@Example.com"), DisplayName: "Alice", Email: "alice@example.com", ExternalID: name("ext-alice"), Active: true})
		if err != nil {
			t.Fatal(err)
		}
		if created.User.Role != "none" || created.User.State != UserActive || created.User.SecretHash != "" || created.Identity.Subject != "" ||
			created.Identity.SCIMExternalID != name("ext-alice") || created.ExternalID() != name("ext-alice") || created.User.Version != "1" {
			t.Fatalf("created = %+v", created)
		}
		if _, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: name("alice@example.COM"), Active: true}); !errors.Is(err, ErrNameExists) {
			t.Errorf("userName differing in case: %v", err)
		}
		if _, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: other, UserName: name("alice@example.com"), Active: true}); !errors.Is(err, ErrNameExists) {
			t.Errorf("userName of another provider's user: %v", err)
		}
		if _, err := store.CreateUser(ctx, User{ID: ids.New(), Name: name("local-admin"), Role: "member", SecretHash: "bcrypt"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: name("LOCAL-ADMIN"), Active: true}); !errors.Is(err, ErrNameExists) {
			t.Errorf("userName of a local account: %v", err)
		}
		if _, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: name("other"), ExternalID: name("ext-alice"), Active: true}); !errors.Is(err, ErrIdentityExists) {
			t.Errorf("a duplicate externalId: %v", err)
		}
		if _, err := store.GetUserByName(ctx, name("other")); !errors.Is(err, ErrNotFound) {
			t.Errorf("a refused create left an account behind: %v", err)
		}
		if shared, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: other, UserName: name("bob"), ExternalID: name("ext-alice"), Active: true}); err != nil || shared.User.Name != name("bob") {
			t.Errorf("the same externalId at another provider: %+v, %v", shared, err)
		}
		bare, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: name("carol"), Active: false})
		if err != nil || bare.ExternalID() != "" || bare.Identity.SCIMExternalID == "" || bare.User.State != UserDisabled || bare.User.DisabledReason != DisabledBySCIM {
			t.Fatalf("an inactive user without externalId = %+v, %v", bare, err)
		}
		if _, err := store.GetSCIMUser(ctx, other, created.User.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("a user visible to another provider: %v", err)
		}
		if _, err := store.GetSCIMUser(ctx, provider, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
			t.Errorf("a malformed id: %v", err)
		}
		if _, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: ids.New(), UserName: name("ghost"), Active: true}); !errors.Is(err, ErrNotFound) {
			t.Errorf("an unknown provider: %v", err)
		}
		page, err := store.ListSCIMUsers(ctx, SCIMUserQuery{ProviderID: provider, UserName: name("ALICE@example.com")})
		if err != nil || len(page.Items) != 1 || page.Items[0].User.ID != created.User.ID || page.Total.Value != 1 || page.Total.Relation != TotalExact {
			t.Errorf("userName filter = %+v, %v", page, err)
		}
		page, err = store.ListSCIMUsers(ctx, SCIMUserQuery{ProviderID: provider, ExternalID: name("ext-alice")})
		if err != nil || len(page.Items) != 1 || page.Items[0].User.ID != created.User.ID {
			t.Errorf("externalId filter = %+v, %v", page, err)
		}
		if page, _ = store.ListSCIMUsers(ctx, SCIMUserQuery{ProviderID: provider, UserName: "nobody-" + suffix}); len(page.Items) != 0 || page.Total.Value != 0 {
			t.Errorf("a filter with no match = %+v", page)
		}
	})

	t.Run("users: update, deactivate, reactivate and delete", func(t *testing.T) {
		user, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: name("dave"), DisplayName: "Dave", Email: "dave@example.com", ExternalID: name("ext-dave"), Active: true})
		if err != nil {
			t.Fatal(err)
		}
		session, err := store.CreateUserSession(ctx, UserSession{ID: ids.New(), UserID: user.User.ID, Kind: UserSessionOIDC, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		verified := true
		if _, err = store.UpdateUser(ctx, UserUpdate{ID: user.User.ID, EmailVerifiedByAdmin: &verified, EmailVerifiedBy: "admin"}, user.User.Version); err != nil {
			t.Fatal(err)
		}
		renamed := name("david")
		address, display, external := "david@example.org", "David", name("ext-david")
		updated, err := store.UpdateSCIMUser(ctx, provider, user.User.ID, SCIMUserChange{UserName: &renamed, DisplayName: &display, Email: &address, ExternalID: &external})
		if err != nil || updated.User.Name != renamed || updated.User.DisplayName != "David" || updated.User.Email != address || updated.ExternalID() != external || updated.User.State != UserActive {
			t.Fatalf("update = %+v, %v", updated, err)
		}
		if updated.User.EmailVerifiedByAdmin {
			t.Error("changing the email through SCIM kept the administrator verification")
		}
		if _, err := store.UpdateSCIMUser(ctx, provider, user.User.ID, SCIMUserChange{UserName: ptr(name("alice@example.com"))}); !errors.Is(err, ErrNameExists) {
			t.Errorf("rename onto another account: %v", err)
		}
		if _, err := store.UpdateSCIMUser(ctx, provider, user.User.ID, SCIMUserChange{ExternalID: ptr(name("ext-alice"))}); !errors.Is(err, ErrIdentityExists) {
			t.Errorf("externalId of another account: %v", err)
		}
		if _, err := store.UpdateSCIMUser(ctx, provider, user.User.ID, SCIMUserChange{ExternalID: ptr("")}); !errors.Is(err, ErrSCIMInvalid) {
			t.Errorf("clearing the only identity key: %v", err)
		}
		if _, err := store.UpdateSCIMUser(ctx, other, user.User.ID, SCIMUserChange{DisplayName: ptr("x")}); !errors.Is(err, ErrNotFound) {
			t.Errorf("update through another provider: %v", err)
		}
		inactive := false
		off, err := store.UpdateSCIMUser(ctx, provider, user.User.ID, SCIMUserChange{Active: &inactive})
		if err != nil || off.User.State != UserDisabled || off.User.DisabledReason != DisabledBySCIM || off.User.SessionVersion != updated.User.SessionVersion+1 {
			t.Fatalf("deactivate = %+v, %v", off, err)
		}
		sessions, err := store.ListUserSessions(ctx, user.User.ID, true)
		if err != nil || len(sessions) != 1 || sessions[0].ID != session.ID || sessions[0].RevokedAt == nil {
			t.Errorf("sessions after deactivation = %+v, %v", sessions, err)
		}
		active := true
		on, err := store.UpdateSCIMUser(ctx, provider, user.User.ID, SCIMUserChange{Active: &active})
		if err != nil || on.User.State != UserActive || on.User.DisabledReason != "" {
			t.Fatalf("reactivate = %+v, %v", on, err)
		}

		manual, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: name("erin"), Active: true})
		if err != nil {
			t.Fatal(err)
		}
		disabled := UserDisabled
		if _, err := store.UpdateUser(ctx, UserUpdate{ID: manual.User.ID, State: &disabled}, manual.User.Version); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpdateSCIMUser(ctx, provider, manual.User.ID, SCIMUserChange{Active: &active}); !errors.Is(err, ErrSCIMAdminDisabled) {
			t.Errorf("re-activating an administrator-disabled account: %v", err)
		}
		if still, _ := store.GetUser(ctx, manual.User.ID); still.State != UserDisabled || still.DisabledReason != DisabledByAdmin {
			t.Errorf("the refused re-activation changed the account: %+v", still)
		}

		// A stale If-Match version is refused under the row lock, and the right one goes through.
		current, err := store.GetSCIMUser(ctx, provider, user.User.ID)
		if err != nil {
			t.Fatal(err)
		}
		stale := []string{"0", current.User.Version + "9"}
		if _, err := store.UpdateSCIMUser(ctx, provider, user.User.ID, SCIMUserChange{DisplayName: ptr("stale"), IfVersions: stale}); !errors.Is(err, ErrVersionConflict) {
			t.Errorf("an update with a stale version: %v", err)
		}
		if err := store.DeleteSCIMUser(ctx, provider, user.User.ID, stale); !errors.Is(err, ErrVersionConflict) {
			t.Errorf("a delete with a stale version: %v", err)
		}
		latest, _ := store.GetSCIMUser(ctx, provider, user.User.ID)
		if matched, err := store.UpdateSCIMUser(ctx, provider, user.User.ID, SCIMUserChange{DisplayName: ptr("David F"), IfVersions: []string{"0", latest.User.Version}}); err != nil || matched.User.DisplayName != "David F" {
			t.Errorf("an update with the current version = %+v, %v", matched, err)
		}

		memberOf, err := store.CreateSCIMGroup(ctx, SCIMGroupCreate{ProviderID: provider, DisplayName: "Leavers " + suffix, MemberIDs: []string{user.User.ID}})
		if err != nil {
			t.Fatal(err)
		}
		if memberships, _ := store.ListUserGroupMemberships(ctx, user.User.ID); len(memberships) != 1 || memberships[0].GroupID != memberOf.ID {
			t.Fatalf("memberships before the delete = %+v", memberships)
		}

		// Delete hides the account from SCIM and disables it; the account stays.
		if err := store.DeleteSCIMUser(ctx, provider, user.User.ID, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetSCIMUser(ctx, provider, user.User.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("the deleted link is still visible: %v", err)
		}
		kept, err := store.GetUser(ctx, user.User.ID)
		if err != nil || kept.State != UserDisabled || kept.DisabledReason != DisabledBySCIM {
			t.Errorf("the account after DELETE = %+v, %v", kept, err)
		}
		if err := store.DeleteSCIMUser(ctx, provider, user.User.ID, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("a second delete: %v", err)
		}
		if memberships, _ := store.ListUserGroupMemberships(ctx, user.User.ID); len(memberships) != 0 {
			t.Errorf("a deleted account kept %d group memberships", len(memberships))
		}
		active = true
		if _, err := store.UpdateSCIMUser(ctx, provider, user.User.ID, SCIMUserChange{Active: &active}); !errors.Is(err, ErrNotFound) {
			t.Errorf("a PATCH active=true brought a deleted account back: %v", err)
		}
		if page, _ := store.ListSCIMUsers(ctx, SCIMUserQuery{ProviderID: provider, UserName: renamed}); len(page.Items) != 0 {
			t.Errorf("a deleted account is still listed: %+v", page.Items)
		}
		if page, _ := store.ListSCIMUsers(ctx, SCIMUserQuery{ProviderID: provider, ExternalID: external}); len(page.Items) != 0 {
			t.Errorf("a deleted account is still found by its old externalId: %+v", page.Items)
		}
		if _, err := store.CreateSCIMGroup(ctx, SCIMGroupCreate{ProviderID: provider, DisplayName: "Ghosts", MemberIDs: []string{user.User.ID}}); !errors.Is(err, ErrSCIMInvalid) {
			t.Errorf("a deleted account joined a group: %v", err)
		}

		// Only the same provider can bring the account back, by creating the user again.
		if _, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: other, UserName: renamed, Active: true}); !errors.Is(err, ErrNameExists) {
			t.Errorf("another provider took over a deleted account: %v", err)
		}
		revived, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: strings.ToUpper(renamed), DisplayName: "Back", Email: "back@example.com", ExternalID: name("ext-back"), Active: true})
		if err != nil || revived.User.ID != user.User.ID || revived.User.State != UserActive || revived.User.DisabledReason != "" || revived.User.DisplayName != "Back" ||
			revived.ExternalID() != name("ext-back") || revived.User.Name != renamed {
			t.Fatalf("revive = %+v, %v", revived, err)
		}
		if again, err := store.GetSCIMUser(ctx, provider, user.User.ID); err != nil || again.User.Version == current.User.Version {
			t.Errorf("the revived account = %+v, %v", again, err)
		}
		if err := store.DeleteSCIMUser(ctx, provider, user.User.ID, nil); err != nil {
			t.Fatal(err)
		}
		// An account an administrator disabled in the meantime, or one that gained a password or a level, is not brought back.
		if _, err := store.UpdateUser(ctx, UserUpdate{ID: user.User.ID, Role: ptr("member")}, mustVersion(t, store, user.User.ID)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: renamed, Active: true}); !errors.Is(err, ErrNameExists) {
			t.Errorf("an account that gained a level was brought back by SCIM: %v", err)
		}
		if _, err := store.UpdateUser(ctx, UserUpdate{ID: user.User.ID, Role: ptr("none")}, mustVersion(t, store, user.User.ID)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpdateUser(ctx, UserUpdate{ID: user.User.ID, State: ptr(UserActive)}, mustVersion(t, store, user.User.ID)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpdateUser(ctx, UserUpdate{ID: user.User.ID, State: ptr(UserDisabled)}, mustVersion(t, store, user.User.ID)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: renamed, Active: true}); !errors.Is(err, ErrNameExists) {
			t.Errorf("an account an administrator disabled was re-activated through DELETE and POST: %v", err)
		}
	})

	t.Run("a platform administrator is untouchable", func(t *testing.T) {
		admin, err := store.CreateUser(ctx, User{ID: ids.New(), Name: name("root"), Role: "admin", SecretHash: "bcrypt"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.LinkProviderIdentity(ctx, ProviderIdentityLink{ProviderID: provider, UserID: admin.ID, Subject: "root-" + suffix, Issuer: "https://idp.example.test", Kind: UserIdentityOIDC}); err != nil {
			t.Fatal(err)
		}
		inactive := false
		if _, err := store.UpdateSCIMUser(ctx, provider, admin.ID, SCIMUserChange{Active: &inactive}); !errors.Is(err, ErrSCIMAdminProtected) {
			t.Errorf("deactivating an administrator: %v", err)
		}
		if _, err := store.UpdateSCIMUser(ctx, provider, admin.ID, SCIMUserChange{DisplayName: ptr("pwned")}); !errors.Is(err, ErrSCIMAdminProtected) {
			t.Errorf("editing an administrator: %v", err)
		}
		if err := store.DeleteSCIMUser(ctx, provider, admin.ID, nil); !errors.Is(err, ErrSCIMAdminProtected) {
			t.Errorf("deleting an administrator: %v", err)
		}
		if still, _ := store.GetUser(ctx, admin.ID); still.State != UserActive || still.DisplayName != "" || still.SessionVersion != admin.SessionVersion {
			t.Errorf("the administrator changed: %+v", still)
		}
	})

	t.Run("users: keyset paging", func(t *testing.T) {
		pager := newProvider(t)
		for index := 0; index < 25; index++ {
			if _, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: pager, UserName: name("page-" + strconv.Itoa(index)), Active: true}); err != nil {
				t.Fatal(err)
			}
		}
		var seen []string
		cursor := ""
		for pages := 0; pages < 10; pages++ {
			page, err := store.ListSCIMUsers(ctx, SCIMUserQuery{ProviderID: pager, Limit: 10, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			if page.Total.Value != 25 || page.Total.Relation != TotalExact {
				t.Errorf("total = %+v", page.Total)
			}
			for _, item := range page.Items {
				seen = append(seen, item.User.ID)
			}
			if cursor = page.NextCursor; cursor == "" {
				break
			}
			if len(page.Items) != 10 {
				t.Errorf("a page with a continuation holds %d items", len(page.Items))
			}
		}
		if len(seen) != 25 || len(slices.Compact(slices.Sorted(slices.Values(seen)))) != 25 {
			t.Fatalf("paging visited %d users (%d distinct)", len(seen), len(slices.Compact(slices.Sorted(slices.Values(seen)))))
		}
		page, _ := store.ListSCIMUsers(ctx, SCIMUserQuery{ProviderID: pager, Limit: 10})
		if skipped, err := store.ListSCIMUsers(ctx, SCIMUserQuery{ProviderID: pager, Limit: 10, SkipTotal: true}); err != nil || skipped.Total.Value != 0 || len(skipped.Items) != 10 || skipped.NextCursor != page.NextCursor {
			t.Errorf("a page without its total = %+v, %v", skipped, err)
		}
		if _, err := store.ListSCIMUsers(ctx, SCIMUserQuery{ProviderID: pager, UserName: "x", Limit: 10, Cursor: page.NextCursor}); !errors.Is(err, ErrInvalidPageToken) {
			t.Errorf("a cursor of another filter: %v", err)
		}
		if _, err := store.ListSCIMUsers(ctx, SCIMUserQuery{ProviderID: pager, Cursor: "garbage"}); !errors.Is(err, ErrInvalidPageToken) {
			t.Errorf("a malformed cursor: %v", err)
		}
	})

	t.Run("groups: scope, membership authority and the admin flag", func(t *testing.T) {
		member, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: name("grace"), Active: true})
		if err != nil {
			t.Fatal(err)
		}
		stranger, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: other, UserName: name("stranger"), Active: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateSCIMGroup(ctx, SCIMGroupCreate{ProviderID: provider, DisplayName: "Bad", MemberIDs: []string{stranger.User.ID}}); !errors.Is(err, ErrSCIMInvalid) {
			t.Errorf("a member of another provider: %v", err)
		}
		if _, err := store.CreateSCIMGroup(ctx, SCIMGroupCreate{ProviderID: provider, DisplayName: " "}); !errors.Is(err, ErrSCIMInvalid) {
			t.Errorf("an empty name: %v", err)
		}
		group, err := store.CreateSCIMGroup(ctx, SCIMGroupCreate{ProviderID: provider, DisplayName: "Engineers", ExternalID: name("g-eng"), MemberIDs: []string{member.User.ID}})
		if err != nil {
			t.Fatal(err)
		}
		if group.Source != UserGroupSourceSCIM || group.GlobalRole != UserGroupRoleNone || group.ProviderID != provider || group.Version != "1" {
			t.Fatalf("group = %+v", group)
		}
		if _, err := store.CreateSCIMGroup(ctx, SCIMGroupCreate{ProviderID: provider, DisplayName: "Dup", ExternalID: name("g-eng")}); !errors.Is(err, ErrUserGroupExternalID) {
			t.Errorf("a duplicate externalId: %v", err)
		}
		if _, err := store.GetSCIMGroup(ctx, other, group.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("a group visible to another provider: %v", err)
		}
		if err := store.DeleteSCIMGroup(ctx, other, group.ID, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("a group deleted through another provider: %v", err)
		}
		local, err := store.CreateUserGroup(ctx, UserGroup{DisplayName: name("local-group"), Source: UserGroupSourceLocal})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetSCIMGroup(ctx, provider, local.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("SCIM reached a local group: %v", err)
		}
		if err := store.DeleteSCIMGroup(ctx, provider, local.ID, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("SCIM deleted a local group: %v", err)
		}
		if err := store.AddUserGroupMember(ctx, group.ID, stranger.User.ID, UserGroupSourceLocal); err != nil {
			t.Fatal(err) // an administrator's membership of the SCIM group
		}
		members, err := store.ListSCIMGroupMembers(ctx, provider, group.ID, 50)
		if err != nil || len(members) != 1 || members[0].UserID != member.User.ID {
			t.Fatalf("members = %+v, %v; an administrator's membership of another provider's account is not the provider's to see", members, err)
		}
		if all, _, err := store.ListUserGroupMembers(ctx, group.ID, 50, ""); err != nil || len(all) != 2 {
			t.Fatalf("the group itself has %+v, %v", all, err)
		}
		// Replace: only the SCIM-sourced memberships are authoritative; the administrator's stays.
		second, _ := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: name("heidi"), Active: true})
		replacement := []string{second.User.ID}
		renamed, err := store.UpdateSCIMGroup(ctx, provider, group.ID, SCIMGroupChange{DisplayName: ptr("Platform engineers"), ReplaceMembers: &replacement})
		if err != nil || renamed.DisplayName != "Platform engineers" || renamed.Version != "2" || renamed.GlobalRole != UserGroupRoleNone {
			t.Fatalf("update = %+v, %v", renamed, err)
		}
		members, _ = store.ListSCIMGroupMembers(ctx, provider, group.ID, 50)
		var ids []string
		for _, entry := range members {
			ids = append(ids, entry.UserID)
		}
		slices.Sort(ids)
		if !slices.Equal(ids, []string{second.User.ID}) {
			t.Errorf("members after replace = %v, want only the SCIM-managed one", ids)
		}
		if all, _, _ := store.ListUserGroupMembers(ctx, group.ID, 50, ""); len(all) != 2 {
			t.Errorf("the administrator's local membership was removed by a SCIM replace: %+v", all)
		}
		if _, err := store.UpdateSCIMGroup(ctx, provider, group.ID, SCIMGroupChange{AddMembers: []string{member.User.ID, second.User.ID}, RemoveMembers: []string{second.User.ID}}); err != nil {
			t.Fatal(err)
		}
		if members, _ = store.ListSCIMGroupMembers(ctx, provider, group.ID, 50); len(members) != 1 || members[0].UserID != member.User.ID {
			t.Errorf("members after add and remove = %+v", members)
		}
		if _, err := store.UpdateSCIMGroup(ctx, provider, group.ID, SCIMGroupChange{AddMembers: []string{stranger.User.ID}}); !errors.Is(err, ErrSCIMInvalid) {
			t.Errorf("adding a user of another provider: %v", err)
		}

		// An administrator flags the SCIM group; SCIM changes never touch the role, and delete plus recreate clears it.
		current, err := store.GetUserGroup(ctx, group.ID)
		if err != nil {
			t.Fatal(err)
		}
		flagged, err := store.SetUserGroupGlobalRole(ctx, group.ID, UserGroupRoleAdmin, current.Version)
		if err != nil {
			t.Fatal(err)
		}
		afterRename, err := store.UpdateSCIMGroup(ctx, provider, group.ID, SCIMGroupChange{DisplayName: ptr("Platform again")})
		if err != nil || afterRename.GlobalRole != UserGroupRoleAdmin || afterRename.Version == flagged.Version {
			t.Fatalf("a SCIM update changed the administrator's flag: %+v, %v", afterRename, err)
		}
		if err := store.DeleteSCIMGroup(ctx, provider, group.ID, []string{"0"}); !errors.Is(err, ErrVersionConflict) {
			t.Errorf("a group delete with a stale version: %v", err)
		}
		if _, err := store.UpdateSCIMGroup(ctx, provider, group.ID, SCIMGroupChange{DisplayName: ptr("Stale"), IfVersions: []string{"0"}}); !errors.Is(err, ErrVersionConflict) {
			t.Errorf("a group update with a stale version: %v", err)
		}
		latestGroup, _ := store.GetSCIMGroup(ctx, provider, group.ID)
		if err := store.DeleteSCIMGroup(ctx, provider, group.ID, []string{latestGroup.Version}); err != nil {
			t.Fatal(err)
		}
		recreated, err := store.CreateSCIMGroup(ctx, SCIMGroupCreate{ProviderID: provider, DisplayName: "Platform again", ExternalID: name("g-eng")})
		if err != nil || recreated.GlobalRole != UserGroupRoleNone || recreated.ID == group.ID {
			t.Fatalf("recreated = %+v, %v", recreated, err)
		}
		if _, err := store.GetSCIMGroup(ctx, provider, group.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("the deleted group: %v", err)
		}

		page, err := store.ListSCIMGroups(ctx, SCIMGroupQuery{ProviderID: provider, ExternalID: name("g-eng")})
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != recreated.ID {
			t.Errorf("externalId filter = %+v, %v", page, err)
		}
		if page, _ = store.ListSCIMGroups(ctx, SCIMGroupQuery{ProviderID: provider, DisplayName: "Platform again"}); len(page.Items) != 1 {
			t.Errorf("displayName filter = %+v", page)
		}
		if page, _ = store.ListSCIMGroups(ctx, SCIMGroupQuery{ProviderID: other}); len(page.Items) != 0 {
			t.Errorf("another provider lists %d groups", len(page.Items))
		}
		if _, err := store.ListSCIMGroups(ctx, SCIMGroupQuery{ProviderID: provider, Cursor: "garbage"}); !errors.Is(err, ErrInvalidPageToken) {
			t.Errorf("a malformed cursor: %v", err)
		}

		// The members of a page of groups come back in one call, capped per group and confined to the provider.
		busy, err := store.CreateSCIMGroup(ctx, SCIMGroupCreate{ProviderID: provider, DisplayName: "Busy", MemberIDs: []string{member.User.ID, second.User.ID}})
		if err != nil {
			t.Fatal(err)
		}
		foreign, err := store.CreateSCIMGroup(ctx, SCIMGroupCreate{ProviderID: other, DisplayName: "Foreign", MemberIDs: []string{stranger.User.ID}})
		if err != nil {
			t.Fatal(err)
		}
		batch, err := store.ListSCIMGroupsMembers(ctx, provider, []string{recreated.ID, busy.ID, foreign.ID}, 1)
		if err != nil || len(batch) != 1 || len(batch[busy.ID]) != 1 || batch[busy.ID][0].UserName == "" {
			t.Errorf("batch members = %+v, %v", batch, err)
		}
		if all, _ := store.ListSCIMGroupsMembers(ctx, provider, []string{busy.ID}, 10); len(all[busy.ID]) != 2 {
			t.Errorf("batch members without a cap = %+v", all)
		}
		if limited, _ := store.ListSCIMGroupMembers(ctx, provider, busy.ID, 1); len(limited) != 1 {
			t.Errorf("a capped member list = %+v", limited)
		}
		if _, err := store.ListSCIMGroupMembers(ctx, other, busy.ID, 10); !errors.Is(err, ErrNotFound) {
			t.Errorf("another provider's member list: %v", err)
		}
	})

	t.Run("the first login binds a SCIM account by externalId only", func(t *testing.T) {
		user, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: name("ivan"), Email: "ivan@example.com", ExternalID: name("ext-ivan"), Active: true})
		if err != nil {
			t.Fatal(err)
		}
		attempt := OIDCIdentityProvision{ProviderID: provider, Issuer: "https://idp.example.test", Subject: "sub-ivan-" + suffix, PreferredUsername: name("ivan"), BindSCIM: true, OccurredAt: time.Now().UTC()}
		if _, _, _, err := store.ResolveOIDCIdentity(ctx, attempt); !errors.Is(err, ErrNotFound) {
			t.Errorf("without an asserted externalId, matching on userName alone: %v", err)
		}
		attempt.ExternalID = SCIMGeneratedExternalIDPrefix + user.User.ID
		if _, _, _, err := store.ResolveOIDCIdentity(ctx, attempt); !errors.Is(err, ErrNotFound) {
			t.Errorf("the generated key matched a login: %v", err)
		}
		attempt.ExternalID = name("ext-wrong")
		if _, _, _, err := store.ResolveOIDCIdentity(ctx, attempt); !errors.Is(err, ErrNotFound) {
			t.Errorf("a wrong externalId: %v", err)
		}
		attempt.ExternalID, attempt.BindSCIM = name("ext-ivan"), false
		if _, _, _, err := store.ResolveOIDCIdentity(ctx, attempt); !errors.Is(err, ErrNotFound) {
			t.Errorf("binding without scim_only: %v", err)
		}
		attempt.BindSCIM = true
		bound, identity, created, err := store.ResolveOIDCIdentity(ctx, attempt)
		if err != nil || created || bound.ID != user.User.ID || identity.Subject != attempt.Subject || identity.Issuer != attempt.Issuer {
			t.Fatalf("bind = %+v %+v created=%v, %v", bound, identity, created, err)
		}
		again, _, _, err := store.ResolveOIDCIdentity(ctx, attempt)
		if err != nil || again.ID != user.User.ID {
			t.Fatalf("a second login = %+v, %v", again, err)
		}
		attempt.Subject = "sub-other-" + suffix
		if _, _, _, err := store.ResolveOIDCIdentity(ctx, attempt); !errors.Is(err, ErrNotFound) {
			t.Errorf("another subject took over a bound account: %v", err)
		}
		listed, err := store.GetSCIMUser(ctx, provider, user.User.ID)
		if err != nil || listed.Identity.Subject != "sub-ivan-"+suffix || listed.ExternalID() != name("ext-ivan") {
			t.Errorf("the SCIM view after the bind = %+v, %v", listed, err)
		}
	})

	t.Run("the admin link flow binds or creates an identity", func(t *testing.T) {
		scimUser, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: name("judy"), Active: true})
		if err != nil {
			t.Fatal(err)
		}
		link := ProviderIdentityLink{ProviderID: provider, UserID: scimUser.User.ID, Subject: "sub-judy-" + suffix, Issuer: "https://idp.example.test", Kind: UserIdentityOIDC}
		bound, err := store.LinkProviderIdentity(ctx, link)
		if err != nil || bound.ID != scimUser.Identity.ID || bound.Subject != link.Subject || bound.SCIMExternalID != scimUser.Identity.SCIMExternalID {
			t.Fatalf("bind = %+v, %v", bound, err)
		}
		if again, err := store.LinkProviderIdentity(ctx, link); err != nil || again.ID != bound.ID {
			t.Errorf("an idempotent repeat = %+v, %v", again, err)
		}
		link.Subject = "sub-judy-2-" + suffix
		if _, err := store.LinkProviderIdentity(ctx, link); !errors.Is(err, ErrIdentityExists) {
			t.Errorf("an account bound to another subject: %v", err)
		}
		local, _ := store.CreateUser(ctx, User{ID: ids.New(), Name: name("mallory"), Role: "member"})
		taken := ProviderIdentityLink{ProviderID: provider, UserID: local.ID, Subject: "sub-judy-" + suffix, Issuer: "https://idp.example.test", Kind: UserIdentityOIDC}
		if _, err := store.LinkProviderIdentity(ctx, taken); !errors.Is(err, ErrIdentityExists) {
			t.Errorf("a subject already bound to another account: %v", err)
		}
		taken.Subject = "sub-mallory-" + suffix
		created, err := store.LinkProviderIdentity(ctx, taken)
		if err != nil || created.UserID != local.ID || created.SCIMExternalID != "" || created.Subject != taken.Subject {
			t.Errorf("a new row for an unprovisioned account = %+v, %v", created, err)
		}
		for label, bad := range map[string]ProviderIdentityLink{
			"empty subject": {ProviderID: provider, UserID: local.ID, Issuer: "i", Kind: UserIdentityOIDC},
			"empty issuer":  {ProviderID: provider, UserID: local.ID, Subject: "s", Kind: UserIdentityOIDC},
			"bad kind":      {ProviderID: provider, UserID: local.ID, Subject: "s", Issuer: "i", Kind: "ldap"},
		} {
			if _, err := store.LinkProviderIdentity(ctx, bad); !errors.Is(err, ErrSCIMInvalid) {
				t.Errorf("%s: %v", label, err)
			}
		}
		if _, err := store.LinkProviderIdentity(ctx, ProviderIdentityLink{ProviderID: provider, UserID: ids.New(), Subject: "s", Issuer: "i", Kind: UserIdentityOIDC}); !errors.Is(err, ErrNotFound) {
			t.Errorf("an unknown account: %v", err)
		}
	})
}
