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

// testSAMLEmailLinkContract covers the administrator-verified email-link path of ADR 0018 Decision 3 on any
// store: only a single non-admin, passwordless account whose email an administrator verified is linked, and
// every other case keeps the default behavior with a machine-readable refusal reason.
func testSAMLEmailLinkContract(t *testing.T, store interface {
	UserStore
	UserIdentityStore
}, providerID, suffix string) {
	t.Helper()
	ctx := context.Background()
	newUser := func(label, role, secret string, verified bool, email string) User {
		t.Helper()
		user, err := store.CreateUser(ctx, User{ID: ids.New(), Name: label + "-" + suffix, Email: email, Role: role, SecretHash: secret, State: UserActive})
		if err != nil {
			t.Fatal(err)
		}
		if verified {
			if user, err = store.UpdateUser(ctx, UserUpdate{ID: user.ID, EmailVerifiedByAdmin: ptr(true), EmailVerifiedBy: "root"}, user.Version); err != nil {
				t.Fatal(err)
			}
		}
		return user
	}
	resolve := func(subject, email string, link, provisioning bool) (User, UserIdentity, bool, EmailLinkDecision, error) {
		t.Helper()
		var decision EmailLinkDecision
		provision := OIDCIdentityProvision{
			Kind: UserIdentitySAML, ProviderID: providerID, Issuer: "https://idp.example.test/m", Subject: subject + "-" + suffix,
			Email: email, PreferredUsername: "saml-" + subject + "-" + suffix, Provision: provisioning, DefaultRole: "none",
			LinkAdminVerifiedEmail: link, EmailLink: &decision,
		}
		user, identity, created, err := store.ResolveOIDCIdentity(ctx, provision)
		return user, identity, created, decision, err
	}
	address := func(label string) string { return label + "-" + suffix + "@example.test" }

	t.Run("eligible account links on first login even without provisioning", func(t *testing.T) {
		target := newUser("eligible", "member", "", true, address("eligible"))
		if target.EmailVerifiedAt == nil || target.EmailVerifiedBy != "root" || !target.EmailVerifiedByAdmin {
			t.Fatalf("verification audit trail = %+v", target)
		}
		user, identity, created, decision, err := resolve("eligible", strings.ToUpper(address("eligible")), true, false)
		if err != nil || created || user.ID != target.ID || identity.UserID != target.ID || identity.Kind != UserIdentitySAML || !decision.Linked || decision.Reason != EmailLinkLinked {
			t.Fatalf("link = %+v %+v created=%v decision=%+v err=%v", user, identity, created, decision, err)
		}
		again, _, created, _, err := resolve("eligible", address("eligible"), true, false)
		if err != nil || created || again.ID != target.ID {
			t.Fatalf("second login = %+v created=%v err=%v", again, created, err)
		}
	})

	refusals := []struct {
		name    string
		role    string
		secret  string
		verify  bool
		link    bool
		reason  string
		prepare func(User)
	}{
		{name: "flag not set", role: "member", verify: false, link: true, reason: EmailLinkNotVerified},
		{name: "admin account", role: "admin", verify: true, link: true, reason: EmailLinkAdminAccount},
		{name: "local password", role: "member", secret: "hash", verify: true, link: true, reason: EmailLinkLocalPassword},
		{name: "provider does not allow linking", role: "member", verify: true, link: false, reason: ""},
		{name: "disabled account", role: "member", verify: true, link: true, reason: EmailLinkDisabled, prepare: func(user User) {
			if _, err := store.UpdateUser(ctx, UserUpdate{ID: user.ID, State: ptr(UserDisabled)}, mustVersion(t, store, user.ID)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "already has an identity at the provider", role: "member", verify: true, link: true, reason: EmailLinkAlreadyLinked, prepare: func(user User) {
			if _, err := store.CreateUserIdentity(ctx, UserIdentity{ID: ids.New(), UserID: user.ID, Kind: UserIdentitySAML, ProviderID: providerID, Issuer: "https://idp.example.test/m", Subject: "other-subject-" + suffix}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for index, tc := range refusals {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			label := "refused" + string(rune('a'+index))
			target := newUser(label, tc.role, tc.secret, tc.verify, address(label))
			if tc.prepare != nil {
				tc.prepare(target)
			}
			if _, _, _, decision, err := resolve(label, address(label), tc.link, false); !errors.Is(err, ErrNotFound) || decision.Linked || decision.Reason != tc.reason {
				t.Fatalf("without provisioning: decision=%+v err=%v", decision, err)
			}
			user, _, created, decision, err := resolve(label+"-jit", address(label), tc.link, true)
			if err != nil || !created || user.ID == target.ID || decision.Linked || decision.Reason != tc.reason {
				t.Fatalf("with provisioning the default behavior is kept: user=%+v created=%v decision=%+v err=%v", user, created, decision, err)
			}
		})
	}

	t.Run("two verified matches are ambiguous", func(t *testing.T) {
		shared := address("shared")
		newUser("shared-one", "member", "", true, shared)
		newUser("shared-two", "member", "", true, shared)
		if _, _, _, decision, err := resolve("shared", shared, true, true); !errors.Is(err, ErrIdentityAmbiguous) || decision.Linked || decision.Reason != EmailLinkAmbiguous {
			t.Fatalf("ambiguous: decision=%+v err=%v", decision, err)
		}
	})
	t.Run("an unverified duplicate does not hide the verified account", func(t *testing.T) {
		shared := address("dup")
		newUser("dup-plain", "member", "", false, shared)
		target := newUser("dup-verified", "member", "", true, shared)
		if user, _, _, decision, err := resolve("dup", shared, true, false); err != nil || user.ID != target.ID || !decision.Linked {
			t.Fatalf("duplicate = %+v decision=%+v err=%v", user, decision, err)
		}
	})
	t.Run("no asserted email", func(t *testing.T) {
		if _, _, _, decision, err := resolve("noemail", "", true, false); !errors.Is(err, ErrNotFound) || decision.Reason != EmailLinkNoEmail {
			t.Fatalf("no email: decision=%+v err=%v", decision, err)
		}
	})
	t.Run("no matching account", func(t *testing.T) {
		if _, _, _, decision, err := resolve("nomatch", address("nomatch"), true, false); !errors.Is(err, ErrNotFound) || decision.Reason != EmailLinkNoMatch {
			t.Fatalf("no match: decision=%+v err=%v", decision, err)
		}
	})
	t.Run("the verification follows the email", func(t *testing.T) {
		user := newUser("follows", "member", "", true, address("follows"))
		same, err := store.UpdateUser(ctx, UserUpdate{ID: user.ID, Email: ptr(address("follows"))}, user.Version)
		if err != nil || !same.EmailVerifiedByAdmin {
			t.Fatalf("an unchanged email keeps the verification: %+v err=%v", same, err)
		}
		changed, err := store.UpdateUser(ctx, UserUpdate{ID: user.ID, Email: ptr(address("follows-new"))}, same.Version)
		if err != nil || changed.EmailVerifiedByAdmin || changed.EmailVerifiedAt != nil || changed.EmailVerifiedBy != "" {
			t.Fatalf("a changed email clears the verification: %+v err=%v", changed, err)
		}
		reverified, err := store.UpdateUser(ctx, UserUpdate{ID: user.ID, EmailVerifiedByAdmin: ptr(true), EmailVerifiedBy: "root"}, changed.Version)
		if err != nil || !reverified.EmailVerifiedByAdmin {
			t.Fatalf("reverify = %+v err=%v", reverified, err)
		}
		cleared, err := store.UpdateUser(ctx, UserUpdate{ID: user.ID, EmailVerifiedByAdmin: ptr(false)}, reverified.Version)
		if err != nil || cleared.EmailVerifiedByAdmin || cleared.EmailVerifiedAt != nil || cleared.EmailVerifiedBy != "" {
			t.Fatalf("clear = %+v err=%v", cleared, err)
		}
		upper, err := store.UpdateUser(ctx, UserUpdate{ID: user.ID, Email: ptr(strings.ToUpper(address("follows")))}, cleared.Version)
		if err != nil || upper.EmailVerifiedByAdmin {
			t.Fatalf("clear stays cleared: %+v err=%v", upper, err)
		}
		again, err := store.UpdateUser(ctx, UserUpdate{ID: user.ID, EmailVerifiedByAdmin: ptr(true), EmailVerifiedBy: "first"}, upper.Version)
		if err != nil || again.EmailVerifiedBy != "first" || again.EmailVerifiedAt == nil {
			t.Fatalf("verify again = %+v err=%v", again, err)
		}
		recased, err := store.UpdateUser(ctx, UserUpdate{ID: user.ID, Email: ptr(address("follows"))}, again.Version)
		if err != nil || !recased.EmailVerifiedByAdmin || recased.Email != address("follows") {
			t.Fatalf("a case-only change is the same address and keeps the verification: %+v err=%v", recased, err)
		}
		second, err := store.UpdateUser(ctx, UserUpdate{ID: user.ID, EmailVerifiedByAdmin: ptr(true), EmailVerifiedBy: "second"}, recased.Version)
		if err != nil || second.EmailVerifiedBy != "first" || !second.EmailVerifiedAt.Equal(*again.EmailVerifiedAt) {
			t.Fatalf("verifying an already verified account keeps the original verifier: %+v err=%v", second, err)
		}
		blank := newUser("blank", "member", "", false, "")
		if _, err := store.UpdateUser(ctx, UserUpdate{ID: blank.ID, Email: ptr("blank-" + suffix + "@example.test"), EmailVerifiedByAdmin: ptr(true)}, blank.Version); !errors.Is(err, ErrVerifierRequired) {
			t.Fatalf("verifying without a verifier = %v", err)
		}
		if _, err := store.UpdateUser(ctx, UserUpdate{ID: blank.ID, EmailVerifiedByAdmin: ptr(true), EmailVerifiedBy: "root"}, blank.Version); !errors.Is(err, ErrEmailRequired) {
			t.Fatalf("verifying an account without an email = %v", err)
		}
	})
	t.Run("a provider email change clears the verification", func(t *testing.T) {
		user := newUser("synced", "member", "", true, address("synced"))
		linked, err := store.CreateUserIdentity(ctx, UserIdentity{ID: ids.New(), UserID: user.ID, Kind: UserIdentityOIDC, ProviderID: providerID, Issuer: "https://idp.example.test/m", Subject: "synced-link-" + suffix})
		if err != nil {
			t.Fatal(err)
		}
		recased, _, _, err := store.ResolveOIDCIdentity(ctx, OIDCIdentityProvision{ProviderID: providerID, Issuer: "https://idp.example.test/m", Subject: linked.Subject, Email: strings.ToUpper(address("synced")), EmailVerified: true})
		if err != nil || !recased.EmailVerifiedByAdmin {
			t.Fatalf("a case-only provider email change keeps the verification: %+v err=%v", recased, err)
		}
		synced, _, _, err := store.ResolveOIDCIdentity(ctx, OIDCIdentityProvision{ProviderID: providerID, Issuer: "https://idp.example.test/m", Subject: linked.Subject, Email: address("synced-moved"), EmailVerified: true})
		if err != nil || synced.Email != address("synced-moved") || synced.EmailVerifiedByAdmin || synced.EmailVerifiedAt != nil || synced.EmailVerifiedBy != "" {
			t.Fatalf("synced = %+v err=%v", synced, err)
		}
	})
}

func TestMemorySAMLEmailLinkContract(t *testing.T) {
	testSAMLEmailLinkContract(t, NewMemoryStore(), ids.New(), strings.ReplaceAll(ids.New(), "-", "")[:12])
}

func mustVersion(t *testing.T, store UserStore, id string) string {
	t.Helper()
	user, err := store.GetUser(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return user.Version
}

func ptr[T any](value T) *T { return &value }
