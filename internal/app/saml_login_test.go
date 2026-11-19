package app

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/identity/saml"
	"github.com/mlops-ca/notarx/internal/identity/saml/samltest"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
)

var samlTestNow = time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

const (
	samlTestEntityID = "https://idp.example.test/metadata"
	samlTestACS      = "https://notarx.example.test/auth/saml/corp/acs"
	samlTestSP       = "https://notarx.example.test/auth/saml/corp/metadata"
)

type capturedAudit struct {
	mu      sync.Mutex
	records []repository.AuditRecord
}

func (c *capturedAudit) RecordAudit(_ context.Context, record repository.AuditRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, record)
	return nil
}

func (c *capturedAudit) reasons() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	reasons := make([]string, 0, len(c.records))
	for _, record := range c.records {
		reasons = append(reasons, record.AuthorizationReason)
	}
	return reasons
}

type samlFixture struct {
	t        *testing.T
	store    *repository.MemoryStore
	runtime  *IdentityRuntime
	handler  samlLoginHandler
	audit    *capturedAudit
	key      samltest.Key
	provider identity.Provider
}

func newSAMLFixture(t *testing.T, mutate func(*IdentityProviderInput)) *samlFixture {
	t.Helper()
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	key := samltest.NewRSAKey(t, "idp")
	store := repository.NewMemoryStore()
	runtime := NewIdentityRuntime(store)
	publicURL, _ := url.Parse("https://notarx.example.test")
	runtime.ConfigureSAML(SAMLRuntimeOptions{Config: config.DefaultSAMLConfig(), PublicURL: publicURL, State: store, Now: func() time.Time { return samlTestNow }})
	input := IdentityProviderInput{
		Slug: "corp", Kind: identity.KindSAML, DisplayName: "Corp SAML", Enabled: true,
		SAML: &identity.SAMLSettings{
			EntityID: samlTestEntityID, SSOURL: "https://idp.example.test/sso",
			Certificates: []identity.SAMLCertificate{{PEM: saml.CertificatePEM(key.Cert)}},
		},
		ProvisioningMode: identity.ProvisioningJIT, JITDefaultRole: identity.JITRoleNone,
	}
	if mutate != nil {
		mutate(&input)
	}
	provider, err := runtime.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	audit := &capturedAudit{}
	return &samlFixture{
		t: t, store: store, runtime: runtime, audit: audit, key: key, provider: provider,
		handler: samlLoginHandler{runtime: runtime, authenticator: testAuthenticator(), identities: store, sessions: store, audit: audit},
	}
}

func (f *samlFixture) response(options samltest.Response) string {
	options.Key = f.key
	options.EntityID, options.SPEntityID, options.ACSURL, options.Now = samlTestEntityID, samlTestSP, samlTestACS, samlTestNow
	return options.Encode(f.t)
}

type samlLogin struct {
	requestID, relay string
	cookie           *http.Cookie
	location         string
}

// begin runs GET /login and returns what the IdP would receive plus the browser's flow cookie.
func (f *samlFixture) begin(redirect string) samlLogin {
	f.t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/auth/saml/corp/login?redirect="+url.QueryEscape(redirect), nil)
	request.SetPathValue("slug", "corp")
	recorder := httptest.NewRecorder()
	f.handler.start(recorder, request)
	if recorder.Code != http.StatusFound {
		f.t.Fatalf("login = %d %s", recorder.Code, recorder.Body.String())
	}
	location := recorder.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil || !strings.HasPrefix(location, "https://idp.example.test/sso") {
		f.t.Fatalf("location = %q, %v", location, err)
	}
	raw, err := base64.StdEncoding.DecodeString(parsed.Query().Get("SAMLRequest"))
	if err != nil {
		f.t.Fatal(err)
	}
	document, err := io.ReadAll(flate.NewReader(bytes.NewReader(raw)))
	if err != nil {
		f.t.Fatal(err)
	}
	text := string(document)
	start := strings.Index(text, `ID="`) + 4
	login := samlLogin{requestID: text[start : start+strings.Index(text[start:], `"`)], relay: parsed.Query().Get("RelayState"), location: location}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == samlFlowCookieName {
			login.cookie = cookie
		}
	}
	if login.cookie == nil || !login.cookie.HttpOnly || !login.cookie.Secure || login.cookie.SameSite != http.SameSiteNoneMode || login.cookie.Path != "/auth/saml/corp" {
		f.t.Fatalf("flow cookie = %+v", login.cookie)
	}
	return login
}

