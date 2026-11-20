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
