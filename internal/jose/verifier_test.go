package jose

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gojose "github.com/go-jose/go-jose/v4"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Unix(1_800_000_000, 0)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type counters struct{ stale, staticMiss, fetchFailed atomic.Int64 }

func (c *counters) StaleKeysServed(string) { c.stale.Add(1) }
func (c *counters) StaticKeyMiss(string)   { c.staticMiss.Add(1) }
func (c *counters) KeyFetchFailed(string)  { c.fetchFailed.Add(1) }

var (
	keyOnce sync.Once
	rsaKey  *rsa.PrivateKey
	ecKey   *ecdsa.PrivateKey
	edKey   ed25519.PrivateKey
)

func testKeys(t testing.TB) {
	t.Helper()
	keyOnce.Do(func() {
		var err error
		if rsaKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if ecKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			panic(err)
		}
		if _, edKey, err = ed25519.GenerateKey(rand.Reader); err != nil {
			panic(err)
		}
	})
}

func keyFor(alg string) crypto.Signer {
	switch alg {
	case RS256:
		return rsaKey
	case ES256:
		return ecKey
	}
	return edKey
}

// issuerServer is an HTTPS OIDC issuer with a mutable JWKS.
type issuerServer struct {
	*httptest.Server
	mu        sync.Mutex
	keys      []Key
	jwksCalls atomic.Int64
	discCalls atomic.Int64
	fail      atomic.Bool
}

func newIssuerServer(t *testing.T, keys ...Key) *issuerServer {
	t.Helper()
	s := &issuerServer{keys: keys}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		s.discCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": s.URL, "jwks_uri": s.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		s.jwksCalls.Add(1)
		if s.fail.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		s.mu.Lock()
		doc, _ := MarshalJWKS(s.keys...)
		s.mu.Unlock()
		_, _ = w.Write(doc)
	})
	s.Server = httptest.NewTLSServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (s *issuerServer) setKeys(keys ...Key) {
	s.mu.Lock()
	s.keys = keys
	s.mu.Unlock()
}

func (s *issuerServer) caPEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}))
}

func (s *issuerServer) issuer() Issuer {
	return Issuer{Issuer: s.URL, Audiences: []string{"notarx"}, Mode: ModeDiscovery, CABundlePEM: s.caPEM(), AllowPrivateNetwork: true}
}

