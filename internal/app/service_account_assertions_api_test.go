package app

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/tokenissuer"
)

type assertionFixture struct {
	t       *testing.T
	store   *auditFaultStore
	runtime *IssuerRuntime
	keys    *countingKeyStore
	handler http.Handler
}

// newAssertionFixture serves the management API with the issuer enabled (or disabled) over a memory store.
func newAssertionFixture(t *testing.T, enabled bool) *assertionFixture {
	t.Helper()
	store := &auditFaultStore{MemoryStore: repository.NewMemoryStore()}
	var (
		runtime *IssuerRuntime
		keys    *countingKeyStore
	)
	if enabled {
		runtime, keys, _ = newIssuerFixture(t)
	}
	handler := newTestGateway(t, GatewayOptions{
		Store: store, Adapter: TestAdapter{}, Issuer: runtime,
		Authenticator: Authenticator{AdminToken: "root", AdminActor: "root", APIKeys: store, ServiceAccounts: store},
	})
	return &assertionFixture{t: t, store: store, runtime: runtime, keys: keys, handler: handler}
}

// auditFaultStore fails the direct audit write of the operations in failing, the way a broken audit table would.
type auditFaultStore struct {
	*repository.MemoryStore
	failing map[string]bool
}

func (s *auditFaultStore) RecordAudit(ctx context.Context, record repository.AuditRecord) error {
	if s.failing[record.Operation] {
		return context.DeadlineExceeded
	}
	return s.MemoryStore.RecordAudit(ctx, record)
}

func (f *assertionFixture) call(method, path, token, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

type createdAssertionAccount struct {
	ID               string `json:"id"`
	AssertionSubject string `json:"assertionSubject"`
	Token            string
}

// account creates a service account with an API key through the management API.
func (f *assertionFixture) account(body string) createdAssertionAccount {
	f.t.Helper()
	created := f.call(http.MethodPost, "/api/v2/service-accounts", "root", body)
	if created.Code != http.StatusCreated {
		f.t.Fatalf("create service account status=%d body=%s", created.Code, created.Body.String())
	}
	var account createdAssertionAccount
	if err := json.NewDecoder(created.Body).Decode(&account); err != nil {
		f.t.Fatal(err)
	}
	credential := f.call(http.MethodPost, "/api/v2/service-accounts/"+account.ID+"/credentials", "root", `{"name":"ci"}`)
	if credential.Code != http.StatusCreated {
		f.t.Fatalf("create credential status=%d body=%s", credential.Code, credential.Body.String())
	}
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(credential.Body).Decode(&issued); err != nil {
		f.t.Fatal(err)
	}
	account.Token = issued.Token
	return account
}

func (f *assertionFixture) allow(accountID, audience string, seconds int) {
	f.t.Helper()
	response := f.call(http.MethodPost, "/api/v2/service-accounts/"+accountID+"/assertion-audiences", "root",
		`{"audience":"`+audience+`","maxLifetimeSeconds":`+itoa(int64(seconds))+`}`)
	if response.Code != http.StatusCreated {
		f.t.Fatalf("allow audience status=%d body=%s", response.Code, response.Body.String())
	}
}

func (f *assertionFixture) mint(accountID, token, body string) *httptest.ResponseRecorder {
	return f.call(http.MethodPost, "/api/v2/service-accounts/"+accountID+"/assertions", token, body)
}

func problemCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("problem body %q: %v", response.Body.String(), err)
	}
	return problem.Code
}

func jwtClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a compact JWT: %q", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestServiceAccountCreationStoresAnImmutableAssertionSubject(t *testing.T) {
	f := newAssertionFixture(t, true)
	account := f.account(`{"name":"deploy.bot-1"}`)
	if account.AssertionSubject != "notarx:service-account:deploy.bot-1" {
		t.Fatalf("default subject = %q", account.AssertionSubject)
	}
	custom := f.account(`{"name":"legacy","assertionSubject":"my-existing-subject"}`)
	if custom.AssertionSubject != "my-existing-subject" {
		t.Fatalf("custom subject = %q", custom.AssertionSubject)
	}
	if response := f.call(http.MethodPost, "/api/v2/service-accounts", "root", `{"name":"other","assertionSubject":"my-existing-subject"}`); response.Code != http.StatusConflict || problemCode(t, response) != problemAssertionSubjectReserved {
		t.Fatalf("reused custom subject: %d %s", response.Code, response.Body.String())
	}
	if response := f.call(http.MethodPost, "/api/v2/service-accounts", "root", `{"name":"has space"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("a name outside [A-Za-z0-9._:-] with the issuer enabled: %d %s", response.Code, response.Body.String())
	}
	if response := f.call(http.MethodPost, "/api/v2/service-accounts", "root", `{"name":"has space","assertionSubject":"chosen"}`); response.Code != http.StatusCreated {
		t.Fatalf("a custom subject replaces the name rule: %d %s", response.Code, response.Body.String())
	}
	// There is no rename operation, and an update never carries the subject.
	list := f.call(http.MethodGet, "/api/v2/service-accounts", "root", "")
	if !strings.Contains(list.Body.String(), `"assertionSubject":"notarx:service-account:deploy.bot-1"`) {
		t.Fatalf("the list omits the subject: %s", list.Body.String())
	}
	before, err := f.store.GetServiceAccount(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	description := "renamed role"
	if _, err := f.store.UpdateServiceAccount(context.Background(), repository.ServiceAccountUpdate{ID: account.ID, Description: &description}, before.Version); err != nil {
		t.Fatal(err)
	}
	after, _ := f.store.GetServiceAccount(context.Background(), account.ID)
	if after.AssertionSubject != before.AssertionSubject {
		t.Fatalf("the subject changed from %q to %q", before.AssertionSubject, after.AssertionSubject)
	}
}

func TestServiceAccountWithoutIssuerOnlyGetsASubjectForLegalNames(t *testing.T) {
	f := newAssertionFixture(t, false)
	if response := f.call(http.MethodPost, "/api/v2/service-accounts", "root", `{"name":"has space"}`); response.Code != http.StatusCreated {
		t.Fatalf("create with the issuer disabled: %d %s", response.Code, response.Body.String())
	}
	account := f.account(`{"name":"plain"}`)
	if account.AssertionSubject != "notarx:service-account:plain" {
		t.Fatalf("the default subject is reserved even while the issuer is disabled: %q", account.AssertionSubject)
	}
	if response := f.mint(account.ID, account.Token, `{"audience":"api://x"}`); response.Code != http.StatusNotFound || problemCode(t, response) != problemIssuerDisabled {
		t.Fatalf("mint with the issuer disabled: %d %s", response.Code, response.Body.String())
	}
}

func TestAssertionAudienceAllowListLifecycle(t *testing.T) {
	f := newAssertionFixture(t, true)
	account := f.account(`{"name":"lister"}`)
	path := "/api/v2/service-accounts/" + account.ID + "/assertion-audiences"
	if response := f.call(http.MethodGet, path, "root", ""); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"items":[]`) {
		t.Fatalf("empty list: %d %s", response.Code, response.Body.String())
	}
	f.allow(account.ID, "api://AzureADTokenExchange", 300)
	if response := f.call(http.MethodPost, path, "root", `{"audience":"api://AzureADTokenExchange","maxLifetimeSeconds":120}`); response.Code != http.StatusOK {
		t.Fatalf("replacing the lifetime answers 200: %d %s", response.Code, response.Body.String())
	}
	if response := f.call(http.MethodPost, path, "root", `{"audience":"https://peer.example"}`); response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"maxLifetimeSeconds":600`) {
		t.Fatalf("default lifetime: %d %s", response.Code, response.Body.String())
	}
	for _, body := range []string{`{"audience":""}`, `{"audience":" padded "}`, `{"audience":"a","maxLifetimeSeconds":59}`, `{"audience":"a","maxLifetimeSeconds":601}`, `{"audience":"a","unknown":1}`, `{"audience":"` + strings.Repeat("x", 256) + `"}`} {
		if response := f.call(http.MethodPost, path, "root", body); response.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, response.Code, response.Body.String())
		}
	}
	list := f.call(http.MethodGet, path, "root", "")
	var page struct {
		Items []struct {
			Audience           string `json:"audience"`
			MaxLifetimeSeconds int    `json:"maxLifetimeSeconds"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil || len(page.Items) != 2 || page.Items[0].Audience != "api://AzureADTokenExchange" || page.Items[0].MaxLifetimeSeconds != 120 {
		t.Fatalf("list = %+v err=%v", page, err)
	}
	if response := f.call(http.MethodDelete, path+"?audience=https://peer.example", "root", ""); response.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", response.Code, response.Body.String())
	}
	if response := f.call(http.MethodDelete, path+"?audience=https://peer.example", "root", ""); response.Code != http.StatusNotFound {
		t.Fatalf("delete twice: %d", response.Code)
	}
	missing := "/api/v2/service-accounts/" + uuid.NewString() + "/assertion-audiences"
	if response := f.call(http.MethodGet, missing, "root", ""); response.Code != http.StatusNotFound {
		t.Fatalf("unknown account: %d", response.Code)
	}
	if response := f.call(http.MethodPost, missing, "root", `{"audience":"a"}`); response.Code != http.StatusNotFound {
		t.Fatalf("unknown account: %d", response.Code)
	}
	// Only an administrator manages allow-lists: the service account itself may not widen its own.
	if response := f.call(http.MethodPost, path, account.Token, `{"audience":"https://evil.example"}`); response.Code != http.StatusForbidden {
		t.Fatalf("a service account edits its own allow-list: %d", response.Code)
	}
	var audits []string
	records, _ := f.store.ListAudits(context.Background(), repository.AuditQuery{Limit: 100})
	for _, record := range records {
		audits = append(audits, record.Operation)
	}
	joined := strings.Join(audits, ",")
	for _, want := range []string{"service_account.assertion_audience.put", "service_account.assertion_audience.delete"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("no %s audit event in %s", want, joined)
		}
	}
}

