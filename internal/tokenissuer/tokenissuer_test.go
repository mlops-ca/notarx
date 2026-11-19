package tokenissuer

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/secrets"
)

const ringKey = "0123456789abcdef0123456789abcdef"

var testGen = Generator{RSABits: 2048}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }
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

func setRing(t *testing.T) {
	t.Helper()
	t.Setenv(secrets.KeyEnv, ringKey)
	t.Setenv(secrets.KeyPreviousEnv, "")
}

func verifyWith(t *testing.T, signer Signer, token, issuer, audience string) *jose.Verified {
	t.Helper()
	jwks, err := jose.MarshalJWKS(signer.PublicKeys(context.Background())...)
	if err != nil {
		t.Fatal(err)
	}
	options := jose.DefaultOptions()
	options.Now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	verifier, err := jose.NewVerifier(options)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := verifier.Verify(context.Background(), token, jose.Issuer{
		Issuer: issuer, Audiences: []string{audience}, Mode: jose.ModeStatic, StaticJWKS: jwks,
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return verified
}

func newManager(store Store, clock *fakeClock, alg string) *Manager {
	config := DefaultLifecycleConfig()
	config.Alg = alg
	return &Manager{Store: store, Config: config, Generator: testGen, Now: clock.Now, Alert: func(string, error) {}}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	setRing(t)
	for _, alg := range []string{jose.RS256, jose.ES256} {
		t.Run(alg, func(t *testing.T) {
			clock := newClock()
			store := NewMemoryStore()
			if _, _, err := newManager(store, clock, alg).Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			signer, err := NewLocalSigner(store, SignerConfig{Alg: alg, Now: clock.Now})
			if err != nil {
				t.Fatal(err)
			}
			claims, err := ClaimBuilder{Issuer: "https://issuer.example", Now: clock.Now}.Build(ClaimRequest{
				Subject: "notarx:service-account:ci", Audiences: []string{"api://AzureADTokenExchange"},
				ServiceAccount: "sa-1", Credential: CredentialAPIKey,
			})
			if err != nil {
				t.Fatal(err)
			}
			token, kid, err := signer.Sign(t.Context(), claims)
			if err != nil {
				t.Fatal(err)
			}
			verified := verifyWith(t, signer, token, "https://issuer.example", "api://AzureADTokenExchange")
			if verified.KeyID != kid || verified.Alg != alg {
				t.Fatalf("kid=%s alg=%s", verified.KeyID, verified.Alg)
			}
			parts := strings.Split(token, ".")
			header := decode(t, parts[0])
			if header["typ"] != "JWT" || header["kid"] != kid || header["alg"] != alg || len(header) != 3 {
				t.Fatalf("header: %v", header)
			}
			payload := decode(t, parts[1])
			for _, forbidden := range []string{"email", "groups", "roles"} {
				if _, ok := payload[forbidden]; ok {
					t.Fatalf("claim %s must not be present", forbidden)
				}
			}
			if payload["nbf"] != payload["iat"] || payload["notarx_sa"] != "sa-1" || payload["notarx_cred"] != "api_key" || len(payload["jti"].(string)) != 32 {
				t.Fatalf("payload: %v", payload)
			}
		})
	}
}

func decode(t *testing.T, segment string) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestKeyUsesThumbprintKidAndSealsPrivateKey(t *testing.T) {
	setRing(t)
	record, err := Generator{}.Generate(jose.RS256, StateNext, newClock().Now())
	if err != nil {
		t.Fatal(err)
	}
	public, err := record.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	thumb, _ := jose.Thumbprint(public.Key)
	if record.KID != thumb {
		t.Fatalf("kid %s is not the thumbprint %s", record.KID, thumb)
	}
	if bits := public.Key.(*rsa.PublicKey).N.BitLen(); bits != 3072 {
		t.Fatalf("default rsa size = %d", bits)
	}
	if _, err := secrets.Open(SealPurpose(record.KID), record.PrivateSealed); err != nil {
		t.Fatalf("sealed under token-signing-key:<kid>: %v", err)
	}
	if _, err := secrets.Open(SealPurpose("other"), record.PrivateSealed); err == nil {
		t.Fatal("the seal must bind the kid")
	}
	if strings.Contains(string(record.PublicJWK), "\"d\"") {
		t.Fatal("the public JWK must not contain private parameters")
	}
	if _, err := (Generator{RSABits: 1024}).Generate(jose.RS256, StateNext, time.Now()); err == nil {
		t.Fatal("small rsa keys must be refused")
	}
	if _, err := testGen.Generate("HS256", StateNext, time.Now()); err == nil {
		t.Fatal("unsupported algorithm must be refused")
	}
}

func TestIntegrityRejectsTampering(t *testing.T) {
	setRing(t)
	record, err := testGen.Generate(jose.ES256, StateActive, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other, _ := testGen.Generate(jose.ES256, StateActive, time.Now())
	if err := record.VerifyIntegrity(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*KeyRecord){
		"swapped public jwk": func(r *KeyRecord) { r.PublicJWK = other.PublicJWK },
		"state":              func(r *KeyRecord) { r.State = StateRetired },
		"alg":                func(r *KeyRecord) { r.Alg = jose.RS256 },
		"mac":                func(r *KeyRecord) { r.IntegrityMAC = other.IntegrityMAC },
		"empty mac":          func(r *KeyRecord) { r.IntegrityMAC = "" },
		"kid":                func(r *KeyRecord) { r.KID = "x" },
	}
	for name, mutate := range cases {
		tampered := record
		mutate(&tampered)
		if err := tampered.VerifyIntegrity(); !errors.Is(err, ErrIntegrity) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := tampered.Signer(); !errors.Is(err, ErrIntegrity) {
			t.Errorf("%s must never sign: %v", name, err)
		}
	}
	// A re-MACed row whose kid is not the thumbprint of its key is also refused.
	forged := record
	forged.KID = "forged"
	if err := forged.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := forged.VerifyIntegrity(); !errors.Is(err, ErrIntegrity) {
		t.Errorf("kid must be the thumbprint: %v", err)
	}
}

func TestIntegrityMACSurvivesJSONBReencoding(t *testing.T) {
	setRing(t)
	record, _ := testGen.Generate(jose.ES256, StateActive, time.Now())
	var generic map[string]any
	_ = json.Unmarshal(record.PublicJWK, &generic)
	reordered, _ := json.MarshalIndent(generic, "", "  ")
	record.PublicJWK = reordered
	if err := record.VerifyIntegrity(); err != nil {
		t.Fatalf("jsonb normalises whitespace and key order: %v", err)
	}
}

func TestClaimBuilder(t *testing.T) {
	clock := newClock()
	builder := ClaimBuilder{Issuer: "https://i.example", Now: clock.Now}
	good := ClaimRequest{Subject: "s", Audiences: []string{"a"}}
	claims, err := builder.Build(good)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Expiry-claims.IssuedAt != 300 || claims.NotBefore != claims.IssuedAt {
		t.Fatalf("default lifetime: %+v", claims)
	}
	other, _ := builder.Build(good)
	if claims.JTI == other.JTI {
		t.Fatal("jti must be random")
	}
	for name, request := range map[string]ClaimRequest{
		"over 10m":     {Subject: "s", Audiences: []string{"a"}, Lifetime: 11 * time.Minute},
		"negative":     {Subject: "s", Audiences: []string{"a"}, Lifetime: -time.Second},
		"multi aud":    {Subject: "s", Audiences: []string{"a", "b"}},
		"no aud":       {Subject: "s"},
		"empty aud":    {Subject: "s", Audiences: []string{" "}},
		"no subject":   {Audiences: []string{"a"}},
		"unknown cred": {Subject: "s", Audiences: []string{"a"}, Credential: "password"},
	} {
		if _, err := builder.Build(request); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := builder.Build(ClaimRequest{Subject: "s", Audiences: []string{"a"}, Lifetime: 10 * time.Minute}); err != nil {
		t.Fatalf("exactly 10m is allowed: %v", err)
	}
	lowered := ClaimBuilder{Issuer: "i", MaxLifetime: time.Minute, Now: clock.Now}
	if _, err := lowered.Build(ClaimRequest{Subject: "s", Audiences: []string{"a"}, Lifetime: 2 * time.Minute}); err == nil {
		t.Fatal("configured ceiling must apply")
	}
	raised := ClaimBuilder{Issuer: "i", MaxLifetime: time.Hour, Now: clock.Now}
	if _, err := raised.Build(ClaimRequest{Subject: "s", Audiences: []string{"a"}, Lifetime: 30 * time.Minute}); err == nil {
		t.Fatal("the hard maximum cannot be raised")
	}
	if _, err := (ClaimBuilder{}).Build(good); err == nil {
		t.Fatal("issuer is required")
	}
}

func TestLifecycleConfigValidate(t *testing.T) {
	if err := DefaultLifecycleConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*LifecycleConfig){
		"alg":        func(c *LifecycleConfig) { c.Alg = "EdDSA" },
		"lead":       func(c *LifecycleConfig) { c.PublishLead = 0 },
		"short lead": func(c *LifecycleConfig) { c.PublishLead = 5 * time.Minute },
		"interval":   func(c *LifecycleConfig) { c.RotationInterval = c.PublishLead },
		"lifetime":   func(c *LifecycleConfig) { c.MaxTokenLifetime = 11 * time.Minute },
		"zero":       func(c *LifecycleConfig) { c.MaxTokenLifetime = 0 },
	} {
		config := DefaultLifecycleConfig()
		mutate(&config)
		if config.Validate() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func states(t *testing.T, store *MemoryStore, alg string) map[State]KeyRecord {
	t.Helper()
	records, _ := store.ListKeys(t.Context())
	out := map[State]KeyRecord{}
	for _, record := range records {
		if record.Alg != alg {
			continue
		}
		if _, dup := out[record.State]; dup && record.State != StateRetired {
			t.Fatalf("two %s keys for %s", record.State, alg)
		}
		out[record.State] = record
	}
	return out
}

func TestLifecyclePublishBeforeUseAndRetention(t *testing.T) {
	setRing(t)
	clock := newClock()
	store := NewMemoryStore()
	manager := newManager(store, clock, jose.RS256)
	pass := func() bool {
		t.Helper()
		_, changed, err := manager.Reconcile(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}
	if !pass() {
		t.Fatal("bootstrap must change state")
	}
	first := states(t, store, jose.RS256)
	if first[StateActive].KID == "" || first[StateNext].KID == "" || first[StateActive].ActivatedAt.IsZero() {
		t.Fatalf("bootstrap creates an active and a next key: %+v", first)
	}
	if pass() {
		t.Fatal("a second pass at the same instant must be a no-op (idempotent)")
	}
	clock.Advance(719 * time.Hour)
	if pass() {
		t.Fatal("nothing happens before the interval")
	}
	clock.Advance(time.Hour) // 720h
	if !pass() {
		t.Fatal("rotation is due")
	}
	second := states(t, store, jose.RS256)
	if second[StateActive].KID != first[StateNext].KID {
		t.Fatal("the published next key becomes active")
	}
	retired := second[StateRetired]
	if retired.KID != first[StateActive].KID {
		t.Fatal("the old active key is retired")
	}
	if want := clock.Now().Add(10*time.Minute + 24*time.Hour); !retired.ExpiresAt.Equal(want) {
		t.Fatalf("retention = %s want %s", retired.ExpiresAt, want)
	}
	if _, ok := second[StateNext]; ok {
		t.Fatal("the next key is generated at interval minus lead, not at once")
	}
	published := PublishedKeys(mustList(t, store), clock.Now(), nil)
	if len(published) != 2 {
		t.Fatalf("active and retired are published: %d", len(published))
	}
	clock.Advance(696 * time.Hour)
	pass()
	third := states(t, store, jose.RS256)
	if third[StateNext].KID == "" {
		t.Fatal("next key generated at rotation_interval - publish_lead")
	}
	if third[StateNext].PublishedAt.Before(third[StateActive].ActivatedAt) {
		t.Fatal("next is published after the active activation")
	}
	// The retired key left the JWKS and the table once retention passed.
	if _, ok := third[StateRetired]; ok {
		t.Fatal("expired retired key must be deleted")
	}
}

func mustList(t *testing.T, store *MemoryStore) []KeyRecord {
	t.Helper()
	records, err := store.ListKeys(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func TestLifecycleNextIsNotPromotedBeforeLeadElapsed(t *testing.T) {
	setRing(t)
	clock := newClock()
	store := NewMemoryStore()
	manager := newManager(store, clock, jose.ES256)
	manager.Config.RotationInterval = 48 * time.Hour
	if _, _, err := manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	bootstrap := states(t, store, jose.ES256)
	// Interval passed, but force the next key to look freshly published.
	clock.Advance(48 * time.Hour)
	fresh := bootstrap[StateNext]
	fresh.PublishedAt = clock.Now().Add(-time.Hour)
	if err := fresh.Seal(); err != nil {
		t.Fatal(err)
	}
	store.Put(fresh)
	if _, changed, err := manager.Reconcile(t.Context()); err != nil || changed {
		t.Fatalf("a key published for 1h must not activate: changed=%v err=%v", changed, err)
	}
	clock.Advance(23 * time.Hour)
	if _, changed, err := manager.Reconcile(t.Context()); err != nil || !changed {
		t.Fatalf("after the lead it activates: changed=%v err=%v", changed, err)
	}
	if got := states(t, store, jose.ES256)[StateActive].KID; got != fresh.KID {
		t.Fatalf("active = %s", got)
	}
}

func TestPerAlgorithmBootstrapAndAlgorithmSwitch(t *testing.T) {
	setRing(t)
	clock := newClock()
	store := NewMemoryStore()
	manager := newManager(store, clock, jose.RS256)
	if _, _, err := manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	rsaSigner, _ := NewLocalSigner(store, SignerConfig{Alg: jose.RS256, Now: clock.Now})
	esSigner, _ := NewLocalSigner(store, SignerConfig{Alg: jose.ES256, Now: clock.Now})
	claims, _ := ClaimBuilder{Issuer: "i", Now: clock.Now}.Build(ClaimRequest{Subject: "s", Audiences: []string{"a"}})
	if _, _, err := esSigner.Sign(t.Context(), claims); !errors.Is(err, ErrNoActiveKey) {
		t.Fatalf("no ES256 key exists yet: %v", err)
	}
	_, rsaKid, err := rsaSigner.Sign(t.Context(), claims)
	if err != nil {
		t.Fatal(err)
	}
	rsaState := states(t, store, jose.RS256)
	// Switching the configured algorithm bootstraps the missing pair and keeps RS256 rotating.
	manager.Config.Alg = jose.ES256
	if _, changed, err := manager.Reconcile(t.Context()); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	es := states(t, store, jose.ES256)
	if es[StateActive].KID == "" || es[StateNext].KID == "" {
		t.Fatalf("ES256 pair: %+v", es)
	}
	if after := states(t, store, jose.RS256); after[StateActive].KID != rsaState[StateActive].KID || after[StateNext].KID != rsaState[StateNext].KID {
		t.Fatal("switching the algorithm must not touch RS256 keys")
	}
	if _, kid, err := esSigner.Sign(t.Context(), claims); err != nil || kid != es[StateActive].KID {
		t.Fatalf("es kid=%s err=%v", kid, err)
	}
	if _, kid, _ := rsaSigner.Sign(t.Context(), claims); kid != rsaKid {
		t.Fatal("the RS256 signer keeps its key")
	}
	if len(esSigner.PublicKeys(t.Context())) != 4 {
		t.Fatalf("both algorithms are published: %d", len(esSigner.PublicKeys(t.Context())))
	}
}

func TestStaleCacheStillSignsWithPublishedKey(t *testing.T) {
	setRing(t)
	clock := newClock()
	store := NewMemoryStore()
	manager := newManager(store, clock, jose.RS256)
	if _, _, err := manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	signer, _ := NewLocalSigner(store, SignerConfig{Alg: jose.RS256, Now: clock.Now})
	claims, _ := ClaimBuilder{Issuer: "i", Now: clock.Now}.Build(ClaimRequest{Subject: "s", Audiences: []string{"a"}})
	_, staleKid, err := signer.Sign(t.Context(), claims)
	if err != nil {
		t.Fatal(err)
	}
	published := states(t, store, jose.RS256)[StateNext].KID
	clock.Advance(720 * time.Hour)
	// The node's cache has expired by now; rotate and sign again from a cache loaded just before.
	if _, err := signer.activeKey(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, kid, err := signer.Sign(t.Context(), claims) // still inside the 30 s cache window
	if err != nil {
		t.Fatal(err)
	}
	if kid != staleKid {
		t.Fatalf("cached kid %s, want the previous active key %s", kid, staleKid)
	}
	// The key the stale node used is retired but still published, so verifiers trust it.
	found := false
	for _, key := range PublishedKeys(mustList(t, store), clock.Now(), nil) {
		found = found || key.KeyID == kid
	}
	if !found {
		t.Fatal("the stale key must remain published")
	}
	if states(t, store, jose.RS256)[StateActive].KID != published {
		t.Fatal("the promoted key is the one published during the lead time")
	}
	clock.Advance(31 * time.Second)
	if _, kid, _ := signer.Sign(t.Context(), claims); kid != published {
		t.Fatal("after the cache expires the node signs with the new key")
	}
}

func TestTamperedRowsNeverSignAndAreNotPublished(t *testing.T) {
	setRing(t)
	clock := newClock()
	store := NewMemoryStore()
	if _, _, err := newManager(store, clock, jose.ES256).Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	active := states(t, store, jose.ES256)[StateActive]
	attacker, _ := testGen.Generate(jose.ES256, StateActive, clock.Now())
	tampered := active
	tampered.PublicJWK = attacker.PublicJWK
	store.Put(tampered)
	var alerts []string
	signer, _ := NewLocalSigner(store, SignerConfig{Alg: jose.ES256, Now: clock.Now, Alert: func(kid string, _ error) { alerts = append(alerts, kid) }})
	claims, _ := ClaimBuilder{Issuer: "i", Now: clock.Now}.Build(ClaimRequest{Subject: "s", Audiences: []string{"a"}})
	if _, _, err := signer.Sign(t.Context(), claims); !errors.Is(err, ErrNoActiveKey) {
		t.Fatalf("a failing row must never sign: %v", err)
	}
	if len(alerts) == 0 {
		t.Fatal("a failing row raises an alert")
	}
	for _, key := range signer.PublicKeys(t.Context()) {
		if key.KeyID == active.KID {
			t.Fatal("a failing row must be omitted from the JWKS")
		}
	}
}

func TestConcurrentManagersConverge(t *testing.T) {
	setRing(t)
	clock := newClock()
	store := NewMemoryStore()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := newManager(store, clock, jose.ES256).Reconcile(context.Background()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	records := mustList(t, store)
	if len(records) != 2 {
		t.Fatalf("expected exactly one active and one next key, got %d", len(records))
	}
	states(t, store, jose.ES256)
}

func TestRotateNowAndRevoke(t *testing.T) {
	setRing(t)
	clock := newClock()
	store := NewMemoryStore()
	manager := newManager(store, clock, jose.ES256)
	changes := 0
	manager.Changed = func() { changes++ }
	if _, err := manager.RotateNow(t.Context(), jose.ES256); !errors.Is(err, ErrNoNextKey) {
		t.Fatalf("no keys yet: %v", err)
	}
	if _, _, err := manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := states(t, store, jose.ES256)
	result, err := manager.RotateNow(t.Context(), jose.ES256)
	if err != nil {
		t.Fatal(err)
	}
	if result.Retired != before[StateActive].KID || result.Activated != before[StateNext].KID || result.Warning == "" {
		t.Fatalf("result: %+v", result)
	}
	after := states(t, store, jose.ES256)
	if after[StateActive].KID != before[StateNext].KID || after[StateRetired].KID != before[StateActive].KID || after[StateNext].KID == "" {
		t.Fatalf("manual rotation: %+v", after)
	}
	// Emergency revoke of the active key: out of the JWKS at once, next promoted.
	if err := manager.Revoke(t.Context(), after[StateActive].KID); err != nil {
		t.Fatal(err)
	}
	for _, key := range PublishedKeys(mustList(t, store), clock.Now(), nil) {
		if key.KeyID == after[StateActive].KID {
			t.Fatal("a revoked key must leave the JWKS immediately")
		}
	}
	if got := states(t, store, jose.ES256)[StateActive].KID; got != after[StateNext].KID {
		t.Fatalf("the next key replaces the revoked active key: %s", got)
	}
	if err := manager.Revoke(t.Context(), "missing"); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("revoke unknown: %v", err)
	}
	if changes != 3 { // bootstrap, rotate, revoke
		t.Fatalf("changes=%d", changes)
	}
	// Next pass removes the revoked row and refills the missing next key.
	if _, _, err := manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(mustList(t, store)) != 2 {
		t.Fatalf("rows: %d", len(mustList(t, store)))
	}
}

func TestSignerFailsClosedWithoutKeyOrRing(t *testing.T) {
	setRing(t)
	store := NewMemoryStore()
	signer, _ := NewLocalSigner(store, SignerConfig{Alg: jose.RS256})
	claims, _ := ClaimBuilder{Issuer: "i"}.Build(ClaimRequest{Subject: "s", Audiences: []string{"a"}})
	if _, _, err := signer.Sign(t.Context(), claims); !errors.Is(err, ErrNoActiveKey) {
		t.Fatal(err)
	}
	if _, err := NewLocalSigner(store, SignerConfig{Alg: "none"}); err == nil {
		t.Fatal("bad algorithm accepted")
	}
	if got := signer.PublicKeys(t.Context()); len(got) != 0 {
		t.Fatal("no keys expected")
	}
	// A changed encryption key without reseal makes signing fail closed.
	if _, _, err := newManager(store, newClock(), jose.ES256).Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	es, _ := NewLocalSigner(store, SignerConfig{Alg: jose.ES256, Now: newClock().Now, Alert: func(string, error) {}})
	t.Setenv(secrets.KeyEnv, "fedcba9876543210fedcba9876543210")
	if _, _, err := es.Sign(t.Context(), claims); err == nil {
		t.Fatal("signing with an unrelated encryption key must fail")
	}
}

func TestJWKSServiceCachesAndInvalidates(t *testing.T) {
	setRing(t)
	clock := newClock()
	store := &countingStore{MemoryStore: NewMemoryStore()}
	if _, _, err := newManager(store, clock, jose.ES256).Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	store.lists = 0
	service := &JWKSService{Store: store, Now: clock.Now}
	body, etag, err := service.Document(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		again, tag, _ := service.Document(t.Context())
		if string(again) != string(body) || tag != etag {
			t.Fatal("cached document changed")
		}
	}
	if store.lists != 1 {
		t.Fatalf("store queried %d times for 6 requests", store.lists)
	}
	if !strings.HasPrefix(etag, `"`) || strings.HasPrefix(etag, "W/") {
		t.Fatalf("etag must be strong: %s", etag)
	}
	service.Invalidate()
	_, _, _ = service.Document(t.Context())
	clock.Advance(time.Minute)
	_, _, _ = service.Document(t.Context())
	if store.lists != 3 {
		t.Fatalf("invalidate and ttl expiry must reload: %d", store.lists)
	}
}

type countingStore struct {
	*MemoryStore
	lists int
}

func (s *countingStore) ListPublicKeys(ctx context.Context) ([]KeyRecord, error) {
	s.lists++
	return s.MemoryStore.ListPublicKeys(ctx)
}

func (s *countingStore) ListKeys(ctx context.Context) ([]KeyRecord, error) {
	s.lists++
	return s.MemoryStore.ListKeys(ctx)
}

func TestMemoryStoreEnforcesUniqueStatesAndTransactions(t *testing.T) {
	setRing(t)
	store := NewMemoryStore()
	a, _ := testGen.Generate(jose.ES256, StateActive, time.Now())
	b, _ := testGen.Generate(jose.ES256, StateActive, time.Now())
	_, err := store.WithRotationLock(t.Context(), func(ctx context.Context, tx KeyTx) error {
		if err := tx.Insert(ctx, a); err != nil {
			return err
		}
		return tx.Insert(ctx, b)
	})
	if !errors.Is(err, ErrUniqueState) {
		t.Fatalf("second active key: %v", err)
	}
	if len(mustList(t, store)) != 0 {
		t.Fatal("a failed transaction must roll back")
	}
	acquired, err := store.WithRotationLock(t.Context(), func(ctx context.Context, tx KeyTx) error {
		inner, _ := store.WithRotationLock(ctx, func(context.Context, KeyTx) error { return nil })
		if inner {
			t.Error("the lock must be exclusive")
		}
		return nil
	})
	if !acquired || err != nil {
		t.Fatal(acquired, err)
	}
}

func TestManagerRunReconcilesUntilCancelled(t *testing.T) {
	setRing(t)
	store := NewMemoryStore()
	manager := newManager(store, newClock(), jose.ES256)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { manager.Run(ctx, time.Millisecond); close(done) }()
	deadline := time.After(5 * time.Second)
	for len(mustList(t, store)) < 2 {
		select {
		case <-deadline:
			t.Fatal("the worker did not bootstrap keys")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
	signer, _ := NewLocalSigner(store, SignerConfig{Alg: jose.ES256})
	if signer.Algorithm() != jose.ES256 {
		t.Fatal(signer.Algorithm())
	}
	LogIntegrityAlert("kid", ErrIntegrity)
}

func TestManagerRejectsInvalidConfig(t *testing.T) {
	setRing(t)
	manager := newManager(NewMemoryStore(), newClock(), jose.RS256)
	manager.Config.PublishLead = 0
	if _, _, err := manager.Reconcile(t.Context()); err == nil {
		t.Fatal("invalid config accepted")
	}
	if _, err := manager.RotateNow(t.Context(), jose.RS256); err == nil {
		t.Fatal("invalid config accepted")
	}
}

func TestJWKSReadsPublicColumnsOnly(t *testing.T) {
	setRing(t)
	clock := newClock()
	store := NewMemoryStore()
	if _, _, err := newManager(store, clock, jose.ES256).Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	public, err := store.ListPublicKeys(t.Context())
	if err != nil || len(public) != 2 {
		t.Fatal(len(public), err)
	}
	for _, record := range public {
		if record.PrivateSealed != "" {
			t.Fatal("the public listing must not carry sealed private keys")
		}
	}
	full, _ := store.ListKeys(t.Context())
	if full[0].PrivateSealed == "" {
		t.Fatal("the full listing keeps them")
	}
	if err := public[0].VerifyIntegrity(); err != nil {
		t.Fatalf("integrity does not depend on the private half: %v", err)
	}
}

func TestRotationPausesWhenAnUntrustedRowHoldsTheSlot(t *testing.T) {
	setRing(t)
	clock := newClock()
	store := NewMemoryStore()
	manager := newManager(store, clock, jose.ES256)
	if _, _, err := manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	active := states(t, store, jose.ES256)[StateActive]
	tampered := active
	tampered.IntegrityMAC = "m1:deadbeef:AAAA"
	store.Put(tampered)
	before := len(mustList(t, store))
	// The failing active row holds the unique slot; creating another would collide, so the pass
	// must pause for this algorithm without an error and without writing.
	if _, changed, err := manager.Reconcile(t.Context()); err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if len(mustList(t, store)) != before {
		t.Fatal("nothing may be created while an untrusted row holds a slot")
	}
	// Revoking the failing row is the documented recovery: the next pass bootstraps again.
	if err := manager.Revoke(t.Context(), active.KID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := states(t, store, jose.ES256)[StateActive].KID; got == active.KID || got == "" {
		t.Fatalf("a new active key must replace the revoked one: %q", got)
	}
}

func TestSignerNamesAnUndecryptableKeyAndInvalidatesItsCache(t *testing.T) {
	setRing(t)
	clock := newClock()
	store := NewMemoryStore()
	manager := newManager(store, clock, jose.ES256)
	if _, _, err := manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	signer, _ := NewLocalSigner(store, SignerConfig{Alg: jose.ES256, Now: clock.Now})
	claims, _ := ClaimBuilder{Issuer: "i", Now: clock.Now}.Build(ClaimRequest{Subject: "s", Audiences: []string{"a"}})
	if _, _, err := signer.Sign(t.Context(), claims); err != nil {
		t.Fatal(err)
	}
	// Drop the cache and corrupt the sealed key: the cause must be visible, not a bare "no key".
	signer.Invalidate()
	active := states(t, store, jose.ES256)[StateActive]
	active.PrivateSealed = "v1:ffffffffffff:AAAA"
	store.Put(active)
	_, _, err := signer.Sign(t.Context(), claims)
	if !errors.Is(err, ErrNoActiveKey) || !strings.Contains(err.Error(), active.KID) {
		t.Fatalf("err=%v", err)
	}
}

func TestRevokeInvalidatesTheSigningKeyCacheThroughChanged(t *testing.T) {
	setRing(t)
	clock := newClock()
	store := NewMemoryStore()
	manager := newManager(store, clock, jose.ES256)
	signer, _ := NewLocalSigner(store, SignerConfig{Alg: jose.ES256, Now: clock.Now})
	manager.Changed = signer.Invalidate
	if _, _, err := manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	claims, _ := ClaimBuilder{Issuer: "i", Now: clock.Now}.Build(ClaimRequest{Subject: "s", Audiences: []string{"a"}})
	_, revoked, err := signer.Sign(t.Context(), claims)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Revoke(t.Context(), revoked); err != nil {
		t.Fatal(err)
	}
	if _, kid, err := signer.Sign(t.Context(), claims); err != nil || kid == revoked {
		t.Fatalf("a revoked key must stop signing at once: kid=%s err=%v", kid, err)
	}
}