func pubKey(t testing.TB, alg, kid string) Key {
	t.Helper()
	testKeys(t)
	key, err := PublicKey(keyFor(alg).Public(), kid, alg)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func newTestVerifier(t testing.TB, clk *clock, metrics Metrics) *Verifier {
	t.Helper()
	opts := DefaultOptions()
	opts.Now = clk.Now
	opts.Metrics = metrics
	v, err := NewVerifier(opts)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func claimsAt(clk *clock, iss string, mutate func(map[string]any)) map[string]any {
	now := clk.Now()
	claims := map[string]any{
		"iss": iss, "sub": "repo:acme/app:ref:refs/heads/main", "aud": "notarx",
		"exp": now.Add(10 * time.Minute).Unix(), "iat": now.Unix(), "nbf": now.Unix(),
		"repository_id": "123456",
	}
	if mutate != nil {
		mutate(claims)
	}
	for k, v := range claims {
		if v == nil {
			delete(claims, k)
		}
	}
	return claims
}

func mint(t testing.TB, alg, kid string, claims map[string]any) string {
	t.Helper()
	testKeys(t)
	signer, err := NewSigner(alg, keyFor(alg), kid)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signer.Sign(claims)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestVerifyAcceptsAllowedAlgorithms(t *testing.T) {
	for _, alg := range DefaultAlgorithms() {
		t.Run(alg, func(t *testing.T) {
			clk := newClock()
			srv := newIssuerServer(t, pubKey(t, alg, "k1"))
			v := newTestVerifier(t, clk, nil)
			verified, err := v.Verify(t.Context(), mint(t, alg, "k1", claimsAt(clk, srv.URL, nil)), srv.issuer())
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if verified.Alg != alg || verified.KeyID != "k1" || verified.Issuer != srv.URL {
				t.Fatalf("verified = %+v", verified)
			}
			if id, ok := verified.Claims["repository_id"].(string); !ok || id != "123456" {
				t.Fatalf("claims lost: %+v", verified.Claims)
			}
			if _, ok := verified.Claims["exp"].(json.Number); !ok {
				t.Fatalf("numbers must stay json.Number, got %T", verified.Claims["exp"])
			}
		})
	}
}

func rawToken(t testing.TB, alg gojose.SignatureAlgorithm, key any, kid string, claims map[string]any, headers map[gojose.HeaderKey]any) string {
	t.Helper()
	options := &gojose.SignerOptions{}
	options.WithType("JWT")
	for k, v := range headers {
		options.WithHeader(k, v)
	}
	signingKey := gojose.SigningKey{Algorithm: alg, Key: key}
	if kid != "" {
		signingKey.Key = gojose.JSONWebKey{Key: key, KeyID: kid}
	}
	signer, err := gojose.NewSigner(signingKey, options)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(claims)
	object, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	token, err := object.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func b64(v any) string {
	data, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(data)
}

func TestVerifyRejectsForbiddenAlgorithms(t *testing.T) {
	testKeys(t)
	clk := newClock()
	srv := newIssuerServer(t, pubKey(t, RS256, "k1"), pubKey(t, ES256, "k2"))
	v := newTestVerifier(t, clk, nil)
	claims := claimsAt(clk, srv.URL, nil)
	none := b64(map[string]string{"alg": "none", "kid": "k1"}) + "." + b64(claims) + "."
	hs256 := rawToken(t, gojose.HS256, []byte("0123456789abcdef0123456789abcdef"), "k1", claims, nil)
	restricted := srv.issuer()
	restricted.Algorithms = []string{RS256}
	for name, tc := range map[string]struct {
		token  string
		issuer Issuer
	}{
		"none":                        {none, srv.issuer()},
		"HS256":                       {hs256, srv.issuer()},
		"ES256 outside issuer list":   {mint(t, ES256, "k2", claims), restricted},
		"unknown algorithm in config": {mint(t, RS256, "k1", claims), Issuer{Issuer: srv.URL, Audiences: []string{"notarx"}, Mode: ModeDiscovery, Algorithms: []string{"HS256"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(t.Context(), tc.token, tc.issuer); err == nil {
				t.Fatal("token was accepted")
			}
		})
	}
	if got := srv.jwksCalls.Load(); got != 0 {
		t.Fatalf("alg must be rejected before key lookup, JWKS fetched %d times", got)
	}
}

func TestVerifyClaims(t *testing.T) {
	skew := DefaultClockSkew
	cases := map[string]struct {
		mutate func(*clock, map[string]any)
		ok     bool
	}{
		"valid":               {nil, true},
		"expired":             {func(c *clock, m map[string]any) { m["exp"] = c.Now().Add(-skew - time.Second).Unix() }, false},
		"exp exactly at skew": {func(c *clock, m map[string]any) { m["exp"] = c.Now().Add(-skew).Unix() }, false},
		"exp inside skew":     {func(c *clock, m map[string]any) { m["exp"] = c.Now().Add(-skew + time.Second).Unix() }, true},
		"nbf in future":       {func(c *clock, m map[string]any) { m["nbf"] = c.Now().Add(skew + time.Minute).Unix() }, false},
		"nbf inside skew":     {func(c *clock, m map[string]any) { m["nbf"] = c.Now().Add(skew - time.Second).Unix() }, true},
		"iat in future":       {func(c *clock, m map[string]any) { m["iat"] = c.Now().Add(skew + time.Minute).Unix() }, false},
		"iat inside skew":     {func(c *clock, m map[string]any) { m["iat"] = c.Now().Add(skew - time.Second).Unix() }, true},
		"over age": {func(c *clock, m map[string]any) {
			m["iat"] = c.Now().Add(-2 * time.Hour).Unix()
			m["exp"] = c.Now().Add(time.Minute).Unix()
		}, false},
		"exp minus iat too big": {func(c *clock, m map[string]any) { m["exp"] = c.Now().Add(25 * time.Hour).Unix() }, false},
		"wrong aud":             {func(c *clock, m map[string]any) { m["aud"] = "someone-else" }, false},
		"aud array match":       {func(c *clock, m map[string]any) { m["aud"] = []string{"x", "notarx"} }, true},
		"aud array mismatch":    {func(c *clock, m map[string]any) { m["aud"] = []string{"x", "y"} }, false},
		"aud missing":           {func(c *clock, m map[string]any) { m["aud"] = nil }, false},
		"wrong iss":             {func(c *clock, m map[string]any) { m["iss"] = "https://other.example.test" }, false},
		"sub missing":           {func(c *clock, m map[string]any) { m["sub"] = nil }, false},
		"exp missing":           {func(c *clock, m map[string]any) { m["exp"] = nil }, false},
		"exp not a number":      {func(c *clock, m map[string]any) { m["exp"] = "soon" }, false},
		"iat absent":            {func(c *clock, m map[string]any) { m["iat"] = nil; m["nbf"] = nil }, true},
		"fractional exp":        {func(c *clock, m map[string]any) { m["exp"] = float64(c.Now().Unix()) + 600.5 }, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clk := newClock()
			srv := newIssuerServer(t, pubKey(t, RS256, "k1"))
			v := newTestVerifier(t, clk, nil)
			claims := claimsAt(clk, srv.URL, func(m map[string]any) {
				if tc.mutate != nil {
					tc.mutate(clk, m)
				}
			})
			_, err := v.Verify(t.Context(), mint(t, RS256, "k1", claims), srv.issuer())
			if tc.ok && err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestVerifyRejectsBadSignatureAndHeaders(t *testing.T) {
	testKeys(t)
	clk := newClock()
	srv := newIssuerServer(t, pubKey(t, RS256, "k1"))
	v := newTestVerifier(t, clk, nil)
	claims := claimsAt(clk, srv.URL, nil)
	attacker, _ := rsa.GenerateKey(rand.Reader, 2048)
	attackerJWK := gojose.JSONWebKey{Key: &attacker.PublicKey, KeyID: "k1", Algorithm: "RS256"}
	forged := rawToken(t, gojose.RS256, attacker, "k1", claims, nil)
	// The attacker embeds its own key in jwk/jku/x5u headers; they must never be used.
	embedded := rawToken(t, gojose.RS256, attacker, "k1", claims, map[gojose.HeaderKey]any{"jwk": attackerJWK, "jku": "https://evil.example.test/jwks", "x5u": "https://evil.example.test/cert"})
	crit := rawToken(t, gojose.RS256, rsaKey, "k1", claims, map[gojose.HeaderKey]any{"crit": []string{"exp"}, "exp": 1})
	tampered := mint(t, RS256, "k1", claims)
	tampered = tampered[:len(tampered)-4] + "AAAA"
	for name, token := range map[string]string{"forged": forged, "embedded jwk": embedded, "crit": crit, "tampered": tampered, "garbage": "a.b.c", "empty": ""} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(t.Context(), token, srv.issuer()); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestVerifyKeyHardening(t *testing.T) {
	testKeys(t)
	clk := newClock()
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Skipf("1024-bit RSA keys unavailable: %v", err)
	}
	weakJWK := Key{Key: &weak.PublicKey, KeyID: "weak", Algorithm: "RS256", Use: "sig"}
	ecAsRSA := Key{Key: &ecKey.PublicKey, KeyID: "mismatch", Use: "sig"}
	pinned := pubKey(t, RS256, "pinned")
	pinned.Algorithm = "ES256" // key says ES256, token says RS256
	srv := newIssuerServer(t, weakJWK, ecAsRSA, pinned, pubKey(t, RS256, "good"))
	v := newTestVerifier(t, clk, nil)
	claims := claimsAt(clk, srv.URL, nil)
	cases := map[string]string{
		"rsa under 2048 bits":      rawToken(t, gojose.RS256, weak, "weak", claims, nil),
		"kty does not match alg":   rawToken(t, gojose.RS256, rsaKey, "mismatch", claims, nil),
		"key alg pinned elsewhere": rawToken(t, gojose.RS256, rsaKey, "pinned", claims, nil),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(t.Context(), token, srv.issuer()); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
	if _, err := v.Verify(t.Context(), mint(t, RS256, "good", claims), srv.issuer()); err != nil {
		t.Fatalf("good key rejected: %v", err)
	}
	// A P-384 key cannot satisfy ES256.
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	srv.setKeys(Key{Key: &p384.PublicKey, KeyID: "p384", Use: "sig"})
	clk.Advance(DefaultJWKSTTL + time.Second)
	token := rawToken(t, gojose.ES384, p384, "p384", claims, nil)
	if _, err := v.Verify(t.Context(), token, srv.issuer()); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("P-384 err = %v", err)
	}
}

func TestVerifyKeyWithoutKid(t *testing.T) {
	testKeys(t)
	clk := newClock()
	single := pubKey(t, RS256, "k1")
	srv := newIssuerServer(t, single)
	v := newTestVerifier(t, clk, nil)
	claims := claimsAt(clk, srv.URL, nil)
	token := rawToken(t, gojose.RS256, rsaKey, "", claims, nil)
	if _, err := v.Verify(t.Context(), token, srv.issuer()); err != nil {
		t.Fatalf("single-key set without kid: %v", err)
	}
	srv.setKeys(single, pubKey(t, ES256, "k2"))
	clk.Advance(DefaultJWKSTTL + time.Second)
	if _, err := v.Verify(t.Context(), token, srv.issuer()); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("multi-key set without kid err = %v", err)
	}
}

func TestUnknownKidRefetchesOnceAndIsRateLimited(t *testing.T) {
	clk := newClock()
	srv := newIssuerServer(t, pubKey(t, RS256, "old"))
	v := newTestVerifier(t, clk, nil)
	claims := claimsAt(clk, srv.URL, nil)
	if _, err := v.Verify(t.Context(), mint(t, RS256, "old", claims), srv.issuer()); err != nil {
		t.Fatal(err)
	}
	base := srv.jwksCalls.Load()
	// The issuer rotates: a new kid appears.
	srv.setKeys(pubKey(t, RS256, "old"), pubKey(t, RS256, "new"))
	if _, err := v.Verify(t.Context(), mint(t, RS256, "new", claims), srv.issuer()); err != nil {
		t.Fatalf("rotated kid: %v", err)
	}
	if got := srv.jwksCalls.Load() - base; got != 1 {
		t.Fatalf("rotation fetched %d times, want 1", got)
	}
	// Forged kids: the first costs one refetch, the following ones are rate limited,
	// and repeating one of them is answered from the negative cache.
	for i := 0; i < 20; i++ {
		kid := "forged-" + string(rune('a'+i))
		if _, err := v.Verify(t.Context(), mint(t, RS256, kid, claims), srv.issuer()); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("forged kid err = %v", err)
		}
	}
	if got := srv.jwksCalls.Load() - base; got != 1 {
		t.Fatalf("forged kids caused %d fetches in total, want 1 (the rotation refetch used the window)", got)
	}
	clk.Advance(DefaultKidRefetchWindow + time.Second)
	if _, err := v.Verify(t.Context(), mint(t, RS256, "forged-a", claims), srv.issuer()); err == nil {
		t.Fatal("forged kid accepted")
	}
	if got := srv.jwksCalls.Load() - base; got != 2 {
		t.Fatalf("after the window fetches = %d, want 2", got)
	}
}

func TestUnknownKidRefetchIsSharedAcrossConcurrentRequests(t *testing.T) {
	clk := newClock()
	srv := newIssuerServer(t, pubKey(t, RS256, "old"))
	v := newTestVerifier(t, clk, nil)
	claims := claimsAt(clk, srv.URL, nil)
	if _, err := v.Verify(t.Context(), mint(t, RS256, "old", claims), srv.issuer()); err != nil {
		t.Fatal(err)
	}
	base := srv.jwksCalls.Load()
	srv.setKeys(pubKey(t, RS256, "old"), pubKey(t, RS256, "new"))
	token := mint(t, RS256, "new", claims)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = v.Verify(context.Background(), token, srv.issuer())
		}()
	}
	wg.Wait()
	if got := srv.jwksCalls.Load() - base; got != 1 {
		t.Fatalf("concurrent unknown kid fetched %d times, want 1", got)
	}
}

func TestJWKSTTLAndPerIssuerOverride(t *testing.T) {
	clk := newClock()
	srv := newIssuerServer(t, pubKey(t, RS256, "k1"))
	v := newTestVerifier(t, clk, nil)
	claims := claimsAt(clk, srv.URL, func(m map[string]any) { m["exp"] = clk.Now().Add(50 * time.Minute).Unix() })
	token := mint(t, RS256, "k1", claims)
	notarx := srv.issuer()
	notarx.JWKSTTL = 60 * time.Second
	if _, err := v.Verify(t.Context(), token, notarx); err != nil {
		t.Fatal(err)
	}
	clk.Advance(59 * time.Second)
	if _, err := v.Verify(t.Context(), token, notarx); err != nil {
		t.Fatal(err)
	}
	if got := srv.jwksCalls.Load(); got != 1 {
		t.Fatalf("fetches inside the 60 s TTL = %d, want 1", got)
	}
	// ADR 0020 revoke: the key is withdrawn and notarx-trusted issuer keys refresh within 60 s.
	srv.setKeys(pubKey(t, ES256, "other"))
	clk.Advance(2 * time.Second)
	if _, err := v.Verify(t.Context(), token, notarx); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("revoked key still accepted: %v", err)
	}
	// The default TTL is 10 minutes, so the same issuer without the override keeps its cache.
	clk2 := newClock()
	srv2 := newIssuerServer(t, pubKey(t, RS256, "k1"))
	v2 := newTestVerifier(t, clk2, nil)
	token2 := mint(t, RS256, "k1", claimsAt(clk2, srv2.URL, nil))
	if _, err := v2.Verify(t.Context(), token2, srv2.issuer()); err != nil {
		t.Fatal(err)
	}
	srv2.setKeys(pubKey(t, ES256, "other"))
	clk2.Advance(2 * time.Minute)
	if _, err := v2.Verify(t.Context(), token2, srv2.issuer()); err != nil {
		t.Fatalf("default TTL must keep the cache for 10 minutes: %v", err)
	}
}

func TestStaleKeysServedOnFetchFailure(t *testing.T) {
	clk := newClock()
	metrics := &counters{}
	srv := newIssuerServer(t, pubKey(t, RS256, "k1"))
	v := newTestVerifier(t, clk, metrics)
	token := mint(t, RS256, "k1", claimsAt(clk, srv.URL, func(m map[string]any) { m["exp"] = clk.Now().Add(100 * time.Hour).Unix() }))
	// iat must stay inside the max age, so refresh iat through a fresh token at each step.
	fresh := func() string {
		return mint(t, RS256, "k1", claimsAt(clk, srv.URL, nil))
	}
	if _, err := v.Verify(t.Context(), token, srv.issuer()); err == nil || !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("exp - iat above max age must be rejected: %v", err)
	}
	if _, err := v.Verify(t.Context(), fresh(), srv.issuer()); err != nil {
		t.Fatal(err)
	}
	srv.fail.Store(true)
	clk.Advance(DefaultJWKSTTL + time.Minute)
	if _, err := v.Verify(t.Context(), fresh(), srv.issuer()); err != nil {
		t.Fatalf("stale keys must be served on failure: %v", err)
	}
	if metrics.stale.Load() == 0 || metrics.fetchFailed.Load() == 0 {
		t.Fatalf("metrics stale=%d failed=%d", metrics.stale.Load(), metrics.fetchFailed.Load())
	}
	calls := srv.jwksCalls.Load()
	if _, err := v.Verify(t.Context(), fresh(), srv.issuer()); err != nil {
		t.Fatal(err)
	}
	if srv.jwksCalls.Load() != calls {
		t.Fatal("a failing issuer must not be hammered inside the refetch window")
	}
	clk.Advance(DefaultJWKSStaleMax + DefaultJWKSTTL)
	if _, err := v.Verify(t.Context(), fresh(), srv.issuer()); !errors.Is(err, ErrKeySourceUnavailable) {
		t.Fatalf("beyond the stale limit err = %v, want ErrKeySourceUnavailable", err)
	}
}

func TestStaticJWKS(t *testing.T) {
	clk := newClock()
	metrics := &counters{}
	doc, err := MarshalJWKS(pubKey(t, ES256, "static-1"))
	if err != nil {
		t.Fatal(err)
	}
	v := newTestVerifier(t, clk, metrics)
	issuer := Issuer{Issuer: "https://kubernetes.default.svc", Audiences: []string{"notarx"}, Mode: ModeStatic, StaticJWKS: doc}
	claims := claimsAt(clk, issuer.Issuer, nil)
	if _, err := v.Verify(t.Context(), mint(t, ES256, "static-1", claims), issuer); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(t.Context(), mint(t, ES256, "rotated", claims), issuer); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("static kid miss err = %v", err)
	}
	if metrics.staticMiss.Load() != 1 {
		t.Fatalf("static kid miss metric = %d", metrics.staticMiss.Load())
	}
	bad := Issuer{Issuer: "https://bad.example.test", Audiences: []string{"notarx"}, Mode: ModeStatic, StaticJWKS: []byte("{}")}
	if _, err := v.Verify(t.Context(), mint(t, ES256, "x", claimsAt(clk, bad.Issuer, nil)), bad); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("empty static jwks err = %v", err)
	}
}

