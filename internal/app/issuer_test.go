package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/secrets"
	"github.com/mlops-ca/notarx/internal/tokenissuer"
)

type countingKeyStore struct {
	*tokenissuer.MemoryStore
	lists atomic.Int64
}

func (s *countingKeyStore) ListKeys(ctx context.Context) ([]tokenissuer.KeyRecord, error) {
	s.lists.Add(1)
	return s.MemoryStore.ListKeys(ctx)
}

func (s *countingKeyStore) ListPublicKeys(ctx context.Context) ([]tokenissuer.KeyRecord, error) {
	s.lists.Add(1)
	return s.MemoryStore.ListPublicKeys(ctx)
}

func issuerTestConfig() config.IssuerConfig {
	return config.IssuerConfig{
		Enabled: true, URL: "https://issuer.example.test", Alg: jose.ES256,
		KeyRotationInterval: 720 * time.Hour, KeyPublishLead: 24 * time.Hour, MaxTokenLifetime: 10 * time.Minute,
	}
}

func newIssuerFixture(t *testing.T) (*IssuerRuntime, *countingKeyStore, http.Handler) {
	t.Helper()
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	t.Setenv(secrets.KeyPreviousEnv, "")
	store := &countingKeyStore{MemoryStore: tokenissuer.NewMemoryStore()}
	runtime, err := NewIssuerRuntime(issuerTestConfig(), store, "deployment")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtime.Manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	mux := newRuntimeLogMux(nil)
	registerIssuerRoutes(mux, runtime)
	return runtime, store, mux
}

func issuerGet(handler http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.RemoteAddr = "192.0.2.10:4000"
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestIssuerRoutesAnswer404WhenDisabled(t *testing.T) {
	disabled := issuerTestConfig()
	disabled.Enabled = false
	runtime, err := NewIssuerRuntime(disabled, tokenissuer.NewMemoryStore(), "id")
	if err != nil || runtime != nil {
		t.Fatalf("a disabled issuer builds nothing: %v %v", runtime, err)
	}
	runtime.StartRotation(t.Context()) // nil-safe, runs nothing
	mux := newRuntimeLogMux(nil)
	registerIssuerRoutes(mux, runtime)
	for _, path := range []string{issuerDiscoveryPath, issuerJWKSPath} {
		if got := issuerGet(mux, path, nil).Code; got != http.StatusNotFound {
			t.Errorf("%s = %d", path, got)
		}
	}
}

func TestIssuerDiscoveryDocument(t *testing.T) {
	_, _, mux := newIssuerFixture(t)
	response := issuerGet(mux, issuerDiscoveryPath, nil)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code)
	}
	for header, want := range map[string]string{"Cache-Control": "public, max-age=300", "Content-Type": "application/json", "Access-Control-Allow-Origin": "*"} {
		if got := response.Header().Get(header); got != want {
			t.Errorf("%s = %q want %q", header, got, want)
		}
	}
	var document map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document["issuer"] != "https://issuer.example.test" || document["jwks_uri"] != "https://issuer.example.test/.well-known/jwks.json" ||
		document["response_types_supported"].([]any)[0] != "id_token" || document["subject_types_supported"].([]any)[0] != "public" {
		t.Fatalf("%v", document)
	}
	for _, forbidden := range []string{"authorization_endpoint", "token_endpoint"} {
		if _, ok := document[forbidden]; ok {
			t.Errorf("%s must not be advertised", forbidden)
		}
	}
	if algs := document["id_token_signing_alg_values_supported"].([]any); len(algs) != 2 || algs[0] != "RS256" || algs[1] != "ES256" {
		t.Fatalf("%v", algs)
	}
}

