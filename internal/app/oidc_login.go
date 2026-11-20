package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/repository"
)

const (
	oidcStateCookieName  = "ag_oidc_state"
	webSessionCookieName = "ag_session"
	oidcFlowLifetime     = 10 * time.Minute
	webSessionLifetime   = 12 * time.Hour
)

type oidcLoginHandler struct {
	runtime       *IdentityRuntime
	authenticator Authenticator
	identities    repository.UserIdentityStore
	sessions      repository.UserSessionStore
	// groups receives the claims-sourced group memberships of a login (ADR 0018 Decision 5); nil disables them.
	groups repository.UserGroupStore
	// audit records the decisions a login makes about groups; nil records nothing.
	audit loginAuditor
}

type oidcFlowState struct {
	Slug      string `json:"slug"`
	State     string `json:"state"`
	Nonce     string `json:"nonce"`
	Verifier  string `json:"verifier"`
	Redirect  string `json:"redirect"`
	ExpiresAt int64  `json:"expiresAt"`
}

// providers lists the enabled providers for the login chooser. A disabled provider is never revealed.
func (h oidcLoginHandler) providers(w http.ResponseWriter, r *http.Request) {
	items := []PublicIdentityProvider{}
	if h.runtime != nil {
		public, err := h.runtime.Public(r.Context())
		if err != nil {
			writeHostedProblem(w, http.StatusServiceUnavailable, "identity_providers_unavailable", "identity providers are unavailable")
			return
		}
		items = append(items, public...)
	}
	writeNativeMavenJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h oidcLoginHandler) browser(r *http.Request) (*oidcProviderState, *authorization.OIDCValidator, error) {
	if h.runtime == nil {
		return nil, nil, errProviderNotFound
	}
	return h.runtime.Browser(r.Context(), r.PathValue("slug"))
}

func oidcFlowCookiePath(slug string) string { return "/auth/oidc/" + slug }

func (h oidcLoginHandler) start(w http.ResponseWriter, r *http.Request) {
	provider, _, err := h.browser(r)
	if errors.Is(err, errOIDCNotEnabled) || errors.Is(err, errProviderNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "OIDC browser login is not configured")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusServiceUnavailable, "oidc_unavailable", "OIDC configuration is unavailable")
		return
	}
	client := provider.browserClient
	state, err := randomURLToken(32)
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "create OIDC login state failed")
		return
	}
	nonce, err := randomURLToken(32)
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "create OIDC login nonce failed")
		return
	}
	verifier, err := randomURLToken(48)
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "create OIDC PKCE verifier failed")
		return
	}
	flow := oidcFlowState{
		Slug: provider.provider.Slug, State: state, Nonce: nonce, Verifier: verifier,
		Redirect:  safeConsoleRedirect(r.URL.Query().Get("redirect")),
		ExpiresAt: time.Now().UTC().Add(oidcFlowLifetime).Unix(),
	}
	encoded, err := signOIDCFlow(flow, h.authenticator.AdminToken)
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "create OIDC login state failed")
		return
	}
	challenge := sha256.Sum256([]byte(verifier))
	authorizationURL, err := client.AuthorizationURL(r.Context(), state, nonce, base64.RawURLEncoding.EncodeToString(challenge[:]))
	if err != nil {
		writeHostedProblem(w, http.StatusServiceUnavailable, "oidc_unavailable", "OIDC provider discovery failed")
		return
	}
	// The cookie path names the provider, so two providers never share or overwrite a flow cookie.
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookieName, Value: encoded, Path: oidcFlowCookiePath(flow.Slug), MaxAge: int(oidcFlowLifetime.Seconds()),
		HttpOnly: true, Secure: oidcSecureCookie(client.RedirectURL()), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, authorizationURL, http.StatusFound)
}

