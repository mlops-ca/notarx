package access

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

// countingSource counts the database statements the caches did not absorb.
type countingSource struct {
	*repository.MemoryStore
	statements atomic.Int64
}

func (s *countingSource) GetHostedRepository(ctx context.Context, id string) (repository.HostedRepository, error) {
	s.statements.Add(1)
	return s.MemoryStore.GetHostedRepository(ctx, id)
}

func (s *countingSource) GetHostedRepositoryByName(ctx context.Context, name string) (repository.HostedRepository, error) {
	s.statements.Add(1)
	return s.MemoryStore.GetHostedRepositoryByName(ctx, name)
}

func (s *countingSource) GetHostedGroup(ctx context.Context, id string) (repository.HostedGroup, error) {
	s.statements.Add(1)
	return s.MemoryStore.GetHostedGroup(ctx, id)
}

func (s *countingSource) GetHostedGroupByName(ctx context.Context, name string) (repository.HostedGroup, error) {
	s.statements.Add(1)
	return s.MemoryStore.GetHostedGroupByName(ctx, name)
}

func (s *countingSource) GetPublicAccessPolicy(ctx context.Context) (repository.PublicAccessPolicy, error) {
	s.statements.Add(1)
	return s.MemoryStore.GetPublicAccessPolicy(ctx)
}

type fixture struct {
	source   *countingSource
	registry *readcache.Registry
	caches   *ReadCaches
	now      time.Time
	repo     repository.HostedRepository
	group    repository.HostedGroup
}

func newFixture(t *testing.T, ttl time.Duration) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{source: &countingSource{MemoryStore: repository.NewMemoryStore()}, registry: readcache.NewRegistry(ttl), now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	f.registry.SetClock(func() time.Time { return f.now })
	var err error
	if f.repo, err = f.source.CreateHostedRepository(ctx, repository.HostedRepository{ID: "11111111-1111-4111-8111-111111111111", Name: "public-raw", Format: repository.FormatRaw, Visibility: repository.VisibilityPublic}); err != nil {
		t.Fatal(err)
	}
	if f.group, _, err = f.source.CreateHostedGroupIdempotently(ctx, repository.HostedGroup{ID: "22222222-2222-4222-8222-222222222222", Name: "public-group", Format: repository.FormatRaw, Visibility: repository.VisibilityPublic, Members: []repository.GroupMember{{RepositoryID: f.repo.ID}}}, "actor", "key", "payload"); err != nil {
		t.Fatal(err)
	}
	f.caches = NewReadCaches(f.source, f.registry)
	f.source.statements.Store(0)
	return f
}

