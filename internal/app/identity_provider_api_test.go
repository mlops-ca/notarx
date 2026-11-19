package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
)

func discoveryServer(t *testing.T) *httptest.Server {
	t.Helper()
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer": provider.URL, "authorization_endpoint": provider.URL + "/authorize",
			"token_endpoint": provider.URL + "/token", "jwks_uri": provider.URL + "/jwks",
		})
	}))
	t.Cleanup(provider.Close)
	return provider
}

func TestIdentityRuntimeSupportsBearerOnlyProvider(t *testing.T) {
	provider := discoveryServer(t)
	store := repository.NewMemoryStore()
	runtime := NewIdentityRuntime(store)
	created, err := runtime.Create(t.Context(), IdentityProviderInput{
		Slug: "api-only", Kind: identity.KindOIDC, DisplayName: "API only", Enabled: true,
		OIDC: identity.OIDCSettings{Issuer: provider.URL, Audience: "notarx-api"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtime.Browser(t.Context(), "api-only"); !errors.Is(err, errOIDCNotEnabled) {
		t.Fatalf("a provider without a browser client must not start a login: %v", err)
	}
	validators, err := runtime.OIDCValidators(t.Context())
	if err != nil || len(validators) != 1 {
		t.Fatalf("API validators=%v err=%v", validators, err)
	}
	result, err := runtime.Test(t.Context(), created.ID)
	if err != nil || !result.Reachable || result.JWKSURL != provider.URL+"/jwks" {
		t.Fatalf("connection test=%#v err=%v", result, err)
	}
}

func TestIdentityProviderLifecycleOverManagementAPI(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	provider := discoveryServer(t)
	store := repository.NewMemoryStore()
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{IdentityRuntime: NewIdentityRuntime(store)}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	call := func(method, path, body, ifMatch string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, "/api/v2"+path, strings.NewReader(body))
		authorize(request, "admin-secret")
		if ifMatch != "" {
			request.Header.Set("If-Match", ifMatch)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v2/authentication/providers", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	if empty := call(http.MethodGet, "/authentication/providers", "", ""); empty.Code != http.StatusOK || strings.TrimSpace(empty.Body.String()) != `{"items":[]}` {
		t.Fatalf("empty list=%d %s", empty.Code, empty.Body.String())
	}

	body := `{"slug":"corp","kind":"oidc","displayName":"Corp","enabled":true,"oidc":{"issuer":"` + provider.URL + `","audience":"notarx-api","clientId":"notarx-console","clientSecret":"runtime-secret","redirectUrl":"http://localhost:4173/auth/oidc/corp/callback","scopes":["openid","profile"],"adminSubjects":[],"memberRoles":["artifact-member"],"adminRoles":["artifact-admin"],"algorithms":["RS256","ES256"]},"attributeMapping":{"subject":"tid:oid"},"provisioningMode":"jit","jitDefaultRole":"none"}`
	created := call(http.MethodPost, "/authentication/providers", body, "")
	if created.Code != http.StatusCreated || created.Header().Get("ETag") != `"1"` || !strings.Contains(created.Body.String(), `"clientSecretConfigured":true`) || strings.Contains(created.Body.String(), "runtime-secret") {
		t.Fatalf("create=%d etag=%q body=%s", created.Code, created.Header().Get("ETag"), created.Body.String())
	}
	var view struct {
		ID               string `json:"id"`
		Slug             string `json:"slug"`
		AttributeMapping struct {
			Subject string `json:"subject"`
		} `json:"attributeMapping"`
		Oidc struct {
			Algorithms []string `json:"algorithms"`
		} `json:"oidc"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &view); err != nil || view.ID == "" || view.Slug != "corp" || view.AttributeMapping.Subject != "tid:oid" || len(view.Oidc.Algorithms) != 2 {
		t.Fatalf("created=%+v err=%v", view, err)
	}
	stored, err := store.GetIdentityProvider(t.Context(), view.ID)
	if err != nil || stored.SecretsSealed == "" || stored.SecretsSealed == "runtime-secret" {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	if opened, err := secrets.Open(identity.SecretPurpose, stored.SecretsSealed); err != nil || opened != "runtime-secret" {
		t.Fatalf("sealing round trip: %q %v", opened, err)
	}
	if duplicate := call(http.MethodPost, "/authentication/providers", body, ""); duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate slug=%d %s", duplicate.Code, duplicate.Body.String())
	}
	for name, bad := range map[string]string{
		"bad slug":  strings.Replace(body, `"slug":"corp"`, `"slug":"Corp!"`, 1),
		"saml kind": strings.Replace(body, `"kind":"oidc"`, `"kind":"saml"`, 1),
		"bad alg":   strings.Replace(body, `"RS256","ES256"`, `"HS256"`, 1),
		"stale jit": strings.Replace(body, `"jitDefaultRole":"none"`, `"jitDefaultRole":"admin"`, 1),
		"no issuer": strings.Replace(body, `"audience":"notarx-api"`, `"audience":""`, 1),
	} {
		if response := call(http.MethodPost, "/authentication/providers", bad, ""); response.Code != http.StatusBadRequest {
			t.Fatalf("%s accepted: %d %s", name, response.Code, response.Body.String())
		}
	}

	got := call(http.MethodGet, "/authentication/providers/"+view.ID, "", "")
	if got.Code != http.StatusOK || got.Header().Get("ETag") != `"1"` {
		t.Fatalf("get=%d etag=%q", got.Code, got.Header().Get("ETag"))
	}
	if missing := call(http.MethodGet, "/authentication/providers/00000000-0000-4000-8000-000000000000", "", ""); missing.Code != http.StatusNotFound {
		t.Fatalf("missing=%d", missing.Code)
	}

	replaceBody := `{"displayName":"Corp SSO","enabled":true,"oidc":{"issuer":"` + provider.URL + `","audience":"notarx-api","clientId":"notarx-console","redirectUrl":"http://localhost:4173/auth/oidc/corp/callback","scopes":["openid"],"adminSubjects":[],"memberRoles":[],"adminRoles":[]},"attributeMapping":{},"provisioningMode":"jit","emailLinkingEnabled":false,"jitDefaultRole":"member"}`
	replaced := call(http.MethodPut, "/authentication/providers/"+view.ID, replaceBody, `"1"`)
	if replaced.Code != http.StatusOK || replaced.Header().Get("ETag") != `"2"` || !strings.Contains(replaced.Body.String(), `"displayName":"Corp SSO"`) || !strings.Contains(replaced.Body.String(), `"clientSecretConfigured":true`) || !strings.Contains(replaced.Body.String(), `"slug":"corp"`) {
		t.Fatalf("replace=%d body=%s", replaced.Code, replaced.Body.String())
	}
	if stale := call(http.MethodPut, "/authentication/providers/"+view.ID, replaceBody, "1"); stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale replace=%d %s", stale.Code, stale.Body.String())
	}
	if junk := call(http.MethodPut, "/authentication/providers/"+view.ID, replaceBody, "abc"); junk.Code != http.StatusPreconditionFailed {
		t.Fatalf("unparsable If-Match=%d", junk.Code)
	}
	cleared := call(http.MethodPut, "/authentication/providers/"+view.ID, strings.Replace(replaceBody, `"redirectUrl"`, `"clearClientSecret":true,"redirectUrl"`, 1), "2")
	if cleared.Code != http.StatusOK || !strings.Contains(cleared.Body.String(), `"clientSecretConfigured":false`) {
		t.Fatalf("clear secret=%d %s", cleared.Code, cleared.Body.String())
	}

	testResponse := call(http.MethodPost, "/authentication/providers/"+view.ID+":test", "", "")
	if testResponse.Code != http.StatusOK || !strings.Contains(testResponse.Body.String(), `"reachable":true`) || !strings.Contains(testResponse.Body.String(), `"jwksUrl":"`+provider.URL+`/jwks"`) {
		t.Fatalf("test=%d body=%s", testResponse.Code, testResponse.Body.String())
	}

	// A provider with a linked identity cannot be deleted; an unlinked one can, with the right version.
	user, err := store.CreateUser(t.Context(), repository.User{ID: "owner", Name: "owner", Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	linked, err := store.CreateUserIdentity(t.Context(), repository.UserIdentity{UserID: user.ID, Kind: repository.UserIdentityOIDC, ProviderID: view.ID, Issuer: provider.URL, Subject: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if inUse := call(http.MethodDelete, "/authentication/providers/"+view.ID, "", "3"); inUse.Code != http.StatusConflict {
		t.Fatalf("delete with linked identities=%d %s", inUse.Code, inUse.Body.String())
	}
	if err := store.DeleteUserIdentity(t.Context(), user.ID, linked.ID); err != nil {
		t.Fatal(err)
	}
	if stale := call(http.MethodDelete, "/authentication/providers/"+view.ID, "", "1"); stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale delete=%d", stale.Code)
	}
	if deleted := call(http.MethodDelete, "/authentication/providers/"+view.ID, "", `"3"`); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete=%d %s", deleted.Code, deleted.Body.String())
	}
	if gone := call(http.MethodDelete, "/authentication/providers/"+view.ID, "", "3"); gone.Code != http.StatusNotFound {
		t.Fatalf("repeat delete=%d", gone.Code)
	}

	operations := map[string]bool{}
	for _, audit := range store.Audits {
		operations[audit.Operation] = true
	}
	for _, operation := range []string{"authentication.provider.create", "authentication.provider.update", "authentication.provider.test", "authentication.provider.delete"} {
		if !operations[operation] {
			t.Fatalf("missing audit %s in %#v", operation, store.Audits)
		}
	}
}

func TestIdentityProviderSecretIsPreservedWhenOmitted(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	store := repository.NewMemoryStore()
	runtime := NewIdentityRuntime(store)
	secret := "first-secret"
	input := IdentityProviderInput{Slug: "corp", Kind: identity.KindOIDC, DisplayName: "Corp", ClientSecret: &secret, OIDC: identity.OIDCSettings{Issuer: "https://idp.example.test", ClientID: "c"}}
	created, err := runtime.Create(t.Context(), input)
	if err != nil || created.SecretsSealed == "" {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	input.ClientSecret = nil
	second, err := runtime.Replace(t.Context(), created.ID, input, created.Version)
	if err != nil || second.SecretsSealed != created.SecretsSealed || second.Version != 2 {
		t.Fatalf("an omitted secret must be kept: %#v err=%v", second, err)
	}
	replacement := "replacement-secret"
	input.ClientSecret, input.ClearClientSecret = &replacement, true
	third, err := runtime.Replace(t.Context(), created.ID, input, second.Version)
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := secrets.Open(identity.SecretPurpose, third.SecretsSealed); err != nil || opened != replacement {
		t.Fatalf("a new secret wins over clear: %q %v", opened, err)
	}
	input.OIDC.ClientID = ""
	input.ClientSecret, input.ClearClientSecret = nil, false
	if _, err := runtime.Replace(t.Context(), created.ID, input, third.Version); !errors.Is(err, identity.ErrInvalid) {
		t.Fatalf("a secret without a client id must be refused: %v", err)
	}
}

func TestIdentityRuntimeSeedsFromEnvironmentOnce(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	store := repository.NewMemoryStore()
	runtime := NewIdentityRuntime(store)
	config := OIDCRuntimeConfig{Enabled: true, Issuer: "https://idp.example.test", Audience: "notarx-api", ClientID: "console", ClientSecret: "env-secret", RedirectURL: "http://localhost:4173/auth/oidc/default/callback"}
	if err := runtime.Seed(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	seeded, err := store.GetIdentityProviderBySlug(t.Context(), "default")
	if err != nil || !seeded.Enabled || seeded.Kind != identity.KindOIDC || seeded.SecretsSealed == "" || seeded.JITDefaultRole != identity.JITRoleMember {
		t.Fatalf("seeded=%#v err=%v", seeded, err)
	}
	// No ongoing merge: a changed environment does not touch the existing row.
	config.Audience = "changed"
	if err := runtime.Seed(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if again, _ := store.GetIdentityProviderBySlug(t.Context(), "default"); again.Version != seeded.Version {
		t.Fatalf("environment merged into an existing provider: %#v", again)
	}
	// Without an issuer nothing is seeded.
	empty := repository.NewMemoryStore()
	if err := NewIdentityRuntime(empty).Seed(t.Context(), OIDCRuntimeConfig{}); err != nil {
		t.Fatal(err)
	}
	if providers, _ := empty.ListIdentityProviders(t.Context()); len(providers) != 0 {
		t.Fatalf("seeded without an issuer: %v", providers)
	}
}

// The registry migration creates a bare 'default' provider for stored identities when no
// oidc_settings row existed; the environment completes it once.
func TestIdentityRuntimeSeedCompletesMigrationStub(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	store := repository.NewMemoryStore()
	stub, err := store.CreateIdentityProvider(t.Context(), identity.Provider{
		ID: "11111111-1111-4111-8111-111111111111", Slug: "default", Kind: identity.KindOIDC, DisplayName: "https://idp.example.test",
		Settings: []byte(`{"issuer":"https://idp.example.test"}`), ProvisioningMode: identity.ProvisioningDisabled, JITDefaultRole: identity.JITRoleNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	config := OIDCRuntimeConfig{Enabled: true, Issuer: "https://idp.example.test", Audience: "notarx-api", ClientID: "console", RedirectURL: "http://localhost:4173/auth/oidc/default/callback"}
	if err := NewIdentityRuntime(store).Seed(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	completed, err := store.GetIdentityProvider(t.Context(), stub.ID)
	if err != nil || !completed.Enabled || completed.Version != stub.Version+1 {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	settings, _ := completed.OIDC()
	if settings.Audience != "notarx-api" || settings.ClientID != "console" {
		t.Fatalf("settings=%#v", settings)
	}
	// A different issuer leaves the stub alone.
	other := repository.NewMemoryStore()
	if _, err := other.CreateIdentityProvider(t.Context(), identity.Provider{
		ID: "22222222-2222-4222-8222-222222222222", Slug: "default", Kind: identity.KindOIDC, DisplayName: "x",
		Settings: []byte(`{"issuer":"https://another.example.test"}`), ProvisioningMode: identity.ProvisioningDisabled, JITDefaultRole: identity.JITRoleNone,
	}); err != nil {
		t.Fatal(err)
	}
	if err := NewIdentityRuntime(other).Seed(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if untouched, _ := other.GetIdentityProviderBySlug(t.Context(), "default"); untouched.Version != 1 || untouched.Enabled {
		t.Fatalf("a stub of another issuer must stay as it is: %#v", untouched)
	}
}
