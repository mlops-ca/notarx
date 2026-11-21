package app

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/repository"
)

func TestOIDCAuthenticatorValidatesRS256ClaimsAndRepositoryAccess(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyID := "gateway-test-key"
	jwks := oidcJWKS(t, keyID, &privateKey.PublicKey)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(jwks)
	}))
	defer server.Close()

	authenticator := Authenticator{
		RepositoryReaders: map[string][]string{"ci-user": {"team/*"}},
		OIDC: NewOIDCValidator(OIDCConfig{
			Issuer:        "https://issuer.example.test",
			Audience:      "notarx",
			JWKSURL:       server.URL,
			AdminSubjects: []string{"gateway-admin"},
		}),
	}
	token := signedOIDCToken(t, privateKey, keyID, "https://issuer.example.test", "notarx", "ci-user", time.Now().Add(time.Minute))
	principal, ok := authenticator.Authenticate("Bearer " + token)
	if !ok || principal.Actor != "ci-user" || principal.Admin || principal.AuthenticationKind != "oidc" || !authenticator.CanReadRepository(principal, "team/app") || authenticator.CanReadRepository(principal, "other/app") {
		t.Fatalf("principal = %#v, authenticated=%t", principal, ok)
	}

	adminToken := signedOIDCToken(t, privateKey, keyID, "https://issuer.example.test", "notarx", "gateway-admin", time.Now().Add(time.Minute))
	admin, ok := authenticator.Authenticate("Bearer " + adminToken)
	if !ok || !admin.Admin || !admin.OIDCAdminSubject || admin.Role != RoleAdmin {
		t.Fatalf("admin = %#v, authenticated=%t", admin, ok)
	}
}

func TestOIDCBearerIdentityUsesBoundLocalAccountRoleAndState(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "bound-identity-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(oidcJWKS(t, keyID, &privateKey.PublicKey))
	}))
	defer server.Close()

	store := repository.NewMemoryStore()
	user, err := store.CreateUser(context.Background(), repository.User{
		ID: "bound-user", Name: "bound-user", Role: string(RoleMember),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateUserIdentity(context.Background(), repository.UserIdentity{
		ID: "bound-identity", UserID: user.ID, Kind: repository.UserIdentityOIDC, ProviderID: "provider-1",
		Issuer: "https://issuer.example.test", Subject: "provider-admin",
	}); err != nil {
		t.Fatal(err)
	}
	authenticator := Authenticator{
		OIDC: NewOIDCValidator(OIDCConfig{
			ProviderID: "provider-1",
			Issuer:     "https://issuer.example.test", Audience: "notarx", JWKSURL: server.URL,
		}),
		Users: store, UserIdentities: store,
	}
	token := signedOIDCTokenWithRoles(
		t, privateKey, keyID, "https://issuer.example.test", "notarx", "provider-admin",
		time.Now().Add(time.Minute), []string{"provider-admin-role"},
	)
	principal, ok := authenticator.Authenticate("Bearer " + token)
	if !ok || principal.Actor != "user:bound-user" || principal.UserID != user.ID || principal.Role != authorization.RoleMember || principal.Admin || principal.AuthenticationKind != authorization.AuthenticationOIDC {
		t.Fatalf("bound bearer principal=%#v ok=%v", principal, ok)
	}

	passwordRestricted, err := store.UpdateUserPassword(context.Background(), user.ID, "local-hash", user.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	principal, ok = authenticator.Authenticate("Bearer " + token)
	if !ok || principal.UserID != user.ID || !principal.MustChangePassword || principal.Role != "" || principal.Admin {
		t.Fatalf("password-restricted bearer principal=%#v ok=%v", principal, ok)
	}
	passwordReady, err := store.UpdateUserPassword(context.Background(), user.ID, "new-local-hash", passwordRestricted.Version, false)
	if err != nil {
		t.Fatal(err)
	}
	disabled := repository.UserDisabled
	if _, err = store.UpdateUser(context.Background(), repository.UserUpdate{ID: user.ID, State: &disabled}, passwordReady.Version); err != nil {
		t.Fatal(err)
	}
	if _, ok = authenticator.Authenticate("Bearer " + token); ok {
		t.Fatal("disabled local account authenticated with bound OIDC bearer")
	}
}

func TestOIDCAuthenticatorRejectsInvalidClaimsAndSignatures(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyID := "gateway-test-key"
	jwks := oidcJWKS(t, keyID, &privateKey.PublicKey)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(jwks)
	}))
	defer server.Close()
	authenticator := Authenticator{OIDC: NewOIDCValidator(OIDCConfig{Issuer: "https://issuer.example.test", Audience: "notarx", JWKSURL: server.URL})}

	for _, token := range []string{
		signedOIDCToken(t, privateKey, keyID, "https://wrong-issuer.example.test", "notarx", "ci-user", time.Now().Add(time.Minute)),
		signedOIDCToken(t, privateKey, keyID, "https://issuer.example.test", "other-audience", "ci-user", time.Now().Add(time.Minute)),
		signedOIDCToken(t, privateKey, keyID, "https://issuer.example.test", "notarx", "ci-user", time.Now().Add(-time.Minute)),
		"eyJhbGciOiJSUzI1NiIsImtpZCI6ImdhdGV3YXktdGVzdC1rZXkifQ.eyJpc3MiOiJodHRwczovL2lzc3Vlci5leGFtcGxlLnRlc3QiLCJzdWIiOiJjaS11c2VyIiwiYXVkIjoiYXJ0aWZhY3QtZ2F0ZXdheSIsImV4cCI6MjAwMDAwMDAwMH0.invalid",
	} {
		if _, ok := authenticator.Authenticate("Bearer " + token); ok {
			t.Fatalf("token was accepted: %q", token)
		}
	}
}