func (h oidcLoginHandler) callback(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	provider, validator, resolveErr := h.browser(r)
	if resolveErr != nil || provider == nil || validator == nil {
		h.clearFlowCookie(w, r, slug)
		h.redirectError(w, r, "not_configured")
		return
	}
	client := provider.browserClient
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		h.clearFlowCookie(w, r, slug)
		h.redirectError(w, r, providerError)
		return
	}
	code, state := r.URL.Query().Get("code"), r.URL.Query().Get("state")
	cookie, err := r.Cookie(oidcStateCookieName)
	if err != nil || code == "" || state == "" {
		h.clearFlowCookie(w, r, slug)
		h.redirectError(w, r, "invalid_callback")
		return
	}
	flow, err := verifyOIDCFlow(cookie.Value, h.authenticator.AdminToken)
	if err != nil || flow.Slug != slug || flow.State != state || time.Now().UTC().Unix() >= flow.ExpiresAt {
		h.clearFlowCookie(w, r, slug)
		h.redirectError(w, r, "invalid_state")
		return
	}
	h.clearFlowCookie(w, r, slug)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tokens, err := client.Exchange(ctx, code, flow.Verifier)
	if err != nil {
		h.redirectError(w, r, "exchange_failed")
		return
	}
	verified, ok := validator.ValidateNonce(ctx, tokens.IDToken, flow.Nonce)
	if !ok {
		h.redirectError(w, r, "invalid_identity")
		return
	}
	if h.identities != nil {
		issuer := verified.Issuer
		if issuer == "" {
			issuer = client.Issuer()
		}
		user, _, _, resolveErr := h.identities.ResolveOIDCIdentity(ctx, repository.OIDCIdentityProvision{
			ProviderID: provider.provider.ID, Issuer: issuer, Subject: verified.Subject, Email: verified.Email,
			DisplayName: verified.DisplayName, PreferredUsername: verified.Username,
			EmailVerified: verified.EmailVerified, Role: string(verified.Role),
			Provision: provider.provider.ProvisioningMode == identity.ProvisioningJIT, MatchEmail: provider.provider.EmailLinkingEnabled,
			DefaultRole: provider.provider.JITDefaultRole, OccurredAt: time.Now().UTC(),
		})
		switch {
		case resolveErr == nil:
			if user.State != repository.UserActive {
				h.redirectError(w, r, "account_disabled")
				return
			}
			if !h.syncClaimsGroups(ctx, r, provider.provider, user.ID, verified) {
				h.redirectError(w, r, "identity_unavailable")
				return
			}
			principal := h.authenticator.PrincipalForActor("user:" + user.Name)
			principal.Admin = user.Role == string(authorization.RoleAdmin)
			principal.Role = authorization.Role(user.Role)
			principal.AuthenticationKind = authorization.AuthenticationOIDC
			principal.OIDCAdminSubject = verified.AdminSubject
			principal.OIDCRoleMappings = verified.RoleMappings
			sessionID := newUserSessionID()
			if h.sessions != nil {
				if _, err := h.sessions.CreateUserSession(ctx, repository.UserSession{
					ID: sessionID, UserID: user.ID, Kind: repository.UserSessionOIDC,
					CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(authorization.UserSessionLifetime),
					IPAddress: requestIPAddress(r), UserAgent: boundedUserAgent(r.UserAgent()),
				}); err != nil {
					h.redirectError(w, r, "session_unavailable")
					return
				}
			}
			session := h.authenticator.IssueWebSessionWithID(principal, sessionID, user.SessionVersion)
			h.setWebSessionCookie(w, client, session)
			values := url.Values{"oidc": {"success"}, "redirect": {safeConsoleRedirect(flow.Redirect)}}
			http.Redirect(w, r, "/login?"+values.Encode(), http.StatusFound)
			return
		case errors.Is(resolveErr, repository.ErrNotFound):
			// Provisioning is disabled and the subject is not manually linked;
			// preserve the existing external-principal behavior.
		case errors.Is(resolveErr, repository.ErrIdentityAmbiguous):
			h.redirectError(w, r, "identity_ambiguous")
			return
		default:
			h.redirectError(w, r, "identity_unavailable")
			return
		}
	}
	principal := h.authenticator.PrincipalForActor(verified.Subject)
	principal.Admin = verified.Admin
	principal.Role = verified.Role
	principal.AuthenticationKind = authorization.AuthenticationOIDC
	principal.OIDCAdminSubject = verified.AdminSubject
	principal.OIDCRoleMappings = verified.RoleMappings
	session := h.authenticator.IssueWebSession(principal)
	h.setWebSessionCookie(w, client, session)
	values := url.Values{"oidc": {"success"}, "redirect": {safeConsoleRedirect(flow.Redirect)}}
	http.Redirect(w, r, "/login?"+values.Encode(), http.StatusFound)
}

