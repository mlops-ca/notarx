//go:build integration

package access

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

func openNode(t *testing.T, ttl time.Duration, listen bool) (*repository.PostgresStore, *ReadCaches) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := repository.NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	registry := readcache.NewRegistry(ttl)
	store.SetCacheInvalidator(registry.Invalidate)
	if listen {
		notifications, err := database.OpenPostgres(databaseURL, database.NotificationPoolConfig())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = notifications.Close() })
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		connected := make(chan struct{}, 1)
		connect := readcache.PostgresConnect(notifications)
		listener := &readcache.Listener{Registry: registry, Connect: func(ctx context.Context) (readcache.Stream, error) {
			stream, err := connect(ctx)
			if err == nil {
				select {
				case connected <- struct{}{}:
				default:
				}
			}
			return stream, err
		}}
		go listener.Run(ctx)
		<-connected
	}
	return store, NewReadCaches(store, registry)
}

func publicRepository(t *testing.T, store *repository.PostgresStore) repository.HostedRepository {
	t.Helper()
	repo, err := store.CreateHostedRepository(context.Background(), repository.HostedRepository{ID: uuid.NewString(), Name: "cache-" + uuid.NewString(), Format: repository.FormatRaw, Visibility: repository.VisibilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func makePrivate(t *testing.T, store *repository.PostgresStore, repo repository.HostedRepository) {
	t.Helper()
	repo.Visibility = repository.VisibilityPrivate
	if _, err := store.UpdateHostedRepository(context.Background(), repo, repo.Version); err != nil {
		t.Fatal(err)
	}
}

// A visibility change made on node A reaches node B through the row trigger's NOTIFY long before the window.
func TestPostgresVisibilityChangeReachesOtherNodeThroughNotify(t *testing.T) {
	nodeA, _ := openNode(t, 30*time.Second, false)
	_, cachesB := openNode(t, 30*time.Second, true) // far above the test deadline: only NOTIFY can evict
	ctx := context.Background()
	repo := publicRepository(t, nodeA)
	if got, err := cachesB.GetHostedRepositoryByName(ctx, repo.Name); err != nil || got.Visibility != repository.VisibilityPublic {
		t.Fatalf("node B read %q, %v", got.Visibility, err)
	}
	makePrivate(t, nodeA, repo)
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := cachesB.GetHostedRepositoryByName(ctx, repo.Name)
		if err == nil && got.Visibility == repository.VisibilityPrivate {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("node B still serves the public repository: the notification did not evict it")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// With the notification dropped (node B has no listener) the window ends the stale public at the regulated
// 1s profile, and a TTL of 0 makes the change effective on the next read.
func TestPostgresVisibilityChangeEndsWithinTheWindowWhenNotificationIsDropped(t *testing.T) {
	for _, ttl := range []time.Duration{time.Second, 0} {
		nodeA, _ := openNode(t, ttl, false)
		_, cachesB := openNode(t, ttl, false)
		ctx := context.Background()
		repo := publicRepository(t, nodeA)
		if got, err := cachesB.GetHostedRepositoryByName(ctx, repo.Name); err != nil || got.Visibility != repository.VisibilityPublic {
			t.Fatalf("ttl %v: node B read %q, %v", ttl, got.Visibility, err)
		}
		makePrivate(t, nodeA, repo)
		deadline := time.Now().Add(ttl + 500*time.Millisecond)
		for {
			got, err := cachesB.GetHostedRepositoryByName(ctx, repo.Name)
			if err == nil && got.Visibility == repository.VisibilityPrivate {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("ttl %v: a stale public outlived the authorization window", ttl)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// A visibility change of a member repository evicts the repository and keeps the cached groups that contain
// it: a group entry holds member ids only, and the next read of the member sees the new visibility.
func TestPostgresMemberVisibilityChangeEvictsTheRepositoryNotItsGroups(t *testing.T) {
	// The fixtures are written before the listening node opens: their own notifications would otherwise
	// arrive after the reads below and evict the entries this test inspects.
	creator, _ := openNode(t, 30*time.Second, false)
	ctx := context.Background()
	repo := publicRepository(t, creator)
	group, _, err := creator.CreateHostedGroupIdempotently(ctx, repository.HostedGroup{ID: uuid.NewString(), Name: "cache-group-" + uuid.NewString(), Format: repository.FormatRaw, Visibility: repository.VisibilityPublic, Members: []repository.GroupMember{{RepositoryID: repo.ID}}}, "actor", uuid.NewString(), "payload")
	if err != nil {
		t.Fatal(err)
	}
	store, caches := openNode(t, 30*time.Second, true)
	if _, err = caches.GetHostedGroupByName(ctx, group.Name); err != nil {
		t.Fatal(err)
	}
	if caches.groupsByName.Len() != 1 {
		t.Fatalf("group entry not cached: %d", caches.groupsByName.Len())
	}
	if _, err = caches.GetHostedRepository(ctx, repo.ID); err != nil {
		t.Fatal(err)
	}
	makePrivate(t, store, repo)
	if caches.groupsByName.Len() != 1 {
		t.Fatalf("a member visibility change evicted the group entry: %d", caches.groupsByName.Len())
	}
	if caches.reposByID.Len() != 0 {
		t.Fatalf("the member entry survived its visibility change: %d", caches.reposByID.Len())
	}
	if got, err := caches.GetHostedRepository(ctx, repo.ID); err != nil || got.Visibility != repository.VisibilityPrivate {
		t.Fatalf("member after its visibility change = %q, %v", got.Visibility, err)
	}
}

// accountFixtures are the rows the authenticated read path resolves: a user, a repository with one grant and a
// repository quarantine policy. They are written through node A.
type accountFixtures struct {
	repo repository.HostedRepository
	user repository.User
}

func newAccountFixtures(t *testing.T, creator *repository.PostgresStore) accountFixtures {
	t.Helper()
	ctx := context.Background()
	repo := publicRepository(t, creator)
	user, err := creator.CreateUser(ctx, repository.User{ID: uuid.NewString(), Name: "cache-" + uuid.NewString(), Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = creator.UpsertRepositoryGrant(ctx, repo.ID, repository.RepositoryGrant{Principal: "user:" + user.Name, Scopes: []string{"read"}}); err != nil {
		t.Fatal(err)
	}
	return accountFixtures{repo: repo, user: user}
}

// mutate disables the user, removes the grant and turns the quarantine read policy on, each in its own
// transaction on node A.
func (f accountFixtures) mutate(t *testing.T, node *repository.PostgresStore) {
	t.Helper()
	ctx := context.Background()
	disabled := repository.UserDisabled
	if _, err := node.UpdateUser(ctx, repository.UserUpdate{ID: f.user.ID, State: &disabled}, f.user.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := node.DeleteRepositoryGrant(ctx, f.repo.ID, "user:"+f.user.Name, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := node.ReplaceRepositoryQuarantineReadPolicy(ctx, f.repo.ID, repository.RepositoryQuarantineReadPolicy{Enabled: true}, "1"); err != nil {
		t.Fatal(err)
	}
}

// effective reports whether node B's caches serve the mutated state.
func (f accountFixtures) effective(t *testing.T, caches *ReadCaches) bool {
	t.Helper()
	ctx := context.Background()
	user, err := caches.GetUser(ctx, f.user.ID)
	if err != nil || user.State != repository.UserDisabled {
		return false
	}
	set, err := caches.GetRepositoryGrants(ctx, f.repo.ID)
	if err != nil || len(set.Grants) != 0 {
		return false
	}
	policy, err := caches.GetRepositoryQuarantineReadPolicy(ctx, f.repo.ID)
	return err == nil && policy.Enabled
}

// warm loads every entry into node B.
func (f accountFixtures) warm(t *testing.T, caches *ReadCaches) {
	t.Helper()
	ctx := context.Background()
	if user, err := caches.GetUser(ctx, f.user.ID); err != nil || user.State != repository.UserActive {
		t.Fatalf("node B read the user as %q, %v", user.State, err)
	}
	if _, err := caches.GetUserByName(ctx, f.user.Name); err != nil {
		t.Fatal(err)
	}
	if set, err := caches.GetRepositoryGrants(ctx, f.repo.ID); err != nil || len(set.Grants) != 1 {
		t.Fatalf("node B read %d grants, %v", len(set.Grants), err)
	}
	if policy, err := caches.GetRepositoryQuarantineReadPolicy(ctx, f.repo.ID); err != nil || policy.Enabled {
		t.Fatalf("node B read quarantine %+v, %v", policy, err)
	}
}

// A disabled user, a removed grant and a quarantine change made on node A reach node B through the row
// triggers' NOTIFY long before the window.
func TestPostgresAccountStateChangesReachOtherNodeThroughNotify(t *testing.T) {
	nodeA, _ := openNode(t, 30*time.Second, false)
	fixtures := newAccountFixtures(t, nodeA)
	_, cachesB := openNode(t, 30*time.Second, true) // far above the test deadline: only NOTIFY can evict
	fixtures.warm(t, cachesB)
	fixtures.mutate(t, nodeA)
	deadline := time.Now().Add(10 * time.Second)
	for !fixtures.effective(t, cachesB) {
		if time.Now().After(deadline) {
			t.Fatal("node B still serves the old account state: a notification did not evict it")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// With the notification dropped (node B has no listener) the window ends the stale state at the regulated 1s
// profile, and a TTL of 0 makes the change effective on the next read.
func TestPostgresAccountStateChangesEndWithinTheWindowWhenNotificationIsDropped(t *testing.T) {
	for _, ttl := range []time.Duration{time.Second, 0} {
		nodeA, _ := openNode(t, ttl, false)
		fixtures := newAccountFixtures(t, nodeA)
		_, cachesB := openNode(t, ttl, false)
		fixtures.warm(t, cachesB)
		fixtures.mutate(t, nodeA)
		deadline := time.Now().Add(ttl + 500*time.Millisecond)
		for !fixtures.effective(t, cachesB) {
			if time.Now().After(deadline) {
				t.Fatalf("ttl %v: stale account state outlived the authorization window", ttl)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// Every grant mutation bumps hosted_repositories.grants_version in its transaction, and the cached repository
// row carries it.
func TestPostgresGrantMutationsBumpGrantsVersion(t *testing.T) {
	store, _ := openNode(t, time.Second, false)
	ctx := context.Background()
	repo := publicRepository(t, store)
	if repo.GrantsVersion != 1 {
		t.Fatalf("a new repository starts at grants_version 1, got %d", repo.GrantsVersion)
	}
	want := repo.GrantsVersion
	read := func() int64 {
		current, err := store.GetHostedRepository(ctx, repo.ID)
		if err != nil {
			t.Fatal(err)
		}
		return current.GrantsVersion
	}
	grant := repository.RepositoryGrant{Principal: "user:ada", Scopes: []string{"read"}}
	if _, err := store.UpsertRepositoryGrant(ctx, repo.ID, grant); err != nil {
		t.Fatal(err)
	}
	want++
	if got := read(); got != want {
		t.Fatalf("upsert: grants_version = %d, want %d", got, want)
	}
	set, err := store.GetRepositoryGrants(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReplaceRepositoryGrants(ctx, repo.ID, []repository.RepositoryGrant{grant}, set.Version); err != nil {
		t.Fatal(err)
	}
	want++
	if got := read(); got != want {
		t.Fatalf("replace: grants_version = %d, want %d", got, want)
	}
	if _, err = store.DeleteRepositoryGrant(ctx, repo.ID, grant.Principal, ""); err != nil {
		t.Fatal(err)
	}
	want++
	if got := read(); got != want {
		t.Fatalf("delete: grants_version = %d, want %d", got, want)
	}
}
