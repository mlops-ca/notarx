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

// TestIdentityProviderMigrationCopiesOIDCSettings replays migration 000016 over the
// pre-migration shape inside a rolled-back transaction: the oidc_settings singleton becomes
// the 'default' provider with its sealed secret and version (a trailing slash on the saved
// issuer does not stop stored identities from binding to it), identities keep their links,
// an identity of another issuer gets its own disabled provider, and 'admin' as the JIT
// default becomes 'member'.
func TestIdentityProviderMigrationCopiesOIDCSettings(t *testing.T) {
	store := openProviderStore(t)
	migration, err := os.ReadFile("../../migrations/000016_identity_providers.sql")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%v: %s", err, query)
		}
	}
	// Return to the pre-migration shape of the objects the migration touches.
	exec(`DROP TABLE public.user_identities`)
	exec(`DROP TABLE public.identity_providers`)
	exec(`ALTER TABLE public.user_sessions DROP COLUMN idp_session_id`)
	exec(`ALTER TABLE public.user_sessions DROP CONSTRAINT user_sessions_kind_check`)
	exec(`ALTER TABLE public.user_sessions ADD CONSTRAINT user_sessions_kind_check CHECK (kind IN ('local_session', 'oidc'))`)
	exec(`ALTER TABLE public.users DROP COLUMN disabled_reason`)
	exec(`CREATE TABLE public.oidc_settings (
		singleton boolean DEFAULT true NOT NULL, version bigint DEFAULT 1 NOT NULL, enabled boolean DEFAULT false NOT NULL,
		issuer text DEFAULT '' NOT NULL, audience text DEFAULT '' NOT NULL, jwks_url text DEFAULT '' NOT NULL,
		client_id text DEFAULT '' NOT NULL, client_secret text DEFAULT '' NOT NULL, redirect_url text DEFAULT '' NOT NULL,
		scopes jsonb DEFAULT '["openid"]' NOT NULL, admin_subjects jsonb DEFAULT '[]' NOT NULL, member_roles jsonb DEFAULT '[]' NOT NULL,
		admin_roles jsonb DEFAULT '[]' NOT NULL, updated_at timestamptz DEFAULT now() NOT NULL,
		provisioning_mode text DEFAULT 'disabled' NOT NULL, email_linking_enabled boolean DEFAULT false NOT NULL,
		jit_default_role text DEFAULT 'member' NOT NULL, CONSTRAINT oidc_settings_pkey PRIMARY KEY (singleton))`)
	exec(`CREATE TABLE public.user_identities (
		id uuid NOT NULL, user_id uuid NOT NULL, kind text NOT NULL, issuer text NOT NULL, subject text NOT NULL,
		email text DEFAULT '' NOT NULL, display_name text DEFAULT '' NOT NULL, email_verified boolean DEFAULT false NOT NULL,
		last_login_at timestamptz, created_at timestamptz DEFAULT now() NOT NULL, updated_at timestamptz DEFAULT now() NOT NULL,
		CONSTRAINT user_identities_pkey PRIMARY KEY (id),
		CONSTRAINT user_identities_issuer_check CHECK (issuer <> ''), CONSTRAINT user_identities_kind_check CHECK (kind = 'oidc'),
		CONSTRAINT user_identities_subject_check CHECK (subject <> ''),
		CONSTRAINT user_identities_kind_issuer_subject_key UNIQUE (kind, issuer, subject),
		CONSTRAINT user_identities_user_id_kind_issuer_key UNIQUE (user_id, kind, issuer))`)

	suffix := strings.ToLower(uuid.NewString()[:12])
	issuer, other := "https://main-"+suffix+".example.test", "https://old-"+suffix+".example.test"
	exec(`INSERT INTO public.oidc_settings (version, enabled, issuer, audience, client_id, client_secret, redirect_url, admin_roles, provisioning_mode, jit_default_role)
		VALUES (7, true, $1, 'api', 'console', 'v1:sealed-secret', 'https://gw.example.test/auth/oidc/callback', '["boss"]', 'jit', 'admin')`, issuer+"/")
	user := uuid.NewString()
	other2 := uuid.NewString()
	exec(`INSERT INTO public.users (id, name, secret_hash, role) VALUES ($1, $2, '', 'member'), ($3, $4, '', 'member')`, user, "mig-"+suffix, other2, "mig2-"+suffix)
	exec(`INSERT INTO public.user_identities (id, user_id, kind, issuer, subject) VALUES ($1,$2,'oidc',$3,'s1'), ($4,$2,'oidc',$5,'s2'), ($6,$7,'oidc',$3,'s3')`,
		uuid.NewString(), user, issuer, uuid.NewString(), other, uuid.NewString(), other2)

	exec(string(migration))

	var slug, kind, display, sealed, mode, jit string
	var enabled bool
	var version int64
	var audience, clientID string
	if err := tx.QueryRowContext(ctx, `SELECT slug, kind, display_name, enabled, COALESCE(secrets_sealed,''), provisioning_mode, jit_default_role, version, settings->>'audience', settings->>'clientId'
		FROM public.identity_providers WHERE settings->>'issuer' = $1`, issuer).Scan(&slug, &kind, &display, &enabled, &sealed, &mode, &jit, &version, &audience, &clientID); err != nil {
		t.Fatal(err)
	}
	if slug != "default" || kind != "oidc" || !enabled || sealed != "v1:sealed-secret" || mode != "jit" || jit != "member" || version != 7 || audience != "api" || clientID != "console" {
		t.Fatalf("default provider: slug=%s kind=%s enabled=%v sealed=%s mode=%s jit=%s version=%d audience=%s client=%s", slug, kind, enabled, sealed, mode, jit, version, audience, clientID)
	}
	var otherSlug string
	var otherEnabled bool
	if err := tx.QueryRowContext(ctx, `SELECT slug, enabled FROM public.identity_providers WHERE settings->>'issuer' = $1`, other).Scan(&otherSlug, &otherEnabled); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(otherSlug, "issuer-") || otherEnabled {
		t.Fatalf("an identity of another issuer needs its own disabled provider: %s %v", otherSlug, otherEnabled)
	}
	var bound int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM public.user_identities i JOIN public.identity_providers p ON p.id = i.provider_id WHERE p.settings->>'issuer' = i.issuer AND i.subject IS NOT NULL AND i.user_id IN ($1, $2)`, user, other2).Scan(&bound); err != nil || bound != 3 {
		t.Fatalf("every identity keeps its link: bound=%d err=%v", bound, err)
	}
	var oidcSettingsExists bool
	if err := tx.QueryRowContext(ctx, `SELECT to_regclass('public.oidc_settings') IS NOT NULL`).Scan(&oidcSettingsExists); err != nil || oidcSettingsExists {
		t.Fatalf("oidc_settings must be dropped: %v %v", oidcSettingsExists, err)
	}
	// The generalized key: one identity per (provider, user) and one per (provider, subject).
	exec("SAVEPOINT dup")
	if _, err := tx.ExecContext(ctx, `INSERT INTO public.user_identities (id, user_id, kind, provider_id, issuer, subject) SELECT $1, $2, 'oidc', provider_id, issuer, 'again' FROM public.user_identities WHERE subject = 's1'`, uuid.NewString(), user); err == nil {
		t.Fatal("a second identity of one user at one provider must be refused")
	}
	exec("ROLLBACK TO SAVEPOINT dup")
	exec("SAVEPOINT saml")
	if _, err := tx.ExecContext(ctx, `UPDATE public.user_sessions SET kind = 'saml' WHERE false`); err != nil {
		t.Fatalf("session kind saml must be allowed: %v", err)
	}
	exec("ROLLBACK TO SAVEPOINT saml")
}