func TestMintAssertionClaimsAndAudit(t *testing.T) {
	f := newAssertionFixture(t, true)
	account := f.account(`{"name":"minter"}`)
	f.allow(account.ID, "api://AzureADTokenExchange", 600)
	response := f.mint(account.ID, account.Token, `{"audience":"api://AzureADTokenExchange"}`)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("mint: %d %v %s", response.Code, response.Header(), response.Body.String())
	}
	var minted struct {
		Token, TokenType, Audience, Subject, Jti, Kid string
		ExpiresAt                                     time.Time
	}
	if err := json.Unmarshal(response.Body.Bytes(), &minted); err != nil {
		t.Fatal(err)
	}
	if minted.TokenType != "JWT" || minted.Subject != "notarx:service-account:minter" || minted.Kid == "" {
		t.Fatalf("response = %+v", minted)
	}
	claims := jwtClaims(t, minted.Token)
	if claims["iss"] != "https://issuer.example.test" || claims["sub"] != minted.Subject || claims["aud"] != "api://AzureADTokenExchange" ||
		claims["notarx_sa"] != account.ID || claims["notarx_cred"] != "api_key" || claims["jti"] != minted.Jti || claims["nbf"] != claims["iat"] {
		t.Fatalf("claims = %v", claims)
	}
	if lifetime := claims["exp"].(float64) - claims["iat"].(float64); lifetime != 300 {
		t.Fatalf("the default lifetime is five minutes, got %vs", lifetime)
	}
	for _, forbidden := range []string{"email", "groups", "roles", "name"} {
		if _, present := claims[forbidden]; present {
			t.Fatalf("assertion carries %s", forbidden)
		}
	}
	keys := f.runtime.Signer.PublicKeys(context.Background())
	found := false
	for _, key := range keys {
		found = found || key.KeyID == minted.Kid
	}
	if !found {
		t.Fatalf("the kid %s of the assertion is not in the published key set", minted.Kid)
	}

	records, err := f.store.ListAudits(context.Background(), repository.AuditQuery{Operation: "service_account.assertion.mint", Limit: 10})
	if err != nil || len(records) != 1 {
		t.Fatalf("mint audit = %v %v", records, err)
	}
	evidence := records[0].Evidence
	if evidence["jti"] != minted.Jti || evidence["kid"] != minted.Kid || evidence["audience"] != "api://AzureADTokenExchange" || evidence["credentialId"] == "" || evidence["serviceAccountId"] != account.ID {
		t.Fatalf("evidence = %v", evidence)
	}
	for key, value := range evidence {
		if strings.Contains(value, minted.Token) || strings.Contains(value, ".") && strings.Count(value, ".") == 2 && strings.HasPrefix(value, "ey") {
			t.Fatalf("evidence %s holds a token", key)
		}
	}
	if records[0].Actor != "service-account:"+account.ID || records[0].Class != repository.AuditClassSync {
		t.Fatalf("record = %+v", records[0])
	}
}

