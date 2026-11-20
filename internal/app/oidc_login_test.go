package app

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
)

func TestOIDCBrowserJITRegistersPendingUserWithoutRepositoryAccess(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "pending-login-key"
	var expectedNonce string
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": provider.URL, "authorization_endpoint": provider.URL + "/authorize",
				"token_endpoint": provider.URL + "/token", "jwks_uri": provider.URL + "/jwks",
			})
		case "/jwks":
			_, _ = w.Write(oidcJWKS(t, keyID, &key.PublicKey))
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id_token": signedOIDCTokenWithClaims(
					t, key, keyID, provider.URL, "notarx-console", "pending-subject",
					time.Now().Add(time.Minute), nil, expectedNonce,
				),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer provider.Close()

	store := repository.NewMemoryStore()
	runtime := NewIdentityRuntime(store)
	_, err = runtime.Create(t.Context(), IdentityProviderInput{
		Slug: "corp", Kind: identity.KindOIDC, DisplayName: "Corp", Enabled: true,
		OIDC: identity.OIDCSettings{
			Issuer: provider.URL, Audience: "notarx-api", ClientID: "notarx-console",
			RedirectURL: "http://localhost:4173/auth/oidc/corp/callback",
		},
		ProvisioningMode: identity.ProvisioningJIT, JITDefaultRole: identity.JITRoleNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{IdentityRuntime: runtime}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	signIn := func() *http.Cookie {
		t.Helper()
		start := httptest.NewRecorder()
		handler.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/auth/oidc/corp/login", nil))
		if start.Code != http.StatusFound {
			t.Fatalf("start=%d body=%s", start.Code, start.Body.String())
		}
		target, err := url.Parse(start.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		expectedNonce = target.Query().Get("nonce")
		var flow *http.Cookie
		for _, cookie := range start.Result().Cookies() {
			if cookie.Name == oidcStateCookieName {
				flow = cookie
			}
		}
		if flow == nil || expectedNonce == "" {
			t.Fatal("OIDC flow state was not issued")
		}
		callback := httptest.NewRequest(http.MethodGet, "/auth/oidc/corp/callback?code=code&state="+url.QueryEscape(target.Query().Get("state")), nil)
		callback.AddCookie(flow)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, callback)
		if response.Code != http.StatusFound {
			t.Fatalf("callback=%d body=%s", response.Code, response.Body.String())
		}
		for _, cookie := range response.Result().Cookies() {
			if cookie.Name == webSessionCookieName && cookie.MaxAge > 0 {
				return cookie
			}
		}
		t.Fatal("browser session was not issued")
		return nil
	}
	cookie := signIn()
	page, err := store.ListUsers(t.Context(), repository.UserListQuery{Role: "none", Limit: 10})
	if err != nil || page.Total.Value != 1 || page.Items[0].SecretHash != "" {
		t.Fatalf("pending users=%+v err=%v", page, err)
	}
	user := page.Items[0]
	identityRequest := httptest.NewRequest(http.MethodGet, "/api/v2/identity", nil)
	identityRequest.AddCookie(cookie)
	identityResponse := httptest.NewRecorder()
	handler.ServeHTTP(identityResponse, identityRequest)
	if identityResponse.Code != http.StatusOK || !strings.Contains(identityResponse.Body.String(), `"role":"none"`) {
		t.Fatalf("pending identity=%d body=%s", identityResponse.Code, identityResponse.Body.String())
	}
	denied := httptest.NewRequest(http.MethodGet, "/api/v2/users", nil)
	denied.AddCookie(cookie)
	deniedResponse := httptest.NewRecorder()
	handler.ServeHTTP(deniedResponse, denied)
	if deniedResponse.Code == http.StatusOK {
		t.Fatal("pending user reached administrator API")
	}
	member := "member"
	user, err = store.UpdateUser(t.Context(), repository.UserUpdate{ID: user.ID, Role: &member}, user.Version)
	if err != nil {
		t.Fatal(err)
	}
	approvedResponse := httptest.NewRecorder()
	handler.ServeHTTP(approvedResponse, identityRequest)
	if approvedResponse.Code != http.StatusOK || !strings.Contains(approvedResponse.Body.String(), `"role":"member"`) {
		t.Fatalf("approved identity=%d body=%s", approvedResponse.Code, approvedResponse.Body.String())
	}
	_ = signIn()
	all, err := store.ListUsers(t.Context(), repository.UserListQuery{Limit: 10})
	if err != nil || all.Total.Value != 1 || all.Items[0].Role != "member" {
		t.Fatalf("repeat sign-in users=%+v err=%v", all, err)
	}
	none := "none"
	_, err = store.UpdateUser(t.Context(), repository.UserUpdate{ID: user.ID, Role: &none}, all.Items[0].Version)
	if err != nil {
		t.Fatal(err)
	}
	revokedResponse := httptest.NewRecorder()
	handler.ServeHTTP(revokedResponse, identityRequest)
	if revokedResponse.Code != http.StatusOK || !strings.Contains(revokedResponse.Body.String(), `"role":"none"`) {
		t.Fatalf("revoked identity=%d body=%s", revokedResponse.Code, revokedResponse.Body.String())
	}
	current, err := store.GetUser(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.RevokeUserSessions(t.Context(), user.ID, current.Version); err != nil {
		t.Fatal(err)
	}
	sessionRevokedResponse := httptest.NewRecorder()
	handler.ServeHTTP(sessionRevokedResponse, identityRequest)
	if sessionRevokedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session identity=%d", sessionRevokedResponse.Code)
	}
	newCookie := signIn()
	current, err = store.GetUser(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	disabled := repository.UserDisabled
	if _, err = store.UpdateUser(t.Context(), repository.UserUpdate{ID: user.ID, State: &disabled}, current.Version); err != nil {
		t.Fatal(err)
	}
	disabledRequest := httptest.NewRequest(http.MethodGet, "/api/v2/identity", nil)
	disabledRequest.AddCookie(newCookie)
	disabledResponse := httptest.NewRecorder()
	handler.ServeHTTP(disabledResponse, disabledRequest)
	if disabledResponse.Code != http.StatusUnauthorized {
		t.Fatalf("disabled account identity=%d", disabledResponse.Code)
	}
}

func TestOIDCBrowserLoginMapsBoundUserAndRevokesCookieSession(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	idp := newStubOIDCProvider(t, "browser-login-key", "gitlab-user", []string{"artifact-member"})
	store := repository.NewMemoryStore()
	runtime := NewIdentityRuntime(store)
	corp, err := runtime.Create(t.Context(), corpProviderInput("corp", idp))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Create(t.Context(), IdentityProviderInput{
		Slug: "hidden", Kind: identity.KindOIDC, DisplayName: "Hidden", Enabled: false,
		OIDC: identity.OIDCSettings{Issuer: idp.URL, Audience: "notarx-api", ClientID: "notarx-console", RedirectURL: "http://localhost:4173/auth/oidc/hidden/callback"},
	}); err != nil {
		t.Fatal(err)
	}
	localUser, err := store.CreateUser(t.Context(), repository.User{
		ID: "browser-oidc-user", Name: "local-user", Role: "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateUserIdentity(t.Context(), repository.UserIdentity{
		ID: "browser-oidc-identity", UserID: localUser.ID, Kind: repository.UserIdentityOIDC, ProviderID: corp.ID,
		Issuer: idp.URL, Subject: "gitlab-user",
	}); err != nil {
		t.Fatal(err)
	}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{IdentityRuntime: runtime}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})

	chooser := httptest.NewRecorder()
	handler.ServeHTTP(chooser, httptest.NewRequest(http.MethodGet, "/auth/idp", nil))
	if chooser.Code != http.StatusOK || !strings.Contains(chooser.Body.String(), `"slug":"corp"`) || strings.Contains(chooser.Body.String(), "hidden") || strings.Contains(chooser.Body.String(), "notarx-console") {
		t.Fatalf("chooser=%d body=%s", chooser.Code, chooser.Body.String())
	}

	hidden := httptest.NewRecorder()
	handler.ServeHTTP(hidden, httptest.NewRequest(http.MethodGet, "/auth/oidc/hidden/login", nil))
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/auth/oidc/nonexistent/login", nil))
	if hidden.Code != http.StatusNotFound || unknown.Code != http.StatusNotFound || hidden.Body.String() != unknown.Body.String() {
		t.Fatalf("a disabled provider must look unknown: hidden=%d unknown=%d", hidden.Code, unknown.Code)
	}

	flowCookie, state := startStubLogin(t, handler, idp, "corp", "/auth/oidc/corp/login?redirect=%2Frepositories")
	if !flowCookie.HttpOnly || flowCookie.SameSite != http.SameSiteLaxMode || flowCookie.Path != "/auth/oidc/corp" {
		t.Fatalf("flow cookie = %#v", flowCookie)
	}
	callback := httptest.NewRequest(http.MethodGet, "/auth/oidc/corp/callback?code=authorization-code&state="+url.QueryEscape(state), nil)
	callback.AddCookie(flowCookie)
	callbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(callbackResponse, callback)
	if callbackResponse.Code != http.StatusFound || !strings.HasPrefix(callbackResponse.Header().Get("Location"), "/login?") {
		t.Fatalf("callback=%d location=%q body=%s", callbackResponse.Code, callbackResponse.Header().Get("Location"), callbackResponse.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range callbackResponse.Result().Cookies() {
		if cookie.Name == webSessionCookieName && cookie.MaxAge > 0 {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie = %#v", sessionCookie)
	}

	identityRequest := httptest.NewRequest(http.MethodGet, "/api/v2/identity", nil)
	identityRequest.AddCookie(sessionCookie)
	identityResponse := httptest.NewRecorder()
	handler.ServeHTTP(identityResponse, identityRequest)
	if identityResponse.Code != http.StatusOK || !strings.Contains(identityResponse.Body.String(), `"actor":"user:local-user"`) || !strings.Contains(identityResponse.Body.String(), `"kind":"oidc"`) || !strings.Contains(identityResponse.Body.String(), `"role":"member"`) {
		t.Fatalf("identity=%d body=%s", identityResponse.Code, identityResponse.Body.String())
	}

	sessions, err := store.ListUserSessions(t.Context(), localUser.ID, false)
	if err != nil || len(sessions) != 1 || sessions[0].Kind != repository.UserSessionOIDC {
		t.Fatalf("OIDC sessions=%+v err=%v", sessions, err)
	}
	logout := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	logout.AddCookie(sessionCookie)
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logout)
	if logoutResponse.Code != http.StatusNoContent {
		t.Fatalf("logout=%d body=%s", logoutResponse.Code, logoutResponse.Body.String())
	}
	revokedIdentity := httptest.NewRequest(http.MethodGet, "/api/v2/identity", nil)
	revokedIdentity.AddCookie(sessionCookie)
	revokedResponse := httptest.NewRecorder()
	handler.ServeHTTP(revokedResponse, revokedIdentity)
	if revokedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("revoked cookie identity=%d body=%s", revokedResponse.Code, revokedResponse.Body.String())
	}
	sessions, err = store.ListUserSessions(t.Context(), localUser.ID, true)
	if err != nil || len(sessions) != 1 || sessions[0].RevokedAt == nil {
		t.Fatalf("revoked OIDC sessions=%+v err=%v", sessions, err)
	}
}

// Two providers with the same subject string are two identities: each signs in on its
// own flow cookie, and a flow started for one provider cannot be completed on the other.
func TestOIDCTwoProvidersSignInIndependently(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	alpha := newStubOIDCProvider(t, "alpha-key", "shared-subject", nil)
	beta := newStubOIDCProvider(t, "beta-key", "shared-subject", nil)
	store := repository.NewMemoryStore()
	runtime := NewIdentityRuntime(store)
	for slug, idp := range map[string]*stubOIDCProvider{"alpha": alpha, "beta": beta} {
		input := corpProviderInput(slug, idp)
		input.ProvisioningMode, input.JITDefaultRole = identity.ProvisioningJIT, identity.JITRoleMember
		if _, err := runtime.Create(t.Context(), input); err != nil {
			t.Fatal(err)
		}
	}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{IdentityRuntime: runtime}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})

	alphaFlow, alphaState := startStubLogin(t, handler, alpha, "alpha", "/auth/oidc/alpha/login")
	betaFlow, betaState := startStubLogin(t, handler, beta, "beta", "/auth/oidc/beta/login")
	if alphaFlow.Path == betaFlow.Path {
		t.Fatalf("flow cookies share a path: %s", alphaFlow.Path)
	}
	cross := httptest.NewRequest(http.MethodGet, "/auth/oidc/beta/callback?code=authorization-code&state="+url.QueryEscape(alphaState), nil)
	cross.AddCookie(alphaFlow)
	crossResponse := httptest.NewRecorder()
	handler.ServeHTTP(crossResponse, cross)
	if loc := crossResponse.Header().Get("Location"); crossResponse.Code != http.StatusFound || !strings.Contains(loc, "oidc_error=invalid_state") {
		t.Fatalf("cross-provider callback=%d location=%q", crossResponse.Code, loc)
	}
	for slug, flow := range map[string]struct {
		cookie *http.Cookie
		state  string
	}{"alpha": {alphaFlow, alphaState}, "beta": {betaFlow, betaState}} {
		callback := httptest.NewRequest(http.MethodGet, "/auth/oidc/"+slug+"/callback?code=authorization-code&state="+url.QueryEscape(flow.state), nil)
		callback.AddCookie(flow.cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, callback)
		if loc := response.Header().Get("Location"); response.Code != http.StatusFound || !strings.Contains(loc, "oidc=success") {
			t.Fatalf("%s callback=%d location=%q", slug, response.Code, loc)
		}
	}
	users, err := store.ListUsers(t.Context(), repository.UserListQuery{Limit: 10})
	if err != nil || users.Total.Value != 2 {
		t.Fatalf("each provider must provision its own account: %+v err=%v", users, err)
	}
	alphaProvider, _ := runtime.store.GetIdentityProviderBySlug(t.Context(), "alpha")
	betaProvider, _ := runtime.store.GetIdentityProviderBySlug(t.Context(), "beta")
	if _, _, err := store.GetUserByProviderIdentity(t.Context(), alphaProvider.ID, "shared-subject"); err != nil {
		t.Fatalf("alpha identity: %v", err)
	}
	if _, _, err := store.GetUserByProviderIdentity(t.Context(), betaProvider.ID, "shared-subject"); err != nil {
		t.Fatalf("beta identity: %v", err)
	}
}