func TestOIDCAuthenticatorIgnoresRoleClaims(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyID := "role-mapping-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(oidcJWKS(t, keyID, &privateKey.PublicKey))
	}))
	defer server.Close()
	authenticator := Authenticator{OIDC: NewOIDCValidator(OIDCConfig{
		Issuer: "https://issuer.example.test", Audience: "notarx", JWKSURL: server.URL,
	})}

	for _, tc := range []struct {
		name  string
		roles []string
	}{
		{name: "member value", roles: []string{"gateway-member"}},
		{name: "admin value wins nothing", roles: []string{"gateway-member", "gateway-admin"}},
		{name: "unmapped", roles: []string{"other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := signedOIDCTokenWithRoles(t, privateKey, keyID, "https://issuer.example.test", "notarx", "ci-user", time.Now().Add(time.Minute), tc.roles)
			principal, ok := authenticator.Authenticate("Bearer " + token)
			if !ok || principal.AuthenticationKind != "oidc" || principal.Role != "" || principal.Admin {
				t.Fatalf("principal=%#v authenticated=%t", principal, ok)
			}
		})
	}
}

func TestOIDCAuthenticatorDiscoversJWKS(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyID := "discovery-test-key"
	jwks := oidcJWKS(t, keyID, &privateKey.PublicKey)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": serverURL(r), "jwks_uri": serverURL(r) + "/keys"})
		case "/keys":
			_, _ = w.Write(jwks)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	// The request host is the test server, so discovery can derive its issuer and
	// JWKS URI without closing over the server before it has been assigned. The
	// discovery document must name the configured issuer (OIDC Discovery 4.3).
	issuer := server.URL
	token := signedOIDCToken(t, privateKey, keyID, issuer, "notarx", "ci-user", time.Now().Add(time.Minute))
	authenticator := Authenticator{OIDC: NewOIDCValidator(OIDCConfig{Issuer: issuer, Audience: "notarx"})}
	if principal, ok := authenticator.Authenticate("Bearer " + token); !ok || principal.Actor != "ci-user" {
		t.Fatalf("principal=%#v authenticated=%t", principal, ok)
	}
}

func serverURL(r *http.Request) string { return "http://" + r.Host }

