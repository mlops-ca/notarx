package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mlops-ca/notarx/internal/identity"
)

func TestForeignKeyViolationIsRecognizedThroughWrapping(t *testing.T) {
	fk := fmt.Errorf("delete: %w", &pgconn.PgError{Code: "23503"})
	if !isForeignKeyViolation(fk) || isForeignKeyViolation(&pgconn.PgError{Code: "23505"}) || isForeignKeyViolation(errors.New("x")) || isForeignKeyViolation(nil) {
		t.Fatal("only SQLSTATE 23503 is a foreign key violation")
	}
}

func provider(slug string) identity.Provider {
	return identity.Provider{ID: "id-" + slug, Slug: slug, Kind: identity.KindOIDC, DisplayName: slug, ProvisioningMode: identity.ProvisioningDisabled, JITDefaultRole: identity.JITRoleNone}
}

func TestMemoryIdentityProviderRegistry(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	created, err := store.CreateIdentityProvider(ctx, provider("alpha"))
	if err != nil || created.Version != 1 {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if _, err := store.CreateIdentityProvider(ctx, provider("alpha")); !errors.Is(err, ErrIdentityProviderSlugExists) {
		t.Fatalf("duplicate slug err=%v", err)
	}
	second, err := store.CreateIdentityProvider(ctx, provider("beta"))
	if err != nil {
		t.Fatal(err)
	}
	renamed := created
	renamed.Slug = "beta"
	if _, err := store.ReplaceIdentityProvider(ctx, renamed, 1); !errors.Is(err, ErrIdentityProviderSlugExists) {
		t.Fatalf("slug collision on replace err=%v", err)
	}
	created.DisplayName = "Alpha"
	replaced, err := store.ReplaceIdentityProvider(ctx, created, 1)
	if err != nil || replaced.Version != 2 || replaced.DisplayName != "Alpha" || replaced.CreatedAt.IsZero() {
		t.Fatalf("replaced=%+v err=%v", replaced, err)
	}
	if _, err := store.ReplaceIdentityProvider(ctx, created, 1); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale replace err=%v", err)
	}
	other := created
	other.Kind = identity.KindSAML
	if _, err := store.ReplaceIdentityProvider(ctx, other, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the kind is immutable: %v", err)
	}
	listed, _ := store.ListIdentityProviders(ctx)
	if len(listed) != 2 || listed[0].Slug != "alpha" || listed[1].Slug != "beta" {
		t.Fatalf("listed=%+v", listed)
	}
	user, err := store.CreateUser(ctx, User{ID: "u1", Name: "u1", Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUserIdentity(ctx, UserIdentity{UserID: user.ID, Kind: UserIdentityOIDC, ProviderID: second.ID, Issuer: "https://i", Subject: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteIdentityProvider(ctx, second.ID, second.Version); !errors.Is(err, ErrIdentityProviderInUse) {
		t.Fatalf("delete of a linked provider err=%v", err)
	}
	if err := store.DeleteIdentityProvider(ctx, replaced.ID, 1); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale delete err=%v", err)
	}
	if err := store.DeleteIdentityProvider(ctx, replaced.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteIdentityProvider(ctx, replaced.ID, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeat delete err=%v", err)
	}
	if seeded, err := store.SeedIdentityProvider(ctx, provider("seed")); err != nil || seeded {
		t.Fatalf("a non-empty registry must not be seeded: %v %v", seeded, err)
	}
	if _, err := store.GetIdentityProviderBySlug(ctx, "alpha"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted provider still found: %v", err)
	}
}
