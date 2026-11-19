package authorization

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// countingKeys counts the statements a node sends to the database.
type countingKeys struct {
	*repository.MemoryStore
	finds, touches atomic.Int32
}

func (c *countingKeys) FindActiveAPIKeyByHash(ctx context.Context, hash string) (repository.APIKey, error) {
	c.finds.Add(1)
	return c.MemoryStore.FindActiveAPIKeyByHash(ctx, hash)
}

func (c *countingKeys) TouchAPIKeys(ctx context.Context, uses []repository.APIKeyUse, notBefore time.Time) error {
	c.touches.Add(1)
	return c.MemoryStore.TouchAPIKeys(ctx, uses, notBefore)
}

// node is one notarx process: its own registry, caches and clock over the shared
// database.
type node struct {
	clock    *testClock
	registry *readcache.Registry
	keys     *CachedAPIKeys
	auth     Authenticator
}

func newNode(t *testing.T, db *countingKeys, ttl time.Duration, start time.Time) *node {
	t.Helper()
	clock := &testClock{now: start}
	registry := readcache.NewRegistry(ttl)
	registry.SetClock(clock.Now)
	keys := NewCachedAPIKeys(db, registry, db, time.Minute, nil)
	keys.now = clock.Now
	accounts := NewCachedServiceAccounts(db, registry)
	return &node{clock: clock, registry: registry, keys: keys, auth: Authenticator{APIKeys: keys, ServiceAccounts: accounts}}
}

