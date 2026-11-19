package upstreamauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
	"github.com/mlops-ca/notarx/internal/tokenissuer"
)

const (
	testTenant = "contoso.onmicrosoft.com"
	testClient = "11111111-2222-3333-4444-555555555555"
	testScope  = "https://storage.example.test/.default"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newEntraSigner(t *testing.T, alg string) (*tokenissuer.LocalSigner, *fakeClock) {
	t.Helper()
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	t.Setenv(secrets.KeyPreviousEnv, "")
	clock := &fakeClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	store := tokenissuer.NewMemoryStore()
	config := tokenissuer.DefaultLifecycleConfig()
	config.Alg = alg
	manager := &tokenissuer.Manager{Store: store, Config: config, Generator: tokenissuer.Generator{RSABits: 2048}, Now: clock.Now}
	if _, _, err := manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	signer, err := tokenissuer.NewLocalSigner(store, tokenissuer.SignerConfig{Alg: alg, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return signer, clock
}

// entraStub plays the Entra token endpoint and, on the same host, an ACR registry.
type entraStub struct {
	t       *testing.T
	server  *httptest.Server
	signer  *tokenissuer.LocalSigner
	clock   *fakeClock
	tokens  atomic.Int64
	slow    time.Duration
	status  atomic.Int64
	forms   chan url.Values
	mu      sync.Mutex
	acrCall []string
}

func newEntraStub(t *testing.T, signer *tokenissuer.LocalSigner, clock *fakeClock) *entraStub {
	stub := &entraStub{t: t, signer: signer, clock: clock, forms: make(chan url.Values, 64)}
	stub.server = httptest.NewTLSServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *entraStub) serve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch {
	case r.URL.Path == "/"+testTenant+"/oauth2/v2.0/token":
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		s.forms <- r.PostForm
		if s.slow > 0 {
			time.Sleep(s.slow)
		}
		if code := int(s.status.Load()); code != 0 {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"secret-detail"}`))
			return
		}
		n := s.tokens.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("aad-token-%d", n), "token_type": "Bearer", "expires_in": 3600})
	case r.URL.Path == "/oauth2/exchange":
		s.record("exchange " + r.PostForm.Get("grant_type") + " " + r.PostForm.Get("access_token") + " " + r.PostForm.Get("tenant") + " " + r.PostForm.Get("service"))
		_ = json.NewEncoder(w).Encode(map[string]any{"refresh_token": jwtWithExp("refresh", s.clock.Now().Add(3*time.Hour))})
	case r.URL.Path == "/oauth2/token":
		s.record("token " + r.PostForm.Get("grant_type") + " " + r.PostForm.Get("scope") + " " + r.PostForm.Get("service"))
		if !strings.HasPrefix(r.PostForm.Get("refresh_token"), "e") { // a JWT header starts with "e"
			http.Error(w, "bad refresh token", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": jwtWithExp("access:"+r.PostForm.Get("scope"), s.clock.Now().Add(time.Hour))})
	case r.URL.Path == "/redirect/oauth2/v2.0/token":
		http.Redirect(w, r, "https://elsewhere.invalid/", http.StatusFound)
	default:
		http.NotFound(w, r)
	}
}

func (s *entraStub) record(call string) {
	s.mu.Lock()
	s.acrCall = append(s.acrCall, call)
	s.mu.Unlock()
}

func (s *entraStub) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.acrCall...)
}

func jwtWithExp(subject string, exp time.Time) string {
	payload, _ := json.Marshal(map[string]any{"sub": subject, "exp": exp.Unix()})
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func (s *entraStub) provider() *Entra {
	return &Entra{
		Signer: s.signer, IssuerURL: "https://notarx.example.test", DeploymentID: "dep-123",
		AuthorityHost: s.server.URL, Audience: AudienceGlobal, HTTPClient: s.server.Client(), Now: s.clock.Now,
		ACRHostAllowed: func(string) bool { return true },
	}
}

func entraAuth() *repository.UpstreamAuth {
	return &repository.UpstreamAuth{Scheme: repository.UpstreamAuthSchemeEntraWorkload, TenantID: testTenant, ClientID: testClient, Scope: testScope, Repository: "azure-blobs"}
}

func installEntra(t *testing.T, provider *Entra) {
	t.Helper()
	repository.AllowEntraWorkload(true)
	SetEntra(provider)
	t.Cleanup(func() {
		SetEntra(nil)
		repository.AllowEntraWorkload(false)
	})
}

func applyTo(t *testing.T, target string, auth *repository.UpstreamAuth) (*http.Request, error) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return request, Apply(request, auth)
}

func TestEntraExchangeSendsSignedAssertionAndCachesTheToken(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.RS256)
	stub := newEntraStub(t, signer, clock)
	provider := stub.provider()
	var events []EntraExchange
	provider.Audit = func(_ context.Context, event EntraExchange) { events = append(events, event) }
	installEntra(t, provider)

	request, err := applyTo(t, "https://blob.example.test/container/file", entraAuth())
	if err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer aad-token-1" {
		t.Fatalf("Authorization = %q", got)
	}
	form := <-stub.forms
	for field, want := range map[string]string{
		"grant_type": "client_credentials", "client_id": testClient, "scope": testScope,
		"client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
	} {
		if form.Get(field) != want {
			t.Errorf("%s = %q want %q", field, form.Get(field), want)
		}
	}
	jwks, _ := jose.MarshalJWKS(signer.PublicKeys(t.Context())...)
	verifier, _ := jose.NewVerifier(jose.Options{ClockSkew: time.Minute, Now: clock.Now})
	verified, err := verifier.Verify(t.Context(), form.Get("client_assertion"), jose.Issuer{
		Issuer: "https://notarx.example.test", Audiences: []string{AudienceGlobal}, Algorithms: []string{jose.RS256}, Mode: jose.ModeStatic, StaticJWKS: jwks,
	})
	if err != nil {
		t.Fatalf("the assertion must verify like Entra would: %v", err)
	}
	if verified.Claims["sub"] != "notarx:instance:dep-123" || verified.Alg != jose.RS256 {
		t.Fatalf("claims=%v alg=%s", verified.Claims, verified.Alg)
	}
	iat, _ := verified.Claims["iat"].(json.Number).Int64()
	exp, _ := verified.Claims["exp"].(json.Number).Int64()
	if exp-iat != 300 || verified.Claims["nbf"] != verified.Claims["iat"] {
		t.Fatalf("lifetime must be 5 minutes: %v", verified.Claims)
	}
	if _, ok := verified.Claims["notarx_sa"]; ok {
		t.Fatal("the instance principal carries no service account claim")
	}

	// A second request reuses the token without another exchange.
	again, err := applyTo(t, "https://blob.example.test/other", entraAuth())
	if err != nil || again.Header.Get("Authorization") != "Bearer aad-token-1" || stub.tokens.Load() != 1 {
		t.Fatalf("err=%v header=%q exchanges=%d", err, again.Header.Get("Authorization"), stub.tokens.Load())
	}
	// The token is replaced five minutes before it expires.
	clock.Advance(54 * time.Minute)
	if _, _ = applyTo(t, "https://blob.example.test/x", entraAuth()); stub.tokens.Load() != 1 {
		t.Fatal("still inside exp - 5 minutes")
	}
	clock.Advance(2 * time.Minute)
	refreshed, err := applyTo(t, "https://blob.example.test/x", entraAuth())
	if err != nil || refreshed.Header.Get("Authorization") != "Bearer aad-token-2" {
		t.Fatalf("err=%v header=%q", err, refreshed.Header.Get("Authorization"))
	}
	if len(events) != 2 || events[0].Subject != "notarx:instance:dep-123" || events[0].TenantID != testTenant ||
		events[0].ClientID != testClient || events[0].Repository != "azure-blobs" || events[0].KeyID == "" || events[0].JTI == "" {
		t.Fatalf("audit events = %+v", events)
	}
}

func TestEntraExplicitSubjectAndPerAuthorityAudience(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.RS256)
	stub := newEntraStub(t, signer, clock)
	provider := stub.provider()
	installEntra(t, provider)
	auth := entraAuth()
	auth.Subject = "notarx:service-account:acr-puller"
	if _, err := applyTo(t, "https://blob.example.test/x", auth); err != nil {
		t.Fatal(err)
	}
	form := <-stub.forms
	parts := strings.Split(form.Get("client_assertion"), ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if !strings.Contains(string(payload), `"sub":"notarx:service-account:acr-puller"`) {
		t.Fatalf("the explicit subject is used verbatim: %s", payload)
	}
	for host, want := range map[string]string{
		"https://" + "login.microsoftonline.com":        AudienceGlobal,
		"https://" + "login.microsoftonline.us":         AudienceUSGov,
		"https://" + "login.partner.microsoftonline.cn": AudienceChina,
	} {
		derived := &Entra{AuthorityHost: host}
		if got, err := derived.audience(); err != nil || got != want {
			t.Errorf("%s: %q %v", host, got, err)
		}
	}
	if _, err := (&Entra{AuthorityHost: "https://login.example.org"}).audience(); err == nil {
		t.Error("an unknown authority host needs an explicit audience")
	}
	if got, _ := (&Entra{AuthorityHost: "https://login.example.org", Audience: "api://custom"}).audience(); got != "api://custom" {
		t.Errorf("override = %q", got)
	}
}

func TestEntraRefusesNonRS256Signing(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.ES256)
	stub := newEntraStub(t, signer, clock)
	installEntra(t, stub.provider())
	if _, err := applyTo(t, "https://blob.example.test/x", entraAuth()); !errors.Is(err, ErrCredential) {
		t.Fatalf("err=%v", err)
	}
	if err := Validate(entraAuth()); !errors.Is(err, ErrCredential) {
		t.Fatalf("validate err=%v", err)
	}
	if stub.tokens.Load() != 0 || len(stub.forms) != 0 {
		t.Fatal("nothing may be sent to Entra with an ES256 key")
	}
	if _, err := stub.provider().AccessToken(t.Context(), entraAuth()); !errors.Is(err, ErrEntraAlgorithm) {
		t.Fatalf("err=%v", err)
	}
}

func TestEntraSingleflightSharesOneExchange(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.RS256)
	stub := newEntraStub(t, signer, clock)
	stub.slow = 150 * time.Millisecond
	installEntra(t, stub.provider())
	var wg sync.WaitGroup
	headers := make(chan string, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			request, err := applyTo(t, "https://blob.example.test/x", entraAuth())
			if err != nil {
				t.Error(err)
				return
			}
			headers <- request.Header.Get("Authorization")
		}()
	}
	wg.Wait()
	close(headers)
	if stub.tokens.Load() != 1 {
		t.Fatalf("%d exchanges for 16 concurrent requests", stub.tokens.Load())
	}
	for header := range headers {
		if header != "Bearer aad-token-1" {
			t.Fatalf("header %q", header)
		}
	}
}

func TestEntraInvalidateForcesOneFreshExchange(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.RS256)
	stub := newEntraStub(t, signer, clock)
	installEntra(t, stub.provider())
	auth := entraAuth()
	if _, err := applyTo(t, "https://blob.example.test/x", auth); err != nil {
		t.Fatal(err)
	}
	other := entraAuth()
	other.Scope = "https://vault.example.test/.default"
	if _, err := applyTo(t, "https://vault.example.test/x", other); err != nil {
		t.Fatal(err)
	}
	Invalidate(auth) // the upstream answered 401
	retry, err := applyTo(t, "https://blob.example.test/x", auth)
	if err != nil || retry.Header.Get("Authorization") != "Bearer aad-token-3" {
		t.Fatalf("err=%v header=%q", err, retry.Header.Get("Authorization"))
	}
	if kept, _ := applyTo(t, "https://vault.example.test/x", other); kept.Header.Get("Authorization") != "Bearer aad-token-2" {
		t.Fatal("another scope keeps its cached token")
	}
	Invalidate(nil)
	(*Entra)(nil).Invalidate(auth)
}

func TestEntraExchangeFailuresNeverLeakUpstreamDetail(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.RS256)
	stub := newEntraStub(t, signer, clock)
	stub.status.Store(http.StatusUnauthorized)
	provider := stub.provider()
	installEntra(t, provider)
	if _, err := applyTo(t, "https://blob.example.test/x", entraAuth()); !errors.Is(err, ErrCredential) {
		t.Fatalf("err=%v", err)
	}
	_, err := provider.AccessToken(t.Context(), entraAuth())
	if !errors.Is(err, ErrEntraExchange) || strings.Contains(err.Error(), "secret-detail") {
		t.Fatalf("err=%v", err)
	}
	stub.status.Store(0)
	if _, err := provider.AccessToken(t.Context(), entraAuth()); err != nil {
		t.Fatalf("a failed exchange must not be cached: %v", err)
	}
}

func TestEntraTokenHostIsFixedAndRedirectsAreRefused(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.RS256)
	stub := newEntraStub(t, signer, clock)
	provider := stub.provider()
	if err := provider.postForm(t.Context(), "https://evil.example.test/token", provider.AuthorityHost, url.Values{}, &struct{}{}); !errors.Is(err, ErrEntraExchange) {
		t.Fatalf("a token request to another host must be refused: %v", err)
	}
	if err := provider.postForm(t.Context(), provider.AuthorityHost+"/redirect/oauth2/v2.0/token", provider.AuthorityHost, url.Values{}, &struct{}{}); !errors.Is(err, ErrEntraExchange) {
		t.Fatalf("redirects must not be followed: %v", err)
	}
	// The tenant cannot steer the request to another path or host.
	hostile := entraAuth()
	hostile.TenantID = "../evil"
	if hostile.Validate() == nil {
		t.Fatal("a tenant with path characters must be rejected by validation")
	}
}

func TestEntraValidateChecksConfigurationWithoutExchanging(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.RS256)
	stub := newEntraStub(t, signer, clock)
	repository.AllowEntraWorkload(true)
	t.Cleanup(func() { repository.AllowEntraWorkload(false) })
	SetEntra(nil)
	if err := Validate(entraAuth()); !errors.Is(err, ErrCredential) {
		t.Fatalf("no provider: %v", err)
	}
	installEntra(t, stub.provider())
	if err := Validate(entraAuth()); err != nil {
		t.Fatal(err)
	}
	if stub.tokens.Load() != 0 {
		t.Fatal("Validate runs on cache hits and must not exchange")
	}
	repository.AllowEntraWorkload(false)
	if err := Validate(entraAuth()); !errors.Is(err, ErrCredential) {
		t.Fatalf("the scheme is rejected while the issuer is disabled: %v", err)
	}
}

func TestACRChainAndCacheReuse(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.RS256)
	stub := newEntraStub(t, signer, clock)
	installEntra(t, stub.provider())
	host := strings.TrimPrefix(stub.server.URL, "https://")
	auth := entraAuth()
	auth.Scope = "https://management.example.test/.default"
	auth.RegistryKind = repository.RegistryKindACR

	first, err := applyTo(t, "https://"+host+"/v2/team/app/manifests/latest", auth)
	if err != nil {
		t.Fatal(err)
	}
	bearer := strings.TrimPrefix(first.Header.Get("Authorization"), "Bearer ")
	if !strings.Contains(decodeSubject(t, bearer), "access:repository:team/app:pull") {
		t.Fatalf("the registry access token is scoped to the repository: %s", decodeSubject(t, bearer))
	}
	want := []string{
		"exchange access_token aad-token-1 " + testTenant + " " + host,
		"token refresh_token repository:team/app:pull " + host,
	}
	if got := stub.calls(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("AAD -> ACR refresh -> access chain: %v", got)
	}
	// The same scope is served from the cache; a new scope reuses the refresh token.
	if _, err := applyTo(t, "https://"+host+"/v2/team/app/blobs/sha256:abc", auth); err != nil {
		t.Fatal(err)
	}
	if len(stub.calls()) != 2 || stub.tokens.Load() != 1 {
		t.Fatalf("cache reuse failed: %v tokens=%d", stub.calls(), stub.tokens.Load())
	}
	if _, err := applyTo(t, "https://"+host+"/v2/team/other/manifests/v1", auth); err != nil {
		t.Fatal(err)
	}
	calls := stub.calls()
	if len(calls) != 3 || !strings.HasPrefix(calls[2], "token refresh_token repository:team/other:pull") || stub.tokens.Load() != 1 {
		t.Fatalf("a new scope reuses the refresh and AAD tokens: %v", calls)
	}
	// The /v2/ ping needs no token.
	ping, err := applyTo(t, "https://"+host+"/v2/", auth)
	if err != nil || ping.Header.Get("Authorization") != "" || len(stub.calls()) != 3 {
		t.Fatalf("err=%v header=%q", err, ping.Header.Get("Authorization"))
	}
	// A 401 drops every token of this credential, ACR ones included.
	Invalidate(auth)
	if _, err := applyTo(t, "https://"+host+"/v2/team/app/manifests/latest", auth); err != nil {
		t.Fatal(err)
	}
	if stub.tokens.Load() != 2 || len(stub.calls()) != 5 {
		t.Fatalf("a fresh chain after invalidate: tokens=%d calls=%v", stub.tokens.Load(), stub.calls())
	}
}

func decodeSubject(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a jwt: %q", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func TestACRRefusesHostsAndPathsOutsideThePolicy(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.RS256)
	stub := newEntraStub(t, signer, clock)
	provider := stub.provider()
	provider.ACRHostAllowed = nil // the default policy: *.azurecr.io, .us and .cn only
	installEntra(t, provider)
	auth := entraAuth()
	auth.RegistryKind = repository.RegistryKindACR
	for _, target := range []string{
		"https://registry.example.com/v2/app/manifests/latest",
		"https://azurecr.io.evil.example/v2/app/manifests/latest",
		"http://" + "myreg.azurecr.io/v2/app/manifests/latest",
		"https://" + "myreg.azurecr.io/v2/app/unknown/latest",
		"https://" + "myreg.azurecr.io/other",
	} {
		if request, err := applyTo(t, target, auth); !errors.Is(err, ErrCredential) || request.Header.Get("Authorization") != "" {
			t.Errorf("%s: err=%v", target, err)
		}
	}
	for host, want := range map[string]bool{"myreg.azurecr.io": true, "my-reg.azurecr.us": true, "r.azurecr.cn": true, "myreg.azurecr.de": false, "azurecr.io": false} {
		if got := provider.acrHostAllowed(host); got != want {
			t.Errorf("%s allowed=%v want %v", host, got, want)
		}
	}
	if scope, ok := acrScope("/v2/_catalog"); !ok || scope != "registry:catalog:*" {
		t.Errorf("catalog scope %q %v", scope, ok)
	}
	if len(stub.calls()) != 0 {
		t.Fatal("no registry may be contacted for a refused target")
	}
}

func TestACRRejectsPlainHTTPAndRefreshTimesFollowExp(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.RS256)
	stub := newEntraStub(t, signer, clock)
	provider := stub.provider()
	installEntra(t, provider)
	auth := entraAuth()
	auth.RegistryKind = repository.RegistryKindACR
	if _, err := provider.ACRToken(t.Context(), auth, &url.URL{Scheme: "http", Host: "x", Path: "/v2/a/manifests/b"}); !errors.Is(err, ErrEntraExchange) {
		t.Fatalf("plain http registry: %v", err)
	}
	// An opaque (non JWT) token is cached for one minute only.
	if got := provider.tokenRefreshTime("opaque"); !got.Equal(clock.Now().Add(time.Minute)) {
		t.Fatalf("refresh time %s", got)
	}
	if got := provider.tokenRefreshTime(jwtWithExp("x", clock.Now().Add(2*time.Minute))); !got.Equal(clock.Now()) {
		t.Fatalf("a token that expires within the margin is not reused: %s", got)
	}
}

func TestEntraAuthWorksOverTLSWithTheDefaultClient(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.RS256)
	stub := newEntraStub(t, signer, clock)
	provider := stub.provider()
	repository.AllowEntraWorkload(true)
	t.Cleanup(func() { repository.AllowEntraWorkload(false) })
	provider.HTTPClient = nil // the default client does not trust the test certificate
	if _, err := provider.AccessToken(t.Context(), entraAuth()); !errors.Is(err, ErrEntraExchange) {
		t.Fatalf("an untrusted certificate must fail the exchange: %v", err)
	}
	client := provider.client()
	if client.Timeout == 0 || client.CheckRedirect == nil {
		t.Fatal("the default client has a timeout and refuses redirects")
	}
}

func TestEntraCacheIsKeyedBySubject(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.RS256)
	stub := newEntraStub(t, signer, clock)
	installEntra(t, stub.provider())
	instance := entraAuth()
	service := entraAuth()
	service.Subject = "notarx:service-account:acr-puller"
	for _, auth := range []*repository.UpstreamAuth{instance, service} {
		if _, err := applyTo(t, "https://blob.example.test/x", auth); err != nil {
			t.Fatal(err)
		}
	}
	if stub.tokens.Load() != 2 {
		t.Fatalf("two subjects need two exchanges, got %d: the federated credential matches on subject", stub.tokens.Load())
	}
	// Changing the subject of a repository must not keep serving the old subject's token.
	renamed := entraAuth()
	renamed.Subject = "notarx:service-account:other"
	if _, err := applyTo(t, "https://blob.example.test/x", renamed); err != nil || stub.tokens.Load() != 3 {
		t.Fatalf("err=%v exchanges=%d", err, stub.tokens.Load())
	}
	// Same application, scope and subject share one token across repositories.
	sibling := entraAuth()
	sibling.Repository = "another-repo"
	if _, err := applyTo(t, "https://blob.example.test/x", sibling); err != nil || stub.tokens.Load() != 3 {
		t.Fatalf("err=%v exchanges=%d", err, stub.tokens.Load())
	}
}

func TestEntraAssertionNeverOutlivesTheConfiguredMaximum(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.RS256)
	stub := newEntraStub(t, signer, clock)
	provider := stub.provider()
	provider.MaxLifetime = time.Minute
	installEntra(t, provider)
	if _, err := applyTo(t, "https://blob.example.test/x", entraAuth()); err != nil {
		t.Fatal(err)
	}
	form := <-stub.forms
	payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(form.Get("client_assertion"), ".")[1])
	var claims struct{ Iat, Exp int64 }
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp-claims.Iat != 60 {
		t.Fatalf("lifetime %d (%v): %s", claims.Exp-claims.Iat, err, payload)
	}
}

func TestEntraAuditsFailedExchangesAsFailures(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.RS256)
	stub := newEntraStub(t, signer, clock)
	stub.status.Store(http.StatusUnauthorized)
	provider := stub.provider()
	repository.AllowEntraWorkload(true)
	t.Cleanup(func() { repository.AllowEntraWorkload(false) })
	var events []EntraExchange
	provider.Audit = func(_ context.Context, event EntraExchange) { events = append(events, event) }
	if _, err := provider.AccessToken(t.Context(), entraAuth()); !errors.Is(err, ErrEntraExchange) {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Succeeded || events[0].JTI == "" {
		t.Fatalf("a rejected exchange is audited once, as a failure: %+v", events)
	}
	stub.status.Store(0)
	if _, err := provider.AccessToken(t.Context(), entraAuth()); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || !events[1].Succeeded {
		t.Fatalf("%+v", events)
	}
}

func TestEntraUsesTheInstalledRequestSender(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.RS256)
	stub := newEntraStub(t, signer, clock)
	provider := stub.provider()
	repository.AllowEntraWorkload(true)
	t.Cleanup(func() { repository.AllowEntraWorkload(false) })
	provider.HTTPClient = nil
	var sent atomic.Int64
	provider.Do = func(r *http.Request) (*http.Response, error) {
		sent.Add(1)
		return stub.server.Client().Do(r)
	}
	if _, err := provider.AccessToken(t.Context(), entraAuth()); err != nil || sent.Load() != 1 {
		t.Fatalf("err=%v sent=%d", err, sent.Load())
	}
}

func TestRetryUnauthorizedRecoversOnceAndOnlyForEntra(t *testing.T) {
	signer, clock := newEntraSigner(t, jose.RS256)
	stub := newEntraStub(t, signer, clock)
	installEntra(t, stub.provider())
	auth := entraAuth()
	request, err := applyTo(t, "https://blob.example.test/x", auth)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := func() *http.Response {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: http.NoBody}
	}
	var seen string
	do := func(r *http.Request) (*http.Response, error) {
		seen = r.Header.Get("Authorization")
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	}
	response, err := RetryUnauthorized(unauthorized(), request, "https://blob.example.test", auth, do)
	if err != nil || response.StatusCode != http.StatusOK || seen != "Bearer aad-token-2" {
		t.Fatalf("status=%v seen=%q err=%v", response, seen, err)
	}
	// Anything else is returned untouched.
	ok := &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}
	if got, _ := RetryUnauthorized(ok, request, "https://blob.example.test", auth, do); got != ok {
		t.Fatal("a successful response is not retried")
	}
	basic := &repository.UpstreamAuth{Scheme: repository.UpstreamAuthSchemeBearer, Secret: "x"}
	refused := unauthorized()
	if got, _ := RetryUnauthorized(refused, request, "https://blob.example.test", basic, do); got != refused {
		t.Fatal("only entra-workload credentials are retried")
	}
	if got, _ := RetryUnauthorized(refused, request, "https://blob.example.test", nil, do); got != refused {
		t.Fatal("no credential, no retry")
	}
	body, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://blob.example.test/x", strings.NewReader("payload"))
	if got, _ := RetryUnauthorized(refused, body, "https://blob.example.test", auth, do); got != refused {
		t.Fatal("requests with a body are never replayed")
	}
	if got, err := RetryUnauthorized(nil, request, "https://blob.example.test", auth, do); got != nil || err != nil {
		t.Fatal("no response, nothing to retry")
	}
	// A retry to a foreign origin never receives the token.
	if _, err := RetryUnauthorized(unauthorized(), request, "https://elsewhere.example.test", auth, do); err != nil {
		t.Fatalf("a different endpoint origin skips the credential rather than failing: %v", err)
	}
	if seen != "" {
		t.Fatalf("the foreign origin was sent %q", seen)
	}
}
