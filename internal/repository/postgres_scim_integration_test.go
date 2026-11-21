//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/repository/explaincheck"
)

func scimTestProvider(t *testing.T, store *PostgresStore) string {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	provider, err := store.CreateIdentityProvider(ctx, identity.Provider{
		ID: uuid.NewString(), Slug: "scim-" + suffix, Kind: identity.KindOIDC, DisplayName: "SCIM " + suffix,
		Settings: []byte(`{"issuer":"https://idp-` + suffix + `.example.test","audience":"api"}`), AttributeMapping: identity.AttributeMapping{ExternalID: "oid"},
		ProvisioningMode: identity.ProvisioningSCIMOnly, JITDefaultRole: identity.JITRoleNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		// Accounts first (their identity rows go with them), then the groups the provider left behind, then the provider.
		_, _ = store.db.ExecContext(ctx, `DELETE FROM users WHERE id IN (SELECT user_id FROM user_identities WHERE provider_id=$1::uuid)`, provider.ID)
		_, _ = store.db.ExecContext(ctx, `DELETE FROM user_groups WHERE provider_id=$1::uuid`, provider.ID)
		_, _ = store.db.ExecContext(ctx, `DELETE FROM identity_providers WHERE id=$1::uuid`, provider.ID)
	})
	return provider.ID
}

// The same contract the memory store passes: the two stores must not drift.
func TestPostgresSCIMContract(t *testing.T) {
	store := userGroupTestStore(t)
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM users WHERE name LIKE 'local-admin-%' AND secret_hash='bcrypt'`)
	})
	scimContract(t, store, func(t *testing.T) string { return scimTestProvider(t, store) })
}

func TestPostgresSCIMWritesAndTheirAuditCommitTogether(t *testing.T) {
	store := userGroupTestStore(t)
	ctx := context.Background()
	provider := scimTestProvider(t, store)
	bad := ManagementAudit("scim:test", "scim.user.create", "/Users", 201)
	bad.Format = "management"
	bad.Evidence = map[string]string{"attributes": "x"}
	// An audit row the table rejects (a status outside the column's range) must roll the create back.
	bad.Status = 1 << 40
	if _, err := store.CreateSCIMUser(ctx, SCIMUserCreate{ProviderID: provider, UserName: "audit-atomic-" + uuid.NewString()[:8], Active: true}, bad); err == nil {
		t.Fatal("a create whose audit cannot be written succeeded")
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM user_identities WHERE provider_id=$1::uuid`, provider).Scan(&count); err != nil || count != 0 {
		t.Fatalf("the create left %d identities behind (err=%v)", count, err)
	}
	if _, err := store.CreateSCIMToken(ctx, SCIMToken{ProviderID: provider, Name: "t", SecretHash: "audit-" + uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour)}, bad); err == nil {
		t.Fatal("a token whose audit cannot be written was created")
	}
	if tokens, _ := store.ListSCIMTokens(ctx, provider); len(tokens) != 0 {
		t.Fatalf("the token create left %d tokens behind", len(tokens))
	}
}

// A provider with more than ListTotalCap users pages by keyset: every page is one index range of
// user_identities_provider_created_idx, the total is bounded by ListTotalCap+1, and no page uses an offset.
func TestPostgresSCIMListsMoreThanTenThousandUsersByKeyset(t *testing.T) {
	store := userGroupTestStore(t)
	ctx := context.Background()
	provider := scimTestProvider(t, store)
	const users = ListTotalCap + 30
	if _, err := store.db.ExecContext(ctx, `WITH created AS (
		INSERT INTO users (id,name,secret_hash,role,created_at) SELECT gen_random_uuid(), 'bulk-' || $2 || '-' || n, '', 'none', now() + (n || ' milliseconds')::interval FROM generate_series(1,$3) AS n RETURNING id, name, created_at)
		INSERT INTO user_identities (id,user_id,kind,provider_id,issuer,scim_external_id,created_at) SELECT gen_random_uuid(), id, 'oidc', $1::uuid, 'scim:bulk', 'x-' || name, created_at FROM created`,
		provider, uuid.NewString()[:8], users); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `ANALYZE user_identities`); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor, pages := "", 0
	for {
		page, err := store.ListSCIMUsers(ctx, SCIMUserQuery{ProviderID: provider, Limit: SCIMMaxPageSize, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if page.Total.Relation != TotalAtLeast || page.Total.Value != ListTotalCap+1 {
			t.Fatalf("total = %+v", page.Total)
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
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	// Each shape of page (unfiltered, by userName, by externalId, continuing a cursor) is served by an index: no
	// sequential scan of the identities or the users.
	for name, shape := range map[string]struct {
		query SCIMUserQuery
		after bool
	}{
		"unfiltered":   {SCIMUserQuery{ProviderID: provider}, false},
		"continuation": {SCIMUserQuery{ProviderID: provider}, true},
		"by userName":  {SCIMUserQuery{ProviderID: provider, UserName: "bulk-1"}, false},
		"by external":  {SCIMUserQuery{ProviderID: provider, ExternalID: "x-bulk-1"}, false},
	} {
		statement, args := scimUserPageSQL(shape.query, shape.after, SCIMMaxPageSize+1)
		if shape.after {
			args = append(args, time.Now().UTC(), uuid.NewString())
		}
		plan, err := explaincheck.Plan(ctx, tx, statement, args...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, relation := range plan.SequentialScans() {
			if relation == "user_identities" || relation == "users" {
				t.Fatalf("%s: a SCIM user page scans %s sequentially: %v", name, relation, plan.Indexes())
			}
		}
	}
}

func TestPostgresSCIMTokenLookupIsAUniqueKeyRead(t *testing.T) {
	store := userGroupTestStore(t)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	plan, err := explaincheck.Plan(ctx, tx, `SELECT `+scimTokenColumns+` FROM identity_provider_scim_tokens WHERE secret_hash=$1 AND revoked_at IS NULL AND expires_at>now()`, "hash")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.UsesIndex("identity_provider_scim_tokens_secret_hash_key") {
		t.Fatalf("the token lookup does not use the unique hash key: %v", plan.Indexes())
	}
	if _, err := store.FindActiveSCIMTokenByHash(ctx, "missing-"+uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing hash: %v", err)
	}
}

func TestPostgresSCIMExpiredTokenDoesNotAuthenticate(t *testing.T) {
	store := userGroupTestStore(t)
	ctx := context.Background()
	provider := scimTestProvider(t, store)
	hash := "expired-" + uuid.NewString()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO identity_provider_scim_tokens (id,provider_id,name,secret_hash,expires_at) VALUES ($1::uuid,$2::uuid,'old',$3, now() - interval '1 second')`, uuid.NewString(), provider, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindActiveSCIMTokenByHash(ctx, hash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired token authenticated: %v", err)
	}
	// Deleting the provider deletes its tokens.
	if _, err := store.db.ExecContext(ctx, `DELETE FROM user_groups WHERE provider_id=$1::uuid`, provider); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM identity_providers WHERE id=$1::uuid`, provider); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM identity_provider_scim_tokens WHERE provider_id=$1::uuid`, provider).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("the provider's tokens survived its deletion: %d (%v)", remaining, err)
	}
}