func TestIssuerJWKSConditionalGetAndHeaders(t *testing.T) {
	_, _, mux := newIssuerFixture(t)
	for _, path := range []string{issuerDiscoveryPath, issuerJWKSPath} {
		first := issuerGet(mux, path, nil)
		etag := first.Header().Get("ETag")
		if first.Code != http.StatusOK || !strings.HasPrefix(etag, `"`) || strings.HasPrefix(etag, "W/") {
			t.Fatalf("%s: %d etag=%q", path, first.Code, etag)
		}
		for _, match := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
			second := issuerGet(mux, path, map[string]string{"If-None-Match": match})
			if second.Code != http.StatusNotModified || second.Body.Len() != 0 || second.Header().Get("ETag") != etag || second.Header().Get("Cache-Control") != "public, max-age=300" {
				t.Fatalf("%s If-None-Match %q: %d body=%d", path, match, second.Code, second.Body.Len())
			}
		}
		if got := issuerGet(mux, path, map[string]string{"If-None-Match": `"different"`}).Code; got != http.StatusOK {
			t.Fatalf("%s: %d", path, got)
		}
	}
	var set struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(issuerGet(mux, issuerJWKSPath, nil).Body.Bytes(), &set); err != nil || len(set.Keys) != 2 {
		t.Fatalf("keys=%v err=%v", set.Keys, err)
	}
	for _, key := range set.Keys {
		if key["use"] != "sig" || key["alg"] != "ES256" || key["kty"] != "EC" || key["kid"] == "" || key["crv"] != "P-256" || key["d"] != nil {
			t.Fatalf("%v", key)
		}
	}
	head := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodHead, issuerJWKSPath, nil)
	request.RemoteAddr = "192.0.2.11:1"
	mux.ServeHTTP(head, request)
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("head: %d %d", head.Code, head.Body.Len())
	}
	post := httptest.NewRecorder()
	mux.ServeHTTP(post, httptest.NewRequest(http.MethodPost, issuerJWKSPath, nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("only GET is served: %d", post.Code)
	}
}

func TestIssuerAnonymousRequestsDoNotQueryTheStorePerRequest(t *testing.T) {
	_, store, mux := newIssuerFixture(t)
	store.lists.Store(0)
	for range 20 {
		if got := issuerGet(mux, issuerJWKSPath, nil).Code; got != http.StatusOK {
			t.Fatal(got)
		}
		issuerGet(mux, issuerDiscoveryPath, nil)
	}
	if got := store.lists.Load(); got > 1 {
		t.Fatalf("the store was queried %d times for 40 anonymous requests", got)
	}
}

func TestIssuerRotationInvalidatesTheJWKSCache(t *testing.T) {
	runtime, store, mux := newIssuerFixture(t)
	before := issuerGet(mux, issuerJWKSPath, nil).Header().Get("ETag")
	records, _ := store.ListKeys(t.Context())
	if err := runtime.Manager.Revoke(t.Context(), records[0].KID); err != nil {
		t.Fatal(err)
	}
	after := issuerGet(mux, issuerJWKSPath, nil).Header().Get("ETag")
	if before == after {
		t.Fatal("a key change must invalidate the cached document at once")
	}
}

func TestIssuerJWKSOmitsRowsFailingIntegrity(t *testing.T) {
	runtime, store, mux := newIssuerFixture(t)
	records, _ := store.ListKeys(t.Context())
	forged := records[0]
	forged.IntegrityMAC = records[1].IntegrityMAC
	store.Put(forged)
	runtime.JWKS.Invalidate()
	var set struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(issuerGet(mux, issuerJWKSPath, nil).Body.Bytes(), &set); err != nil {
		t.Fatal(err)
	}
	if len(set.Keys) != 1 || set.Keys[0]["kid"] == forged.KID {
		t.Fatalf("the forged row must be omitted: %v", set.Keys)
	}
}