func (f *fixture) resolveAll(t *testing.T, ctx context.Context) (repository.HostedRepository, repository.HostedGroup, repository.PublicAccessPolicy) {
	t.Helper()
	repo, err := f.caches.GetHostedRepositoryByName(ctx, f.repo.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.caches.GetHostedRepository(ctx, f.repo.ID); err != nil {
		t.Fatal(err)
	}
	group, err := f.caches.GetHostedGroupByName(ctx, f.group.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.caches.GetHostedGroup(ctx, f.group.ID); err != nil {
		t.Fatal(err)
	}
	policy, err := f.caches.GetPublicAccessPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return repo, group, policy
}

// A protocol read on a warm cache issues no database statement for repository, group or policy resolution
// (the acceptance bound is one).
func TestWarmCacheResolvesRepositoryGroupAndPolicyWithoutStatements(t *testing.T) {
	f := newFixture(t, 5*time.Second)
	ctx := context.Background()
	f.resolveAll(t, ctx)
	cold := f.source.statements.Load()
	if cold != 5 {
		t.Fatalf("cold resolution issued %d statements, want 5", cold)
	}
	for range 50 {
		f.resolveAll(t, ctx)
	}
	if warm := f.source.statements.Load() - cold; warm > 1 {
		t.Fatalf("50 warm resolutions issued %d statements, want at most 1", warm)
	}
}

// A visibility change reaches the next read through the scope invalidation, and the access decision never
// serves a stale public: the repository entry goes by id. A cached group holds only its member ids, which the
// access decision resolves through the repository cache, so the group entry stays.
func TestVisibilityChangeEvictsTheRepositoryAndKeepsItsGroups(t *testing.T) {
	f := newFixture(t, 5*time.Second)
	ctx := context.Background()
	f.resolveAll(t, ctx)
	private := f.repo
	private.Visibility = repository.VisibilityPrivate
	if _, err := f.source.UpdateHostedRepository(ctx, private, f.repo.Version); err != nil {
		t.Fatal(err)
	}
	// What the NOTIFY of the row trigger (or the synchronous local eviction) does on every node.
	f.registry.HandleNotification(readcache.Payload(repository.CacheScopeRepository, f.repo.ID))
	repo, err := f.caches.GetHostedRepositoryByName(ctx, f.repo.Name)
	if err != nil || repo.Visibility != repository.VisibilityPrivate {
		t.Fatalf("repository after a visibility change = %q, %v", repo.Visibility, err)
	}
	byID, err := f.caches.GetHostedRepository(ctx, f.repo.ID)
	if err != nil || byID.Visibility != repository.VisibilityPrivate {
		t.Fatalf("repository by id after a visibility change = %q, %v", byID.Visibility, err)
	}
	before := f.source.statements.Load()
	if _, err = f.caches.GetHostedGroupByName(ctx, f.group.Name); err != nil {
		t.Fatal(err)
	}
	if f.source.statements.Load() != before {
		t.Fatal("a repository change evicted a cached group")
	}
}

// With every NOTIFY dropped the entry still ends within the authorization window, at the regulated 1s profile
// as well, and a TTL of zero disables the caches.
func TestStaleEntryEndsWithinTheWindowWhenNotificationIsLost(t *testing.T) {
	for _, ttl := range []time.Duration{5 * time.Second, time.Second} {
		f := newFixture(t, ttl)
		ctx := context.Background()
		f.resolveAll(t, ctx)
		private := f.repo
		private.Visibility = repository.VisibilityPrivate
		if _, err := f.source.UpdateHostedRepository(ctx, private, f.repo.Version); err != nil {
			t.Fatal(err)
		}
		repo, _ := f.caches.GetHostedRepositoryByName(ctx, f.repo.Name)
		if repo.Visibility != repository.VisibilityPublic {
			t.Fatalf("ttl %v: the cache should still serve the entry inside the window", ttl)
		}
		f.now = f.now.Add(ttl)
		repo, _ = f.caches.GetHostedRepositoryByName(ctx, f.repo.Name)
		if repo.Visibility != repository.VisibilityPrivate {
			t.Fatalf("ttl %v: a lost notification left a stale public past the window", ttl)
		}
	}
	f := newFixture(t, 0)
	ctx := context.Background()
	f.resolveAll(t, ctx)
	f.resolveAll(t, ctx)
	if got := f.source.statements.Load(); got != 10 {
		t.Fatalf("a TTL of 0 must read through every time: %d statements, want 10", got)
	}
}

func TestManagementReadsBypassTheCaches(t *testing.T) {
	f := newFixture(t, 5*time.Second)
	ctx := readcache.Bypass(context.Background())
	f.resolveAll(t, ctx)
	f.resolveAll(t, ctx)
	if got := f.source.statements.Load(); got != 10 {
		t.Fatalf("a bypassed context must read through: %d statements, want 10", got)
	}
}

func TestNotFoundIsCachedBrieflyAndClearedByTheInvalidationOfAnyRepository(t *testing.T) {
	f := newFixture(t, 5*time.Second)
	ctx := context.Background()
	if _, err := f.caches.GetHostedRepositoryByName(ctx, "future"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("unknown name = %v", err)
	}
	before := f.source.statements.Load()
	for range 5 {
		if _, err := f.caches.GetHostedRepositoryByName(ctx, "future"); !errors.Is(err, repository.ErrNotFound) {
			t.Fatal(err)
		}
	}
	if f.source.statements.Load() != before {
		t.Fatal("a flood of unknown names must be absorbed by the negative cache")
	}
	created, err := f.source.CreateHostedRepository(ctx, repository.HostedRepository{ID: "33333333-3333-4333-8333-333333333333", Name: "future", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	f.registry.HandleNotification(readcache.Payload(repository.CacheScopeRepository, created.ID))
	if _, err = f.caches.GetHostedRepositoryByName(ctx, "future"); err != nil {
		t.Fatalf("a created repository must not stay hidden by a cached not-found: %v", err)
	}
}

func TestCachedValuesAreCopiesSoACallerCannotChangeWhatOthersRead(t *testing.T) {
	f := newFixture(t, 5*time.Second)
	ctx := context.Background()
	group, _ := f.caches.GetHostedGroupByName(ctx, f.group.Name)
	group.Members[0].RepositoryID = "tampered"
	again, _ := f.caches.GetHostedGroupByName(ctx, f.group.Name)
	if again.Members[0].RepositoryID != f.repo.ID {
		t.Fatalf("a caller edit leaked into the cache: %q", again.Members[0].RepositoryID)
	}
}
