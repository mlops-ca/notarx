package authorization

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

const (
	testWorkloadIssuer   = "https://ci.example.test"
	testWorkloadAudience = "https://notarx.example.test"
	testWorkloadAccount  = "66666666-6666-4666-8666-666666666666"
)

type recordingObserver struct {
	mu     sync.Mutex
	events []WorkloadEvent
}

func (r *recordingObserver) ObserveWorkload(_ context.Context, event WorkloadEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recordingObserver) last(t *testing.T) WorkloadEvent {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		t.Fatal("no workload event was observed")
	}
	return r.events[len(r.events)-1]
}

func (r *recordingObserver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// countingWorkloadStore counts the reads the authenticator makes.
type countingWorkloadStore struct {
	*repository.MemoryStore
	mu    sync.Mutex
	reads int
}

func (s *countingWorkloadStore) bump() {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
}

func (s *countingWorkloadStore) GetWorkloadIssuerByIssuer(ctx context.Context, issuer string) (repository.WorkloadIssuer, error) {
	s.bump()
	return s.MemoryStore.GetWorkloadIssuerByIssuer(ctx, issuer)
}

func (s *countingWorkloadStore) ListActiveWorkloadTrustPolicies(ctx context.Context, issuerID string, now time.Time) ([]repository.WorkloadTrustPolicy, error) {
	s.bump()
	return s.MemoryStore.ListActiveWorkloadTrustPolicies(ctx, issuerID, now)
}

type workloadFixture struct {
	store    *countingWorkloadStore
	signer   *jose.Signer
	observer *recordingObserver
	issuer   repository.WorkloadIssuer
	policy   repository.WorkloadTrustPolicy
	workload *WorkloadAuthenticator
	registry *readcache.Registry
}

func newWorkloadFixture(t *testing.T, ttl time.Duration, singleUse bool) *workloadFixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.ES256, key, "k1")
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := jose.PublicKey(key.Public(), "k1", jose.ES256)
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := jose.MarshalJWKS(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	f := &workloadFixture{store: &countingWorkloadStore{MemoryStore: repository.NewMemoryStore()}, signer: signer, observer: &recordingObserver{}}
	ctx := context.Background()
	if f.issuer, err = f.store.CreateWorkloadIssuer(ctx, repository.WorkloadIssuer{
		Name: "ci", Kind: repository.WorkloadKindGeneric, Issuer: testWorkloadIssuer, Audiences: []string{testWorkloadAudience},
		AllowedAlgs: []string{jose.ES256}, JWKSMode: repository.WorkloadJWKSStatic, StaticJWKS: jwks,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.CreateServiceAccount(ctx, repository.ServiceAccount{ID: testWorkloadAccount, Name: "ci"}); err != nil {
		t.Fatal(err)
	}
	if f.policy, err = f.store.CreateWorkloadTrustPolicy(ctx, repository.WorkloadTrustPolicy{
		Name: "publish", IssuerID: f.issuer.ID, ServiceAccountID: testWorkloadAccount, SingleUse: singleUse,
		Conditions: []byte(`[{"claim":["sub"],"op":"eq","values":["repo:acme/app"]}]`),
	}); err != nil {
		t.Fatal(err)
	}
	f.registry = readcache.NewRegistry(ttl)
	verifier, err := jose.NewVerifier(jose.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if f.workload, err = NewWorkloadAuthenticator(WorkloadOptions{
		Store: f.store, Accounts: f.store, Verifier: verifier, Observer: f.observer, ClockSkew: time.Minute, Cache: f.registry,
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *workloadFixture) token(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	now := time.Now()
	claims := map[string]any{
		"iss": testWorkloadIssuer, "sub": "repo:acme/app", "aud": testWorkloadAudience,
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(), "jti": "jti-" + now.Format(time.RFC3339Nano),
	}
	if mutate != nil {
		mutate(claims)
	}
	token, err := f.signer.Sign(claims)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (f *workloadFixture) authenticator() Authenticator {
	return Authenticator{
		AdminToken: "admin-secret", AdminActor: "admin", ResolverToken: "resolver-secret", ResolverActor: "resolver",
		ServiceAccounts: f.store, Workload: f.workload,
	}
}

func TestLooksLikeJWT(t *testing.T) {
	good := "eyJhbGciOiJFUzI1NiJ9.eyJpc3MiOiJ4In0.c2ln"
	cases := map[string]bool{
		good:                                   true,
		"":                                     false,
		"v2.abc.123.sig":                       false,
		"eyJhbGciOiJFUzI1NiJ9.eyJpc3MiOiJ4In0": false,
		"eyJhbGciOiJFUzI1NiJ9..c2ln":           false,
		"bm90anNvbg.eyJpc3MiOiJ4In0.c2ln":      false,
		"e30.eyJpc3MiOiJ4In0.c2ln":             false, // header without alg
		"eyJhbGciOiJFUzI1NiJ9.eyJp+c3MiOiJ4In0.c2ln": false,
		strings.Repeat("a", maxWorkloadTokenBytes+1): false,
	}
	for token, want := range cases {
		if got := LooksLikeJWT(token); got != want {
			t.Errorf("LooksLikeJWT(%.30q) = %v, want %v", token, got, want)
		}
	}
}

func TestWorkloadBearerAuthenticatesServiceAccount(t *testing.T) {
	f := newWorkloadFixture(t, 0, false)
	token := f.token(t, nil)
	principal, ok := f.authenticator().Authenticate("Bearer " + token)
	if !ok {
		t.Fatal("a valid workload token was refused")
	}
	if principal.Actor != "service-account:"+testWorkloadAccount || principal.AuthenticationKind != AuthenticationWorkloadFederation || principal.CredentialID != f.policy.ID {
		t.Fatalf("principal = %+v", principal)
	}
	claims := principal.WorkloadClaims
	if claims == nil || claims.Issuer != testWorkloadIssuer || claims.Subject != "repo:acme/app" || claims.PolicyName != "publish" || claims.JTIHash == "" || claims.Referenced[`["sub"]`] != "repo:acme/app" {
		t.Fatalf("workload claims = %+v", claims)
	}
	event := f.observer.last(t)
	if !event.Success || event.Reason != WorkloadAuthenticated || event.PolicyID != f.policy.ID || event.ServiceAccountID != testWorkloadAccount || event.JTIHash != claims.JTIHash {
		t.Fatalf("event = %+v", event)
	}
	if strings.Contains(event.Detail+event.JTIHash+event.Subject, token) {
		t.Fatal("the event carries the raw token")
	}
}

func TestWorkloadFailuresAreRefusedAndObserved(t *testing.T) {
	f := newWorkloadFixture(t, 0, false)
	auth := f.authenticator()
	cases := []struct {
		name   string
		mutate func(map[string]any)
		reason string
	}{
		{"wrong audience", func(c map[string]any) { c["aud"] = "https://elsewhere.example.test" }, WorkloadVerifyFailed},
		{"expired", func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }, WorkloadVerifyFailed},
		{"no matching policy", func(c map[string]any) { c["sub"] = "repo:acme/other" }, WorkloadNoPolicyMatch},
	}
	for _, tc := range cases {
		before := f.observer.count()
		if _, ok := auth.Authenticate("Bearer " + f.token(t, tc.mutate)); ok {
			t.Fatalf("%s: token was accepted", tc.name)
		}
		if f.observer.count() != before+1 {
			t.Fatalf("%s: want one event, got %d", tc.name, f.observer.count()-before)
		}
		if event := f.observer.last(t); event.Success || event.Reason != tc.reason {
			t.Fatalf("%s: event = %+v, want reason %s", tc.name, event, tc.reason)
		}
	}
	if event := f.observer.last(t); !strings.Contains(event.Detail, "publish") {
		t.Fatalf("operator detail does not name the failing policy: %q", event.Detail)
	}
	// An issuer nobody registered is not a workload token: no event, other paths proceed.
	before := f.observer.count()
	if _, ok := auth.Authenticate("Bearer " + f.token(t, func(c map[string]any) { c["iss"] = "https://unknown.example.test" })); ok {
		t.Fatal("a token of an unknown issuer was accepted")
	}
	if f.observer.count() != before {
		t.Fatal("an unknown issuer must not produce a workload event")
	}
}

func TestWorkloadBasicUsernames(t *testing.T) {
	f := newWorkloadFixture(t, 0, false)
	ctx := context.Background()
	other, err := f.store.CreateWorkloadTrustPolicy(ctx, repository.WorkloadTrustPolicy{
		Name: "other", IssuerID: f.issuer.ID, ServiceAccountID: testWorkloadAccount,
		Conditions: []byte(`[{"claim":["sub"],"op":"eq","values":["repo:acme/else"]}]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	auth := f.authenticator()
	token := f.token(t, nil)
	if principal, ok := auth.AuthenticateBasicContext(ctx, "oidc", token); !ok || principal.CredentialID != f.policy.ID {
		t.Fatalf("oidc username: %+v %v", principal, ok)
	}
	if principal, ok := auth.AuthenticateBasicContext(ctx, "publish", token); !ok || principal.CredentialID != f.policy.ID {
		t.Fatalf("policy-name username: %+v %v", principal, ok)
	}
	if _, ok := auth.AuthenticateBasicContext(ctx, other.Name, token); ok {
		t.Fatal("a token that does not match the pinned policy was accepted")
	}
	if _, ok := auth.AuthenticateBasicContext(ctx, "nobody", token); ok {
		t.Fatal("a username that names no policy ran the workload path")
	}
	if _, ok := auth.AuthenticateBasic("", token); ok {
		t.Fatal("an empty username was accepted")
	}
}

func TestWorkloadSingleUseRejectsReplay(t *testing.T) {
	for _, ttl := range []time.Duration{0, 5 * time.Second} {
		f := newWorkloadFixture(t, ttl, true)
		auth := f.authenticator()
		token := f.token(t, nil)
		if _, ok := auth.Authenticate("Bearer " + token); !ok {
			t.Fatalf("ttl %s: first use was refused", ttl)
		}
		if _, ok := auth.Authenticate("Bearer " + token); ok {
			t.Fatalf("ttl %s: a replayed single-use token was accepted (the cache must not serve it)", ttl)
		}
		if event := f.observer.last(t); event.Reason != WorkloadReplay {
			t.Fatalf("ttl %s: event = %+v", ttl, event)
		}
		noJTI := f.token(t, func(c map[string]any) { delete(c, "jti") })
		if _, ok := auth.Authenticate("Bearer " + noJTI); ok {
			t.Fatalf("ttl %s: a single-use policy accepted a token without jti", ttl)
		}
		if event := f.observer.last(t); event.Reason != WorkloadMissingJTI {
			t.Fatalf("ttl %s: event = %+v", ttl, event)
		}
	}
}

func TestWorkloadDisabledPolicyIssuerAndAccountAreRefused(t *testing.T) {
	ctx := context.Background()
	t.Run("policy", func(t *testing.T) {
		f := newWorkloadFixture(t, 0, false)
		policy := f.policy
		policy.State = repository.WorkloadDisabled
		if _, err := f.store.UpdateWorkloadTrustPolicy(ctx, policy, policy.Version); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.authenticator().Authenticate("Bearer " + f.token(t, nil)); ok {
			t.Fatal("a disabled policy authenticated")
		}
		if f.observer.last(t).Reason != WorkloadPolicyNotFound {
			t.Fatalf("event = %+v", f.observer.last(t))
		}
	})
	t.Run("issuer", func(t *testing.T) {
		f := newWorkloadFixture(t, 0, false)
		issuer := f.issuer
		issuer.State = repository.WorkloadDisabled
		if _, err := f.store.UpdateWorkloadIssuer(ctx, issuer, issuer.Version); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.authenticator().Authenticate("Bearer " + f.token(t, nil)); ok {
			t.Fatal("a disabled issuer authenticated")
		}
		if f.observer.last(t).Reason != WorkloadIssuerDisabled {
			t.Fatalf("event = %+v", f.observer.last(t))
		}
	})
	t.Run("account", func(t *testing.T) {
		f := newWorkloadFixture(t, 0, false)
		disabled := repository.ServiceAccountDisabled
		account, err := f.store.GetServiceAccount(ctx, testWorkloadAccount)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.UpdateServiceAccount(ctx, repository.ServiceAccountUpdate{ID: testWorkloadAccount, State: &disabled}, account.Version); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.authenticator().Authenticate("Bearer " + f.token(t, nil)); ok {
			t.Fatal("a disabled service account authenticated")
		}
		if f.observer.last(t).Reason != WorkloadAccountInactive {
			t.Fatalf("event = %+v", f.observer.last(t))
		}
	})
}

func TestWorkloadAmbiguousPoliciesAreRefused(t *testing.T) {
	f := newWorkloadFixture(t, 0, false)
	ctx := context.Background()
	if _, err := f.store.CreateServiceAccount(ctx, repository.ServiceAccount{ID: "77777777-7777-4777-8777-777777777777", Name: "second"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateWorkloadTrustPolicy(ctx, repository.WorkloadTrustPolicy{
		Name: "twin", IssuerID: f.issuer.ID, ServiceAccountID: "77777777-7777-4777-8777-777777777777",
		Conditions: []byte(`[{"claim":["sub"],"op":"prefix","values":["repo:acme/"]}]`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.authenticator().Authenticate("Bearer " + f.token(t, nil)); ok {
		t.Fatal("a token matching policies of two accounts authenticated")
	}
	if f.observer.last(t).Reason != WorkloadAmbiguousPolicy {
		t.Fatalf("event = %+v", f.observer.last(t))
	}
}

func TestWorkloadVerifiedTokenIsCachedAndRevocationIsBounded(t *testing.T) {
	f := newWorkloadFixture(t, 5*time.Second, false)
	now := time.Now()
	f.registry.SetClock(func() time.Time { return now })
	auth := f.authenticator()
	token := f.token(t, nil)
	ctx := context.Background()
	if _, ok := auth.AuthenticateContext(ctx, "Bearer "+token); !ok {
		t.Fatal("first request refused")
	}
	reads := f.store.reads
	events := f.observer.count()
	for range 50 {
		if _, ok := auth.AuthenticateContext(ctx, "Bearer "+token); !ok {
			t.Fatal("a warm request was refused")
		}
	}
	// The issuer row is read for the iss lookup (the production store serves it from the issuer cache);
	// verification, the policy list and the account are not read again.
	if got := f.store.reads - reads; got != 50 {
		t.Fatalf("warm requests read %d times, want only the 50 issuer lookups", got)
	}
	if f.observer.count() != events {
		t.Fatal("cache hits must not repeat verification events")
	}

	// A policy change with the notification lost: the window bounds it.
	policy := f.policy
	policy.State = repository.WorkloadDisabled
	if _, err := f.store.UpdateWorkloadTrustPolicy(context.Background(), policy, policy.Version); err != nil {
		t.Fatal(err)
	}
	now = now.Add(4 * time.Second)
	if _, ok := auth.AuthenticateContext(ctx, "Bearer "+token); !ok {
		t.Fatal("inside the window the cached decision may still be served")
	}
	now = now.Add(2 * time.Second)
	if _, ok := auth.AuthenticateContext(ctx, "Bearer "+token); ok {
		t.Fatal("the disabled policy still authenticated after the window")
	}

	// With the notification delivered the next request is already refused.
	current, err := f.store.GetWorkloadTrustPolicy(context.Background(), f.policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.State = repository.WorkloadActive
	if _, err := f.store.UpdateWorkloadTrustPolicy(context.Background(), current, current.Version); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second) // a denial is remembered for at most one second
	if _, ok := auth.AuthenticateContext(ctx, "Bearer "+token); !ok {
		t.Fatal("re-enabled policy was refused")
	}
	current, _ = f.store.GetWorkloadTrustPolicy(context.Background(), f.policy.ID)
	current.State = repository.WorkloadDisabled
	if _, err := f.store.UpdateWorkloadTrustPolicy(context.Background(), current, current.Version); err != nil {
		t.Fatal(err)
	}
	f.registry.Invalidate(repository.CacheScopeWorkloadPolicy, current.ID)
	if _, ok := auth.AuthenticateContext(ctx, "Bearer "+token); ok {
		t.Fatal("an invalidated policy still authenticated")
	}
	// Authenticate without a request context reads through the caches.
	if _, ok := auth.Authenticate("Bearer " + token); ok {
		t.Fatal("a bypassing read served a disabled policy")
	}
}

// A bumped issuer, policy or service-account epoch turns a cached verdict into a miss, so a lost eviction of the
// entry cannot keep serving it past the bump (ADR 0023 section 6).
func TestWorkloadEpochBumpTurnsACachedVerdictIntoAMiss(t *testing.T) {
	scopes := []string{repository.CacheScopeWorkloadIssuer, repository.CacheScopeWorkloadPolicy, repository.CacheScopeServiceAccount}
	for _, scope := range scopes {
		t.Run(scope, func(t *testing.T) {
			f := newWorkloadFixture(t, 5*time.Second, false)
			auth := f.authenticator()
			token := f.token(t, nil)
			ctx := context.Background()
			if _, ok := auth.AuthenticateContext(ctx, "Bearer "+token); !ok {
				t.Fatal("first request refused")
			}
			issuer, err := f.store.GetWorkloadIssuer(ctx, f.issuer.ID)
			if err != nil {
				t.Fatal(err)
			}
			issuer.Audiences = []string{"https://other.example.test"}
			if _, err := f.store.UpdateWorkloadIssuer(ctx, issuer, issuer.Version); err != nil {
				t.Fatal(err)
			}
			if _, ok := auth.AuthenticateContext(ctx, "Bearer "+token); !ok {
				t.Fatal("inside the window with no notification the cached verdict is still served")
			}
			f.registry.Invalidate(scope, "unrelated-id")
			if _, ok := auth.AuthenticateContext(ctx, "Bearer "+token); ok {
				t.Fatalf("a bumped %s epoch left the cached verdict in force", scope)
			}
		})
	}
}

func TestWorkloadPrincipalTokenIsRevalidated(t *testing.T) {
	f := newWorkloadFixture(t, 0, false)
	auth := f.authenticator()
	principal, ok := auth.Authenticate("Bearer " + f.token(t, nil))
	if !ok {
		t.Fatal("workload token refused")
	}
	issued := auth.IssuePrincipalToken(principal)
	got, ok := auth.Authenticate("Bearer " + issued)
	if !ok || got.AuthenticationKind != AuthenticationWorkloadFederation || got.CredentialID != f.policy.ID || got.Actor != principal.Actor {
		t.Fatalf("principal token: %+v %v", got, ok)
	}
	if _, ok := auth.Authenticate("Bearer " + auth.IssuePrincipalTokenFor(principal, time.Hour)); !ok {
		t.Fatal("a longer lifetime was refused")
	}
	// A principal token without a policy id, or for a deployment with workload federation off, is refused.
	bare := principal
	bare.CredentialID = ""
	if _, ok := auth.Authenticate("Bearer " + auth.IssuePrincipalToken(bare)); ok {
		t.Fatal("a workload token without a policy id was accepted")
	}
	off := auth
	off.Workload = nil
	if _, ok := off.Authenticate("Bearer " + issued); ok {
		t.Fatal("a workload token was accepted while workload federation is off")
	}
	policy := f.policy
	policy.State = repository.WorkloadDisabled
	if _, err := f.store.UpdateWorkloadTrustPolicy(context.Background(), policy, policy.Version); err != nil {
		t.Fatal(err)
	}
	if _, ok := auth.Authenticate("Bearer " + issued); ok {
		t.Fatal("an issued token outlived its disabled policy")
	}
}

func TestWorkloadDoesNotChangeOtherCredentialPaths(t *testing.T) {
	f := newWorkloadFixture(t, 0, false)
	auth := f.authenticator()
	ctx := context.Background()
	if p, ok := auth.Authenticate("Bearer admin-secret"); !ok || p.AuthenticationKind != AuthenticationStaticAdmin {
		t.Fatalf("admin token: %+v %v", p, ok)
	}
	if p, ok := auth.Authenticate("Bearer resolver-secret"); !ok || p.AuthenticationKind != AuthenticationStaticResolver {
		t.Fatalf("resolver token: %+v %v", p, ok)
	}
	if p, ok := auth.AuthenticateBasicContext(ctx, "bob", "resolver-secret"); !ok || p.Actor != "bob" {
		t.Fatalf("resolver basic: %+v %v", p, ok)
	}
	if _, ok := auth.Authenticate("Bearer not-a-token"); ok {
		t.Fatal("garbage authenticated")
	}
	if _, ok := auth.AuthenticateBasicContext(ctx, "bob", "wrong"); ok {
		t.Fatal("wrong password authenticated")
	}
	if f.observer.count() != 0 {
		t.Fatalf("non-JWT credentials produced %d workload events", f.observer.count())
	}
	v1 := auth.IssueToken("someone")
	if p, ok := auth.Authenticate("Bearer " + v1); !ok || p.Actor != "someone" {
		t.Fatalf("v1 token: %+v %v", p, ok)
	}
	disabled := auth
	disabled.Workload = nil
	if _, ok := disabled.Authenticate("Bearer " + f.token(t, nil)); ok {
		t.Fatal("a workload JWT authenticated with the feature off")
	}
}

// flakyVerifier fails with an unavailable key source until it is told to recover.
type flakyVerifier struct {
	jose.TokenVerifier
	down  bool
	calls int
}

func (v *flakyVerifier) Verify(ctx context.Context, token string, issuer jose.Issuer) (*jose.Verified, error) {
	v.calls++
	if v.down {
		return nil, &jose.Error{Kind: jose.ErrKeySourceUnavailable, Reason: "issuer keys could not be fetched"}
	}
	return v.TokenVerifier.Verify(ctx, token, issuer)
}

func TestWorkloadKeySourceOutageIsNotCachedAsAVerdict(t *testing.T) {
	f := newWorkloadFixture(t, 5*time.Second, false)
	flaky := &flakyVerifier{TokenVerifier: f.workload.opts.Verifier, down: true}
	f.workload.opts.Verifier = flaky
	auth := f.authenticator()
	token := f.token(t, nil)
	ctx := context.Background()
	if _, ok := auth.AuthenticateContext(ctx, "Bearer "+token); ok {
		t.Fatal("a token authenticated while the key source was down")
	}
	if event := f.observer.last(t); event.Reason != WorkloadKeysUnavailable {
		t.Fatalf("event = %+v", event)
	}
	flaky.down = false
	if _, ok := auth.AuthenticateContext(ctx, "Bearer "+token); !ok {
		t.Fatal("the outage was remembered as a verdict on the token")
	}
	if flaky.calls != 2 {
		t.Fatalf("verifier calls = %d, want 2 (the outage must not be cached)", flaky.calls)
	}
}

func TestWorkloadAuthenticateDeferredConsumesSingleUseOnlyOnCommit(t *testing.T) {
	f := newWorkloadFixture(t, 0, true)
	token := f.token(t, nil)
	ctx := context.Background()
	for range 2 {
		if _, commit, ok := f.workload.AuthenticateDeferred(ctx, token, ""); !ok || commit == nil {
			t.Fatal("a verified single-use token was refused before commit")
		}
	}
	_, commit, ok := f.workload.AuthenticateDeferred(ctx, token, "")
	if !ok || !commit() {
		t.Fatal("the first commit must succeed")
	}
	if _, again, ok := f.workload.AuthenticateDeferred(ctx, token, ""); !ok || again() {
		t.Fatal("a second commit of the same token must be a replay")
	}
	if _, _, ok := f.workload.AuthenticateDeferred(ctx, "not-a-jwt", ""); ok {
		t.Fatal("garbage was accepted")
	}
	if issuer, ok := f.workload.LookupIssuer(ctx, token); !ok || issuer.ID != f.issuer.ID {
		t.Fatalf("lookup = %+v %v", issuer, ok)
	}
}

func TestWorkloadAuditSubjectIsBounded(t *testing.T) {
	f := newWorkloadFixture(t, 0, false)
	long := strings.Repeat("s", 4000)
	if _, ok := f.authenticator().Authenticate("Bearer " + f.token(t, func(c map[string]any) { c["sub"] = long })); ok {
		t.Fatal("a non-matching subject authenticated")
	}
	if event := f.observer.last(t); len(event.Subject) > 256 {
		t.Fatalf("subject of %d bytes reached the audit event", len(event.Subject))
	}
}

func TestNewWorkloadAuthenticatorRequiresDependencies(t *testing.T) {
	if _, err := NewWorkloadAuthenticator(WorkloadOptions{}); err == nil {
		t.Fatal("expected an error without a store")
	}
}
