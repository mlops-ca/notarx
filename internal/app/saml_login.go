package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/identity/saml"
	"github.com/mlops-ca/notarx/internal/repository"
)

const samlFlowCookieName = "ag_saml_state"

// samlAuditor records the precise reason of a sign-in decision; the browser only ever sees a generic code.
type samlAuditor interface {
	RecordAudit(context.Context, repository.AuditRecord) error
}

// samlLoginHandler serves the browser flow of ADR 0018 section 3: login (HTTP-Redirect AuthnRequest),
// the assertion consumer service and the service provider metadata. It mirrors oidcLoginHandler.
type samlLoginHandler struct {
	runtime       *IdentityRuntime
	authenticator Authenticator
	identities    repository.UserIdentityStore
	sessions      repository.UserSessionStore
	audit         samlAuditor
	limiter       *samlRateLimiter
}

// limited reports whether the client has used up its anonymous SAML budget.
func (h samlLoginHandler) limited(r *http.Request) bool {
	// The peer address, not X-Forwarded-For: a client-supplied header would let one caller dodge the budget or fill the table.
	peer := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	return h.limiter != nil && !h.limiter.allow(peer, h.runtimeNow())
}

// samlFlowState is the signed flow cookie. It binds the browser to one AuthnRequest: the request ID
// (whose single-use row lives in saml_request_state), an opaque RelayState and the console target.
type samlFlowState struct {
	Slug      string `json:"slug"`
	RequestID string `json:"requestId"`
	Relay     string `json:"relay"`
	Redirect  string `json:"redirect"`
	ExpiresAt int64  `json:"expiresAt"`
}

func samlFlowCookiePath(slug string) string { return "/auth/saml/" + slug }

func (h samlLoginHandler) ready() (*IdentityRuntime, bool) {
	if h.runtime == nil || h.runtime.saml.state == nil {
		return nil, false
	}
	return h.runtime, true
}

