package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

func testSAMLStateContract(t *testing.T, store SAMLStateStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	provider, other := ids.New(), ids.New()

	t.Run("request state is single use", func(t *testing.T) {
		id, err := store.CreateSAMLRequestState(ctx, provider, now.Add(10*time.Minute))
		if err != nil || id == "" {
			t.Fatalf("create = %q, %v", id, err)
		}
		if ok, err := store.ConsumeSAMLRequestState(ctx, other, id, now); err != nil || ok {
			t.Fatalf("consume for another provider = %v, %v", ok, err)
		}
		if ok, err := store.ConsumeSAMLRequestState(ctx, provider, id, now); err != nil || !ok {
			t.Fatalf("first consume = %v, %v", ok, err)
		}
		if ok, err := store.ConsumeSAMLRequestState(ctx, provider, id, now); err != nil || ok {
			t.Fatalf("second consume = %v, %v", ok, err)
		}
	})
	t.Run("expired and unknown state is refused", func(t *testing.T) {
		id, err := store.CreateSAMLRequestState(ctx, provider, now.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := store.ConsumeSAMLRequestState(ctx, provider, id, now.Add(time.Minute)); err != nil || ok {
			t.Fatalf("consume at expiry = %v, %v", ok, err)
		}
		for _, bad := range []string{"", "not-a-uuid", ids.New()} {
			if ok, err := store.ConsumeSAMLRequestState(ctx, provider, bad, now); err != nil || ok {
				t.Fatalf("consume %q = %v, %v", bad, ok, err)
			}
		}
		if ok, err := store.ConsumeSAMLRequestState(ctx, "x", id, now); err != nil || ok {
			t.Fatalf("consume with a bad provider = %v, %v", ok, err)
		}
	})
	t.Run("assertion id replay is refused", func(t *testing.T) {
		id := "_" + ids.New()
		if ok, err := store.RecordSAMLAssertionID(ctx, provider, id, now.Add(time.Hour)); err != nil || !ok {
			t.Fatalf("first record = %v, %v", ok, err)
		}
		if ok, err := store.RecordSAMLAssertionID(ctx, provider, id, now.Add(time.Hour)); err != nil || ok {
			t.Fatalf("duplicate record = %v, %v", ok, err)
		}
		if ok, err := store.RecordSAMLAssertionID(ctx, other, id, now.Add(time.Hour)); err != nil || !ok {
			t.Fatalf("same id for another provider = %v, %v", ok, err)
		}
	})
	t.Run("invalid arguments", func(t *testing.T) {
		if _, err := store.CreateSAMLRequestState(ctx, "x", now); !errors.Is(err, ErrSAMLInvalid) {
			t.Fatalf("create with bad provider = %v", err)
		}
		for _, id := range []string{"", strings.Repeat("a", MaxSAMLAssertionIDLength+1)} {
			if _, err := store.RecordSAMLAssertionID(ctx, provider, id, now); !errors.Is(err, ErrSAMLInvalid) {
				t.Fatalf("record %d bytes = %v", len(id), err)
			}
		}
		if _, err := store.RecordSAMLAssertionID(ctx, "x", "a", now); !errors.Is(err, ErrSAMLInvalid) {
			t.Fatalf("record with bad provider = %v", err)
		}
	})
}

func TestMemorySAMLStateContract(t *testing.T) { testSAMLStateContract(t, NewMemoryStore()) }

// testSAMLIdentityContract covers the SAML kind of the identity registry on any store: a first sign-in
// creates a saml identity keyed by (provider, NameID), a SAML session is accepted, and an asserted
// email never matches an existing account because the provision carries no verified email.
func testSAMLIdentityContract(t *testing.T, store interface {
	UserStore
	UserIdentityStore
	UserSessionStore
}, providerID, suffix string) {
	t.Helper()
	ctx := context.Background()
	existing, err := store.CreateUser(ctx, User{ID: ids.New(), Name: "ada-" + suffix, Email: "ada-" + suffix + "@example.test", Role: "member", State: UserActive})
	if err != nil {
		t.Fatal(err)
	}
	provision := OIDCIdentityProvision{
		Kind: UserIdentitySAML, ProviderID: providerID, Issuer: "https://idp.example.test/m", Subject: "name-id-" + suffix,
		Email: existing.Email, DisplayName: "Ada", PreferredUsername: "saml-" + suffix, Provision: true, DefaultRole: "none",
	}
	user, link, created, err := store.ResolveOIDCIdentity(ctx, provision)
	if err != nil || !created || link.Kind != UserIdentitySAML || user.ID == existing.ID || user.Role != "none" {
		t.Fatalf("first sign-in = %+v %+v created=%v err=%v", user, link, created, err)
	}
	again, _, created, err := store.ResolveOIDCIdentity(ctx, provision)
	if err != nil || created || again.ID != user.ID {
		t.Fatalf("second sign-in = %+v created=%v err=%v", again, created, err)
	}
	found, stored, err := store.GetUserByProviderIdentity(ctx, providerID, provision.Subject)
	if err != nil || found.ID != user.ID || stored.Kind != UserIdentitySAML {
		t.Fatalf("lookup = %+v %+v %v", found, stored, err)
	}
	provision.Subject, provision.Provision = "unknown-"+suffix, false
	if _, _, _, err := store.ResolveOIDCIdentity(ctx, provision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown subject without provisioning = %v", err)
	}
	now := time.Now().UTC()
	if _, err := store.CreateUserSession(ctx, UserSession{ID: ids.New(), UserID: user.ID, Kind: UserSessionSAML, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("a saml session = %v", err)
	}
}

func TestMemorySAMLIdentityContract(t *testing.T) {
	store := NewMemoryStore()
	testSAMLIdentityContract(t, store, ids.New(), strings.ReplaceAll(ids.New(), "-", "")[:12])
}
