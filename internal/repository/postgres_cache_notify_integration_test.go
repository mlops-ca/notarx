//go:build integration

package repository

import (
	"context"
	"database/sql"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/readcache"
)

// cacheNotifications listens on the read-cache channel and returns what arrives until the channel stays quiet.
type cacheNotifications struct {
	conn     *sql.Conn
	received chan string
}

func listenCacheChannel(t *testing.T, databaseURL string) *cacheNotifications {
	t.Helper()
	pool, err := database.OpenPostgres(databaseURL, database.NotificationPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	conn, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := database.ListenChannels(context.Background(), conn, "notarx_cache"); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	listener := &cacheNotifications{conn: conn, received: make(chan string, 4096)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, payload, err := database.WaitForNotificationPayload(ctx, conn)
			if err != nil {
				return
			}
			listener.received <- payload
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = conn.Close()
		<-done
	})
	return listener
}

// drain returns the payloads received so far and waits until none arrives for the quiet period.
func (l *cacheNotifications) drain(quiet time.Duration) []string {
	var payloads []string
	for {
		select {
		case payload := <-l.received:
			payloads = append(payloads, payload)
		case <-time.After(quiet):
			return payloads
		}
	}
}

func TestPostgresBulkWritesSendABoundedNumberOfCacheNotifications(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	prefix := "notify-" + uuid.NewString()[:8] + "-"
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM hosted_repositories WHERE name LIKE $1`, prefix+"%")
	})
	const bulk = 300
	listener := listenCacheChannel(t, databaseURL)

	// One statement that inserts many repositories announces the scope once.
	if _, err := store.db.ExecContext(ctx, `INSERT INTO hosted_repositories(id,name,format) SELECT gen_random_uuid(), $1||g, 'raw' FROM generate_series(1,$2) g`, prefix, bulk); err != nil {
		t.Fatal(err)
	}
	if got := listener.drain(500 * time.Millisecond); !slices.Equal(got, []string{"repository:"}) {
		t.Fatalf("a %d-row insert sent %d notifications %v, want one scope flush", bulk, len(got), got)
	}
	// So does a bulk update (a secrets reseal bumps every repository version) and a bulk delete.
	if _, err := store.db.ExecContext(ctx, `UPDATE hosted_repositories SET version=version+1 WHERE name LIKE $1`, prefix+"%"); err != nil {
		t.Fatal(err)
	}
	if got := listener.drain(500 * time.Millisecond); !slices.Equal(got, []string{"repository:"}) {
		t.Fatalf("a %d-row update sent %d notifications %v, want one scope flush", bulk, len(got), got)
	}

	// A statement that changes one row, or a few, still announces each entity by key.
	var first, second string
	if err := store.db.QueryRowContext(ctx, `SELECT id::text FROM hosted_repositories WHERE name=$1`, prefix+"1").Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT id::text FROM hosted_repositories WHERE name=$1`, prefix+"2").Scan(&second); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE hosted_repositories SET version=version+1 WHERE id=$1::uuid`, first); err != nil {
		t.Fatal(err)
	}
	if got := listener.drain(500 * time.Millisecond); !slices.Equal(got, []string{"repository:" + first}) {
		t.Fatalf("a single-row update sent %v, want the key of the row", got)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE hosted_repositories SET version=version+1 WHERE id IN ($1::uuid,$2::uuid)`, first, second); err != nil {
		t.Fatal(err)
	}
	got := listener.drain(500 * time.Millisecond)
	slices.Sort(got)
	want := []string{"repository:" + first, "repository:" + second}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("a two-row update sent %v, want %v", got, want)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM hosted_repositories WHERE name LIKE $1`, prefix+"%"); err != nil {
		t.Fatal(err)
	}
	if got := listener.drain(500 * time.Millisecond); !slices.Equal(got, []string{"repository:"}) {
		t.Fatalf("a %d-row delete sent %d notifications %v, want one scope flush", bulk, len(got), got)
	}
}

func TestPostgresGroupMemberBulkWritesAnnounceGroupsInBoundedNumber(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	member := ceilingRepo(t, store, VisibilityPrivate)
	group, err := ceilingGroup(t, store, VisibilityPrivate, member)
	if err != nil {
		t.Fatal(err)
	}
	listener := listenCacheChannel(t, databaseURL)
	listener.drain(200 * time.Millisecond)
	// Rewriting every member row of the group is one statement and one announcement of the group.
	if _, err := store.db.ExecContext(ctx, `UPDATE hosted_group_members SET position=position WHERE group_id=$1::uuid`, group.ID); err != nil {
		t.Fatal(err)
	}
	if got := listener.drain(500 * time.Millisecond); !slices.Equal(got, []string{"group:" + group.ID}) {
		t.Fatalf("a member update sent %v, want the group", got)
	}
}

// A bulk write on any table of the second read-cache group (migration 000011) sends one scope flush, never one
// notification per row, and applying what arrived empties the cache of the scope on a listening node.
func TestPostgresAccountStateBulkWritesSendABoundedNumberOfCacheNotifications(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	const bulk = 1000
	prefix := "bulk-" + uuid.NewString()[:8] + "-"
	t.Cleanup(func() {
		for _, stmt := range []string{
			`DELETE FROM workload_issuers WHERE name LIKE $1`,
			`DELETE FROM users WHERE name LIKE $1`,
			`DELETE FROM token_signing_keys WHERE kid LIKE $1`,
			`DELETE FROM hosted_repositories WHERE name LIKE $1`,
		} {
			_, _ = store.db.ExecContext(context.Background(), stmt, prefix+"%")
		}
	})
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	listener := listenCacheChannel(t, databaseURL)
	exec(`INSERT INTO hosted_repositories(id,name,format) SELECT gen_random_uuid(), $1||g, 'raw' FROM generate_series(1,$2) g`, prefix, bulk)
	exec(`INSERT INTO repository_grant_sets(repository_id) SELECT id FROM hosted_repositories WHERE name LIKE $1`, prefix+"%")
	listener.drain(500 * time.Millisecond)

	cases := []struct {
		name, scope, insert, update, remove string
		insertArgs                          []any
	}{
		{
			name: "users", scope: CacheScopeUser,
			insertArgs: []any{prefix, bulk},
			insert:     `INSERT INTO users(id,name,secret_hash,role) SELECT gen_random_uuid(), $1||g, 'x', 'member' FROM generate_series(1,$2) g`,
			update:     `UPDATE users SET session_version=session_version+1 WHERE name LIKE $1`,
			remove:     `DELETE FROM users WHERE name LIKE $1`,
		},
		{
			name: "grants", scope: CacheScopeGrants,
			insertArgs: []any{prefix + "%"},
			insert:     `INSERT INTO repository_grants(repository_id,principal,scopes) SELECT id, 'user:a', ARRAY['read'] FROM hosted_repositories WHERE name LIKE $1`,
			update:     `UPDATE repository_grants SET scopes=ARRAY['write'] WHERE repository_id IN (SELECT id FROM hosted_repositories WHERE name LIKE $1)`,
			remove:     `DELETE FROM repository_grants WHERE repository_id IN (SELECT id FROM hosted_repositories WHERE name LIKE $1)`,
		},
		{
			name: "quarantine", scope: CacheScopeQuarantine,
			insertArgs: []any{prefix + "%"},
			insert:     `INSERT INTO repository_quarantine_read_policies(repository_id) SELECT id FROM hosted_repositories WHERE name LIKE $1`,
			update:     `UPDATE repository_quarantine_read_policies SET enabled=true WHERE repository_id IN (SELECT id FROM hosted_repositories WHERE name LIKE $1)`,
			remove:     `DELETE FROM repository_quarantine_read_policies WHERE repository_id IN (SELECT id FROM hosted_repositories WHERE name LIKE $1)`,
		},
		{
			name: "workload issuers", scope: CacheScopeWorkloadIssuer,
			insertArgs: []any{prefix, bulk},
			insert:     `INSERT INTO workload_issuers(id,name,kind,issuer,audiences,jwks_mode,jwks_uri) SELECT gen_random_uuid(), $1||g, 'generic', 'https://issuer.example/'||$1||g, ARRAY['aud'], 'discovery', 'https://issuer.example/jwks' FROM generate_series(1,$2) g`,
			update:     `UPDATE workload_issuers SET version=version+1 WHERE name LIKE $1`,
			remove:     `DELETE FROM workload_issuers WHERE name LIKE $1`,
		},
		{
			name: "signing keys", scope: "signing_keys",
			insertArgs: []any{prefix, bulk},
			insert:     `INSERT INTO token_signing_keys(kid,alg,state,public_jwk,private_sealed,integrity_mac,created_at) SELECT $1||g, 'ES256', 'retired', '{}'::jsonb, 'x', 'x', now() FROM generate_series(1,$2) g`,
			update:     `UPDATE token_signing_keys SET sealing_ref='local' WHERE kid LIKE $1`,
			remove:     `DELETE FROM token_signing_keys WHERE kid LIKE $1`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			registry := readcache.NewRegistry(time.Minute)
			cache := readcache.New[string, string](registry, readcache.Options[string]{Name: c.name, Scope: c.scope})
			load := func(context.Context) (string, error) { return "v", nil }
			steps := []struct {
				verb  string
				query string
				args  []any
			}{
				{"insert", c.insert, c.insertArgs},
				{"update", c.update, []any{prefix + "%"}},
				{"delete", c.remove, []any{prefix + "%"}},
			}
			for _, step := range steps {
				if _, err := cache.Get(ctx, "entry", load); err != nil || cache.Len() != 1 {
					t.Fatalf("%s: could not fill the cache: %v", step.verb, err)
				}
				exec(step.query, step.args...)
				got := listener.drain(500 * time.Millisecond)
				if want := []string{c.scope + ":"}; !slices.Equal(got, want) {
					t.Fatalf("a %d-row %s sent %d notifications %v, want %v", bulk, step.verb, len(got), got, want)
				}
				for _, payload := range got {
					registry.HandleNotification(payload)
				}
				if cache.Len() != 0 {
					t.Fatalf("a %s flush left %d entries in the cache", step.verb, cache.Len())
				}
			}
		})
	}
}
