package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

func (s *countingSource) GetUser(ctx context.Context, id string) (repository.User, error) {
	s.statements.Add(1)
	return s.MemoryStore.GetUser(ctx, id)
}

func (s *countingSource) GetUserByName(ctx context.Context, name string) (repository.User, error) {
	s.statements.Add(1)
	return s.MemoryStore.GetUserByName(ctx, name)
}

func (s *countingSource) GetRepositoryQuarantineReadPolicy(ctx context.Context, id string) (repository.RepositoryQuarantineReadPolicy, error) {
	s.statements.Add(1)
	return s.MemoryStore.GetRepositoryQuarantineReadPolicy(ctx, id)
}

func (s *countingSource) GetRepositoryGrants(ctx context.Context, id string) (repository.RepositoryGrantSet, error) {
	s.statements.Add(1)
	return s.MemoryStore.GetRepositoryGrants(ctx, id)
}

func (s *countingSource) GetWorkloadIssuer(ctx context.Context, id string) (repository.WorkloadIssuer, error) {
	s.statements.Add(1)
	return s.MemoryStore.GetWorkloadIssuer(ctx, id)
}

func (s *countingSource) GetWorkloadIssuerByIssuer(ctx context.Context, issuer string) (repository.WorkloadIssuer, error) {
	s.statements.Add(1)
	return s.MemoryStore.GetWorkloadIssuerByIssuer(ctx, issuer)
}

func (s *countingSource) ListActiveWorkloadTrustPolicies(ctx context.Context, issuerID string, now time.Time) ([]repository.WorkloadTrustPolicy, error) {
	s.statements.Add(1)
	return s.MemoryStore.ListActiveWorkloadTrustPolicies(ctx, issuerID, now)
}

const stateUserID = "44444444-4444-4444-8444-444444444444"

