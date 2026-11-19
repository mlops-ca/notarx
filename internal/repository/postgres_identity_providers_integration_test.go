//go:build integration

package repository

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/identity"
)

func openProviderStore(t *testing.T) *PostgresStore {
	t.Helper()
	connection := os.Getenv("TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	store, err := NewPostgresStore(connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestPostgresIdentityProviderLifecycle(t *testing.T) {
	store := openProviderStore(t)
	ctx := context.Background()
	suffix := strings.ToLower(uuid.NewString()[:12])
	provider := identity.Provider{
		ID: uuid.NewString(), Slug: "pg-" + suffix, Kind: identity.KindOIDC, DisplayName: "PG provider",
		Settings: []byte(`{"issuer":"https://idp-` + suffix + `.example.test","audience":"api"}`), SecretsSealed: "v1:sealed",
		AttributeMapping: identity.AttributeMapping{Subject: "tid:oid"}, ProvisioningMode: identity.ProvisioningJIT,
		EmailLinkingEnabled: true, JITDefaultRole: identity.JITRoleMember,
	}
	created, err := store.CreateIdentityProvider(ctx, provider)
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || created.SecretsSealed != "v1:sealed" || created.AttributeMapping.Subject != "tid:oid" || created.ProvisioningMode != identity.ProvisioningJIT {
		t.Fatalf("created=%+v", created)
	}
	if _, err := store.CreateIdentityProvider(ctx, identity.Provider{ID: uuid.NewString(), Slug: provider.Slug, Kind: identity.KindOIDC, DisplayName: "dup", ProvisioningMode: identity.ProvisioningDisabled, JITDefaultRole: identity.JITRoleNone}); !errors.Is(err, ErrIdentityProviderSlugExists) {
		t.Fatalf("duplicate slug err=%v", err)
	}
	for _, bad := range []identity.Provider{
		{ID: uuid.NewString(), Slug: "Bad_Slug", Kind: identity.KindOIDC, DisplayName: "x", ProvisioningMode: identity.ProvisioningDisabled, JITDefaultRole: identity.JITRoleNone},
		{ID: uuid.NewString(), Slug: "adm-" + suffix, Kind: identity.KindOIDC, DisplayName: "x", ProvisioningMode: identity.ProvisioningDisabled, JITDefaultRole: "admin"},
	} {
		if _, err := store.CreateIdentityProvider(ctx, bad); err == nil {
			t.Fatalf("the database must refuse %+v", bad)
		}
	}
	bySlug, err := store.GetIdentityProviderBySlug(ctx, provider.Slug)
	if err != nil || bySlug.ID != provider.ID {
		t.Fatalf("by slug=%+v err=%v", bySlug, err)
	}
	if _, err := store.GetIdentityProvider(ctx, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err=%v", err)
	}
	listed, err := store.ListIdentityProviders(ctx)
	found := false
	for _, item := range listed {
		found = found || item.ID == provider.ID
	}
	if err != nil || !found {
		t.Fatalf("list err=%v found=%v", err, found)
	}

	created.DisplayName, created.SecretsSealed = "renamed", ""
	replaced, err := store.ReplaceIdentityProvider(ctx, created, 1)
	if err != nil || replaced.Version != 2 || replaced.DisplayName != "renamed" || replaced.SecretsSealed != "" {
		t.Fatalf("replaced=%+v err=%v", replaced, err)
	}
	if _, err := store.ReplaceIdentityProvider(ctx, created, 1); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale replace err=%v", err)
	}
	ghost := created
	ghost.ID = uuid.NewString()
	if _, err := store.ReplaceIdentityProvider(ctx, ghost, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replace of a missing provider err=%v", err)
	}

	user, err := store.CreateUser(ctx, User{ID: uuid.NewString(), Name: "pg-provider-" + suffix, Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DeleteUser(ctx, user.ID) })
	linked, err := store.CreateUserIdentity(ctx, UserIdentity{ID: uuid.NewString(), UserID: user.ID, Kind: UserIdentityOIDC, ProviderID: provider.ID, Issuer: "https://idp-" + suffix + ".example.test", Subject: "subject-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUserIdentity(ctx, UserIdentity{ID: uuid.NewString(), UserID: user.ID, Kind: UserIdentityOIDC, ProviderID: provider.ID, Issuer: "https://idp-" + suffix + ".example.test", Subject: "subject-2"}); !errors.Is(err, ErrIdentityExists) {
		t.Fatalf("a second identity of one user at one provider err=%v", err)
	}
	loaded, loadedIdentity, err := store.GetUserByProviderIdentity(ctx, provider.ID, "subject-1")
	if err != nil || loaded.ID != user.ID || loadedIdentity.ID != linked.ID || loadedIdentity.ProviderID != provider.ID {
		t.Fatalf("loaded=%+v identity=%+v err=%v", loaded, loadedIdentity, err)
	}
	if _, _, err := store.GetUserByProviderIdentity(ctx, uuid.NewString(), "subject-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the same subject at another provider is another identity: %v", err)
	}
	if err := store.DeleteIdentityProvider(ctx, provider.ID, 2); !errors.Is(err, ErrIdentityProviderInUse) {
		t.Fatalf("delete with linked identities err=%v", err)
	}
	if err := store.DeleteUserIdentity(ctx, user.ID, linked.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteIdentityProvider(ctx, provider.ID, 1); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale delete err=%v", err)
	}
	if err := store.DeleteIdentityProvider(ctx, provider.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteIdentityProvider(ctx, provider.ID, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeat delete err=%v", err)
	}
}

// TestPostgresIdentityProviderSeedOnlyWhenEmpty needs an empty registry, so it runs in a
// transaction-free check: it seeds only when no provider exists and is otherwise a no-op.
func TestPostgresIdentityProviderSeedIsNoOpWhenProvidersExist(t *testing.T) {
	store := openProviderStore(t)
	ctx := context.Background()
	suffix := strings.ToLower(uuid.NewString()[:12])
	existing, err := store.CreateIdentityProvider(ctx, identity.Provider{ID: uuid.NewString(), Slug: "seed-" + suffix, Kind: identity.KindOIDC, DisplayName: "x", ProvisioningMode: identity.ProvisioningDisabled, JITDefaultRole: identity.JITRoleNone})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DeleteIdentityProvider(ctx, existing.ID, existing.Version) })
	seeded, err := store.SeedIdentityProvider(ctx, identity.Provider{ID: uuid.NewString(), Slug: "default-" + suffix, Kind: identity.KindOIDC, DisplayName: "seed", ProvisioningMode: identity.ProvisioningDisabled, JITDefaultRole: identity.JITRoleNone})
	if err != nil || seeded {
		t.Fatalf("seeded=%v err=%v", seeded, err)
	}
	if _, err := store.GetIdentityProviderBySlug(ctx, "default-"+suffix); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the seed must not insert while providers exist: %v", err)
	}
}
