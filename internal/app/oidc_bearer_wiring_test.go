package app

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/repository"
)

// oidcBearerIssuer serves OIDC discovery and the signing keys of one issuer and signs tokens for it.
type oidcBearerIssuer struct {
	t      *testing.T
	key    *rsa.PrivateKey
	keyID  string
	server *httptest.Server
}

func newOIDCBearerIssuer(t *testing.T) *oidcBearerIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &oidcBearerIssuer{t: t, key: key, keyID: "bearer-wiring-key"}
	issuer.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": issuer.server.URL, "authorization_endpoint": issuer.server.URL + "/authorize",
				"token_endpoint": issuer.server.URL + "/token", "jwks_uri": issuer.server.URL + "/jwks",
			})
		case "/jwks":
			_, _ = w.Write(oidcJWKS(t, issuer.keyID, &key.PublicKey))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(issuer.server.Close)
	return issuer
}

func (i *oidcBearerIssuer) token(subject string) string {
	return signedOIDCToken(i.t, i.key, i.keyID, i.server.URL, "notarx-api", subject, time.Now().Add(time.Minute))
}

// bearerIdentity calls GET /api/v2/identity with the token and returns the status and decoded body.
func bearerIdentity(t *testing.T, handler http.Handler, token string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v2/identity", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := map[string]any{}
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	return response.Code, body
}

// TestOIDCBearerIsAcceptedInEverySupportedWiring pins issue 438: an OIDC bearer token must authenticate in
// every wiring that serves API requests with identity providers, and a validator that belongs to no
// provider must still be refused once identities are stored (ADR 0018: the identity key is the provider
// and the subject). The only production wiring (cmd/notarx) always passes an IdentityRuntime as the
// OIDCSource; the Gateway assembles its authenticator from it exactly like these cases do.
func TestOIDCBearerIsAcceptedInEverySupportedWiring(t *testing.T) {
	issuer := newOIDCBearerIssuer(t)
	store := repository.NewMemoryStore()
	runtime := NewIdentityRuntime(store)
	provider, err := runtime.Create(t.Context(), IdentityProviderInput{
		Slug: "corp", Kind: identity.KindOIDC, DisplayName: "Corp", Enabled: true,
		OIDC: identity.OIDCSettings{
			Issuer: issuer.server.URL, Audience: "notarx-api", ClientID: "notarx-console",
			RedirectURL: "http://localhost:4173/auth/oidc/corp/callback",
		},
		ProvisioningMode: identity.ProvisioningDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	linked, err := store.CreateUser(t.Context(), repository.User{ID: "linked-user", Name: "linked-user", Role: "member", State: repository.UserActive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateUserIdentity(t.Context(), repository.UserIdentity{
		ID: "linked-identity", UserID: linked.ID, Kind: repository.UserIdentityOIDC, ProviderID: provider.ID, Issuer: issuer.server.URL, Subject: "linked-subject",
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("gateway with the identity runtime, as cmd/notarx wires it", func(t *testing.T) {
		handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{IdentityRuntime: runtime}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
		if status, body := bearerIdentity(t, handler, issuer.token("linked-subject")); status != http.StatusOK || body["actor"] != "user:linked-user" {
			t.Fatalf("linked subject = %d %v", status, body)
		}
		if status, body := bearerIdentity(t, handler, issuer.token("unlinked-subject")); status != http.StatusOK || body["actor"] != "unlinked-subject" {
			t.Fatalf("an unlinked subject keeps the external-principal behavior = %d %v", status, body)
		}
		if status, _ := bearerIdentity(t, handler, "not-a-token"); status != http.StatusUnauthorized {
			t.Fatalf("garbage token = %d", status)
		}
	})

	t.Run("authenticator with the identity runtime as source", func(t *testing.T) {
		authenticator := Authenticator{OIDCSource: runtime, UserIdentities: store, Users: store}
		principal, ok := authenticator.Authenticate("Bearer " + issuer.token("linked-subject"))
		if !ok || principal.UserID != linked.ID {
			t.Fatalf("principal = %#v ok=%v", principal, ok)
		}
	})

	t.Run("gateway without an identity runtime has no OIDC validator and refuses bearers", func(t *testing.T) {
		handler := newTestGateway(t, GatewayOptions{Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
		if status, _ := bearerIdentity(t, handler, issuer.token("linked-subject")); status != http.StatusUnauthorized {
			t.Fatalf("without providers no OIDC token can authenticate, got %d", status)
		}
	})

	t.Run("a validator that belongs to no provider is refused while identities are stored", func(t *testing.T) {
		legacy := Authenticator{
			OIDC:           NewOIDCValidator(OIDCConfig{Issuer: issuer.server.URL, Audience: "notarx-api", JWKSURL: issuer.server.URL + "/jwks"}),
			UserIdentities: store, Users: store,
		}
		if _, ok := legacy.Authenticate("Bearer " + issuer.token("linked-subject")); ok {
			t.Fatal("a provider-less validator must fail closed: it cannot tell a disabled linked account from an unlinked subject")
		}
		withoutIdentities := Authenticator{OIDC: legacy.OIDC}
		if _, ok := withoutIdentities.Authenticate("Bearer " + issuer.token("anyone")); !ok {
			t.Fatal("without stored identities there is nothing to fail closed on")
		}
	})

	t.Run("a disabled linked account is refused", func(t *testing.T) {
		handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{IdentityRuntime: runtime}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
		disabled := repository.UserDisabled
		if _, err := store.UpdateUser(t.Context(), repository.UserUpdate{ID: linked.ID, State: &disabled}, linked.Version); err != nil {
			t.Fatal(err)
		}
		if status, _ := bearerIdentity(t, handler, issuer.token("linked-subject")); status != http.StatusUnauthorized {
			t.Fatalf("a disabled account = %d", status)
		}
	})
}

// TestAuthenticatorsWithoutOIDCRefuseBearerTokens covers the other wirings that build an Authenticator
// (the worker and maintenance commands): they carry no OIDC source or validator, so no OIDC token is
// accepted and nothing panics on the nil validator.
func TestAuthenticatorsWithoutOIDCRefuseBearerTokens(t *testing.T) {
	issuer := newOIDCBearerIssuer(t)
	store := repository.NewMemoryStore()
	for name, authenticator := range map[string]Authenticator{
		"static credentials only": {AdminToken: "root", ResolverToken: "resolver"},
		"with identity stores":    {AdminToken: "root", UserIdentities: store, Users: store},
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := authenticator.Authenticate("Bearer " + issuer.token("someone")); ok {
				t.Fatal("an OIDC token authenticated without any provider")
			}
			if _, ok := authenticator.Authenticate("Bearer " + strings.Repeat("x", 40)); ok {
				t.Fatal("garbage authenticated")
			}
		})
	}
}