func TestIssuerRoutesAreRateLimitedPerClient(t *testing.T) {
	_, _, mux := newIssuerFixture(t)
	var limited int
	for range issuerRateBurst + 10 {
		if issuerGet(mux, issuerDiscoveryPath, nil).Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("a flood from one client must be throttled")
	}
	other := httptest.NewRequest(http.MethodGet, issuerDiscoveryPath, nil)
	other.RemoteAddr = "192.0.2.99:1"
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, other)
	if response.Code != http.StatusOK {
		t.Fatalf("another client keeps its own budget: %d", response.Code)
	}
}

func TestClientLimiterRefillsAndBoundsItsTable(t *testing.T) {
	limiter := newClientLimiter(2, time.Second, 3)
	now := time.Unix(1000, 0)
	first, second, third := limiter.allow("a", now), limiter.allow("a", now), limiter.allow("a", now)
	if !first || !second || third {
		t.Fatal("burst of two")
	}
	if !limiter.allow("a", now.Add(time.Second)) {
		t.Fatal("one token per interval is refilled")
	}
	for _, client := range []string{"b", "c", "d", "e"} {
		limiter.allow(client, now)
	}
	if len(limiter.buckets) > 4 { // three clients and the shared overflow bucket
		t.Fatalf("table grew to %d", len(limiter.buckets))
	}
	if limiter.allow("a", now.Add(10*time.Second)) == false {
		t.Fatal("a refilled client is admitted again")
	}
}

func TestIssuerRateLimitIgnoresForwardedForHeaders(t *testing.T) {
	_, _, mux := newIssuerFixture(t)
	var limited int
	for i := range issuerRateBurst + 10 {
		// A caller-chosen header must not buy a fresh budget.
		response := issuerGet(mux, issuerDiscoveryPath, map[string]string{"X-Forwarded-For": "198.51.100." + strconv.Itoa(i)})
		if response.Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("X-Forwarded-For must not select the rate limit bucket")
	}
}

func TestClientLimiterEvictsOnlyIdleClientsWhenFull(t *testing.T) {
	limiter := newClientLimiter(2, time.Second, 2)
	now := time.Unix(1000, 0)
	limiter.allow("busy", now)
	limiter.allow("busy", now) // drained
	limiter.allow("idle", now) // one token left of two
	if !limiter.allow("new", now.Add(5*time.Second)) {
		t.Fatal("a new client is admitted once idle buckets were evicted")
	}
	if _, kept := limiter.buckets["busy"]; kept {
		t.Log("busy refilled during the wait and was evicted as idle")
	}
}

func TestIssuerJWKSStoreFailureIs503(t *testing.T) {
	runtime, _, mux := newIssuerFixture(t)
	runtime.JWKS.Store = failingKeyReader{}
	runtime.JWKS.Invalidate()
	if got := issuerGet(mux, issuerJWKSPath, nil).Code; got != http.StatusServiceUnavailable {
		t.Fatal(got)
	}
}

type failingKeyReader struct{}

func (failingKeyReader) ListPublicKeys(context.Context) ([]tokenissuer.KeyRecord, error) {
	return nil, context.DeadlineExceeded
}

func TestIssuerRotationWorkerStartsAndStops(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	t.Setenv(secrets.KeyPreviousEnv, "")
	store := tokenissuer.NewMemoryStore()
	runtime, err := NewIssuerRuntime(issuerTestConfig(), store, "id")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	runtime.StartRotation(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if records, _ := store.ListKeys(ctx); len(records) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the worker did not bootstrap keys")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
}

func TestNewIssuerRuntimeRejectsBadLifecycle(t *testing.T) {
	bad := issuerTestConfig()
	bad.KeyPublishLead = 0
	if _, err := NewIssuerRuntime(bad, tokenissuer.NewMemoryStore(), "id"); err == nil {
		t.Fatal("accepted")
	}
	bad = issuerTestConfig()
	bad.Alg = "none"
	if _, err := NewIssuerRuntime(bad, tokenissuer.NewMemoryStore(), "id"); err == nil {
		t.Fatal("accepted")
	}
}