func (f *samlFixture) post(login *samlLogin, response, relay string) *httptest.ResponseRecorder {
	f.t.Helper()
	form := url.Values{"SAMLResponse": {response}}
	if relay != "" {
		form.Set("RelayState", relay)
	}
	request := httptest.NewRequest(http.MethodPost, "/auth/saml/corp/acs", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetPathValue("slug", "corp")
	if login != nil && login.cookie != nil {
		request.AddCookie(login.cookie)
	}
	recorder := httptest.NewRecorder()
	f.handler.acs(recorder, request)
	return recorder
}

func redirectParams(t *testing.T, recorder *httptest.ResponseRecorder) url.Values {
	t.Helper()
	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d %s", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil || location.Path != "/login" {
		t.Fatalf("location = %q, %v", recorder.Header().Get("Location"), err)
	}
	return location.Query()
}

func TestSAMLLoginEndToEndCreatesASAMLSession(t *testing.T) {
	f := newSAMLFixture(t, nil)
	login := f.begin("/repositories")
	recorder := f.post(&login, f.response(samltest.Response{RequestID: login.requestID, NameID: "persistent-1"}), login.relay)
	params := redirectParams(t, recorder)
	if params.Get("oidc") != "success" || params.Get("redirect") != "/repositories" {
		t.Fatalf("redirect params = %v", params)
	}
	var session *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == webSessionCookieName && cookie.MaxAge > 0 {
			session = cookie
		}
		if cookie.Name == samlFlowCookieName && cookie.MaxAge >= 0 {
			t.Fatal("the flow cookie must be cleared")
		}
	}
	if session == nil || !session.HttpOnly || !session.Secure {
		t.Fatalf("session cookie = %+v", session)
	}
	user, link, err := f.store.GetUserByProviderIdentity(t.Context(), f.provider.ID, "persistent-1")
	if err != nil || link.Kind != repository.UserIdentitySAML || link.Issuer != samlTestEntityID || user.Role != "none" {
		t.Fatalf("identity = %+v %+v %v", user, link, err)
	}
	sessions, err := f.store.ListUserSessions(t.Context(), user.ID, false)
	if err != nil || len(sessions) != 1 || sessions[0].Kind != repository.UserSessionSAML {
		t.Fatalf("sessions = %+v, %v", sessions, err)
	}
	if reasons := f.audit.reasons(); len(reasons) != 1 || reasons[0] != "login" {
		t.Fatalf("audit = %v", reasons)
	}
	// A second sign-in of the same subject reuses the account.
	again := f.begin("/")
	f.post(&again, f.response(samltest.Response{RequestID: again.requestID, NameID: "persistent-1", AssertionID: "_second"}), again.relay)
	if page, _ := f.store.ListUsers(t.Context(), repository.UserListQuery{Limit: 10}); page.Total != 1 {
		t.Fatalf("users = %d, want 1", page.Total)
	}
}