func TestOIDCSubjectIsRecordedForDeniedRepositoryRead(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyID := "gateway-test-key"
	jwks := oidcJWKS(t, keyID, &privateKey.PublicKey)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(jwks)
	}))
	defer server.Close()
	store := repository.NewMemoryStore()
	_, err = store.CreateGroup(context.Background(), repository.Group{Name: "team", Members: []repository.Member{{Name: "hosted", Type: repository.MemberHosted, Endpoint: "test://available"}}})
	if err != nil {
		t.Fatal(err)
	}
	authenticator := Authenticator{
		RepositoryReaders: map[string][]string{"ci-user": {"team/allowed/*"}},
		OIDC:              NewOIDCValidator(OIDCConfig{ProviderID: "provider-1", Issuer: "https://issuer.example.test", Audience: "notarx", JWKSURL: server.URL}),
	}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: authenticator})
	token := signedOIDCToken(t, privateKey, keyID, "https://issuer.example.test", "notarx", "ci-user", time.Now().Add(time.Minute))
	request := httptest.NewRequest(http.MethodGet, "/v2/team/denied/manifests/latest", nil)
	authorize(request, token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || len(store.Audits) != 1 || store.Audits[0].Actor != "ci-user" || store.Audits[0].Outcome != repository.AuditAccessDenied {
		t.Fatalf("response=%d audits=%#v", response.Code, store.Audits)
	}
}

func oidcJWKS(t *testing.T, keyID string, key *rsa.PublicKey) []byte {
	t.Helper()
	exponent := big.NewInt(int64(key.E)).Bytes()
	document := map[string]any{"keys": []map[string]string{{"kid": keyID, "kty": "RSA", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(exponent)}}}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func signedOIDCToken(t *testing.T, key *rsa.PrivateKey, keyID, issuer, audience, subject string, expires time.Time) string {
	return signedOIDCTokenWithRoles(t, key, keyID, issuer, audience, subject, expires, nil)
}

func signedOIDCTokenWithRoles(t *testing.T, key *rsa.PrivateKey, keyID, issuer, audience, subject string, expires time.Time, roles []string) string {
	return signedOIDCTokenWithClaims(t, key, keyID, issuer, audience, subject, expires, roles, "")
}

func signedOIDCTokenWithClaims(t *testing.T, key *rsa.PrivateKey, keyID, issuer, audience, subject string, expires time.Time, roles []string, nonce string) string {
	t.Helper()
	return signedOIDCTokenWithExtraClaims(t, key, keyID, issuer, audience, subject, expires, roles, nonce, nil)
}

// signedOIDCTokenWithExtraClaims adds claims such as groups or the Entra ID overage markers to the token.
func signedOIDCTokenWithExtraClaims(t *testing.T, key *rsa.PrivateKey, keyID, issuer, audience, subject string, expires time.Time, roles []string, nonce string, extra map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": keyID})
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"iss": issuer, "aud": audience, "sub": subject, "exp": expires.Unix()}
	if roles != nil {
		claims["realm_access"] = map[string]any{"roles": roles}
	}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	for name, value := range extra {
		claims[name] = value
	}
	claimBytes, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claimBytes)
	digest := sha256.Sum256([]byte(payload))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return payload + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// An OIDC validator that belongs to no provider cannot tell whether the subject is a
// linked account, so it must not authenticate a token while identities are stored: a
// disabled account would otherwise pass on the role claims of the token.
func TestOIDCBearerWithoutProviderFailsClosedWhenIdentitiesAreStored(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "no-provider-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(oidcJWKS(t, keyID, &privateKey.PublicKey))
	}))
	defer server.Close()
	validator := NewOIDCValidator(OIDCConfig{Issuer: "https://issuer.example.test", Audience: "notarx", JWKSURL: server.URL})
	token := signedOIDCToken(t, privateKey, keyID, "https://issuer.example.test", "notarx", "someone", time.Now().Add(time.Minute))
	if _, ok := (Authenticator{OIDC: validator}).Authenticate("Bearer " + token); !ok {
		t.Fatal("without stored identities the external-principal behavior is unchanged")
	}
	stored := Authenticator{OIDC: validator, UserIdentities: repository.NewMemoryStore()}
	if _, ok := stored.Authenticate("Bearer " + token); ok {
		t.Fatal("a provider-less validator must fail closed while identities are stored")
	}
}