func TestIdentityChooserIsEmptyWithoutProviders(t *testing.T) {
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: repository.NewMemoryStore(), Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/auth/idp", nil))
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"items":[]}` {
		t.Fatalf("chooser=%d body=%s", response.Code, response.Body.String())
	}
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/auth/oidc/default/login", nil))
	if login.Code != http.StatusNotFound {
		t.Fatalf("login without providers=%d", login.Code)
	}
}

// stubOIDCProvider is a minimal OIDC IdP: discovery, JWKS and a token endpoint that
// signs an ID token for the nonce of the flow under test.
type stubOIDCProvider struct {
	*httptest.Server
	mu      sync.Mutex
	nonce   string
	subject string
	roles   []string
	// extra claims are added to the ID token the next token request returns.
	extra map[string]any
}

func newStubOIDCProvider(t *testing.T, keyID, subject string, roles []string) *stubOIDCProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &stubOIDCProvider{subject: subject, roles: roles}
	idp.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": idp.URL, "authorization_endpoint": idp.URL + "/authorize",
				"token_endpoint": idp.URL + "/token", "jwks_uri": idp.URL + "/jwks",
			})
		case "/jwks":
			_, _ = w.Write(oidcJWKS(t, keyID, &key.PublicKey))
		case "/token":
			if err := r.ParseForm(); err != nil || r.Form.Get("code") != "authorization-code" || r.Form.Get("code_verifier") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			idp.mu.Lock()
			nonce, extra := idp.nonce, idp.extra
			idp.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "provider-access-token", "token_type": "Bearer", "expires_in": 300,
				"id_token": signedOIDCTokenWithExtraClaims(t, key, keyID, idp.URL, "notarx-console", idp.subject, time.Now().Add(time.Minute), idp.roles, nonce, extra),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(idp.Close)
	return idp
}

func (p *stubOIDCProvider) setExtraClaims(extra map[string]any) {
	p.mu.Lock()
	p.extra = extra
	p.mu.Unlock()
}

func (p *stubOIDCProvider) setNonce(nonce string) {
	p.mu.Lock()
	p.nonce = nonce
	p.mu.Unlock()
}

func corpProviderInput(slug string, idp *stubOIDCProvider) IdentityProviderInput {
	return IdentityProviderInput{
		Slug: slug, Kind: identity.KindOIDC, DisplayName: strings.ToUpper(slug), Enabled: true,
		OIDC: identity.OIDCSettings{
			Issuer: idp.URL, Audience: "notarx-api", ClientID: "notarx-console",
			RedirectURL: "http://localhost:4173/auth/oidc/" + slug + "/callback", Scopes: []string{"openid", "profile"},
			MemberRoles: []string{"artifact-member"},
		},
	}
}

// startStubLogin runs the start step of the flow and returns the flow cookie and state.
func startStubLogin(t *testing.T, handler http.Handler, idp *stubOIDCProvider, slug, path string) (*http.Cookie, string) {
	t.Helper()
	start := httptest.NewRecorder()
	handler.ServeHTTP(start, httptest.NewRequest(http.MethodGet, path, nil))
	if start.Code != http.StatusFound {
		t.Fatalf("start %s=%d body=%s", slug, start.Code, start.Body.String())
	}
	target, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	nonce, state := target.Query().Get("nonce"), target.Query().Get("state")
	if nonce == "" || state == "" || target.Query().Get("code_challenge") == "" {
		t.Fatalf("authorization URL = %s", target)
	}
	idp.setNonce(nonce)
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name == oidcStateCookieName {
			return cookie, state
		}
	}
	t.Fatal("OIDC flow state was not issued")
	return nil, ""
}