func TestMintAssertionRefusals(t *testing.T) {
	f := newAssertionFixture(t, true)
	account := f.account(`{"name":"refuse"}`)
	other := f.account(`{"name":"other"}`)
	f.allow(account.ID, "api://AzureADTokenExchange", 120)
	body := `{"audience":"api://AzureADTokenExchange"}`

	// Not on the allow-list: 403 with the stable problem code, never a token.
	if response := f.mint(account.ID, account.Token, `{"audience":"https://unlisted.example"}`); response.Code != http.StatusForbidden || problemCode(t, response) != problemAssertionAudienceNotAllowed {
		t.Fatalf("unlisted audience: %d %s", response.Code, response.Body.String())
	}
	// The static administrator token and an administrator API key are not workload credentials.
	if response := f.mint(account.ID, "root", body); response.Code != http.StatusForbidden || problemCode(t, response) != problemAssertionPrincipal {
		t.Fatalf("static admin: %d %s", response.Code, response.Body.String())
	}
	adminKey := "agc_admin-api-key"
	if _, err := f.store.CreateAPIKey(context.Background(), repository.APIKey{ID: uuid.NewString(), Name: "admin", SecretHash: authorization.HashAPIKey(adminKey), Roles: []string{"admin"}}); err != nil {
		t.Fatal(err)
	}
	if response := f.mint(account.ID, adminKey, body); response.Code != http.StatusForbidden || problemCode(t, response) != problemAssertionPrincipal {
		t.Fatalf("admin API key: %d %s", response.Code, response.Body.String())
	}
	// A service account mints only for itself.
	if response := f.mint(account.ID, other.Token, body); response.Code != http.StatusForbidden {
		t.Fatalf("another account's key: %d %s", response.Code, response.Body.String())
	}
	if response := f.mint(account.ID, "", body); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", response.Code)
	}
	// The lifetime is bounded by the allow-list entry (120 s here), not only by the global ceiling.
	if response := f.mint(account.ID, account.Token, `{"audience":"api://AzureADTokenExchange","lifetimeSeconds":121}`); response.Code != http.StatusBadRequest || problemCode(t, response) != problemAssertionLifetime {
		t.Fatalf("lifetime above the allow-list: %d %s", response.Code, response.Body.String())
	}
	if response := f.mint(account.ID, account.Token, `{"audience":"api://AzureADTokenExchange","lifetimeSeconds":601}`); response.Code != http.StatusBadRequest {
		t.Fatalf("lifetime above the hard ceiling: %d", response.Code)
	}
	if response := f.mint(account.ID, account.Token, `{"audience":"api://AzureADTokenExchange","lifetimeSeconds":0}`); response.Code != http.StatusBadRequest {
		t.Fatalf("zero lifetime: %d", response.Code)
	}
	if response := f.mint(account.ID, account.Token, `{"audience":""}`); response.Code != http.StatusBadRequest {
		t.Fatalf("empty audience: %d", response.Code)
	}
	// The default lifetime shrinks to the allow-list entry instead of failing.
	ok := f.mint(account.ID, account.Token, body)
	if ok.Code != http.StatusOK {
		t.Fatalf("default lifetime under a 120 s entry: %d %s", ok.Code, ok.Body.String())
	}
	var minted struct{ Token string }
	_ = json.Unmarshal(ok.Body.Bytes(), &minted)
	if claims := jwtClaims(t, minted.Token); claims["exp"].(float64)-claims["iat"].(float64) != 120 {
		t.Fatalf("lifetime = %v", claims)
	}
	// A disabled account's credential stops authenticating, so it cannot mint either.
	disabled := repository.ServiceAccountDisabled
	current, _ := f.store.GetServiceAccount(context.Background(), account.ID)
	if _, err := f.store.UpdateServiceAccount(context.Background(), repository.ServiceAccountUpdate{ID: account.ID, State: &disabled}, current.Version); err != nil {
		t.Fatal(err)
	}
	if response := f.mint(account.ID, account.Token, body); response.Code != http.StatusUnauthorized {
		t.Fatalf("disabled account: %d", response.Code)
	}
	records, _ := f.store.ListAudits(context.Background(), repository.AuditQuery{Operation: "service_account.assertion.mint", Limit: 10})
	if len(records) != 1 {
		t.Fatalf("only the successful mint is audited, got %d records", len(records))
	}
}