func TestSAMLACSRefusals(t *testing.T) {
	type attempt struct {
		name, code, reason string
		run                func(f *samlFixture) *httptest.ResponseRecorder
	}
	valid := func(f *samlFixture, login samlLogin, options samltest.Response) string {
		options.RequestID = login.requestID
		return f.response(options)
	}
	cases := []attempt{
		{"idp initiated without a flow", "invalid_state", "idp_initiated_refused", func(f *samlFixture) *httptest.ResponseRecorder {
			return f.post(nil, f.response(samltest.Response{InResponseTo: samltest.Ptr(""), ConfirmationTo: samltest.Ptr("")}), "")
		}},
		{"relay state mismatch", "invalid_state", "relay_state_mismatch", func(f *samlFixture) *httptest.ResponseRecorder {
			login := f.begin("/")
			return f.post(&login, valid(f, login, samltest.Response{}), "another-relay")
		}},
		{"missing response", "invalid_response", "missing_response", func(f *samlFixture) *httptest.ResponseRecorder {
			login := f.begin("/")
			return f.post(&login, "", login.relay)
		}},
		{"response for another request", "invalid_response", "in_response_to_mismatch", func(f *samlFixture) *httptest.ResponseRecorder {
			login := f.begin("/")
			return f.post(&login, valid(f, samlLogin{requestID: "_someone-elses-request"}, samltest.Response{}), login.relay)
		}},
		{"unsigned assertion", "invalid_response", "unsigned", func(f *samlFixture) *httptest.ResponseRecorder {
			login := f.begin("/")
			return f.post(&login, valid(f, login, samltest.Response{SignAssertion: samltest.Ptr(false)}), login.relay)
		}},
		{"unknown signer", "invalid_response", "signature_invalid", func(f *samlFixture) *httptest.ResponseRecorder {
			login := f.begin("/")
			options := samltest.Response{RequestID: login.requestID, Key: samltest.NewRSAKey(t, "evil"), EntityID: samlTestEntityID, SPEntityID: samlTestSP, ACSURL: samlTestACS, Now: samlTestNow}
			return f.post(&login, options.Encode(t), login.relay)
		}},
		{"expired", "invalid_response", "assertion_expired", func(f *samlFixture) *httptest.ResponseRecorder {
			login := f.begin("/")
			return f.post(&login, valid(f, login, samltest.Response{NotOnOrAfter: samlTestNow.Add(-time.Hour).Format(time.RFC3339)}), login.relay)
		}},
		{"tampered flow cookie", "invalid_state", "idp_initiated_refused", func(f *samlFixture) *httptest.ResponseRecorder {
			login := f.begin("/")
			login.cookie.Value += "x"
			return f.post(&login, valid(f, login, samltest.Response{}), login.relay)
		}},
		{"not base64", "invalid_response", "malformed_response", func(f *samlFixture) *httptest.ResponseRecorder {
			login := f.begin("/")
			return f.post(&login, "@@@", login.relay)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSAMLFixture(t, nil)
			params := redirectParams(t, tc.run(f))
			if params.Get("oidc_error") != tc.code || params.Get("oidc") != "" {
				t.Fatalf("redirect params = %v, want oidc_error=%s", params, tc.code)
			}
			reasons := f.audit.reasons()
			if len(reasons) != 1 || reasons[0] != tc.reason {
				t.Fatalf("audit reasons = %v, want [%s]", reasons, tc.reason)
			}
			if page, _ := f.store.ListUsers(t.Context(), repository.UserListQuery{Limit: 10}); page.Total != 0 {
				t.Fatal("a refused response created an account")
			}
			for _, record := range f.audit.records {
				if record.Class != repository.AuditClassSync || record.Outcome != repository.AuditAccessDenied || strings.Contains(record.AuthorizationReason, "user-1") {
					t.Fatalf("audit record = %+v", record)
				}
			}
		})
	}
}

func TestSAMLRequestStateIsSingleUse(t *testing.T) {
	f := newSAMLFixture(t, nil)
	login := f.begin("/")
	body := f.response(samltest.Response{RequestID: login.requestID})
	if params := redirectParams(t, f.post(&login, body, login.relay)); params.Get("oidc") != "success" {
		t.Fatalf("first use = %v", params)
	}
	// The same browser replays the same flow cookie and response: the request state is gone.
	params := redirectParams(t, f.post(&login, body, login.relay))
	if params.Get("oidc_error") != "invalid_state" {
		t.Fatalf("replay = %v", params)
	}
	if reasons := f.audit.reasons(); reasons[len(reasons)-1] != "request_state_consumed_or_expired" {
		t.Fatalf("audit = %v", reasons)
	}
}

func TestSAMLAssertionReplayIsRefusedUntilItExpires(t *testing.T) {
	f := newSAMLFixture(t, nil)
	first := f.begin("/")
	second := f.begin("/")
	// Both flows are legitimate, but the IdP somehow answered the second request with the first assertion
	// re-addressed: the assertion ID is the same, so it is refused even with a fresh request state.
	if params := redirectParams(t, f.post(&first, f.response(samltest.Response{RequestID: first.requestID, AssertionID: "_same"}), first.relay)); params.Get("oidc") != "success" {
		t.Fatalf("first = %v", params)
	}
	params := redirectParams(t, f.post(&second, f.response(samltest.Response{RequestID: second.requestID, AssertionID: "_same"}), second.relay))
	if params.Get("oidc_error") != "invalid_response" {
		t.Fatalf("replay = %v", params)
	}
	if reasons := f.audit.reasons(); reasons[len(reasons)-1] != "assertion_replayed" {
		t.Fatalf("audit = %v", reasons)
	}
}

