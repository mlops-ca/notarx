package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/identity/saml/samltest"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
)

// scimHarness is a gateway on the memory store with one SCIM-capable provider and a token created through the
// management API, so the tests exercise the same path an operator uses.
type scimHarness struct {
	t        *testing.T
	store    *repository.MemoryStore
	runtime  *IdentityRuntime
	handler  http.Handler
	provider identity.Provider
	token    string
}

func scimProviderInput(slug string) IdentityProviderInput {
	return IdentityProviderInput{
		Slug: slug, Kind: identity.KindOIDC, DisplayName: strings.ToUpper(slug), Enabled: true,
		OIDC: identity.OIDCSettings{
			Issuer: "https://login.example.test/" + slug + "/v2.0", Audience: "notarx-api", ClientID: "notarx-console",
			RedirectURL: "https://notarx.example.test/auth/oidc/" + slug + "/callback", Scopes: []string{"openid"},
		},
		ProvisioningMode: identity.ProvisioningSCIMOnly, JITDefaultRole: identity.JITRoleNone,
	}
}

func newSCIMHarness(t *testing.T, options SCIMOptions) *scimHarness {
	t.Helper()
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	store := repository.NewMemoryStore()
	runtime := NewIdentityRuntime(store)
	provider, err := runtime.Create(t.Context(), scimProviderInput("entra"))
	if err != nil {
		t.Fatal(err)
	}
	h := &scimHarness{t: t, store: store, runtime: runtime, provider: provider}
	h.handler = newTestGateway(t, GatewayOptions{Dependencies: Dependencies{IdentityRuntime: runtime}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator(), SCIM: options})
	h.token = h.createToken(provider.ID, "provisioning", 90*24*time.Hour)
	return h
}

func (h *scimHarness) admin(method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest(method, "/api/v2"+path, strings.NewReader(body))
	authorize(request, "admin-secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, request)
	return recorder
}

func (h *scimHarness) createToken(providerID, name string, lifetime time.Duration) string {
	h.t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name, "expiresAt": time.Now().UTC().Add(lifetime).Format(time.RFC3339)})
	created := h.admin(http.MethodPost, "/authentication/providers/"+providerID+"/scim-tokens", string(body))
	if created.Code != http.StatusCreated {
		h.t.Fatalf("create token = %d %s", created.Code, created.Body.String())
	}
	var decoded struct{ Token string }
	if err := json.Unmarshal(created.Body.Bytes(), &decoded); err != nil || !strings.HasPrefix(decoded.Token, scimTokenPrefix) {
		h.t.Fatalf("token response = %s", created.Body.String())
	}
	return decoded.Token
}

// scim sends a SCIM request with the harness token.
func (h *scimHarness) scim(method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.scimAs(h.token, method, path, body, nil)
}

func (h *scimHarness) scimAs(token, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest(method, "https://notarx.example.test/scim/v2"+path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/scim+json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeSCIM(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("response is not JSON (%d): %s", recorder.Code, recorder.Body.String())
	}
	return decoded
}

func expectSCIMError(t *testing.T, recorder *httptest.ResponseRecorder, status int, scimType string) {
	t.Helper()
	body := decodeSCIM(t, recorder)
	if recorder.Code != status || body["status"] != strconv.Itoa(status) || scimType != "" && body["scimType"] != scimType ||
		!strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/scim+json") {
		t.Fatalf("response = %d %s, want %d %s", recorder.Code, recorder.Body.String(), status, scimType)
	}
}

