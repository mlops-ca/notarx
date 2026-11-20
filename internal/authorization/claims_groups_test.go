package authorization

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/repository"
)

type recordingAudit struct {
	mu      sync.Mutex
	records []repository.AuditRecord
}

func (a *recordingAudit) RecordAudit(_ context.Context, record repository.AuditRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, record)
	return nil
}

type countingSyncer struct {
	*repository.MemoryStore
	calls int
	fail  bool
}

func (s *countingSyncer) SyncClaimsUserGroups(ctx context.Context, sync repository.ClaimsGroupSync, audit ...repository.AuditRecord) (int, error) {
	s.calls++
	if s.fail {
		return 0, errors.New("store down")
	}
	return s.MemoryStore.SyncClaimsUserGroups(ctx, sync, audit...)
}

const refreshProvider = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

func TestBearerTokensRefreshClaimsGroupsThrottledAndOnChange(t *testing.T) {
	store := repository.NewMemoryStore()
	ctx := context.Background()
	user, err := store.CreateUser(ctx, repository.User{ID: uuid.NewString(), Name: "ada", Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateUserIdentity(ctx, repository.UserIdentity{ID: uuid.NewString(), UserID: user.ID, Kind: repository.UserIdentityOIDC, ProviderID: refreshProvider, Issuer: "https://idp.example", Subject: "ada-sub"}); err != nil {
		t.Fatal(err)
	}
	engineers, err := store.CreateUserGroup(ctx, repository.UserGroup{DisplayName: "Engineers", Source: repository.UserGroupSourceClaims, ProviderID: refreshProvider, ExternalID: "engineers"})
	if err != nil {
		t.Fatal(err)
	}
	syncer := &countingSyncer{MemoryStore: store}
	audit := &recordingAudit{}
	refresher := NewClaimsGroupRefresher(syncer, audit, time.Minute)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	refresher.now = func() time.Time { return now }
	authenticator := Authenticator{Users: store, UserIdentities: store, UserGroups: store, ClaimsGroups: refresher}

	bearer := func(identity OIDCIdentity) Principal {
		t.Helper()
		identity.ProviderID, identity.ProviderSlug, identity.Subject = refreshProvider, "corp", "ada-sub"
		principal, ok := authenticator.oidcPrincipal(ctx, identity)
		if !ok {
			t.Fatal("bearer principal was refused")
		}
		return authenticator.WithUserGroups(ctx, principal)
	}

	// The first token applies its groups to the request that carries it.
	if principal := bearer(OIDCIdentity{Groups: []string{"engineers", "other"}}); len(principal.GroupIDs) != 1 || principal.GroupIDs[0] != engineers.ID {
		t.Fatalf("principal = %#v, want the claims group", principal)
	}
	// Unchanged groups inside the interval cost nothing; past it they are written again.
	bearer(OIDCIdentity{Groups: []string{"other", "engineers"}})
	bearer(OIDCIdentity{Groups: []string{"engineers", "other"}})
	if syncer.calls != 1 {
		t.Fatalf("sync calls = %d, want 1 inside the interval", syncer.calls)
	}
	now = now.Add(2 * time.Minute)
	bearer(OIDCIdentity{Groups: []string{"engineers"}})
	if syncer.calls != 2 {
		t.Fatalf("sync calls = %d, want a refresh past the interval", syncer.calls)
	}
	// A withdrawn group takes effect at once: the changed groups do not wait for the interval.
	if principal := bearer(OIDCIdentity{}); len(principal.GroupIDs) != 0 || syncer.calls != 3 {
		t.Fatalf("after the IdP withdrew the group: principal=%#v calls=%d", principal, syncer.calls)
	}
	// An overage asserts no groups and is audited once, not on every refresh.
	for range 3 {
		bearer(OIDCIdentity{Groups: []string{"engineers"}, GroupsOverage: true})
	}
	if len(audit.records) != 1 || audit.records[0].Operation != "auth.oidc.groups_overage" || audit.records[0].Resource != "auth/oidc/corp" {
		t.Fatalf("overage audit = %#v", audit.records)
	}
	if principal := bearer(OIDCIdentity{Groups: []string{"engineers"}, GroupsOverage: true}); len(principal.GroupIDs) != 0 {
		t.Fatalf("an overage token carried groups: %#v", principal)
	}

	// A failing store never fails the authentication and is retried on the next request.
	syncer.fail = true
	before := syncer.calls
	bearer(OIDCIdentity{Groups: []string{"engineers"}})
	bearer(OIDCIdentity{Groups: []string{"engineers"}})
	if syncer.calls != before+2 {
		t.Fatalf("failed syncs were not retried: %d calls", syncer.calls-before)
	}
}

func TestClaimsGroupRefresherIgnoresMissingInputsAndBoundsItsMemory(t *testing.T) {
	var nilRefresher *ClaimsGroupRefresher
	nilRefresher.Refresh(context.Background(), OIDCIdentity{ProviderID: "p"}, "u") // a nil refresher is a no-op

	syncer := &countingSyncer{MemoryStore: repository.NewMemoryStore()}
	refresher := NewClaimsGroupRefresher(syncer, nil, 0)
	if refresher.interval != DefaultClaimsGroupRefreshInterval {
		t.Fatalf("interval = %v", refresher.interval)
	}
	refresher.Refresh(context.Background(), OIDCIdentity{}, "u")
	refresher.Refresh(context.Background(), OIDCIdentity{ProviderID: "p"}, "")
	if syncer.calls != 0 {
		t.Fatalf("a refresh without a provider or a user wrote: %d calls", syncer.calls)
	}
	syncer.fail = true
	for i := range maxClaimsRefreshEntries + 5 {
		syncer.fail = false
		refresher.syncer = okSyncer{}
		refresher.Refresh(context.Background(), OIDCIdentity{ProviderID: "p"}, uuid.NewString()+string(rune(i%7)))
	}
	if len(refresher.last) > maxClaimsRefreshEntries {
		t.Fatalf("the refresher remembers %d accounts, want at most %d", len(refresher.last), maxClaimsRefreshEntries)
	}
}

type okSyncer struct{}

func (okSyncer) SyncClaimsUserGroups(context.Context, repository.ClaimsGroupSync, ...repository.AuditRecord) (int, error) {
	return 0, nil
}