func TestSAMLIdPInitiatedNeedsAnAllowlistedRelayState(t *testing.T) {
	f := newSAMLFixture(t, func(in *IdentityProviderInput) {
		in.SAML.AllowIdPInitiated, in.SAML.RelayStateAllowlist = true, []string{"/repositories", "/"}
	})
	body := f.response(samltest.Response{InResponseTo: samltest.Ptr(""), ConfirmationTo: samltest.Ptr("")})
	params := redirectParams(t, f.post(nil, body, "/repositories"))
	if params.Get("oidc") != "success" || params.Get("redirect") != "/repositories" {
		t.Fatalf("allowed = %v", params)
	}
	other := f.response(samltest.Response{InResponseTo: samltest.Ptr(""), ConfirmationTo: samltest.Ptr(""), AssertionID: "_other"})
	if params := redirectParams(t, f.post(nil, other, "https://evil.example.test/")); params.Get("oidc_error") != "invalid_state" {
		t.Fatalf("a relay state outside the allowlist = %v", params)
	}
	if reasons := f.audit.reasons(); reasons[len(reasons)-1] != "relay_state_not_allowed" {
		t.Fatalf("audit = %v", reasons)
	}
	// The relay state never becomes an open redirect: an allowlisted entry still goes through safeConsoleRedirect.
	if safeConsoleRedirect("//evil.example.test") != "/" {
		t.Fatal("open redirect")
	}
}