func TestSCIMTokenManagementAPI(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true, MaxPage: 200})
	base := "/authentication/providers/" + h.provider.ID + "/scim-tokens"
	expiry := func(lifetime time.Duration) string {
		return `{"name":"Okta","expiresAt":"` + time.Now().UTC().Add(lifetime).Format(time.RFC3339) + `"}`
	}

	created := h.admin(http.MethodPost, base, expiry(30*24*time.Hour))
	if created.Code != http.StatusCreated || created.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create = %d cache=%q %s", created.Code, created.Header().Get("Cache-Control"), created.Body.String())
	}
	body := decodeSCIM(t, created)
	plaintext, _ := body["token"].(string)
	if !strings.HasPrefix(plaintext, scimTokenPrefix) || len(plaintext) < 40 || body["state"] != "active" || body["providerId"] != h.provider.ID || body["name"] != "Okta" {
		t.Fatalf("created = %v", body)
	}

	// The list never carries the secret, and neither the store nor the audit trail holds the plaintext.
	listed := h.admin(http.MethodGet, base, "")
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), plaintext) || strings.Contains(listed.Body.String(), "token") || strings.Contains(listed.Body.String(), "secret") {
		t.Fatalf("list = %d %s", listed.Code, listed.Body.String())
	}
	if items := decodeSCIM(t, listed)["items"].([]any); len(items) != 2 {
		t.Fatalf("list holds %d tokens, want the harness token and this one", len(items))
	}
	for _, record := range h.store.Audits {
		if strings.Contains(record.Resource+record.Operation+record.AuthorizationReason+record.Actor, plaintext) {
			t.Fatalf("the plaintext token reached the audit trail: %+v", record)
		}
	}
	stored, err := h.store.FindActiveSCIMTokenByHash(t.Context(), plaintext)
	if err == nil && stored.SecretHash == plaintext {
		t.Fatal("the plaintext is the stored value")
	}

	for name, request := range map[string]string{
		"no expiry":     `{"name":"x"}`,
		"past expiry":   expiry(-time.Hour),
		"over 365 days": expiry(366 * 24 * time.Hour),
		"empty name":    `{"name":" ","expiresAt":"` + time.Now().UTC().Add(time.Hour).Format(time.RFC3339) + `"}`,
		"unknown field": `{"name":"x","expiresAt":"2999-01-01T00:00:00Z","extra":1}`,
		"not json":      `nope`,
		"name too long": `{"name":"` + strings.Repeat("n", 129) + `","expiresAt":"` + time.Now().UTC().Add(time.Hour).Format(time.RFC3339) + `"}`,
	} {
		if got := h.admin(http.MethodPost, base, request); got.Code != http.StatusBadRequest {
			t.Errorf("%s: create = %d %s, want 400", name, got.Code, got.Body.String())
		}
	}
	if edge := h.admin(http.MethodPost, base, expiry(364*24*time.Hour)); edge.Code != http.StatusCreated {
		t.Errorf("a token just inside the cap = %d %s", edge.Code, edge.Body.String())
	}
	unknown := "/authentication/providers/" + uuid.NewString() + "/scim-tokens"
	if got := h.admin(http.MethodPost, unknown, expiry(time.Hour)); got.Code != http.StatusNotFound {
		t.Errorf("unknown provider create = %d", got.Code)
	}
	if got := h.admin(http.MethodGet, unknown, ""); got.Code != http.StatusNotFound {
		t.Errorf("unknown provider list = %d", got.Code)
	}
	anonymous := httptest.NewRecorder()
	h.handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodPost, "/api/v2"+base, strings.NewReader(expiry(time.Hour))))
	if anonymous.Code != http.StatusUnauthorized {
		t.Errorf("anonymous create = %d", anonymous.Code)
	}

	if got := h.scimAs(plaintext, http.MethodGet, "/ServiceProviderConfig", "", nil); got.Code != http.StatusOK {
		t.Fatalf("the new token does not authenticate: %d %s", got.Code, got.Body.String())
	}
	revoked := h.admin(http.MethodDelete, base+"/"+body["id"].(string), "")
	if revoked.Code != http.StatusOK || decodeSCIM(t, revoked)["state"] != "revoked" || decodeSCIM(t, revoked)["revokedAt"] == nil {
		t.Fatalf("revoke = %d %s", revoked.Code, revoked.Body.String())
	}
	expectSCIMError(t, h.scimAs(plaintext, http.MethodGet, "/ServiceProviderConfig", "", nil), http.StatusUnauthorized, "")
	again := h.admin(http.MethodDelete, base+"/"+body["id"].(string), "")
	if again.Code != http.StatusOK || decodeSCIM(t, again)["revokedAt"] != decodeSCIM(t, revoked)["revokedAt"] {
		t.Errorf("a second revoke = %d %s", again.Code, again.Body.String())
	}
	if got := h.admin(http.MethodDelete, base+"/"+uuid.NewString(), ""); got.Code != http.StatusNotFound {
		t.Errorf("revoke of an unknown token = %d", got.Code)
	}
	operations := map[string]bool{}
	for _, record := range h.store.Audits {
		operations[record.Operation] = true
	}
	for _, operation := range []string{"authentication.scim-token.create", "authentication.scim-token.revoke"} {
		if !operations[operation] {
			t.Errorf("missing audit %s", operation)
		}
	}
}

