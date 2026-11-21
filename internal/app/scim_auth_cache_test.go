package app

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

// countingAuthStore counts the two lookups a SCIM request authenticates with.
type countingAuthStore struct {
	scimStore
	tokens, providers atomic.Int64
}

func (s *countingAuthStore) FindActiveSCIMTokenByHash(ctx context.Context, hash string) (repository.SCIMToken, error) {
	s.tokens.Add(1)
	return s.scimStore.FindActiveSCIMTokenByHash(ctx, hash)
}

func (s *countingAuthStore) GetIdentityProvider(ctx context.Context, id string) (identity.Provider, error) {
	s.providers.Add(1)
	return s.scimStore.GetIdentityProvider(ctx, id)
}

// recordingAudit keeps the audit records of the server under test.
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

func (a *recordingAudit) snapshot() []repository.AuditRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]repository.AuditRecord(nil), a.records...)
}

func scimCall(server *scimServer, method, path, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "https://notarx.example.test/scim/v2"+path, nil)
	if body != "" {
		request = httptest.NewRequest(method, "https://notarx.example.test/scim/v2"+path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/scim+json")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

func TestSCIMAuthenticationLookupsAreCachedForTheRevocationWindow(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	for name, tc := range map[string]struct {
		registry                  *readcache.Registry
		wantTokenReads, wantReads int64
	}{
		"cached":   {readcache.NewRegistry(5 * time.Second), 1, 1},
		"disabled": {readcache.NewRegistry(0), 5, 5},
		"no cache": {nil, 5, 5},
	} {
		t.Run(name, func(t *testing.T) {
			store := &countingAuthStore{scimStore: h.store}
			server := newSCIMServer(SCIMOptions{Enabled: true}, store, nil, tc.registry)
			for range 5 {
				if got := scimCall(server, http.MethodGet, "/Users", h.token, ""); got.Code != http.StatusOK {
					t.Fatalf("list = %d %s", got.Code, got.Body.String())
				}
			}
			if store.tokens.Load() != tc.wantTokenReads || store.providers.Load() != tc.wantReads {
				t.Fatalf("token lookups = %d, provider lookups = %d, want %d and %d", store.tokens.Load(), store.providers.Load(), tc.wantTokenReads, tc.wantReads)
			}
		})
	}
}

// A revoked token, a disabled provider and a deleted provider stop authenticating on the notification (the eviction a
// store trigger sends to every node) and, with the notification lost, when the revocation window ends.
func TestSCIMRevocationReachesCachedAuthentication(t *testing.T) {
	type change func(h *scimHarness)
	revoke := func(h *scimHarness) {
		tokens, err := h.store.ListSCIMTokens(t.Context(), h.provider.ID)
		if err != nil || len(tokens) != 1 {
			t.Fatalf("tokens = %v, %v", tokens, err)
		}
		if _, err := h.store.RevokeSCIMToken(t.Context(), h.provider.ID, tokens[0].ID); err != nil {
			t.Fatal(err)
		}
	}
	disable := func(h *scimHarness) {
		current, err := h.store.GetIdentityProvider(t.Context(), h.provider.ID)
		if err != nil {
			t.Fatal(err)
		}
		current.Enabled = false
		if _, err := h.store.ReplaceIdentityProvider(t.Context(), current, current.Version); err != nil {
			t.Fatal(err)
		}
	}
	remove := func(h *scimHarness) {
		current, err := h.store.GetIdentityProvider(t.Context(), h.provider.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.store.DeleteIdentityProvider(t.Context(), current.ID, current.Version); err != nil {
			t.Fatal(err)
		}
	}
	scopes := map[string]string{"revoke": repository.CacheScopeSCIMToken, "disable": repository.CacheScopeIdentityProvider, "delete": repository.CacheScopeIdentityProvider}
	for name, apply := range map[string]change{"revoke": revoke, "disable": disable, "delete": remove} {
		for _, notified := range []bool{true, false} {
			label := name + "/notified"
			if !notified {
				label = name + "/notification lost"
			}
			t.Run(label, func(t *testing.T) {
				h := newSCIMHarness(t, SCIMOptions{Enabled: true})
				now := time.Now()
				registry := readcache.NewRegistry(5 * time.Second)
				registry.SetClock(func() time.Time { return now })
				server := newSCIMServer(SCIMOptions{Enabled: true}, h.store, nil, registry)
				if got := scimCall(server, http.MethodGet, "/Users", h.token, ""); got.Code != http.StatusOK {
					t.Fatalf("before = %d", got.Code)
				}
				apply(h)
				if got := scimCall(server, http.MethodGet, "/Users", h.token, ""); got.Code != http.StatusOK {
					t.Fatalf("inside the window without a notification = %d, want the cached 200", got.Code)
				}
				if notified {
					registry.Invalidate(scopes[name], "")
				} else {
					now = now.Add(5*time.Second + time.Millisecond)
				}
				if got := scimCall(server, http.MethodGet, "/Users", h.token, ""); got.Code != http.StatusUnauthorized {
					t.Fatalf("after the revocation = %d %s, want 401", got.Code, got.Body.String())
				}
			})
		}
	}
}

func TestSCIMTokenThatExpiredInsideTheWindowIsReloadedNotServed(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	short := h.createToken(h.provider.ID, "short", time.Minute)
	registry := readcache.NewRegistry(time.Hour)
	now := time.Now()
	registry.SetClock(func() time.Time { return now })
	store := &countingAuthStore{scimStore: h.store}
	server := newSCIMServer(SCIMOptions{Enabled: true}, store, nil, registry)
	for range 2 {
		if got := scimCall(server, http.MethodGet, "/Users", short, ""); got.Code != http.StatusOK {
			t.Fatalf("before expiry = %d", got.Code)
		}
	}
	if store.tokens.Load() != 1 {
		t.Fatalf("token lookups before expiry = %d, want 1", store.tokens.Load())
	}
	now = now.Add(2 * time.Minute) // the token expired, the entry is still inside the cache window
	scimCall(server, http.MethodGet, "/Users", short, "")
	if store.tokens.Load() != 2 {
		t.Fatalf("an expired token was served from the cache: %d lookups", store.tokens.Load())
	}
}

func TestSCIMAuditOfPureReadsIsBatchedAndEveryCallIsStillAudited(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	audit := &recordingAudit{}
	server := newSCIMServer(SCIMOptions{Enabled: true}, h.store, audit, readcache.NewRegistry(5*time.Second))
	calls := []struct {
		method, path, token, body string
		status                    int
		class                     repository.AuditClass
	}{
		{http.MethodGet, "/Users", h.token, "", http.StatusOK, repository.AuditClassAsync},
		{http.MethodGet, "/Groups", h.token, "", http.StatusOK, repository.AuditClassAsync},
		{http.MethodGet, "/ServiceProviderConfig", h.token, "", http.StatusOK, repository.AuditClassAsync},
		{http.MethodGet, "/Users/does-not-exist", h.token, "", http.StatusNotFound, repository.AuditClassSync},
		{http.MethodPost, "/Users", h.token, `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"audited"}`, http.StatusCreated, repository.AuditClassSync},
		{http.MethodGet, "/Users", "scim_not-a-token", "", http.StatusUnauthorized, repository.AuditClassSync},
	}
	for _, call := range calls {
		if got := scimCall(server, call.method, call.path, call.token, call.body); got.Code != call.status {
			t.Fatalf("%s %s = %d %s, want %d", call.method, call.path, got.Code, got.Body.String(), call.status)
		}
	}
	records := audit.snapshot()
	// The created user's record is written inside the store transaction, so the recorder of the server holds the other five.
	if len(records) < len(calls)-1 {
		t.Fatalf("audit records = %d, want one per call: %+v", len(records), records)
	}
	classes := map[string]repository.AuditClass{}
	for _, record := range records {
		classes[record.Operation+" "+record.Resource] = record.Class
	}
	for _, record := range records[:3] {
		if record.Class != repository.AuditClassAsync || record.Status != http.StatusOK {
			t.Fatalf("a successful read was audited as %+v, want async", record)
		}
	}
	for _, record := range records[3:] {
		if record.Class != repository.AuditClassSync {
			t.Fatalf("%s %s status %d was audited as %s, want sync", record.Operation, record.Resource, record.Status, record.Class)
		}
	}
}

// roundTripStore adds a fixed round trip to the two authentication lookups, standing in for a database on the network.
type roundTripStore struct {
	scimStore
	delay time.Duration
}

func (s roundTripStore) FindActiveSCIMTokenByHash(ctx context.Context, hash string) (repository.SCIMToken, error) {
	time.Sleep(s.delay)
	return s.scimStore.FindActiveSCIMTokenByHash(ctx, hash)
}

func (s roundTripStore) GetIdentityProvider(ctx context.Context, id string) (identity.Provider, error) {
	time.Sleep(s.delay)
	return s.scimStore.GetIdentityProvider(ctx, id)
}

// BenchmarkSCIMAuthenticate measures the authentication of one SCIM request against a store that costs a 200 microsecond
// round trip per lookup, with and without the cache; the numbers are recorded in docs/performance-baseline.md.
func BenchmarkSCIMAuthenticate(b *testing.B) {
	for _, cached := range []bool{false, true} {
		name := "uncached"
		if cached {
			name = "cached"
		}
		b.Run(name, func(b *testing.B) {
			store := repository.NewMemoryStore()
			provider, err := store.CreateIdentityProvider(b.Context(), identity.Provider{ID: uuid.NewString(), Slug: "bench", Kind: identity.KindOIDC, DisplayName: "Bench", Enabled: true})
			if err != nil {
				b.Fatal(err)
			}
			token := scimTokenPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
			if _, err := store.CreateSCIMToken(b.Context(), repository.SCIMToken{ProviderID: provider.ID, Name: "bench", SecretHash: authorization.HashAPIKey(token), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				b.Fatal(err)
			}
			var registry *readcache.Registry
			if cached {
				registry = readcache.NewRegistry(5 * time.Second)
			}
			counting := &countingAuthStore{scimStore: roundTripStore{scimStore: store, delay: 200 * time.Microsecond}}
			server := newSCIMServer(SCIMOptions{Enabled: true}, counting, nil, registry)
			request := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			b.ResetTimer()
			for range b.N {
				if _, failure := server.authenticate(request); failure != nil {
					b.Fatal(failure)
				}
			}
			b.ReportMetric(float64(counting.tokens.Load()+counting.providers.Load())/float64(b.N), "lookups/op")
		})
	}
}