func TestSAMLProvisioningRules(t *testing.T) {
	t.Run("not linked without provisioning", func(t *testing.T) {
		f := newSAMLFixture(t, func(in *IdentityProviderInput) { in.ProvisioningMode = identity.ProvisioningDisabled })
		login := f.begin("/")
		params := redirectParams(t, f.post(&login, f.response(samltest.Response{RequestID: login.requestID}), login.relay))
		if params.Get("oidc_error") != "account_not_linked" {
			t.Fatalf("params = %v", params)
		}
	})
	t.Run("disabled account", func(t *testing.T) {
		f := newSAMLFixture(t, nil)
		login := f.begin("/")
		f.post(&login, f.response(samltest.Response{RequestID: login.requestID, AssertionID: "_a"}), login.relay)
		user, _, _ := f.store.GetUserByProviderIdentity(t.Context(), f.provider.ID, "user-1")
		if _, err := f.store.UpdateUser(t.Context(), repository.UserUpdate{ID: user.ID, State: samltest.Ptr(repository.UserDisabled)}, user.Version); err != nil {
			t.Fatal(err)
		}
		again := f.begin("/")
		params := redirectParams(t, f.post(&again, f.response(samltest.Response{RequestID: again.requestID, AssertionID: "_b"}), again.relay))
		if params.Get("oidc_error") != "account_disabled" {
			t.Fatalf("params = %v", params)
		}
	})
	t.Run("an asserted email never links an existing account", func(t *testing.T) {
		f := newSAMLFixture(t, func(in *IdentityProviderInput) { in.EmailLinkingEnabled = true })
		if _, err := f.store.CreateUser(t.Context(), repository.User{ID: ids.New(), Name: "ada", Email: "ada@example.test", Role: "member", State: repository.UserActive}); err != nil {
			t.Fatal(err)
		}
		login := f.begin("/")
		body := f.response(samltest.Response{RequestID: login.requestID, NameID: "Ada@Example.test", NameIDFormat: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress"})
		redirectParams(t, f.post(&login, body, login.relay))
		page, _ := f.store.ListUsers(t.Context(), repository.UserListQuery{Limit: 10})
		if page.Total != 2 {
			t.Fatalf("users = %d: the sign-in must create its own account instead of taking over ada", page.Total)
		}
		if _, link, err := f.store.GetUserByProviderIdentity(t.Context(), f.provider.ID, "ada@example.test"); err != nil || link.UserID == "" {
			t.Fatalf("the lower-cased emailAddress NameID is the identity key: %+v %v", link, err)
		}
	})
	// ADR 0018 Decision 3: a verified-email account links only when the provider allows it, the account is
	// not an administrator, has no local password, and exactly one such account matches the asserted email.
	adminVerified := func(t *testing.T, f *samlFixture, name, role, secret string) repository.User {
		t.Helper()
		user, err := f.store.CreateUser(t.Context(), repository.User{ID: ids.New(), Name: name, Email: "ada@example.test", Role: role, SecretHash: secret, State: repository.UserActive})
		if err != nil {
			t.Fatal(err)
		}
		user, err = f.store.UpdateUser(t.Context(), repository.UserUpdate{ID: user.ID, EmailVerifiedByAdmin: samltest.Ptr(true), EmailVerifiedBy: "root"}, user.Version)
		if err != nil {
			t.Fatal(err)
		}
		return user
	}
	signInWithEmail := func(t *testing.T, f *samlFixture, nameID, format string) url.Values {
		t.Helper()
		login := f.begin("/")
		return redirectParams(t, f.post(&login, f.response(samltest.Response{
			RequestID: login.requestID, NameID: nameID, NameIDFormat: format, Attributes: samltest.Attribute("email", "Ada@Example.test"),
		}), login.relay))
	}
	for _, tc := range []struct {
		name       string
		linking    bool
		role       string
		secret     string
		wantLinked bool
		wantReason string
	}{
		{"verified passwordless member links", true, "member", "", true, "email_link_linked"},
		{"provider without email linking", false, "member", "", false, ""},
		{"administrator is never linked", true, "admin", "", false, "email_link_admin_account"},
		{"local password is never linked", true, "member", "hash", false, "email_link_local_password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSAMLFixture(t, func(in *IdentityProviderInput) { in.EmailLinkingEnabled = tc.linking })
			target := adminVerified(t, f, "ada", tc.role, tc.secret)
			params := signInWithEmail(t, f, "opaque-persistent-id", "")
			linkedTo, _, err := f.store.GetUserByProviderIdentity(t.Context(), f.provider.ID, "opaque-persistent-id")
			if err != nil || (linkedTo.ID == target.ID) != tc.wantLinked {
				t.Fatalf("linked user = %+v err=%v, want linked=%v params=%v", linkedTo, err, tc.wantLinked, params)
			}
			if tc.wantLinked && params.Get("oidc") != "success" {
				t.Fatalf("params = %v", params)
			}
			reasons, found := f.audit.reasons(), false
			for _, reason := range reasons {
				if tc.wantReason != "" && reason == tc.wantReason {
					found = true
				}
				if tc.wantReason == "" && strings.HasPrefix(reason, "email_link_") {
					t.Fatalf("the email-link path is off but audited %v", reasons)
				}
			}
			if tc.wantReason != "" && !found {
				t.Fatalf("audit reasons = %v, want %s", reasons, tc.wantReason)
			}
		})
	}
	t.Run("two verified accounts are ambiguous", func(t *testing.T) {
		f := newSAMLFixture(t, func(in *IdentityProviderInput) { in.EmailLinkingEnabled = true })
		adminVerified(t, f, "ada-one", "member", "")
		adminVerified(t, f, "ada-two", "member", "")
		if params := signInWithEmail(t, f, "opaque-persistent-id", ""); params.Get("oidc_error") != "identity_ambiguous" {
			t.Fatalf("params = %v", params)
		}
	})
	t.Run("an emailAddress NameID alone never links a verified account", func(t *testing.T) {
		f := newSAMLFixture(t, func(in *IdentityProviderInput) { in.EmailLinkingEnabled = true })
		target := adminVerified(t, f, "ada", "member", "")
		login := f.begin("/")
		redirectParams(t, f.post(&login, f.response(samltest.Response{
			RequestID: login.requestID, NameID: "ada@example.test", NameIDFormat: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress",
			Attributes: samltest.Attribute("displayName", "Ada"),
		}), login.relay))
		user, _, err := f.store.GetUserByProviderIdentity(t.Context(), f.provider.ID, "ada@example.test")
		if err != nil || user.ID == target.ID {
			t.Fatalf("the NameID must not link the verified account: %+v %v", user, err)
		}
	})
}

// TestSAMLProvisionLinkingRules is the table of ADR 0018 section 3: no combination of provider options,
// NameID format and asserted email makes the resolution match an existing account by an asserted email,
// the administrator-verified link path follows only the provider's email_linking_enabled, and a role
// never comes from the assertion.
func TestSAMLProvisionLinkingRules(t *testing.T) {
	for _, tc := range []struct {
		name          string
		mode          identity.ProvisioningMode
		emailLinking  bool
		format        string
		wantProvision bool
	}{
		{"disabled", identity.ProvisioningDisabled, false, "persistent", false},
		{"disabled with email linking", identity.ProvisioningDisabled, true, "emailAddress", false},
		{"jit", identity.ProvisioningJIT, false, "persistent", true},
		{"jit with email linking", identity.ProvisioningJIT, true, "persistent", true},
		{"jit emailAddress with email linking", identity.ProvisioningJIT, true, "emailAddress", true},
		{"scim only", identity.ProvisioningSCIMOnly, true, "unspecified", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := identity.Provider{ID: "p", ProvisioningMode: tc.mode, EmailLinkingEnabled: tc.emailLinking, JITDefaultRole: identity.JITRoleMember}
			claims := saml.Claims{NameID: "n", Email: "admin@example.test", Username: "admin", Groups: []string{"admins"}}
			got := samlProvision(provider, identity.SAMLSettings{EntityID: "e", NameIDFormat: tc.format}, claims, samlTestNow, nil)
			if got.MatchEmail || got.EmailVerified || got.LinkAdminVerifiedEmail != tc.emailLinking || got.Provision != tc.wantProvision || got.Role != "" || got.Kind != repository.UserIdentitySAML || got.Subject != "n" || got.Issuer != "e" {
				t.Fatalf("provision = %+v", got)
			}
		})
	}
}