func TestMintAssertionFailsClosedWhenTheAuditCannotBeWritten(t *testing.T) {
	f := newAssertionFixture(t, true)
	account := f.account(`{"name":"audited"}`)
	f.allow(account.ID, "api://x", 600)
	f.store.failing = map[string]bool{"service_account.assertion.mint": true}
	response := f.mint(account.ID, account.Token, `{"audience":"api://x"}`)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "eyJ") {
		t.Fatalf("an unaudited mint must not hand out a token: %d %s", response.Code, response.Body.String())
	}
}

func TestMintAssertionWithoutASigningKey(t *testing.T) {
	enabled := newAssertionFixture(t, true)
	account := enabled.account(`{"name":"keyless"}`)
	enabled.allow(account.ID, "api://x", 600)
	// Sign with no active key: the issuer answers 503, never an unsigned token.
	enabled.runtime.Signer = failingSigner{}
	if response := enabled.mint(account.ID, account.Token, `{"audience":"api://x"}`); response.Code != http.StatusServiceUnavailable || problemCode(t, response) != problemSigningUnavailable {
		t.Fatalf("no signing key: %d %s", response.Code, response.Body.String())
	}
}

type failingSigner struct{ tokenissuer.Signer }

func (failingSigner) Sign(context.Context, tokenissuer.Claims) (string, string, error) {
	return "", "", tokenissuer.ErrNoActiveKey
}