// syncClaimsGroups replaces the claims-sourced memberships of the account with the groups the token asserts. A
// token that overflowed its groups claim (Entra ID overage) asserts none, and the overage is audited; it reports
// false only when the store failed, so a membership the provider withdrew is never left in place unknowingly.
func (h oidcLoginHandler) syncClaimsGroups(ctx context.Context, r *http.Request, provider identity.Provider, userID string, verified authorization.OIDCIdentity) bool {
	if h.groups == nil {
		return true
	}
	groups := verified.Groups
	if verified.GroupsOverage {
		groups = nil
		if h.audit != nil {
			_ = h.audit.RecordAudit(ctx, repository.GroupsOverageAudit("oidc", provider.Slug, time.Now(), http.StatusFound, r.Header.Get("X-Request-Id")))
		}
	}
	_, err := h.groups.SyncClaimsUserGroups(ctx, repository.ClaimsGroupSync{ProviderID: provider.ID, UserID: userID, ExternalIDs: groups})
	return err == nil
}

func (h oidcLoginHandler) setWebSessionCookie(w http.ResponseWriter, client *authorization.OIDCClient, session string) {
	http.SetCookie(w, &http.Cookie{
		Name: webSessionCookieName, Value: session, Path: "/", MaxAge: int(webSessionLifetime.Seconds()),
		HttpOnly: true, Secure: oidcSecureCookie(client.RedirectURL()), SameSite: http.SameSiteLaxMode,
	})
}

func (h oidcLoginHandler) logout(w http.ResponseWriter, r *http.Request) {
	if principal, ok := h.authenticator.Authenticate(r.Header.Get("Authorization")); ok && principal.UserID != "" && principal.SessionID != "" && h.sessions != nil {
		_, _ = h.sessions.RevokeUserSession(r.Context(), principal.UserID, principal.SessionID)
	}
	http.SetCookie(w, &http.Cookie{
		Name: webSessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: oidcSecureRequest(r), SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h oidcLoginHandler) session(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.authenticator.Authenticate(r.Header.Get("Authorization"))
	if !ok {
		writeNativeMavenJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	writeNativeMavenJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"identity":      currentIdentityResponse(principal),
	})
}

func (h oidcLoginHandler) redirectError(w http.ResponseWriter, r *http.Request, code string) {
	values := url.Values{"oidc_error": {code}}
	http.Redirect(w, r, "/login?"+values.Encode(), http.StatusFound)
}

func (h oidcLoginHandler) clearFlowCookie(w http.ResponseWriter, r *http.Request, slug string) {
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookieName, Value: "", Path: oidcFlowCookiePath(slug), MaxAge: -1,
		HttpOnly: true, Secure: oidcSecureRequest(r), SameSite: http.SameSiteLaxMode,
	})
}

func sessionCookieAuthentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			if cookie, err := r.Cookie(webSessionCookieName); err == nil && cookie.Value != "" {
				clone := r.Clone(r.Context())
				clone.Header.Set("Authorization", "Bearer "+cookie.Value)
				r = clone
			}
		}
		next.ServeHTTP(w, r)
	})
}

func randomURLToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func signOIDCFlow(flow oidcFlowState, secret string) (string, error) {
	return signFlowPayload(flow, secret)
}

// signFlowPayload signs a flow cookie payload with an HMAC of the signing secret.
func signFlowPayload(flow any, secret string) (string, error) {
	if secret == "" {
		return "", errors.New("login state signing secret is empty")
	}
	payload, err := json.Marshal(flow)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verifyOIDCFlow(value, secret string) (oidcFlowState, error) {
	var flow oidcFlowState
	if err := verifyFlowPayload(value, secret, &flow); err != nil {
		return oidcFlowState{}, err
	}
	if flow.State == "" || flow.Nonce == "" || flow.Verifier == "" || flow.ExpiresAt == 0 {
		return oidcFlowState{}, errors.New("invalid OIDC state payload")
	}
	return flow, nil
}

// verifyFlowPayload checks the HMAC of a flow cookie and decodes its payload into target.
func verifyFlowPayload(value, secret string, target any) error {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || secret == "" {
		return errors.New("invalid login state")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return errors.New("invalid login state signature")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return errors.New("invalid login state signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(payload, target) != nil {
		return errors.New("invalid login state payload")
	}
	return nil
}

func safeConsoleRedirect(value string) string {
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, "\\") {
		return "/"
	}
	return value
}

func oidcSecureCookie(redirectURL string) bool {
	parsed, err := url.Parse(redirectURL)
	return err == nil && parsed.Scheme == "https"
}

func oidcSecureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