func TestVerifyConfigurationErrors(t *testing.T) {
	clk := newClock()
	v := newTestVerifier(t, clk, nil)
	for name, issuer := range map[string]Issuer{
		"no issuer":    {Audiences: []string{"a"}, Mode: ModeDiscovery},
		"no audience":  {Issuer: "https://i.example.test", Mode: ModeDiscovery},
		"unknown mode": {Issuer: "https://i.example.test", Audiences: []string{"a"}, Mode: "other"},
		"bad CA":       {Issuer: "https://i.example.test", Audiences: []string{"a"}, Mode: ModeDiscovery, CABundlePEM: "not pem"},
	} {
		token := mint(t, RS256, "k", claimsAt(clk, issuer.Issuer, nil))
		if _, err := v.Verify(t.Context(), token, issuer); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("%s: err = %v, want ErrInvalidConfig", name, err)
		}
	}
	for name, opts := range map[string]Options{
		"skew above 5m":  {ClockSkew: 6 * time.Minute},
		"negative skew":  {ClockSkew: -time.Second},
		"negative age":   {MaxTokenAge: -time.Second},
		"negative stale": {JWKSStaleMax: -time.Second},
	} {
		if _, err := NewVerifier(opts); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if v, err := NewVerifier(Options{}); err != nil || v.opts.JWKSTTL != DefaultJWKSTTL || v.opts.KidRefetchInterval != DefaultKidRefetchWindow {
		t.Fatalf("zero options: %v %+v", err, v)
	}
}