func seedKey(t *testing.T, db *countingKeys, token string) repository.APIKey {
	t.Helper()
	key, err := db.CreateAPIKey(context.Background(), repository.APIKey{ID: "key-" + token, Name: token, SecretHash: HashAPIKey(token), Roles: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// TestRevokedKeyIsRejectedOnOtherNodeWithinAuthzCacheTTL is the revocation test
// of ADR 0023 section 6 with the NOTIFY dropped: node B never hears of the
// revocation and must stop accepting the key once its TTL has elapsed. The
// regulated profiles (1s and 0) are covered too.
func TestRevokedKeyIsRejectedOnOtherNodeWithinAuthzCacheTTL(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	for _, ttl := range []time.Duration{5 * time.Second, time.Second, 0} {
		t.Run(ttl.String(), func(t *testing.T) {
			db := &countingKeys{MemoryStore: repository.NewMemoryStore()}
			key := seedKey(t, db, "token")
			nodeA, nodeB := newNode(t, db, ttl, start), newNode(t, db, ttl, start)
			header := "Bearer token"
			if _, ok := nodeB.auth.AuthenticateContext(context.Background(), header); !ok {
				t.Fatal("node B rejected a valid key")
			}
			if _, err := db.RevokeAPIKey(context.Background(), key.ID); err != nil {
				t.Fatal(err)
			}
			nodeA.registry.Invalidate(repository.CacheScopeAPIKey, key.ID) // the mutating node evicts synchronously
			if _, ok := nodeA.auth.AuthenticateContext(context.Background(), header); ok {
				t.Fatal("node A accepted a key it just revoked")
			}
			if ttl > 0 {
				nodeB.clock.Advance(ttl - time.Nanosecond)
				if _, ok := nodeB.auth.AuthenticateContext(context.Background(), header); !ok {
					t.Fatal("expected the cached entry to serve inside the window")
				}
				nodeB.clock.Advance(time.Nanosecond)
			}
			if _, ok := nodeB.auth.AuthenticateContext(context.Background(), header); ok {
				t.Fatalf("node B accepted a revoked key after the %s window", ttl)
			}
		})
	}
}

func TestNotificationRevokesKeyImmediately(t *testing.T) {
	db := &countingKeys{MemoryStore: repository.NewMemoryStore()}
	key := seedKey(t, db, "token")
	node := newNode(t, db, 5*time.Second, time.Unix(1_700_000_000, 0))
	_, _ = node.auth.AuthenticateContext(context.Background(), "Bearer token")
	_, _ = db.RevokeAPIKey(context.Background(), key.ID)
	node.registry.HandleNotification(readcache.Payload(repository.CacheScopeAPIKey, key.ID))
	if _, ok := node.auth.AuthenticateContext(context.Background(), "Bearer token"); ok {
		t.Fatal("a delivered notification did not evict the revoked key")
	}
}

func TestWarmAuthenticatedReadIssuesNoDatabaseStatement(t *testing.T) {
	db := &countingKeys{MemoryStore: repository.NewMemoryStore()}
	seedKey(t, db, "token")
	node := newNode(t, db, 5*time.Second, time.Unix(1_700_000_000, 0))
	if _, ok := node.auth.AuthenticateContext(context.Background(), "Bearer token"); !ok {
		t.Fatal("cold authentication failed")
	}
	before := db.finds.Load()
	for range 100 {
		if _, ok := node.auth.AuthenticateContext(context.Background(), "Bearer token"); !ok {
			t.Fatal("warm authentication failed")
		}
	}
	if got := db.finds.Load() - before; got > 1 {
		t.Fatalf("warm cache issued %d statements, want at most 1", got)
	}
}

func TestServiceAccountDisableEvictsPrincipal(t *testing.T) {
	db := &countingKeys{MemoryStore: repository.NewMemoryStore()}
	ctx := context.Background()
	account, err := db.CreateServiceAccount(ctx, repository.ServiceAccount{ID: "11111111-1111-1111-1111-111111111111", Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateServiceAccountCredential(ctx, repository.APIKey{ID: "cred", ServiceAccountID: account.ID, Name: "c", SecretHash: HashAPIKey("sa-token")}); err != nil {
		t.Fatal(err)
	}
	node := newNode(t, db, 5*time.Second, time.Unix(1_700_000_000, 0))
	if p, ok := node.auth.AuthenticateContext(context.Background(), "Bearer sa-token"); !ok || p.Actor != "service-account:"+account.ID {
		t.Fatalf("principal = %#v ok = %t", p, ok)
	}
	disabled := repository.ServiceAccountDisabled
	if _, err := db.UpdateServiceAccount(ctx, repository.ServiceAccountUpdate{ID: account.ID, State: &disabled}, account.Version); err != nil {
		t.Fatal(err)
	}
	node.registry.Invalidate(repository.CacheScopeServiceAccount, account.ID)
	if _, ok := node.auth.AuthenticateContext(context.Background(), "Bearer sa-token"); ok {
		t.Fatal("a disabled service account still authenticated")
	}
}

// expiringKeys answers every lookup with the key and counts the lookups, so only
// the cache can notice the expiry.
type expiringKeys struct {
	*countingKeys
	key   repository.APIKey
	loads *atomic.Int32
}

func (e expiringKeys) FindActiveAPIKeyByHash(context.Context, string) (repository.APIKey, error) {
	e.loads.Add(1)
	return e.key, nil
}

func TestExpiredKeyIsNotServedFromCache(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	expires := start.Add(2 * time.Second)
	loads := &atomic.Int32{}
	db := expiringKeys{countingKeys: &countingKeys{MemoryStore: repository.NewMemoryStore()}, loads: loads,
		key: repository.APIKey{ID: "k", Name: "k", SecretHash: HashAPIKey("t"), Roles: []string{"admin"}, ExpiresAt: &expires}}
	clock := &testClock{now: start}
	registry := readcache.NewRegistry(30 * time.Second)
	registry.SetClock(clock.Now)
	auth := Authenticator{APIKeys: NewCachedAPIKeys(db, registry, nil, time.Minute, nil)}
	for range 2 {
		if _, ok := auth.AuthenticateContext(context.Background(), "Bearer t"); !ok {
			t.Fatal("unexpired key rejected")
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("loads before expiry = %d, want 1", loads.Load())
	}
	clock.Advance(2 * time.Second)
	_, _ = auth.AuthenticateContext(context.Background(), "Bearer t")
	if loads.Load() != 2 {
		t.Fatal("a cached key was served at its expires_at instead of being re-read")
	}
}

func TestNegativeResultIsShortAndNewKeyIsVisibleOnCreator(t *testing.T) {
	db := &countingKeys{MemoryStore: repository.NewMemoryStore()}
	node := newNode(t, db, 30*time.Second, time.Unix(1_700_000_000, 0))
	if _, ok := node.auth.AuthenticateContext(context.Background(), "Bearer late"); ok {
		t.Fatal("unknown key authenticated")
	}
	if _, err := node.keys.CreateAPIKey(context.Background(), repository.APIKey{ID: "k", Name: "k", SecretHash: HashAPIKey("late"), Roles: []string{"admin"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := node.auth.AuthenticateContext(context.Background(), "Bearer late"); !ok {
		t.Fatal("a cached denial hid a key created on the same node")
	}
}

func TestLastUsedIsThrottledAndFlushedInBatches(t *testing.T) {
	db := &countingKeys{MemoryStore: repository.NewMemoryStore()}
	key := seedKey(t, db, "token")
	node := newNode(t, db, 5*time.Second, time.Unix(1_700_000_000, 0))
	for range 50 {
		if _, ok := node.auth.AuthenticateContext(context.Background(), "Bearer token"); !ok {
			t.Fatal("authentication failed")
		}
	}
	if db.touches.Load() != 0 {
		t.Fatal("authenticating wrote last_used_at")
	}
	if err := node.keys.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if db.touches.Load() != 1 {
		t.Fatalf("flush statements = %d, want 1", db.touches.Load())
	}
	stored := lastUsed(t, db, key.ID)
	if stored == nil || !stored.Equal(node.clock.Now().UTC()) {
		t.Fatalf("last_used_at = %v", stored)
	}
	// A use inside the interval leaves the stored value alone.
	node.clock.Advance(10 * time.Second)
	_, _ = node.auth.AuthenticateContext(context.Background(), "Bearer token")
	if err := node.keys.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again := lastUsed(t, db, key.ID); !again.Equal(*stored) {
		t.Fatalf("a write inside the interval moved last_used_at to %v", again)
	}
	node.clock.Advance(time.Minute)
	_, _ = node.auth.AuthenticateContext(context.Background(), "Bearer token")
	_ = node.keys.Flush(context.Background())
	if later := lastUsed(t, db, key.ID); !later.After(*stored) {
		t.Fatal("last_used_at never advanced past the interval")
	}
}

func lastUsed(t *testing.T, db *countingKeys, id string) *time.Time {
	t.Helper()
	keys, err := db.ListAPIKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if key.ID == id {
			return key.LastUsedAt
		}
	}
	t.Fatalf("key %s missing", id)
	return nil
}

type failingUsage struct {
	repository.APIKeyStore
	fail atomic.Bool
	got  atomic.Int32
}

func (f *failingUsage) TouchAPIKeys(_ context.Context, uses []repository.APIKeyUse, _ time.Time) error {
	if f.fail.Load() {
		return errors.New("database down")
	}
	f.got.Add(int32(len(uses)))
	return nil
}

func TestFailedFlushKeepsUsesForRetry(t *testing.T) {
	store := &failingUsage{APIKeyStore: repository.NewMemoryStore()}
	registry := readcache.NewRegistry(time.Second)
	keys := NewCachedAPIKeys(store, registry, store, time.Minute, nil)
	keys.touch("a")
	store.fail.Store(true)
	if err := keys.Flush(context.Background()); err == nil {
		t.Fatal("flush error swallowed")
	}
	store.fail.Store(false)
	if err := keys.Flush(context.Background()); err != nil || store.got.Load() != 1 {
		t.Fatalf("retry err=%v uses=%d", err, store.got.Load())
	}
}

// TestAuthenticateWithoutRequestContextReadsThrough pins the safe default: a
// caller that passes no request context never sees a cached principal, so a
// revocation is effective at once even inside the window.
func TestAuthenticateWithoutRequestContextReadsThrough(t *testing.T) {
	db := &countingKeys{MemoryStore: repository.NewMemoryStore()}
	key := seedKey(t, db, "token")
	node := newNode(t, db, 30*time.Second, time.Unix(1_700_000_000, 0))
	if _, ok := node.auth.AuthenticateContext(context.Background(), "Bearer token"); !ok {
		t.Fatal("cached authentication failed")
	}
	if _, err := db.RevokeAPIKey(context.Background(), key.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := node.auth.Authenticate("Bearer token"); ok {
		t.Fatal("Authenticate served a revoked key from the cache")
	}
	if _, ok := node.auth.AuthenticateBasic("ci", "token"); ok {
		t.Fatal("AuthenticateBasic served a revoked key from the cache")
	}
	if _, ok := node.auth.AuthenticateContext(readcache.Bypass(context.Background()), "Bearer token"); ok {
		t.Fatal("a bypassed context served a revoked key from the cache")
	}
}
