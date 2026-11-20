package access

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

func (s *countingSource) ListUserGroupMemberships(ctx context.Context, userID string) ([]repository.UserGroupMembership, error) {
	s.statements.Add(1)
	return s.MemoryStore.ListUserGroupMemberships(ctx, userID)
}

func (f *fixture) addGroupMember(t *testing.T) (repository.User, repository.UserGroup) {
	t.Helper()
	ctx := context.Background()
	user := f.addUser(t)
	group, err := f.source.CreateUserGroup(ctx, repository.UserGroup{ID: uuid.NewString(), DisplayName: "team", Source: repository.UserGroupSourceLocal})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.source.AddUserGroupMember(ctx, group.ID, user.ID, repository.UserGroupSourceLocal); err != nil {
		t.Fatal(err)
	}
	f.source.statements.Store(0)
	return user, group
}

func groupIDs(memberships []repository.UserGroupMembership) []string {
	ids := make([]string, 0, len(memberships))
	for _, membership := range memberships {
		ids = append(ids, membership.GroupID)
	}
	return ids
}

// A membership removed on node A is denied on node B within NOTARX_AUTHZ_CACHE_TTL even when the NOTIFY is lost, at
// the default 5s, the regulated 1s and the top of the range 30s; with the TTL at 0 the cache is bypassed.
func TestRemovedMembershipIsEffectiveWithinTheWindowWhenTheNotificationIsLost(t *testing.T) {
	for _, ttl := range []time.Duration{5 * time.Second, time.Second, 30 * time.Second, 0} {
		f := newFixture(t, ttl)
		ctx := context.Background()
		user, group := f.addGroupMember(t)
		got, err := f.caches.ListUserGroupMemberships(ctx, user.ID)
		if err != nil || len(got) != 1 || got[0].GroupID != group.ID {
			t.Fatalf("ttl %v: memberships = %v, %v", ttl, got, err)
		}
		if err := f.source.RemoveUserGroupMember(ctx, group.ID, user.ID); err != nil {
			t.Fatal(err)
		}
		if ttl > 0 {
			if again, _ := f.caches.ListUserGroupMemberships(ctx, user.ID); len(again) != 1 {
				t.Fatalf("ttl %v: the entry must hold inside the window, got %v", ttl, groupIDs(again))
			}
			f.now = f.now.Add(ttl)
		}
		if again, err := f.caches.ListUserGroupMemberships(ctx, user.ID); err != nil || len(again) != 0 {
			t.Fatalf("ttl %v: a lost notification left the revoked membership past the window: %v, %v", ttl, groupIDs(again), err)
		}
	}
}

func TestZeroTTLBypassesTheGroupCacheAndABypassContextReadsThrough(t *testing.T) {
	f := newFixture(t, 0)
	ctx := context.Background()
	user, _ := f.addGroupMember(t)
	for range 3 {
		if _, err := f.caches.ListUserGroupMemberships(ctx, user.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.source.statements.Load(); got != 3 {
		t.Fatalf("TTL 0 issued %d statements for 3 reads, want 3", got)
	}

	warm := newFixture(t, 5*time.Second)
	user, _ = warm.addGroupMember(t)
	for range 3 {
		if _, err := warm.caches.ListUserGroupMemberships(readcache.Bypass(ctx), user.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got := warm.source.statements.Load(); got != 3 {
		t.Fatalf("bypassed reads issued %d statements, want 3", got)
	}
}

func TestMembershipNotificationEvictsTheUserAndAGroupChangeFlushesTheScope(t *testing.T) {
	f := newFixture(t, 5*time.Second)
	ctx := context.Background()
	user, group := f.addGroupMember(t)
	other, err := f.source.CreateUser(ctx, repository.User{ID: uuid.NewString(), Name: "grace", Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.source.AddUserGroupMember(ctx, group.ID, other.ID, repository.UserGroupSourceLocal); err != nil {
		t.Fatal(err)
	}
	f.source.statements.Store(0)
	for range 20 {
		for _, id := range []string{user.ID, other.ID} {
			if _, err := f.caches.ListUserGroupMemberships(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := f.source.statements.Load(); got != 2 {
		t.Fatalf("40 reads of two warm accounts issued %d statements, want 2", got)
	}

	// A membership change announces the user id: only that account is evicted.
	if err := f.source.RemoveUserGroupMember(ctx, group.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	f.registry.HandleNotification(readcache.Payload(repository.CacheScopeUserGroup, user.ID))
	if got, _ := f.caches.ListUserGroupMemberships(ctx, user.ID); len(got) != 0 {
		t.Fatalf("the evicted account still resolves %v", groupIDs(got))
	}
	if got, _ := f.caches.ListUserGroupMemberships(ctx, other.ID); len(got) != 1 {
		t.Fatalf("the other account lost its group: %v", groupIDs(got))
	}

	// A change to the group row (its version is bumped) announces the whole scope: every cached entry is a miss.
	before := f.source.statements.Load()
	fetched, err := f.source.GetUserGroup(ctx, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.source.SetUserGroupGlobalRole(ctx, group.ID, repository.UserGroupRoleAdmin, fetched.Version); err != nil {
		t.Fatal(err)
	}
	f.registry.HandleNotification(readcache.Payload(repository.CacheScopeUserGroup, ""))
	got, err := f.caches.ListUserGroupMemberships(ctx, other.ID)
	if err != nil || len(got) != 1 || got[0].GlobalRole != repository.UserGroupRoleAdmin {
		t.Fatalf("after the group changed: %v, %v", got, err)
	}
	if f.source.statements.Load() == before {
		t.Fatal("the flushed entry was served instead of being loaded again")
	}
}

func TestACachedMembershipListIsACopy(t *testing.T) {
	f := newFixture(t, 5*time.Second)
	ctx := context.Background()
	user, group := f.addGroupMember(t)
	first, _ := f.caches.ListUserGroupMemberships(ctx, user.ID)
	first[0].GroupID = "tampered"
	if again, _ := f.caches.ListUserGroupMemberships(ctx, user.ID); again[0].GroupID != group.ID {
		t.Fatalf("a caller changed the cached entry: %v", groupIDs(again))
	}
}