func TestSCIMRoutesAre404WhileDisabledAndNeedAValidToken(t *testing.T) {
	disabled := newSCIMHarness(t, SCIMOptions{})
	for _, path := range []string{"/ServiceProviderConfig", "/Schemas", "/ResourceTypes", "/Users", "/Users/" + uuid.NewString(), "/Groups", "/Unknown", ""} {
		for _, token := range []string{"", disabled.token, "wrong"} {
			if got := disabled.scimAs(token, http.MethodGet, path, "", nil); got.Code != http.StatusNotFound || strings.Contains(got.Body.String(), "SCIM") || strings.Contains(got.Body.String(), "token") {
				t.Errorf("disabled %q with token %q = %d %s", path, token, got.Code, got.Body.String())
			}
		}
	}
	if got := disabled.scimAs(disabled.token, http.MethodPost, "/Users", `{"userName":"x"}`, nil); got.Code != http.StatusNotFound {
		t.Errorf("disabled create = %d", got.Code)
	}
	if got := disabled.scimAs(disabled.token, http.MethodGet, "/Users/", "", nil); got.Code != http.StatusNotFound {
		t.Errorf("disabled trailing slash = %d", got.Code)
	}

	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	other, err := h.runtime.Create(t.Context(), scimProviderInput("okta"))
	if err != nil {
		t.Fatal(err)
	}
	otherToken := h.createToken(other.ID, "okta", time.Hour)
	for name, token := range map[string]string{"none": "", "unknown": scimTokenPrefix + "unknown", "wrong scheme": "", "no prefix": "abcdef"} {
		headers := map[string]string{}
		if name == "wrong scheme" {
			headers["Authorization"] = "Basic " + h.token
		}
		got := h.scimAs(token, http.MethodGet, "/Users", "", headers)
		expectSCIMError(t, got, http.StatusUnauthorized, "")
		if got.Header().Get("WWW-Authenticate") == "" || strings.Contains(got.Body.String(), "expired") || strings.Contains(got.Body.String(), "revoked") {
			t.Errorf("%s: 401 = %v %s", name, got.Header(), got.Body.String())
		}
	}
	// A token acts only for its own provider.
	created := h.scim(http.MethodPost, "/Users", `{"userName":"ada@example.com","externalId":"e-1"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	id := decodeSCIM(t, created)["id"].(string)
	expectSCIMError(t, h.scimAs(otherToken, http.MethodGet, "/Users/"+id, "", nil), http.StatusNotFound, "")
	expectSCIMError(t, h.scimAs(otherToken, http.MethodPatch, "/Users/"+id, `{"Operations":[{"op":"replace","path":"active","value":false}]}`, nil), http.StatusNotFound, "")
	expectSCIMError(t, h.scimAs(otherToken, http.MethodDelete, "/Users/"+id, "", nil), http.StatusNotFound, "")
	if list := decodeSCIM(t, h.scimAs(otherToken, http.MethodGet, "/Users", "", nil)); list["totalResults"] != float64(0) {
		t.Errorf("another provider lists %v users", list["totalResults"])
	}
	if user, _ := h.store.GetUser(t.Context(), id); user.State != repository.UserActive {
		t.Error("a token of another provider changed the account")
	}
}

func TestSCIMFailedAuthenticationsAreRateLimitedButValidTokensAreNever(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	for attempt := 0; attempt < samlRateBurst; attempt++ {
		got := h.scimAs(scimTokenPrefix+"guess", http.MethodGet, "/Users", "", nil)
		expectSCIMError(t, got, http.StatusUnauthorized, "")
	}
	limited := h.scimAs(scimTokenPrefix+"guess", http.MethodGet, "/Users", "", nil)
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("after the burst = %d %v", limited.Code, limited.Header())
	}
	// Someone guessing tokens must not lock the identity provider out, even behind a proxy that gives every client one
	// address: a valid token is served whatever the peer's failures.
	if got := h.scim(http.MethodGet, "/ServiceProviderConfig", ""); got.Code != http.StatusOK {
		t.Fatalf("a valid token from a peer over its failure budget = %d %s", got.Code, got.Body.String())
	}
	// The failures after the budget are neither audited nor counted as new audit rows.
	failures := 0
	for _, record := range h.store.Audits {
		if record.Operation == "scim.authenticate" {
			failures++
		}
	}
	if failures != samlRateBurst {
		t.Errorf("%d authentication failures were audited, want exactly the %d inside the budget", failures, samlRateBurst)
	}
	other := httptest.NewRequest(http.MethodGet, "https://notarx.example.test/scim/v2/Users", nil)
	other.RemoteAddr = "203.0.113.9:4000"
	other.Header.Set("Authorization", "Bearer "+scimTokenPrefix+"guess")
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, other)
	expectSCIMError(t, recorder, http.StatusUnauthorized, "")
}

// failingSCIMStore makes the token or provider lookup fail like a database outage.
type failingSCIMStore struct {
	scimStore
	tokenErr, providerErr error
}

func (f failingSCIMStore) FindActiveSCIMTokenByHash(ctx context.Context, hash string) (repository.SCIMToken, error) {
	if f.tokenErr != nil {
		return repository.SCIMToken{}, f.tokenErr
	}
	return f.scimStore.FindActiveSCIMTokenByHash(ctx, hash)
}

func (f failingSCIMStore) GetIdentityProvider(ctx context.Context, id string) (identity.Provider, error) {
	if f.providerErr != nil {
		return identity.Provider{}, f.providerErr
	}
	return f.scimStore.GetIdentityProvider(ctx, id)
}

func TestSCIMStoreOutagesAre503AndNeverCountAsFailedAuthentications(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	outage := errors.New("database down")
	for name, store := range map[string]scimStore{
		"token lookup":    failingSCIMStore{scimStore: h.store, tokenErr: outage},
		"provider lookup": failingSCIMStore{scimStore: h.store, providerErr: outage},
	} {
		server := newSCIMServer(SCIMOptions{Enabled: true}, store, nil)
		for attempt := 0; attempt < samlRateBurst+5; attempt++ {
			request := httptest.NewRequest(http.MethodGet, "https://notarx.example.test/scim/v2/Users", nil)
			request.Header.Set("Authorization", "Bearer "+h.token)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s outage = %d %s", name, recorder.Code, recorder.Body.String())
			}
		}
		if len(server.failures.buckets) != 0 {
			t.Fatalf("%s: an outage was counted against the peer: %+v", name, server.failures.buckets)
		}
	}
}

func TestSCIMTokensOfADisabledProviderStopAuthenticating(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	if got := h.scim(http.MethodGet, "/Users", ""); got.Code != http.StatusOK {
		t.Fatalf("enabled provider = %d", got.Code)
	}
	current, err := h.store.GetIdentityProvider(t.Context(), h.provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Enabled = false
	if _, err := h.store.ReplaceIdentityProvider(t.Context(), current, current.Version); err != nil {
		t.Fatal(err)
	}
	h.runtime.invalidate()
	expectSCIMError(t, h.scim(http.MethodGet, "/Users", ""), http.StatusUnauthorized, "")
	expectSCIMError(t, h.scim(http.MethodPost, "/Users", `{"userName":"late"}`), http.StatusUnauthorized, "")
	current, _ = h.store.GetIdentityProvider(t.Context(), h.provider.ID)
	current.Enabled = true
	if _, err := h.store.ReplaceIdentityProvider(t.Context(), current, current.Version); err != nil {
		t.Fatal(err)
	}
	if got := h.scim(http.MethodGet, "/Users", ""); got.Code != http.StatusOK {
		t.Fatalf("re-enabled provider = %d", got.Code)
	}
}

func TestSCIMDiscoveryMatchesTheAdvertisedCapabilities(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true, MaxPage: 123})
	config := h.scim(http.MethodGet, "/ServiceProviderConfig", "")
	if config.Code != http.StatusOK || !strings.HasPrefix(config.Header().Get("Content-Type"), "application/scim+json") {
		t.Fatalf("config = %d %v", config.Code, config.Header())
	}
	document := decodeSCIM(t, config)
	flag := func(name string) bool { return document[name].(map[string]any)["supported"].(bool) }
	if !flag("patch") || flag("bulk") || !flag("filter") || flag("changePassword") || flag("sort") || !flag("etag") ||
		document["filter"].(map[string]any)["maxResults"] != float64(123) {
		t.Fatalf("config = %s", config.Body.String())
	}
	if document["meta"].(map[string]any)["location"] != "/scim/v2/ServiceProviderConfig" {
		t.Errorf("location = %v", document["meta"])
	}
	types := decodeSCIM(t, h.scim(http.MethodGet, "/ResourceTypes", ""))
	if types["totalResults"] != float64(2) {
		t.Errorf("resource types = %v", types)
	}
	if one := h.scim(http.MethodGet, "/ResourceTypes/User", ""); one.Code != http.StatusOK || decodeSCIM(t, one)["endpoint"] != "/Users" {
		t.Errorf("resource type = %d %s", one.Code, one.Body.String())
	}
	schemas := decodeSCIM(t, h.scim(http.MethodGet, "/Schemas", ""))
	if schemas["totalResults"] != float64(2) {
		t.Errorf("schemas = %v", schemas)
	}
	if one := h.scim(http.MethodGet, "/Schemas/urn:ietf:params:scim:schemas:core:2.0:Group", ""); one.Code != http.StatusOK || decodeSCIM(t, one)["name"] != "Group" {
		t.Errorf("schema = %d %s", one.Code, one.Body.String())
	}
	expectSCIMError(t, h.scim(http.MethodGet, "/Schemas/unknown", ""), http.StatusNotFound, "")
	expectSCIMError(t, h.scim(http.MethodGet, "/ServiceProviderConfig/x", ""), http.StatusNotFound, "")
	expectSCIMError(t, h.scim(http.MethodGet, "/Bulk", ""), http.StatusNotFound, "")
	expectSCIMError(t, h.scim(http.MethodGet, "/Users/a/b", ""), http.StatusNotFound, "")
	expectSCIMError(t, h.scim(http.MethodPost, "/ServiceProviderConfig", "{}"), http.StatusMethodNotAllowed, "")
	expectSCIMError(t, h.scim(http.MethodPost, "/Schemas", "{}"), http.StatusMethodNotAllowed, "")
	expectSCIMError(t, h.scim(http.MethodDelete, "/Users", ""), http.StatusMethodNotAllowed, "")
	expectSCIMError(t, h.scim(http.MethodPost, "/Users/"+uuid.NewString(), "{}"), http.StatusMethodNotAllowed, "")
	expectSCIMError(t, h.scim(http.MethodPut, "/Groups", "{}"), http.StatusMethodNotAllowed, "")
	expectSCIMError(t, h.scim(http.MethodGet, "/Users/not-a-uuid", ""), http.StatusNotFound, "")
	expectSCIMError(t, h.scim(http.MethodGet, "/Groups/not-a-uuid", ""), http.StatusNotFound, "")
}

func TestSCIMLocationsUseThePublicURLWhenConfigured(t *testing.T) {
	publicURL, _ := url.Parse("https://notarx.example.com")
	h := newSCIMHarness(t, SCIMOptions{Enabled: true, PublicURL: publicURL})
	created := h.scim(http.MethodPost, "/Users", `{"userName":"ada"}`)
	if created.Code != http.StatusCreated || !strings.HasPrefix(created.Header().Get("Location"), "https://notarx.example.com/scim/v2/Users/") {
		t.Fatalf("create = %d location=%q", created.Code, created.Header().Get("Location"))
	}
}

func TestProviderLinkAPIBindsASCIMAccountForAProviderWithoutExternalID(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	id := h.createUser("ivy@example.com", "")
	link := func(userID, subject string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"userId": userID, "subject": subject})
		return h.admin(http.MethodPost, "/authentication/providers/"+h.provider.ID+"/links", string(body))
	}
	linked := link(id, "tid:oid-ivy")
	body := decodeSCIM(t, linked)
	if linked.Code != http.StatusOK || body["subject"] != "tid:oid-ivy" || body["providerId"] != h.provider.ID || body["userId"] != id || body["scimExternalId"] != nil {
		t.Fatalf("link = %d %s", linked.Code, linked.Body.String())
	}
	// The provider asserts no externalId, so the admin-linked subject is what signs the account in.
	account, identity, err := h.store.GetUserByProviderIdentity(t.Context(), h.provider.ID, "tid:oid-ivy")
	if err != nil || account.ID != id || identity.Subject != "tid:oid-ivy" {
		t.Fatalf("linked account = %+v %+v, %v", account, identity, err)
	}
	if again := link(id, "tid:oid-ivy"); again.Code != http.StatusOK {
		t.Errorf("an idempotent repeat = %d %s", again.Code, again.Body.String())
	}
	if other := link(id, "tid:oid-other"); other.Code != http.StatusConflict {
		t.Errorf("an account bound to another subject = %d", other.Code)
	}
	local, _ := h.store.CreateUser(t.Context(), repository.User{ID: uuid.NewString(), Name: "judy", Role: "member"})
	if taken := link(local.ID, "tid:oid-ivy"); taken.Code != http.StatusConflict {
		t.Errorf("a subject bound to another account = %d", taken.Code)
	}
	if created := link(local.ID, "tid:oid-judy"); created.Code != http.StatusOK {
		t.Errorf("an account the provider never provisioned = %d %s", created.Code, created.Body.String())
	}
	if missing := link(uuid.NewString(), "tid:oid-x"); missing.Code != http.StatusNotFound {
		t.Errorf("an unknown account = %d", missing.Code)
	}
	if bad := h.admin(http.MethodPost, "/authentication/providers/"+h.provider.ID+"/links", `{"userId":"`+id+`","subject":" "}`); bad.Code != http.StatusBadRequest {
		t.Errorf("a blank subject = %d", bad.Code)
	}
	if bad := h.admin(http.MethodPost, "/authentication/providers/"+h.provider.ID+"/links", `{"userId":"`+id+`","subject":"x","extra":1}`); bad.Code != http.StatusBadRequest {
		t.Errorf("an unknown field = %d", bad.Code)
	}
	if unknown := h.admin(http.MethodPost, "/authentication/providers/"+uuid.NewString()+"/links", `{"userId":"`+id+`","subject":"x"}`); unknown.Code != http.StatusNotFound {
		t.Errorf("an unknown provider = %d", unknown.Code)
	}
	anonymous := httptest.NewRecorder()
	h.handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodPost, "/api/v2/authentication/providers/"+h.provider.ID+"/links", strings.NewReader(`{"userId":"`+id+`","subject":"x"}`)))
	if anonymous.Code != http.StatusUnauthorized {
		t.Errorf("anonymous link = %d", anonymous.Code)
	}
	var audited bool
	for _, record := range h.store.Audits {
		audited = audited || record.Operation == "authentication.provider.link"
	}
	if !audited {
		t.Error("the link was not audited")
	}
	// The SCIM-created row now carries both keys, and the user list reports the externalId only when the IdP set one.
	identities := h.admin(http.MethodGet, "/users/"+id+"/identities", "")
	if identities.Code != http.StatusOK || !strings.Contains(identities.Body.String(), `"subject":"tid:oid-ivy"`) || strings.Contains(identities.Body.String(), "notarx-scim-user") {
		t.Errorf("identities = %d %s", identities.Code, identities.Body.String())
	}
}

func TestSCIMOnlySAMLLoginBindsByTheAssertedExternalID(t *testing.T) {
	f := newSAMLFixture(t, func(input *IdentityProviderInput) {
		input.ProvisioningMode = identity.ProvisioningSCIMOnly
		input.AttributeMapping = identity.AttributeMapping{ExternalID: "employeeNumber"}
	})
	provisioned, err := f.store.CreateSCIMUser(t.Context(), repository.SCIMUserCreate{ProviderID: f.provider.ID, UserName: "saml-user", ExternalID: "E-77", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	attempt := func(nameID, attributes, assertionID string) url.Values {
		t.Helper()
		login := f.begin("/")
		return redirectParams(t, f.post(&login, f.response(samltest.Response{RequestID: login.requestID, NameID: nameID, AssertionID: assertionID, Attributes: attributes}), login.relay))
	}
	if params := attempt("name-1", samltest.Attribute("username", "saml-user"), "_one"); params.Get("oidc_error") != "account_not_linked" {
		t.Fatalf("a login that matched only by userName = %v", params)
	}
	if params := attempt("name-1", samltest.Attribute("employeeNumber", "E-00"), "_two"); params.Get("oidc_error") != "account_not_linked" {
		t.Fatalf("a wrong externalId = %v", params)
	}
	if _, _, err := f.store.GetUserByProviderIdentity(t.Context(), f.provider.ID, "name-1"); err == nil {
		t.Fatal("the subject was bound without a match")
	}
	if params := attempt("name-1", samltest.Attribute("employeeNumber", "E-77"), "_three"); params.Get("oidc") != "success" {
		t.Fatalf("the matching externalId = %v", params)
	}
	account, link, err := f.store.GetUserByProviderIdentity(t.Context(), f.provider.ID, "name-1")
	if err != nil || account.ID != provisioned.User.ID || link.SCIMExternalID != "E-77" {
		t.Fatalf("bound account = %+v %+v, %v", account, link, err)
	}
	if params := attempt("name-2", samltest.Attribute("employeeNumber", "E-77"), "_four"); params.Get("oidc_error") != "account_not_linked" {
		t.Fatalf("a second subject took over the bound account = %v", params)
	}
	inactive := false
	if _, err := f.store.UpdateSCIMUser(t.Context(), f.provider.ID, provisioned.User.ID, repository.SCIMUserChange{Active: &inactive}); err != nil {
		t.Fatal(err)
	}
	if params := attempt("name-1", samltest.Attribute("employeeNumber", "E-77"), "_five"); params.Get("oidc_error") != "account_disabled" {
		t.Fatalf("login of a deactivated account = %v", params)
	}
}