func TestSAMLMetadataAndUnknownProviders(t *testing.T) {
	f := newSAMLFixture(t, func(in *IdentityProviderInput) { in.SAML.SignRequests = true })
	request := httptest.NewRequest(http.MethodGet, "/auth/saml/corp/metadata", nil)
	request.SetPathValue("slug", "corp")
	recorder := httptest.NewRecorder()
	f.handler.metadata(recorder, request)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/samlmetadata+xml" ||
		!strings.Contains(body, `entityID="`+samlTestSP+`"`) || !strings.Contains(body, `Location="`+samlTestACS+`"`) || strings.Count(body, "<md:KeyDescriptor") != 1 || strings.Contains(body, "PRIVATE") {
		t.Fatalf("metadata = %d %s", recorder.Code, body)
	}
	// A signed login carries a signature.
	login := f.begin("/")
	if !strings.Contains(login.location, "Signature=") {
		t.Fatalf("login URL is not signed: %s", login.location)
	}
	for _, slug := range []string{"missing", "oidc-one"} {
		for name, call := range map[string]func(http.ResponseWriter, *http.Request){"start": f.handler.start, "metadata": f.handler.metadata, "acs": f.handler.acs} {
			request := httptest.NewRequest(http.MethodGet, "/auth/saml/"+slug+"/x", nil)
			request.SetPathValue("slug", slug)
			recorder := httptest.NewRecorder()
			call(recorder, request)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("%s %s = %d", name, slug, recorder.Code)
			}
		}
	}
	// A disabled provider is indistinguishable from an unknown one.
	provider, _ := f.runtime.Get(t.Context(), f.provider.ID)
	disabled, err := f.runtime.Replace(t.Context(), provider.ID, IdentityProviderInput{
		DisplayName: provider.DisplayName, Enabled: false, SAML: &identity.SAMLSettings{EntityID: samlTestEntityID, SSOURL: "https://idp.example.test/sso", Certificates: []identity.SAMLCertificate{{PEM: saml.CertificatePEM(f.key.Cert)}}},
		ProvisioningMode: provider.ProvisioningMode, JITDefaultRole: provider.JITDefaultRole,
	}, provider.Version)
	if err != nil || disabled.Enabled {
		t.Fatal(err)
	}
	f.runtime.invalidate()
	request = httptest.NewRequest(http.MethodGet, "/auth/saml/corp/metadata", nil)
	request.SetPathValue("slug", "corp")
	recorder = httptest.NewRecorder()
	f.handler.metadata(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("disabled provider = %d", recorder.Code)
	}
}

func TestSAMLUnavailableWithoutPublicURLOrState(t *testing.T) {
	f := newSAMLFixture(t, nil)
	f.runtime.saml.publicURL = nil
	request := httptest.NewRequest(http.MethodGet, "/auth/saml/corp/login", nil)
	request.SetPathValue("slug", "corp")
	recorder := httptest.NewRecorder()
	f.handler.start(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("no public URL = %d", recorder.Code)
	}
	unconfigured := samlLoginHandler{runtime: NewIdentityRuntime(f.store), authenticator: testAuthenticator()}
	recorder = httptest.NewRecorder()
	unconfigured.start(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("no SAML runtime = %d", recorder.Code)
	}
}

func TestSAMLResponseSizeCap(t *testing.T) {
	f := newSAMLFixture(t, nil)
	login := f.begin("/")
	huge := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), 2<<20))
	params := redirectParams(t, f.post(&login, huge, login.relay))
	if params.Get("oidc_error") != "invalid_response" {
		t.Fatalf("params = %v", params)
	}
	if reasons := f.audit.reasons(); reasons[len(reasons)-1] != "response_too_large" {
		t.Fatalf("audit = %v", reasons)
	}
}