func TestDiscoveryIssuerMustMatch(t *testing.T) {
	clk := newClock()
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"https://elsewhere.example.test","jwks_uri":"https://elsewhere.example.test/jwks"}`))
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	issuer := Issuer{Issuer: srv.URL, Audiences: []string{"notarx"}, Mode: ModeDiscovery, AllowPrivateNetwork: true,
		CABundlePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))}
	v := newTestVerifier(t, clk, nil)
	if _, err := v.Verify(t.Context(), mint(t, RS256, "k1", claimsAt(clk, srv.URL, nil)), issuer); !errors.Is(err, ErrKeySourceUnavailable) {
		t.Fatalf("err = %v, want ErrKeySourceUnavailable", err)
	}
}

func TestExplicitJWKSURISkipsDiscovery(t *testing.T) {
	clk := newClock()
	srv := newIssuerServer(t, pubKey(t, RS256, "k1"))
	issuer := srv.issuer()
	issuer.JWKSURI = srv.URL + "/jwks"
	v := newTestVerifier(t, clk, nil)
	if _, err := v.Verify(t.Context(), mint(t, RS256, "k1", claimsAt(clk, srv.URL, nil)), issuer); err != nil {
		t.Fatal(err)
	}
	if srv.discCalls.Load() != 0 {
		t.Fatal("discovery must be skipped when jwks_uri is configured")
	}
}

func TestConfigChangeReplacesKeySet(t *testing.T) {
	clk := newClock()
	srv := newIssuerServer(t, pubKey(t, RS256, "k1"))
	v := newTestVerifier(t, clk, nil)
	token := mint(t, RS256, "k1", claimsAt(clk, srv.URL, nil))
	if _, err := v.Verify(t.Context(), token, srv.issuer()); err != nil {
		t.Fatal(err)
	}
	srv.setKeys(pubKey(t, ES256, "other"))
	changed := srv.issuer()
	changed.AllowPrivateNetwork = true
	changed.JWKSURI = srv.URL + "/jwks"
	if _, err := v.Verify(t.Context(), token, changed); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("a changed issuer configuration must not reuse cached keys: %v", err)
	}
}

func TestParseJWKSSkipsUnusableKeys(t *testing.T) {
	good := pubKey(t, RS256, "good")
	doc, _ := MarshalJWKS(good)
	var set map[string]any
	_ = json.Unmarshal(doc, &set)
	keys := set["keys"].([]any)
	keys = append(keys, map[string]any{"kty": "oct", "k": "AAAA", "kid": "sym"}, map[string]any{"kty": "RSA"}, map[string]any{"kty": "EC", "crv": "P-256", "x": "AA", "y": "AA", "kid": "bad"})
	set["keys"] = keys
	data, _ := json.Marshal(set)
	parsed, err := ParseJWKS(data)
	if err != nil || len(parsed) != 1 || parsed[0].KeyID != "good" {
		t.Fatalf("ParseJWKS = %v, %v", parsed, err)
	}
	if _, err := ParseJWKS([]byte("nope")); err == nil {
		t.Fatal("non-JSON accepted")
	}
	if _, err := ParseJWKS([]byte(`{"keys":[]}`)); err == nil {
		t.Fatal("empty set accepted")
	}
}

func TestErrorFormatting(t *testing.T) {
	err := unavailable("fetch failed", errors.New("boom"))
	if !errors.Is(err, ErrKeySourceUnavailable) || errors.Is(err, ErrInvalidToken) || err.Error() == "" {
		t.Fatalf("err = %v", err)
	}
	var detail *Error
	if !errors.As(err, &detail) || detail.Reason != "fetch failed" || detail.Unwrap() == nil {
		t.Fatalf("detail = %+v", detail)
	}
	if err := invalid("x"); err.Error() != "jose: invalid token: x" {
		t.Fatalf("err = %q", err)
	}
}

var _ = x509.NewCertPool

func TestColdStartConcurrentRequestsJoinTheFirstFetch(t *testing.T) {
	clk := newClock()
	srv := newIssuerServer(t, pubKey(t, RS256, "k1"))
	v := newTestVerifier(t, clk, nil)
	token := mint(t, RS256, "k1", claimsAt(clk, srv.URL, nil))
	var wg sync.WaitGroup
	failures := make(chan error, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := v.Verify(context.Background(), token, srv.issuer()); err != nil {
				failures <- err
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("a request that overlapped the first fetch failed: %v", err)
	}
	if got := srv.jwksCalls.Load(); got != 1 {
		t.Fatalf("cold start fetched %d times, want 1", got)
	}
}

func TestMaxTokenAgeWithoutIAT(t *testing.T) {
	clk := newClock()
	srv := newIssuerServer(t, pubKey(t, RS256, "k1"))
	v := newTestVerifier(t, clk, nil)
	noIAT := func(exp time.Duration) string {
		return mint(t, RS256, "k1", claimsAt(clk, srv.URL, func(m map[string]any) {
			m["iat"], m["nbf"] = nil, nil
			m["exp"] = clk.Now().Add(exp).Unix()
		}))
	}
	if _, err := v.Verify(t.Context(), noIAT(30*time.Minute), srv.issuer()); err != nil {
		t.Fatalf("short-lived token without iat: %v", err)
	}
	if _, err := v.Verify(t.Context(), noIAT(10*365*24*time.Hour), srv.issuer()); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("a ten year token without iat err = %v", err)
	}
}