// A federated workload principal (ADR 0019) that resolves to the service account may mint, and its assertion says so.
func TestFederatedWorkloadPrincipalMintsAnAssertion(t *testing.T) {
	ctx := context.Background()
	f := newAssertionFixture(t, true)
	account := f.account(`{"name":"federated"}`)
	f.allow(account.ID, "api://AzureADTokenExchange", 600)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.ES256, key, "ci-key")
	if err != nil {
		t.Fatal(err)
	}
	public, err := jose.PublicKey(key.Public(), "ci-key", jose.ES256)
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := jose.MarshalJWKS(public)
	if err != nil {
		t.Fatal(err)
	}
	const issuerURL, audience = "https://ci.example.test", "https://notarx.example.test"
	issuer, err := f.store.CreateWorkloadIssuer(ctx, repository.WorkloadIssuer{
		Name: "ci", Kind: repository.WorkloadKindGeneric, Issuer: issuerURL, Audiences: []string{audience},
		AllowedAlgs: []string{jose.ES256}, JWKSMode: repository.WorkloadJWKSStatic, StaticJWKS: jwks,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := f.store.CreateWorkloadTrustPolicy(ctx, repository.WorkloadTrustPolicy{
		Name: "mint", IssuerID: issuer.ID, ServiceAccountID: account.ID,
		Conditions: []byte(`[{"claim":["sub"],"op":"eq","values":["repo:acme/app"]}]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := jose.NewVerifier(jose.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	workload, err := authorization.NewWorkloadAuthenticator(authorization.WorkloadOptions{
		Store: f.store, Accounts: f.store, Verifier: verifier, ClockSkew: time.Minute, Observer: WorkloadObserver{Metrics: &Metrics{}, Audit: &auditSink{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestGateway(t, GatewayOptions{
		Store: f.store, Adapter: TestAdapter{}, Issuer: f.runtime,
		Authenticator: Authenticator{AdminToken: "root", AdminActor: "root", APIKeys: f.store, ServiceAccounts: f.store, Workload: workload},
	})
	now := time.Now()
	jwt, err := signer.Sign(map[string]any{"iss": issuerURL, "sub": "repo:acme/app", "aud": audience, "jti": "run-1", "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v2/service-accounts/"+account.ID+"/assertions", strings.NewReader(`{"audience":"api://AzureADTokenExchange"}`))
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("federated mint: %d %s", response.Code, response.Body.String())
	}
	var minted struct{ Token string }
	if err := json.Unmarshal(response.Body.Bytes(), &minted); err != nil {
		t.Fatal(err)
	}
	if claims := jwtClaims(t, minted.Token); claims["notarx_cred"] != "workload_federation" || claims["notarx_sa"] != account.ID {
		t.Fatalf("claims = %v", claims)
	}
	records, _ := f.store.ListAudits(ctx, repository.AuditQuery{Operation: "service_account.assertion.mint", Limit: 10})
	if len(records) != 1 || records[0].Evidence["credentialId"] != policy.ID || records[0].Evidence["credentialKind"] != "workload_federation" {
		t.Fatalf("audit = %+v", records)
	}
}

func TestIssuerAdminAPI(t *testing.T) {
	f := newAssertionFixture(t, true)
	status := f.call(http.MethodGet, "/api/v2/issuer", "root", "")
	var view struct {
		Enabled   bool   `json:"enabled"`
		IssuerURL string `json:"issuerUrl"`
		Algorithm string `json:"algorithm"`
		Keys      []struct {
			Kid, Alg, State string
		} `json:"keys"`
	}
	if status.Code != http.StatusOK || json.Unmarshal(status.Body.Bytes(), &view) != nil || !view.Enabled || view.IssuerURL != "https://issuer.example.test" || view.Algorithm != "ES256" || len(view.Keys) != 2 {
		t.Fatalf("status = %d %s", status.Code, status.Body.String())
	}
	if strings.Contains(status.Body.String(), "private") || strings.Contains(status.Body.String(), "sealed") {
		t.Fatalf("status exposes key material: %s", status.Body.String())
	}
	if response := f.call(http.MethodGet, "/api/v2/issuer", "", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status: %d", response.Code)
	}
	account := f.account(`{"name":"not-admin"}`)
	if response := f.call(http.MethodGet, "/api/v2/issuer", account.Token, ""); response.Code != http.StatusForbidden {
		t.Fatalf("a service account reads the issuer: %d", response.Code)
	}

	var active string
	for _, key := range view.Keys {
		if key.State == "active" {
			active = key.Kid
		}
	}
	rotated := f.call(http.MethodPost, "/api/v2/issuer/rotation", "root", "")
	var rotation struct{ Algorithm, Retired, Activated, Warning string }
	if rotated.Code != http.StatusOK || json.Unmarshal(rotated.Body.Bytes(), &rotation) != nil || rotation.Retired != active || rotation.Activated == "" || rotation.Warning == "" || rotation.Algorithm != "ES256" {
		t.Fatalf("rotate = %d %s", rotated.Code, rotated.Body.String())
	}
	if response := f.call(http.MethodPost, "/api/v2/issuer/rotation", "root", `{"algorithm":"RS512"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown algorithm: %d", response.Code)
	}
	if response := f.call(http.MethodPost, "/api/v2/issuer/keys/"+rotation.Activated+"/revoke", "root", ""); response.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", response.Code, response.Body.String())
	}
	if response := f.call(http.MethodPost, "/api/v2/issuer/keys/nope/revoke", "root", ""); response.Code != http.StatusNotFound || problemCode(t, response) != "key_not_found" {
		t.Fatalf("revoke unknown: %d %s", response.Code, response.Body.String())
	}
	// Both operator actions are audited in the transaction of the key change, with key ids and no key material.
	seen := map[string]tokenissuer.AuditEvent{}
	for _, event := range f.keys.Audits() {
		seen[event.Operation] = event
	}
	if seen["issuer.rotate"].Evidence["activated"] != rotation.Activated || seen["issuer.revoke"].Evidence["kid"] != rotation.Activated || seen["issuer.rotate"].Actor != "root" {
		t.Fatalf("audit = %+v", seen)
	}
}

func TestIssuerAdminAPIWhileDisabled(t *testing.T) {
	f := newAssertionFixture(t, false)
	status := f.call(http.MethodGet, "/api/v2/issuer", "root", "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"enabled":false`) || !strings.Contains(status.Body.String(), `"keys":[]`) {
		t.Fatalf("status = %d %s", status.Code, status.Body.String())
	}
	if response := f.call(http.MethodPost, "/api/v2/issuer/rotation", "root", ""); response.Code != http.StatusNotFound || problemCode(t, response) != problemIssuerDisabled {
		t.Fatalf("rotate: %d %s", response.Code, response.Body.String())
	}
	if response := f.call(http.MethodPost, "/api/v2/issuer/keys/k/revoke", "root", ""); response.Code != http.StatusNotFound {
		t.Fatalf("revoke: %d", response.Code)
	}
}