func (f *fixture) addUser(t *testing.T) repository.User {
	t.Helper()
	user, err := f.source.CreateUser(context.Background(), repository.User{ID: stateUserID, Name: "ada", Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	f.source.statements.Store(0)
	return user
}

func (f *fixture) disableUser(t *testing.T, user repository.User) {
	t.Helper()
	state := repository.UserDisabled
	if _, err := f.source.UpdateUser(context.Background(), repository.UserUpdate{ID: user.ID, State: &state}, user.Version); err != nil {
		t.Fatal(err)
	}
}

// A disabled user is effective on the next read once the scope is invalidated, and within the window when the
// notification is lost, at 5s, at the regulated 1s profile and at 0.
func TestDisabledUserIsEffectiveWithinTheWindow(t *testing.T) {
	for _, ttl := range []time.Duration{5 * time.Second, time.Second, 0} {
		f := newFixture(t, ttl)
		ctx := context.Background()
		user := f.addUser(t)
		byID, err := f.caches.GetUser(ctx, user.ID)
		if err != nil || byID.State != repository.UserActive {
			t.Fatalf("ttl %v: user by id = %q, %v", ttl, byID.State, err)
		}
		if byName, err := f.caches.GetUserByName(ctx, user.Name); err != nil || byName.State != repository.UserActive {
			t.Fatalf("ttl %v: user by name = %q, %v", ttl, byName.State, err)
		}
		f.disableUser(t, user)
		if ttl > 0 {
			if again, _ := f.caches.GetUser(ctx, user.ID); again.State != repository.UserActive {
				t.Fatalf("ttl %v: the entry must hold inside the window", ttl)
			}
			f.now = f.now.Add(ttl)
		}
		if again, err := f.caches.GetUser(ctx, user.ID); err != nil || again.State != repository.UserDisabled {
			t.Fatalf("ttl %v: a lost notification left the disabled user active past the window: %q, %v", ttl, again.State, err)
		}
		if again, err := f.caches.GetUserByName(ctx, user.Name); err != nil || again.State != repository.UserDisabled {
			t.Fatalf("ttl %v: by name past the window: %q, %v", ttl, again.State, err)
		}
	}
}

func TestUserNotificationEvictsBothLookupsAndCachesWarmReads(t *testing.T) {
	f := newFixture(t, 5*time.Second)
	ctx := context.Background()
	user := f.addUser(t)
	for range 20 {
		if _, err := f.caches.GetUser(ctx, user.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.caches.GetUserByName(ctx, user.Name); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.source.statements.Load(); got != 2 {
		t.Fatalf("40 reads of a warm user issued %d statements, want 2", got)
	}
	f.disableUser(t, user)
	f.registry.HandleNotification(readcache.Payload(repository.CacheScopeUser, user.ID))
	if got, _ := f.caches.GetUser(ctx, user.ID); got.State != repository.UserDisabled {
		t.Fatalf("by id after the notification = %q", got.State)
	}
	if got, _ := f.caches.GetUserByName(ctx, user.Name); got.State != repository.UserDisabled {
		t.Fatalf("by name after the notification = %q", got.State)
	}
}

func TestUnknownUserIsAbsorbedBrieflyAndSessionReadsBypass(t *testing.T) {
	f := newFixture(t, 5*time.Second)
	ctx := context.Background()
	for range 5 {
		if _, err := f.caches.GetUserByName(ctx, "nobody"); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("unknown user = %v", err)
		}
	}
	if got := f.source.statements.Load(); got != 1 {
		t.Fatalf("a flood of unknown names issued %d statements, want 1", got)
	}
	user := f.addUser(t)
	bypass := readcache.Bypass(ctx)
	f.disableUser(t, user)
	for range 3 {
		if got, err := f.caches.GetUser(bypass, user.ID); err != nil || got.State != repository.UserDisabled {
			t.Fatalf("a bypassed read must see the database: %q, %v", got.State, err)
		}
	}
	if got := f.source.statements.Load(); got != 3 {
		t.Fatalf("3 bypassed reads issued %d statements", got)
	}
}

func TestCachedUserIsACopy(t *testing.T) {
	f := newFixture(t, 5*time.Second)
	ctx := context.Background()
	user := f.addUser(t)
	first, _ := f.caches.GetUser(ctx, user.ID)
	first.PasswordChangedAt = nil
	if second, _ := f.caches.GetUser(ctx, user.ID); second.PasswordChangedAt != nil {
		t.Fatal("unexpected password time on a user without a password")
	}
	locked := time.Now().Add(time.Hour)
	first.LockedUntil = &locked
	if second, _ := f.caches.GetUser(ctx, user.ID); second.LockedUntil != nil {
		t.Fatal("a caller edit leaked into the cache")
	}
}

func (f *fixture) setQuarantine(t *testing.T, enabled bool) {
	t.Helper()
	current, err := f.source.MemoryStore.GetRepositoryQuarantineReadPolicy(context.Background(), f.repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.source.ReplaceRepositoryQuarantineReadPolicy(context.Background(), f.repo.ID, repository.RepositoryQuarantineReadPolicy{Enabled: enabled}, current.Version); err != nil {
		t.Fatal(err)
	}
}

func TestQuarantineReadPolicyChangeIsEffectiveWithinTheWindow(t *testing.T) {
	for _, ttl := range []time.Duration{5 * time.Second, time.Second, 0} {
		f := newFixture(t, ttl)
		ctx := context.Background()
		if policy, err := f.caches.GetRepositoryQuarantineReadPolicy(ctx, f.repo.ID); err != nil || policy.Enabled {
			t.Fatalf("ttl %v: initial policy %+v, %v", ttl, policy, err)
		}
		f.setQuarantine(t, true)
		if ttl > 0 {
			f.registry.HandleNotification(readcache.Payload(repository.CacheScopeQuarantine, "another-repository"))
			if policy, _ := f.caches.GetRepositoryQuarantineReadPolicy(ctx, f.repo.ID); policy.Enabled {
				t.Fatalf("ttl %v: the notification of another repository evicted this entry", ttl)
			}
			f.now = f.now.Add(ttl)
		}
		if policy, err := f.caches.GetRepositoryQuarantineReadPolicy(ctx, f.repo.ID); err != nil || !policy.Enabled {
			t.Fatalf("ttl %v: a lost notification left quarantine off past the window: %+v, %v", ttl, policy, err)
		}
	}
	f := newFixture(t, 5*time.Second)
	ctx := context.Background()
	_, _ = f.caches.GetRepositoryQuarantineReadPolicy(ctx, f.repo.ID)
	f.setQuarantine(t, true)
	f.registry.HandleNotification(readcache.Payload(repository.CacheScopeQuarantine, f.repo.ID))
	if policy, _ := f.caches.GetRepositoryQuarantineReadPolicy(ctx, f.repo.ID); !policy.Enabled {
		t.Fatal("the notification must evict the repository's policy")
	}
}

func (f *fixture) grant(t *testing.T, principal string) {
	t.Helper()
	if _, err := f.source.UpsertRepositoryGrant(context.Background(), f.repo.ID, repository.RepositoryGrant{Principal: principal, Scopes: []string{"read"}}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) grantPrincipals(t *testing.T, ctx context.Context) []string {
	t.Helper()
	set, err := f.caches.GetRepositoryGrants(ctx, f.repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	var principals []string
	for _, grant := range set.Grants {
		principals = append(principals, grant.Principal)
	}
	return principals
}

// A bumped grants version turns a cached grant set into a miss with no eviction of the grants scope: the
// version comes from the repository row, whose own invalidation (the row trigger of the version bump) reloads it.
func TestBumpedGrantsVersionIsAMiss(t *testing.T) {
	f := newFixture(t, 5*time.Second)
	ctx := context.Background()
	f.grant(t, "user:ada")
	f.source.statements.Store(0)
	if got := f.grantPrincipals(t, ctx); len(got) != 1 {
		t.Fatalf("grants = %v", got)
	}
	if cold := f.source.statements.Load(); cold != 2 {
		t.Fatalf("a cold grant lookup issued %d statements, want 2 (repository, grants)", cold)
	}
	for range 20 {
		f.grantPrincipals(t, ctx)
	}
	if warm := f.source.statements.Load(); warm != 2 {
		t.Fatalf("warm grant lookups issued %d statements in total, want 2", warm)
	}
	f.grant(t, "user:grace")
	f.registry.HandleNotification(readcache.Payload(repository.CacheScopeRepository, f.repo.ID))
	if got := f.grantPrincipals(t, ctx); len(got) != 2 {
		t.Fatalf("grants after the version bump = %v, want 2: a cached set outlived its version", got)
	}
}

// A removed grant is effective within the window with every notification dropped, at 1s and at 0.
func TestRemovedGrantIsEffectiveWithinTheWindow(t *testing.T) {
	for _, ttl := range []time.Duration{5 * time.Second, time.Second, 0} {
		f := newFixture(t, ttl)
		ctx := context.Background()
		f.grant(t, "user:ada")
		if got := f.grantPrincipals(t, ctx); len(got) != 1 {
			t.Fatalf("ttl %v: grants = %v", ttl, got)
		}
		if _, err := f.source.DeleteRepositoryGrant(ctx, f.repo.ID, "user:ada", ""); err != nil {
			t.Fatal(err)
		}
		if ttl > 0 {
			f.now = f.now.Add(ttl)
		}
		if got := f.grantPrincipals(t, ctx); len(got) != 0 {
			t.Fatalf("ttl %v: a removed grant survived the window: %v", ttl, got)
		}
	}
}

func TestGrantsNotificationEvictsTheSetAndManagementReadsBypass(t *testing.T) {
	f := newFixture(t, 5*time.Second)
	ctx := context.Background()
	f.grant(t, "user:ada")
	f.grantPrincipals(t, ctx)
	f.grant(t, "user:grace")
	f.registry.HandleNotification(readcache.Payload(repository.CacheScopeGrants, f.repo.ID))
	f.registry.HandleNotification(readcache.Payload(repository.CacheScopeRepository, f.repo.ID))
	if got := f.grantPrincipals(t, ctx); len(got) != 2 {
		t.Fatalf("grants after the notification = %v", got)
	}
	f.source.statements.Store(0)
	bypass := readcache.Bypass(ctx)
	for range 3 {
		if _, err := f.caches.GetRepositoryGrants(bypass, f.repo.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.source.statements.Load(); got != 3 {
		t.Fatalf("3 bypassed grant reads issued %d statements, want 3 (no repository read)", got)
	}
	if _, err := f.caches.GetRepositoryGrants(ctx, "55555555-5555-4555-8555-555555555555"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("grants of an unknown repository = %v", err)
	}
}

func TestCachedGrantSetIsACopy(t *testing.T) {
	f := newFixture(t, 5*time.Second)
	ctx := context.Background()
	f.grant(t, "user:ada")
	first, _ := f.caches.GetRepositoryGrants(ctx, f.repo.ID)
	first.Grants[0].Scopes[0] = "tampered"
	if second, _ := f.caches.GetRepositoryGrants(ctx, f.repo.ID); second.Grants[0].Scopes[0] != "read" {
		t.Fatalf("a caller edit leaked into the cache: %v", second.Grants[0].Scopes)
	}
}

// An authenticated protocol read on a warm cache issues no statement for user, group membership, repository,
// group, policy, quarantine and grant resolution (the acceptance bound is one).
func TestWarmAuthenticatedReadResolvesEverythingWithoutStatements(t *testing.T) {
	f := newFixture(t, 5*time.Second)
	ctx := context.Background()
	user, group := f.addGroupMember(t)
	f.grant(t, "user:ada")
	resolve := func() {
		memberships, err := f.caches.ListUserGroupMemberships(ctx, user.ID)
		if err != nil || len(memberships) != 1 || memberships[0].GroupID != group.ID {
			t.Fatalf("memberships = %v, %v", groupIDs(memberships), err)
		}
		f.resolveAll(t, ctx)
		if _, err := f.caches.GetUserByName(ctx, user.Name); err != nil {
			t.Fatal(err)
		}
		if _, err := f.caches.GetRepositoryQuarantineReadPolicy(ctx, f.repo.ID); err != nil {
			t.Fatal(err)
		}
		f.grantPrincipals(t, ctx)
	}
	resolve()
	cold := f.source.statements.Load()
	for range 50 {
		resolve()
	}
	if warm := f.source.statements.Load() - cold; warm > 1 {
		t.Fatalf("50 warm authenticated reads issued %d statements, want at most 1", warm)
	}
}

const workloadIssuerURL = "https://token.actions.example.test/org"

func (f *fixture) addWorkload(t *testing.T) (repository.WorkloadIssuer, repository.WorkloadTrustPolicy) {
	t.Helper()
	ctx := context.Background()
	issuer, err := f.source.CreateWorkloadIssuer(ctx, repository.WorkloadIssuer{
		Name: "gh-org", Kind: repository.WorkloadKindGitHubActions, Issuer: workloadIssuerURL,
		Audiences: []string{"https://notarx.example.test"}, JWKSMode: repository.WorkloadJWKSDiscovery})
	if err != nil {
		t.Fatal(err)
	}
	account, err := f.source.CreateServiceAccount(ctx, repository.ServiceAccount{ID: "66666666-6666-4666-8666-666666666666", Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := f.source.CreateWorkloadTrustPolicy(ctx, repository.WorkloadTrustPolicy{
		Name: "main", IssuerID: issuer.ID, ServiceAccountID: account.ID,
		Conditions: []byte(`[{"claim":["repository_id"],"op":"eq","values":["1296269"]}]`)})
	if err != nil {
		t.Fatal(err)
	}
	f.source.statements.Store(0)
	return issuer, policy
}

// A changed issuer and a disabled trust policy are effective on the next read after the notification and within
// the window when it is lost, at 5s, at 1s and at 0.
func TestWorkloadIssuerAndPolicyChangesAreEffectiveWithinTheWindow(t *testing.T) {
	for _, ttl := range []time.Duration{5 * time.Second, time.Second, 0} {
		f := newFixture(t, ttl)
		ctx := context.Background()
		issuer, policy := f.addWorkload(t)
		if got, err := f.caches.GetWorkloadIssuerByIssuer(ctx, workloadIssuerURL); err != nil || got.State != repository.WorkloadActive {
			t.Fatalf("ttl %v: issuer = %q, %v", ttl, got.State, err)
		}
		if got, err := f.caches.GetWorkloadIssuer(ctx, issuer.ID); err != nil || got.State != repository.WorkloadActive {
			t.Fatalf("ttl %v: issuer by id = %q, %v", ttl, got.State, err)
		}
		if got, err := f.caches.ListActiveWorkloadTrustPolicies(ctx, issuer.ID, f.now); err != nil || len(got) != 1 {
			t.Fatalf("ttl %v: policies = %v, %v", ttl, got, err)
		}
		issuer.State = repository.WorkloadDisabled
		if _, err := f.source.UpdateWorkloadIssuer(ctx, issuer, issuer.Version); err != nil {
			t.Fatal(err)
		}
		policy.State = repository.WorkloadDisabled
		if _, err := f.source.UpdateWorkloadTrustPolicy(ctx, policy, policy.Version); err != nil {
			t.Fatal(err)
		}
		if ttl > 0 {
			f.now = f.now.Add(ttl)
		}
		if got, _ := f.caches.GetWorkloadIssuerByIssuer(ctx, workloadIssuerURL); got.State != repository.WorkloadDisabled {
			t.Fatalf("ttl %v: a changed issuer survived the window: %q", ttl, got.State)
		}
		if got, _ := f.caches.GetWorkloadIssuer(ctx, issuer.ID); got.State != repository.WorkloadDisabled {
			t.Fatalf("ttl %v: a changed issuer by id survived the window: %q", ttl, got.State)
		}
		if got, _ := f.caches.ListActiveWorkloadTrustPolicies(ctx, issuer.ID, f.now); len(got) != 0 {
			t.Fatalf("ttl %v: a disabled policy survived the window: %v", ttl, got)
		}
	}
}

func TestWorkloadNotificationsAndPolicyExpiry(t *testing.T) {
	f := newFixture(t, 5*time.Second)
	ctx := context.Background()
	issuer, policy := f.addWorkload(t)
	expires := f.now.Add(time.Second)
	policy.ExpiresAt = &expires
	if _, err := f.source.UpdateWorkloadTrustPolicy(ctx, policy, policy.Version); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.caches.ListActiveWorkloadTrustPolicies(ctx, issuer.ID, f.now); len(got) != 1 {
		t.Fatalf("policies before expiry = %v", got)
	}
	f.source.statements.Store(0)
	f.now = f.now.Add(2 * time.Second) // inside the 5s window, past the policy expiry
	if got, _ := f.caches.ListActiveWorkloadTrustPolicies(ctx, issuer.ID, f.now); len(got) != 0 {
		t.Fatalf("an expired policy must stop matching at its expiry: %v", got)
	}
	if f.source.statements.Load() != 0 {
		t.Fatal("the expiry is applied on the cached list, not by a statement")
	}
	// A new policy of the issuer is not in the cached list: any policy notification flushes the cache.
	other, err := f.source.CreateWorkloadTrustPolicy(ctx, repository.WorkloadTrustPolicy{Name: "other", IssuerID: issuer.ID, ServiceAccountID: policy.ServiceAccountID, Conditions: policy.Conditions})
	if err != nil {
		t.Fatal(err)
	}
	f.registry.HandleNotification(readcache.Payload(repository.CacheScopeWorkloadPolicy, other.ID))
	if got, _ := f.caches.ListActiveWorkloadTrustPolicies(ctx, issuer.ID, f.now); len(got) != 1 || got[0].ID != other.ID {
		t.Fatalf("policies after the notification = %v", got)
	}
	// An unknown iss value is absorbed for a second and cleared by an issuer notification.
	if _, err := f.caches.GetWorkloadIssuerByIssuer(ctx, "https://unknown.example.test"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("unknown issuer = %v", err)
	}
	f.registry.HandleNotification(readcache.Payload(repository.CacheScopeWorkloadIssuer, issuer.ID))
	if f.caches.issuersByIss.Len() != 0 {
		t.Fatalf("issuer entries survived their notification: %d", f.caches.issuersByIss.Len())
	}
}