func (h samlLoginHandler) start(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.ready()
	if !ok {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "SAML login is not configured")
		return
	}
	if h.limited(r) {
		w.Header().Set("Retry-After", "60")
		writeHostedProblem(w, http.StatusTooManyRequests, "rate_limited", "too many sign-in attempts; try again shortly")
		return
	}
	provider, err := runtime.SAMLBrowser(r.Context(), r.PathValue("slug"))
	switch {
	case errors.Is(err, errProviderNotFound):
		writeHostedProblem(w, http.StatusNotFound, "not_found", "SAML login is not configured")
		return
	case err != nil:
		writeHostedProblem(w, http.StatusServiceUnavailable, "saml_unavailable", "SAML login is unavailable")
		return
	}
	now := runtime.saml.clock()
	stateID, err := runtime.saml.state.CreateSAMLRequestState(r.Context(), provider.provider.ID, now.Add(runtime.saml.config.RequestTTL))
	if err != nil {
		writeHostedProblem(w, http.StatusServiceUnavailable, "saml_unavailable", "SAML login is unavailable")
		return
	}
	relay, err := randomURLToken(18)
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "create SAML login state failed")
		return
	}
	request := saml.LoginRequest{
		IdPEntityID: provider.settings.EntityID, SSOURL: provider.settings.SSOURL, NameIDFormat: provider.settings.NameIDFormat,
		SP: provider.endpoints, RequestID: saml.RequestIDFor(stateID), RelayState: relay,
	}
	if provider.settings.SignRequests {
		keys, err := runtime.spKeys(provider.provider)
		key, ok := keys.Signing()
		if err != nil || !ok {
			writeHostedProblem(w, http.StatusServiceUnavailable, "saml_unavailable", "SAML login is unavailable")
			return
		}
		request.SignKey = &key
	}
	target, err := saml.BuildLoginURL(request)
	if err != nil {
		writeHostedProblem(w, http.StatusServiceUnavailable, "saml_unavailable", "SAML login is unavailable")
		return
	}
	flow := samlFlowState{
		Slug: provider.provider.Slug, RequestID: request.RequestID, Relay: relay,
		Redirect: safeConsoleRedirect(r.URL.Query().Get("redirect")), ExpiresAt: now.Add(runtime.saml.config.RequestTTL).Unix(),
	}
	encoded, err := signFlowPayload(flow, h.authenticator.AdminToken)
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "create SAML login state failed")
		return
	}
	// The IdP posts back cross-site, so the cookie must be SameSite=None, which browsers only accept as Secure.
	// The cookie path names the provider, so two providers never share or overwrite a flow cookie.
	http.SetCookie(w, &http.Cookie{
		Name: samlFlowCookieName, Value: encoded, Path: samlFlowCookiePath(flow.Slug), MaxAge: int(runtime.saml.config.RequestTTL.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

func (h samlLoginHandler) metadata(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.ready()
	if !ok {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "SAML login is not configured")
		return
	}
	provider, err := runtime.SAMLBrowser(r.Context(), r.PathValue("slug"))
	switch {
	case errors.Is(err, errProviderNotFound):
		writeHostedProblem(w, http.StatusNotFound, "not_found", "SAML login is not configured")
		return
	case err != nil:
		writeHostedProblem(w, http.StatusServiceUnavailable, "saml_unavailable", "SAML metadata is unavailable")
		return
	}
	keys, err := runtime.spKeys(provider.provider)
	if err != nil {
		writeHostedProblem(w, http.StatusServiceUnavailable, "saml_unavailable", "SAML metadata is unavailable")
		return
	}
	body, err := saml.SPMetadata(provider.endpoints, provider.settings.NameIDFormat, provider.settings.SignRequests, keys)
	if err != nil {
		writeHostedProblem(w, http.StatusServiceUnavailable, "saml_unavailable", "SAML metadata is unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(body)
}

// acs consumes the posted response. Every failure redirects to the console with a generic code and
// writes the precise reason to the audit log.
func (h samlLoginHandler) acs(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	runtime, ok := h.ready()
	if !ok {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "SAML login is not configured")
		return
	}
	if h.limited(r) {
		h.fail(w, r, slug, "rate_limited", "rate_limited")
		return
	}
	provider, err := runtime.SAMLBrowser(r.Context(), slug)
	switch {
	case errors.Is(err, errProviderNotFound):
		writeHostedProblem(w, http.StatusNotFound, "not_found", "SAML login is not configured")
		return
	case err != nil:
		h.clearFlowCookie(w, r, slug)
		h.fail(w, r, slug, "not_configured", "provider_unavailable")
		return
	}
	h.clearFlowCookie(w, r, slug)
	r.Body = http.MaxBytesReader(w, r.Body, runtime.saml.config.MaxResponseBytes*4+(64<<10))
	if err := r.ParseForm(); err != nil {
		h.fail(w, r, slug, "invalid_response", string(saml.ReasonTooLarge))
		return
	}
	encoded, relay := r.PostFormValue("SAMLResponse"), r.PostFormValue("RelayState")
	if encoded == "" {
		h.fail(w, r, slug, "invalid_response", "missing_response")
		return
	}
	flow, hasFlow := h.flow(r, slug, runtime.saml.clock())
	request := saml.Request{SAMLResponse: encoded}
	var target string
	if hasFlow {
		if relay != flow.Relay {
			h.fail(w, r, slug, "invalid_state", "relay_state_mismatch")
			return
		}
		stateID, ok := saml.StateIDFrom(flow.RequestID)
		if !ok {
			h.fail(w, r, slug, "invalid_state", "request_id_invalid")
			return
		}
		// Consume before verifying: a request state is single use even when verification then fails.
		consumed, err := runtime.saml.state.ConsumeSAMLRequestState(r.Context(), provider.provider.ID, stateID, runtime.saml.clock())
		if err != nil {
			h.fail(w, r, slug, "identity_unavailable", "state_store_unavailable")
			return
		}
		if !consumed {
			h.fail(w, r, slug, "invalid_state", "request_state_consumed_or_expired")
			return
		}
		request.InResponseTo, target = flow.RequestID, flow.Redirect
	} else {
		if !provider.settings.AllowIdPInitiated {
			h.fail(w, r, slug, "invalid_state", "idp_initiated_refused")
			return
		}
		if !slices.Contains(provider.settings.RelayStateAllowlist, relay) {
			h.fail(w, r, slug, "invalid_state", "relay_state_not_allowed")
			return
		}
		request.AllowUnsolicited, target = true, safeConsoleRedirect(relay)
	}
	verifier, err := provider.verifier(runtime.saml.config, runtime.saml.now)
	if err != nil {
		h.fail(w, r, slug, "not_configured", string(saml.ReasonConfiguration))
		return
	}
	claims, err := verifier.Verify(r.Context(), request)
	if err != nil {
		reason := string(saml.ReasonOf(err))
		if reason == "" {
			reason = "verification_failed"
		}
		h.fail(w, r, slug, "invalid_response", reason)
		return
	}
	fresh, err := runtime.saml.state.RecordSAMLAssertionID(r.Context(), provider.provider.ID, claims.AssertionID, claims.ReplayUntil)
	if err != nil {
		h.fail(w, r, slug, "identity_unavailable", "replay_store_unavailable")
		return
	}
	if !fresh {
		h.fail(w, r, slug, "invalid_response", "assertion_replayed")
		return
	}
	h.signIn(w, r, provider, claims, target)
}

func (h samlLoginHandler) flow(r *http.Request, slug string, now time.Time) (samlFlowState, bool) {
	cookie, err := r.Cookie(samlFlowCookieName)
	if err != nil {
		return samlFlowState{}, false
	}
	var flow samlFlowState
	if verifyFlowPayload(cookie.Value, h.authenticator.AdminToken, &flow) != nil || flow.Slug != slug || flow.RequestID == "" || flow.Relay == "" || now.Unix() >= flow.ExpiresAt {
		return samlFlowState{}, false
	}
	return flow, true
}

// samlProvision maps verified claims to the identity resolution request. The linking rules of ADR 0018
// section 3 are encoded here: a SAML assertion never carries a provider-verified email, so an asserted
// email (and an emailAddress NameID) never links an existing account; an unknown subject is created only
// when the provider provisions just in time; roles never come from the assertion in this release.
func samlProvision(provider identity.Provider, settings identity.SAMLSettings, claims saml.Claims, now time.Time) repository.OIDCIdentityProvision {
	return repository.OIDCIdentityProvision{
		Kind: repository.UserIdentitySAML, ProviderID: provider.ID, Issuer: settings.EntityID, Subject: claims.NameID,
		Email: claims.Email, DisplayName: claims.DisplayName, PreferredUsername: claims.Username,
		EmailVerified: false, MatchEmail: false,
		Provision: provider.ProvisioningMode == identity.ProvisioningJIT, DefaultRole: provider.JITDefaultRole, OccurredAt: now,
	}
}

func (h samlLoginHandler) signIn(w http.ResponseWriter, r *http.Request, provider samlProviderRuntime, claims saml.Claims, target string) {
	if h.identities == nil || h.sessions == nil {
		h.fail(w, r, provider.provider.Slug, "identity_unavailable", "store_unavailable")
		return
	}
	now := h.runtimeNow()
	user, _, _, err := h.identities.ResolveOIDCIdentity(r.Context(), samlProvision(provider.provider, provider.settings, claims, now))
	switch {
	case err == nil:
	case errors.Is(err, repository.ErrNotFound):
		h.fail(w, r, provider.provider.Slug, "account_not_linked", "subject_not_linked")
		return
	case errors.Is(err, repository.ErrIdentityAmbiguous):
		h.fail(w, r, provider.provider.Slug, "identity_ambiguous", "identity_ambiguous")
		return
	default:
		h.fail(w, r, provider.provider.Slug, "identity_unavailable", "identity_store_error")
		return
	}
	if user.State != repository.UserActive {
		h.fail(w, r, provider.provider.Slug, "account_disabled", "account_disabled")
		return
	}
	// A federated browser session is authenticated like an OIDC one; user_sessions.kind records saml.
	principal := h.authenticator.PrincipalForActor("user:" + user.Name)
	principal.Admin = user.Role == string(authorization.RoleAdmin)
	principal.Role = authorization.Role(user.Role)
	principal.AuthenticationKind = authorization.AuthenticationOIDC
	sessionID := newUserSessionID()
	if _, err := h.sessions.CreateUserSession(r.Context(), repository.UserSession{
		ID: sessionID, UserID: user.ID, Kind: repository.UserSessionSAML, CreatedAt: now, ExpiresAt: now.Add(authorization.UserSessionLifetime),
		IPAddress: requestIPAddress(r), UserAgent: boundedUserAgent(r.UserAgent()),
	}); err != nil {
		h.fail(w, r, provider.provider.Slug, "session_unavailable", "session_store_error")
		return
	}
	session := h.authenticator.IssueWebSessionWithID(principal, sessionID, user.SessionVersion)
	http.SetCookie(w, &http.Cookie{
		Name: webSessionCookieName, Value: session, Path: "/", MaxAge: int(webSessionLifetime.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	h.record(r, provider.provider.Slug, "user:"+user.Name, repository.AuditResolved, http.StatusFound, "login")
	values := url.Values{"oidc": {"success"}, "redirect": {safeConsoleRedirect(target)}}
	http.Redirect(w, r, "/login?"+values.Encode(), http.StatusFound)
}

func (h samlLoginHandler) runtimeNow() time.Time {
	if h.runtime != nil {
		return h.runtime.saml.clock()
	}
	return time.Now().UTC()
}

// samlInfrastructureReasons are the audit reasons caused by a failing dependency instead of a refused response.
var samlInfrastructureReasons = map[string]bool{
	"provider_unavailable": true, "state_store_unavailable": true, "replay_store_unavailable": true,
	"store_unavailable": true, "identity_store_error": true, "session_store_error": true,
}

// fail redirects to the console with a generic code and audits the precise reason.
func (h samlLoginHandler) fail(w http.ResponseWriter, r *http.Request, slug, code, reason string) {
	outcome := repository.AuditAccessDenied
	if samlInfrastructureReasons[reason] {
		// A store outage is not a denial: keep it out of the access-denied counts that alerting reads.
		outcome = repository.AuditStorageError
	}
	h.record(r, slug, "saml:"+slug, outcome, http.StatusFound, reason)
	values := url.Values{"oidc_error": {code}}
	http.Redirect(w, r, "/login?"+values.Encode(), http.StatusFound)
}

// record writes the decision to the audit log. The reason is a machine-readable code and never
// contains assertion content.
func (h samlLoginHandler) record(r *http.Request, slug, actor string, outcome repository.AuditOutcome, status int, reason string) {
	if h.audit == nil {
		return
	}
	_ = h.audit.RecordAudit(r.Context(), repository.AuditRecord{
		Class: repository.AuditClassSync, Actor: actor, Outcome: outcome, OccurredAt: h.runtimeNow(), Format: "management",
		Resource: "auth/saml/" + strings.ToLower(slug), Operation: "auth.saml.acs", Status: status, CacheDisposition: "bypass",
		AuthorizationSource: "saml", AuthorizationReason: reason,
	})
}

func (h samlLoginHandler) clearFlowCookie(w http.ResponseWriter, _ *http.Request, slug string) {
	http.SetCookie(w, &http.Cookie{
		Name: samlFlowCookieName, Value: "", Path: samlFlowCookiePath(slug), MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode,
	})
}
