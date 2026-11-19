package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
	"github.com/mlops-ca/notarx/internal/tokenissuer"
	"github.com/mlops-ca/notarx/internal/upstreamauth"
)

func newEntraProxyFixture(t *testing.T, upstreamStatus func(authorization string) int) (member repository.Member, exchanges, upstreamCalls *atomic.Int64) {
	t.Helper()
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	t.Setenv(secrets.KeyPreviousEnv, "")
	store := tokenissuer.NewMemoryStore()
	config := tokenissuer.DefaultLifecycleConfig()
	manager := &tokenissuer.Manager{Store: store, Config: config, Generator: tokenissuer.Generator{RSABits: 2048}}
	if _, _, err := manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	signer, err := tokenissuer.NewLocalSigner(store, tokenissuer.SignerConfig{Alg: jose.RS256})
	if err != nil {
		t.Fatal(err)
	}
	exchanges, upstreamCalls = &atomic.Int64{}, &atomic.Int64{}
	entra := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := exchanges.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-" + string(rune('0'+n)), "token_type": "Bearer", "expires_in": 3600})
	}))
	t.Cleanup(entra.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		status := upstreamStatus(r.Header.Get("Authorization"))
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte("artifact"))
		}
	}))
	t.Cleanup(upstream.Close)
	repository.AllowEntraWorkload(true)
	upstreamauth.SetEntra(&upstreamauth.Entra{
		Signer: signer, IssuerURL: "https://notarx.example.test", DeploymentID: "dep", AuthorityHost: entra.URL,
		Audience: upstreamauth.AudienceGlobal, HTTPClient: entra.Client(),
	})
	t.Cleanup(func() {
		upstreamauth.SetEntra(nil)
		repository.AllowEntraWorkload(false)
	})
	member = repository.Member{
		Type: repository.MemberProxy, Name: "azure", Endpoint: upstream.URL,
		UpstreamAuth: &repository.UpstreamAuth{
			Scheme: repository.UpstreamAuthSchemeEntraWorkload, TenantID: "contoso.onmicrosoft.com",
			ClientID: "11111111-2222-3333-4444-555555555555", Scope: "https://storage.example.test/.default", Repository: "azure",
		},
	}
	return member, exchanges, upstreamCalls
}

func TestProxyRetriesOnceWithAFreshEntraTokenAfter401(t *testing.T) {
	member, exchanges, calls := newEntraProxyFixture(t, func(authorization string) int {
		if authorization == "Bearer tok-2" {
			return http.StatusOK
		}
		return http.StatusUnauthorized
	})
	client := UpstreamClient{AllowHTTPForTesting: true}
	response, err := client.FetchMaven(t.Context(), http.MethodGet, member, "g/a/1/a-1.jar", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK || exchanges.Load() != 2 || calls.Load() != 2 {
		t.Fatalf("status=%d exchanges=%d upstream calls=%d", response.StatusCode, exchanges.Load(), calls.Load())
	}
}

func TestProxyRetriesEntra401OnlyOnce(t *testing.T) {
	member, exchanges, calls := newEntraProxyFixture(t, func(string) int { return http.StatusUnauthorized })
	response, err := (UpstreamClient{AllowHTTPForTesting: true}).FetchMaven(t.Context(), http.MethodGet, member, "g/a/1/a-1.jar", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusUnauthorized || exchanges.Load() != 2 || calls.Load() != 2 {
		t.Fatalf("a second 401 is final: status=%d exchanges=%d calls=%d", response.StatusCode, exchanges.Load(), calls.Load())
	}
}

func TestProxyReusesTheEntraTokenAcrossRequests(t *testing.T) {
	member, exchanges, calls := newEntraProxyFixture(t, func(string) int { return http.StatusOK })
	for range 3 {
		response, err := (UpstreamClient{AllowHTTPForTesting: true}).FetchMaven(t.Context(), http.MethodGet, member, "g/a/1/a-1.jar", nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
	}
	if exchanges.Load() != 1 || calls.Load() != 3 {
		t.Fatalf("exchanges=%d calls=%d", exchanges.Load(), calls.Load())
	}
}

func TestProxyFailsClosedWhenTheEntraCredentialIsUnavailable(t *testing.T) {
	member, _, calls := newEntraProxyFixture(t, func(string) int { return http.StatusOK })
	upstreamauth.SetEntra(nil)
	if _, err := (UpstreamClient{AllowHTTPForTesting: true}).FetchMaven(t.Context(), http.MethodGet, member, "g/a/1/a-1.jar", nil); err == nil {
		t.Fatal("an unavailable credential must never fall back to an anonymous request")
	}
	if calls.Load() != 0 {
		t.Fatal("nothing may reach the upstream")
	}
}
