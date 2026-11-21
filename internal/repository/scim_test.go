package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/ids"
)

func newMemorySCIMProvider(store *MemoryStore) func(t *testing.T) string {
	return func(t *testing.T) string {
		t.Helper()
		id := ids.New()
		if _, err := store.CreateIdentityProvider(context.Background(), identity.Provider{
			ID: id, Slug: "p" + id[28:], Kind: identity.KindOIDC, DisplayName: "p", ProvisioningMode: identity.ProvisioningSCIMOnly, JITDefaultRole: identity.JITRoleNone,
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
}

func TestMemorySCIMContract(t *testing.T) {
	store := NewMemoryStore()
	scimContract(t, store, newMemorySCIMProvider(store))
}

func TestMemorySCIMWritesAreAuditedAtomically(t *testing.T) {
	store := NewMemoryStore()
	provider := newMemorySCIMProvider(store)(t)
	ctx := context.Background()
	failing := errors.New("audit store down")
	store.SetAuditFault(func(AuditRecord) error { return failing })
	audit := ManagementAudit("scim:test", "scim.user.create", "/Users", 201)
	if _, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: "ada", Active: true}, audit); !errors.Is(err, failing) {
		t.Fatalf("create with a failing audit: %v", err)
	}
	if _, err := store.GetUserByName(ctx, "ada"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the create took effect without its audit: %v", err)
	}
	store.SetAuditFault(nil)
	user, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: "ada", Active: true}, audit)
	if err != nil {
		t.Fatal(err)
	}
	store.SetAuditFault(func(AuditRecord) error { return failing })
	inactive := false
	if _, err := store.UpdateSCIMUser(ctx, provider, user.User.ID, SCIMUserChange{Active: &inactive}, audit); !errors.Is(err, failing) {
		t.Fatalf("deactivate with a failing audit: %v", err)
	}
	if now, _ := store.GetUser(ctx, user.User.ID); now.State != UserActive {
		t.Fatal("the deactivation took effect without its audit")
	}
	if err := store.DeleteSCIMUser(ctx, provider, user.User.ID, nil, audit); !errors.Is(err, failing) {
		t.Fatalf("delete with a failing audit: %v", err)
	}
	token := SCIMToken{ProviderID: provider, Name: "t", SecretHash: "h", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := store.CreateSCIMToken(ctx, token, audit); !errors.Is(err, failing) {
		t.Fatalf("token create with a failing audit: %v", err)
	}
	if _, err := store.FindActiveSCIMTokenByHash(ctx, "h"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the token exists without its audit: %v", err)
	}
}

// A listing of more than ListTotalCap users pages by keyset: every user is visited once, the total is a lower bound
// above the cap, and no page is read with an offset (the PostgreSQL statement has none, see sqlcast and sqllint).
func TestMemorySCIMListsMoreThanTenThousandUsersByKeyset(t *testing.T) {
	store := NewMemoryStore()
	provider := newMemorySCIMProvider(store)(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const users = ListTotalCap + 25
	store.mu.Lock()
	for index := 0; index < users; index++ {
		userID, at := ids.New(), base.Add(time.Duration(index)*time.Millisecond)
		store.users[userID] = User{ID: userID, Name: "bulk-" + userID, Role: "none", State: UserActive, CreatedAt: at}
		identityID := ids.New()
		store.userIdentities[identityID] = UserIdentity{ID: identityID, UserID: userID, Kind: UserIdentityOIDC, ProviderID: provider, SCIMExternalID: "x-" + userID, CreatedAt: at}
	}
	store.mu.Unlock()
	seen := make(map[string]bool, users)
	cursor, pages := "", 0
	for {
		page, err := store.ListSCIMUsers(context.Background(), SCIMUserQuery{ProviderID: provider, Limit: SCIMMaxPageSize, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if page.Total.Relation != TotalAtLeast || page.Total.Value != ListTotalCap+1 {
			t.Fatalf("total = %+v, want a lower bound of %d", page.Total, ListTotalCap+1)
		}
		for _, item := range page.Items {
			if seen[item.User.ID] {
				t.Fatalf("user %s served twice", item.User.ID)
			}
			seen[item.User.ID] = true
		}
		pages++
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	if len(seen) != users || pages != 11 {
		t.Fatalf("visited %d users in %d pages", len(seen), pages)
	}
}

func TestMemorySCIMExpiredTokenDoesNotAuthenticate(t *testing.T) {
	store := NewMemoryStore()
	provider := newMemorySCIMProvider(store)(t)
	token, err := store.CreateSCIMToken(context.Background(), SCIMToken{ProviderID: provider, Name: "t", SecretHash: "expiring", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindActiveSCIMTokenByHash(context.Background(), "expiring"); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	token.ExpiresAt = time.Now().Add(-time.Second)
	store.scimTokens[token.ID] = token
	store.mu.Unlock()
	if _, err := store.FindActiveSCIMTokenByHash(context.Background(), "expiring"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired token authenticated: %v", err)
	}
}

func TestSCIMTokenActive(t *testing.T) {
	now := time.Now()
	revoked := now
	for name, test := range map[string]struct {
		token SCIMToken
		want  bool
	}{
		"fresh":                 {SCIMToken{ExpiresAt: now.Add(time.Hour)}, true},
		"expired":               {SCIMToken{ExpiresAt: now.Add(-time.Second)}, false},
		"at the expiry instant": {SCIMToken{ExpiresAt: now}, false},
		"revoked":               {SCIMToken{ExpiresAt: now.Add(time.Hour), RevokedAt: &revoked}, false},
	} {
		if got := test.token.Active(now); got != test.want {
			t.Errorf("%s: Active = %v, want %v", name, got, test.want)
		}
	}
}

func TestSCIMUserExternalIDHidesTheGeneratedKey(t *testing.T) {
	generated := SCIMUser{Identity: UserIdentity{SCIMExternalID: SCIMGeneratedExternalIDPrefix + "abc"}}
	set := SCIMUser{Identity: UserIdentity{SCIMExternalID: "from-the-idp"}}
	if generated.ExternalID() != "" || set.ExternalID() != "from-the-idp" {
		t.Fatalf("external ids = %q, %q", generated.ExternalID(), set.ExternalID())
	}
}

func TestMemorySCIMCreateAuditNamesTheAccountAndWhetherItWasTakenOver(t *testing.T) {
	store := NewMemoryStore()
	provider := newMemorySCIMProvider(store)(t)
	ctx := context.Background()
	audit := ManagementAudit("scim:test", "scim.user.create", "scim/Users", 201)
	created, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: "fresh", Active: true}, audit)
	if err != nil {
		t.Fatal(err)
	}
	linked, err := store.CreateUser(ctx, User{ID: ids.New(), Name: "linked", Role: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LinkProviderIdentity(ctx, ProviderIdentityLink{ProviderID: provider, UserID: linked.ID, Subject: "s", Issuer: "https://idp.example.test", Kind: UserIdentityOIDC}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: "linked", Active: true}, audit); err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateSCIMGroup(ctx, SCIMGroupCreate{ProviderID: provider, DisplayName: "g"}, ManagementAudit("scim:test", "scim.group.create", "scim/Groups", 201))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, record := range store.Audits {
		seen[record.Resource] = record.Evidence["result"]
	}
	if seen["scim/Users/"+created.User.ID] != "created" || seen["scim/Users/"+linked.ID] != "taken_over" {
		t.Fatalf("audit = %v", seen)
	}
	if _, ok := seen["scim/Groups/"+group.ID]; !ok {
		t.Fatalf("the group create was not audited with its id: %v", seen)
	}
}
