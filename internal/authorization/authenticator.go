// Package authorization authenticates gateway principals and evaluates
// repository access policy without depending on an HTTP protocol.
package authorization

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

type Principal struct {
	Actor              string
	Admin              bool
	Role               Role
	RepositoryPatterns []string
	AuthenticationKind AuthenticationKind
	OIDCAdminSubject   bool
	OIDCRoleMappings   []OIDCRoleMappingMatch
	MustChangePassword bool
	UserID             string
	SessionID          string
	// GroupIDs are the user groups of the account, resolved on every authenticated request (ADR 0018 Decision 5),
	// at most repository.MaxUserGroupMemberships. They are never part of a principal token or a session, so a
	// removed membership withdraws authority on the next request.
	GroupIDs []string
	// CredentialID is a non-secret revocable service credential identity.
	CredentialID string
	// WorkloadClaims is the bounded audit summary of a workload_federation principal; nil otherwise.
	WorkloadClaims *WorkloadClaims
}

// AuthenticationKind is the bounded credential source retained after
// authentication. It is safe to expose in authorization diagnostics and never
// contains token material or provider claims.
type AuthenticationKind string

const (
	UserSessionLifetime                                       = 12 * time.Hour
	AuthenticationStaticAdmin              AuthenticationKind = "static_admin"
	AuthenticationStaticResolver           AuthenticationKind = "static_resolver"
	AuthenticationLocalSession             AuthenticationKind = "local_session"
	AuthenticationAPIKey                   AuthenticationKind = "api_key"
	AuthenticationServiceAccountCredential AuthenticationKind = "service_account_credential"
	AuthenticationOIDC                     AuthenticationKind = "oidc"
	// AuthenticationWorkloadFederation is declared with the workload authenticator (workload.go).
)

// OIDCRoleMappingMatch records only configured realm-role mappings that were
// present in a validated token. Unmapped provider roles are deliberately not
// retained.
type OIDCRoleMappingMatch struct {
	ExternalRole string `json:"externalRole"`
	GatewayRole  Role   `json:"gatewayRole"`
}

// OIDCValidatorSource supplies one validator per enabled identity provider
// that accepts API bearer tokens (ADR 0018).
type OIDCValidatorSource interface {
	// OIDCValidators returns the validators worth trying for token: the ones of
	// providers whose issuer matches the token's unverified iss claim, so a slow or
	// unreachable provider never delays the tokens of another.
	OIDCValidators(ctx context.Context, token string) ([]*OIDCValidator, error)
}

// UserGroupSource resolves the user groups of an account (ADR 0018 Decision 5). The production source is the
// read cache of internal/access, so a lookup costs no statement while the entry is fresh.
type UserGroupSource interface {
	ListUserGroupMemberships(ctx context.Context, userID string) ([]repository.UserGroupMembership, error)
}

type Authenticator struct {
	AdminToken    string
	ResolverToken string
	// ConanTokenSecret signs only Conan protocol tokens; it is never a login credential.
	ConanTokenSecret string
	AdminActor       string
	ResolverActor    string
	// LocalAuthMaxAttempts and LocalAuthLockout control temporary lockout for
	// local password authentication. Zero values use conservative defaults.
	LocalAuthMaxAttempts int
	LocalAuthLockout     time.Duration
	// RepositoryReaders maps an actor to exact repository names or prefix
	// patterns ending in /*. A nil map means the deployment configured no
	// reader policy at all, which denies every unmatched caller unless
	// LegacyReadPermissive restores the pre-0.4 posture.
	RepositoryReaders map[string][]string
	// LegacyReadPermissive makes a deployment that configured no reader
	// patterns admit an unmatched caller instead of denying it. Denying is the
	// default, so this is an explicit opt-out an operator sets to keep the
	// pre-0.4 legacy posture while it inventories the actors that read without
	// a grant; the zero value must never widen access.
	LegacyReadPermissive bool
	// RepositoryWriters is intentionally separate from readers: Maven deploy
	// must never turn a download grant into publication authority.
	RepositoryWriters map[string][]string
	// OIDC is a single fixed validator. Production wiring does not use it: cmd/notarx passes an
	// OIDCSource (the identity provider registry), whose validators carry their provider id. A validator
	// without a ProviderID fails closed while UserIdentities is set (see oidcPrincipal), so use OIDC only
	// without stored identities.
	OIDC *OIDCValidator
	// OIDCSource supplies the per-provider validators of the identity provider registry (ADR 0018).
	OIDCSource OIDCValidatorSource
	// Workload authenticates workload JWTs (ADR 0019). Nil disables workload federation
	// (NOTARX_WORKLOAD_ENABLED=false), so JWTs reach only the human OIDC path.
	Workload        *WorkloadAuthenticator
	APIKeys         repository.APIKeyStore
	ServiceAccounts repository.ServiceAccountStore
	Users           repository.UserStore
	UserIdentities  repository.UserIdentityStore
	UserSessions    repository.UserSessionStore
	// UserGroups supplies the groups of a named account. A nil source means no groups, so every principal
	// keeps exactly the authority of its own account.
	UserGroups UserGroupSource
	// ClaimsGroups applies the groups an OIDC bearer token asserts to a linked account (throttled); nil leaves the
	// claims-sourced memberships to browser sign-ins.
	ClaimsGroups *ClaimsGroupRefresher
}

// Authenticate reads credentials from the database, never from the read caches
// (ADR 0023 section 6): a caller with no request context keeps the revocation
// guarantee of an uncached lookup. Protocol read paths opt in to the caches with
// AuthenticateContext and the request context.
func (a Authenticator) Authenticate(header string) (Principal, bool) {
	return a.AuthenticateContext(readcache.Bypass(context.Background()), header)
}

// AuthenticateContext includes credential, account and OIDC lookups in the
// caller's cancellation budget. Existing protocol callers keep Authenticate.
func (a Authenticator) AuthenticateContext(ctx context.Context, header string) (Principal, bool) {
	principal, ok := a.authenticateCredential(ctx, header)
	if !ok {
		return Principal{}, false
	}
	return a.WithUserGroups(ctx, principal), true
}

// WithUserGroups adds the groups of a named account to its principal and lifts its level to the highest global
// role of those groups (ADR 0018 Decision 5). The level is evaluated here on every request and never copied into
// a credential, so removing a membership or changing a group's role takes effect on the next request. The
// account was already checked active, a principal that must change its password stays blocked, and a failed
// lookup leaves the account with its own authority only: a group can only ever add access, so failing to read
// one never widens it.
func (a Authenticator) WithUserGroups(ctx context.Context, principal Principal) Principal {
	if a.UserGroups == nil || principal.UserID == "" || principal.MustChangePassword {
		return principal
	}
	memberships, err := a.UserGroups.ListUserGroupMemberships(ctx, principal.UserID)
	if err != nil || len(memberships) == 0 {
		return principal
	}
	if len(memberships) > repository.MaxUserGroupMemberships {
		memberships = memberships[:repository.MaxUserGroupMemberships]
	}
	level := principal.Role
	principal.GroupIDs = make([]string, 0, len(memberships))
	for _, membership := range memberships {
		principal.GroupIDs = append(principal.GroupIDs, membership.GroupID)
		if candidate := Role(membership.EffectiveGroupRole()); roleRank(candidate) > roleRank(level) {
			level = candidate
		}
	}
	principal.Role = level
	principal.Admin = level == RoleAdmin
	return principal
}

func (a Authenticator) authenticateCredential(ctx context.Context, header string) (Principal, bool) {
	const bearer = "Bearer "
	if !strings.HasPrefix(header, bearer) {
		return Principal{}, false
	}
	token := strings.TrimPrefix(header, bearer)
	if tokenMatches(token, a.AdminToken) {
		return Principal{Actor: a.AdminActor, Admin: true, Role: RoleAdmin, AuthenticationKind: AuthenticationStaticAdmin}, true
	}
	if tokenMatches(token, a.ResolverToken) {
		principal := a.PrincipalForActor(a.ResolverActor)
		principal.AuthenticationKind = AuthenticationStaticResolver
		return principal, true
	}
	if principal, ok := a.principalToken(ctx, token); ok {
		return principal, true
	}
	if principal, ok := a.webSessionPrincipal(ctx, token); ok {
		return principal, true
	}
	if actor, ok := a.tokenActor(token); ok {
		principal, active := a.principalForTokenActor(ctx, actor)
		return principal, active
	}
	if principal, ok := a.apiKeyPrincipal(ctx, token); ok {
		return principal, true
	}
	if userID, sessionID, sessionVersion, ok := a.userSessionClaims(token); ok && a.Users != nil {
		user, err := a.Users.GetUser(ctx, userID)
		versionMatches := sessionVersion == user.SessionVersion || sessionVersion == 0 && user.SessionVersion == 1
		if err == nil && user.State == repository.UserActive && versionMatches && a.userSessionIsActive(ctx, userID, sessionID) {
			role := Role(user.Role)
			if user.MustChangePassword {
				return Principal{Actor: "user:" + user.Name, AuthenticationKind: AuthenticationLocalSession, MustChangePassword: true, UserID: user.ID, SessionID: sessionID}, true
			}
			return Principal{Actor: "user:" + user.Name, Admin: role == RoleAdmin, Role: role, AuthenticationKind: AuthenticationLocalSession, UserID: user.ID, SessionID: sessionID}, true
		}
	}
	if principal, ok := a.Workload.Authenticate(ctx, token, ""); ok {
		return principal, true
	}
	validators := []*OIDCValidator{a.OIDC}
	if a.OIDCSource != nil {
		if resolved, err := a.OIDCSource.OIDCValidators(ctx, token); err == nil {
			validators = resolved
		}
	}
	for _, validator := range validators {
		if validator == nil {
			continue
		}
		if identity, ok := validator.Validate(ctx, token); ok {
			return a.oidcPrincipal(ctx, identity)
		}
	}
	return Principal{}, false
}

// oidcPrincipal maps a validated API bearer token onto a principal. The identity
// key is (provider id, subject); a token of a provider with no linked account
// keeps the external-principal behavior.
func (a Authenticator) oidcPrincipal(ctx context.Context, identity OIDCIdentity) (Principal, bool) {
	if a.UserIdentities != nil && identity.ProviderID == "" {
		// Identities are keyed by provider (ADR 0018). A validator that belongs to no
		// provider cannot tell whether the subject is a linked, possibly disabled
		// account, so it fails closed instead of trusting the token's role claims.
		return Principal{}, false
	}
	if a.UserIdentities != nil {
		user, _, err := a.UserIdentities.GetUserByProviderIdentity(ctx, identity.ProviderID, identity.Subject)
		if err == nil {
			if user.State != repository.UserActive {
				return Principal{}, false
			}
			role := Role(user.Role)
			if user.MustChangePassword {
				return Principal{
					Actor: "user:" + user.Name, RepositoryPatterns: a.RepositoryReaders["user:"+user.Name],
					AuthenticationKind: AuthenticationOIDC, MustChangePassword: true, UserID: user.ID,
				}, true
			}
			a.ClaimsGroups.Refresh(ctx, identity, user.ID)
			return Principal{
				Actor: "user:" + user.Name, Admin: role == RoleAdmin, Role: role,
				RepositoryPatterns: a.RepositoryReaders["user:"+user.Name], AuthenticationKind: AuthenticationOIDC,
				OIDCAdminSubject: identity.AdminSubject, OIDCRoleMappings: identity.RoleMappings, UserID: user.ID,
			}, true
		}
		if !errors.Is(err, repository.ErrNotFound) {
			return Principal{}, false
		}
	}
	principal := a.PrincipalForActor(identity.Subject)
	principal.Admin = identity.Admin
	principal.Role = identity.Role
	principal.AuthenticationKind = AuthenticationOIDC
	principal.OIDCAdminSubject = identity.AdminSubject
	principal.OIDCRoleMappings = identity.RoleMappings
	return principal, true
}

func (a Authenticator) principalForTokenActor(ctx context.Context, actor string) (Principal, bool) {
	if !strings.HasPrefix(actor, "user:") || a.Users == nil {
		return a.PrincipalForActor(actor), true
	}
	name := strings.TrimPrefix(actor, "user:")
	user, err := a.Users.GetUserByName(ctx, name)
	if err != nil || user.State != repository.UserActive {
		return Principal{}, false
	}
	role := Role(user.Role)
	if user.MustChangePassword {
		return Principal{Actor: actor, RepositoryPatterns: a.RepositoryReaders[actor], AuthenticationKind: AuthenticationLocalSession, MustChangePassword: true, UserID: user.ID}, true
	}
	return Principal{Actor: actor, Admin: role == RoleAdmin, Role: role, RepositoryPatterns: a.RepositoryReaders[actor], AuthenticationKind: AuthenticationLocalSession, UserID: user.ID}, true
}

// AuthenticateBasic validates the resolver credential used by Maven, Conan,
// and native protocol clients, then returns the credential's actor principal.
func (a Authenticator) AuthenticateBasic(username, password string) (Principal, bool) {
	return a.AuthenticateBasicContext(readcache.Bypass(context.Background()), username, password)
}

// AuthenticateBasicContext is AuthenticateBasic that serves a service-account
// credential from the read caches unless ctx asks to read through (Bypass).
func (a Authenticator) AuthenticateBasicContext(ctx context.Context, username, password string) (Principal, bool) {
	if username == "" {
		return Principal{}, false
	}
	if a.ResolverPasswordMatches(password) {
		principal := a.PrincipalForActor(username)
		principal.AuthenticationKind = AuthenticationStaticResolver
		return principal, true
	}
	if principal, ok := a.apiKeyPrincipal(ctx, password); ok {
		if principal.AuthenticationKind != AuthenticationServiceAccountCredential {
			return Principal{}, false
		}
		return principal, true
	}
	return a.Workload.AuthenticateBasic(ctx, username, password)
}

func (a Authenticator) apiKeyPrincipal(ctx context.Context, token string) (Principal, bool) {
	if a.APIKeys == nil || token == "" {
		return Principal{}, false
	}
	key, err := a.APIKeys.FindActiveAPIKeyByHash(ctx, HashAPIKey(token))
	if err != nil {
		return Principal{}, false
	}
	if key.ServiceAccountID != "" {
		if a.ServiceAccounts == nil {
			return Principal{}, false
		}
		account, accountErr := a.ServiceAccounts.GetServiceAccount(ctx, key.ServiceAccountID)
		if accountErr != nil || account.State != repository.ServiceAccountActive {
			return Principal{}, false
		}
		return Principal{
			Actor:              "service-account:" + account.ID,
			CredentialID:       key.ID,
			AuthenticationKind: AuthenticationServiceAccountCredential,
		}, true
	}
	role := RoleFromRoles(key.Roles)
	return Principal{Actor: "api-key:" + key.ID, Admin: role == RoleAdmin, Role: role, AuthenticationKind: AuthenticationAPIKey}, true
}

// ResolverPasswordMatches validates a resolver secret without exposing the
// secret or the constant-time comparison implementation to protocol packages.
func (a Authenticator) ResolverPasswordMatches(password string) bool {
	return tokenMatches(password, a.ResolverToken)
}

func (a Authenticator) PrincipalForActor(actor string) Principal {
	return Principal{Actor: actor, RepositoryPatterns: a.RepositoryReaders[actor], AuthenticationKind: AuthenticationStaticResolver}
}

// AccountStateReason names the principal-wide condition that blocks every
// repository path before any repository authority is considered, so the managed
// authorizer, the legacy static-policy paths, and the management API all agree.
// A pending account has no approval yet; an account whose password must change
// cannot exercise repository permissions until it does. Changing the password is
// authenticated separately, so it stays reachable. Empty means no block.
func (p Principal) AccountStateReason() string {
	if p.Role == RoleNone {
		return "authorization_pending"
	}
	if p.MustChangePassword {
		return "password_change_required"
	}
	return ""
}

func (p Principal) RepositoryAccessBlocked() bool {
	return p.AccountStateReason() != ""
}

// CanReadRepository reports whether the principal may read the named legacy
// repository. An administrator, a global read role, or a matching reader
// pattern qualifies. A deployment that configured no reader patterns at all
// denies an otherwise-unmatched caller, and NOTARX_LEGACY_READ_DEFAULT=allow
// restores the pre-0.4 posture that admitted it. A pending or password-change
// account is never admitted, whatever the posture.
func (a Authenticator) CanReadRepository(principal Principal, repositoryName string) bool {
	if principal.CanReadRepository(repositoryName) {
		return true
	}
	return !principal.RepositoryAccessBlocked() && a.RepositoryReaders == nil && a.LegacyReadPermissive
}

// CanReadRepository reports whether the principal's own authority - account
// state, administrator identity, global read role, or reader pattern - allows
// reading the named legacy repository. It knows nothing about deployment
// configuration; callers that need the documented fallback use the
// Authenticator method above.
func (p Principal) CanReadRepository(repositoryName string) bool {
	if p.RepositoryAccessBlocked() {
		return false
	}
	if p.Admin || RoleAllows(p.Role, RepositoryRead) {
		return true
	}
	for _, pattern := range p.RepositoryPatterns {
		if pattern == repositoryName || strings.HasSuffix(pattern, "/*") && strings.HasPrefix(repositoryName, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}

// CanReadMavenRepository treats a Maven Group as its repository boundary.
// `group/*` is accepted for compatibility with path-shaped grant policies,
// without broadening OCI's wildcard semantics.
func (a Authenticator) CanReadMavenRepository(principal Principal, groupName string) bool {
	if principal.RepositoryAccessBlocked() {
		return false
	}
	if a.CanReadRepository(principal, groupName) {
		return true
	}
	for _, pattern := range principal.RepositoryPatterns {
		if strings.TrimSuffix(pattern, "/*") == groupName && strings.HasSuffix(pattern, "/*") {
			return true
		}
	}
	return false
}

func (a Authenticator) CanWriteMavenRepository(principal Principal, repositoryName string) bool {
	if principal.RepositoryAccessBlocked() {
		return false
	}
	if principal.Admin || RoleAllows(principal.Role, RepositoryWrite) {
		return true
	}
	for _, pattern := range a.RepositoryWriters[principal.Actor] {
		if pattern == repositoryName || strings.HasSuffix(pattern, "/*") && strings.HasPrefix(repositoryName, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}

func (a Authenticator) IssueToken(actor string) string {
	expiresAt := time.Now().UTC().Add(5 * time.Minute).Unix()
	payload := "v1." + base64.RawURLEncoding.EncodeToString([]byte(actor)) + "." + strconv.FormatInt(expiresAt, 10)
	mac := hmac.New(sha256.New, []byte(a.ResolverToken))
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// anonymousTokenLifetime bounds an anonymous pull token (ADR 0029, "OCI
// anonymous pulls").
const anonymousTokenLifetime = 5 * time.Minute

// IssueAnonymousToken mints the bearer token the OCI token endpoint hands an
// unauthenticated client that asks only to pull. It names no principal and
// carries no authority: a request bearing it is served exactly as an anonymous
// request (see IsAnonymousToken), so it can never read what an anonymous caller
// cannot. It exists because Docker, containerd, Podman and ORAS fetch a token
// after the registry challenge even for public repositories.
func (a Authenticator) IssueAnonymousToken() string {
	if a.ResolverToken == "" {
		return ""
	}
	payload := "an1." + strconv.FormatInt(time.Now().UTC().Add(anonymousTokenLifetime).Unix(), 10)
	mac := hmac.New(sha256.New, []byte(a.ResolverToken))
	_, _ = mac.Write([]byte("anonymous-pull:" + payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// IsAnonymousToken reports whether an Authorization header carries a valid,
// unexpired anonymous pull token.
func (a Authenticator) IsAnonymousToken(header string) bool {
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || a.ResolverToken == "" {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "an1" {
		return false
	}
	expiresAt, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().UTC().Unix() >= expiresAt {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(a.ResolverToken))
	_, _ = mac.Write([]byte("anonymous-pull:" + strings.Join(parts[:2], ".")))
	return hmac.Equal(signature, mac.Sum(nil))
}

// IssuePrincipalToken mints a short-lived protocol token that preserves the
// authenticated principal's role. Legacy actor-only tokens remain supported
// through IssueToken for existing clients and tests.
func (a Authenticator) IssuePrincipalToken(principal Principal) string {
	return a.IssuePrincipalTokenFor(principal, 5*time.Minute)
}

// IssuePrincipalTokenFor is IssuePrincipalToken with an explicit lifetime. A workload_federation
// principal carries its policy id, which revalidation checks on every use.
func (a Authenticator) IssuePrincipalTokenFor(principal Principal, lifetime time.Duration) string {
	expiresAt := time.Now().UTC().Add(lifetime).Unix()
	var policyID string
	if principal.AuthenticationKind == AuthenticationWorkloadFederation {
		policyID = principal.CredentialID
	}
	claims, _ := json.Marshal(struct {
		Actor            string                 `json:"a"`
		Role             Role                   `json:"r,omitempty"`
		Admin            bool                   `json:"d,omitempty"`
		Authentication   AuthenticationKind     `json:"k,omitempty"`
		OIDCAdminSubject bool                   `json:"s,omitempty"`
		OIDCRoleMappings []OIDCRoleMappingMatch `json:"m,omitempty"`
		PolicyID         string                 `json:"p,omitempty"`
	}{
		Actor: principal.Actor, Role: principal.Role, Admin: principal.Admin,
		Authentication: principal.AuthenticationKind, OIDCAdminSubject: principal.OIDCAdminSubject,
		OIDCRoleMappings: principal.OIDCRoleMappings, PolicyID: policyID,
	})
	payload := "v2." + base64.RawURLEncoding.EncodeToString(claims) + "." + strconv.FormatInt(expiresAt, 10)
	mac := hmac.New(sha256.New, []byte(a.ResolverToken))
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// IssueUserSession mints a stateless session token for a local user. The token
// carries only the user id and an expiry; the user's current role and active
// state are rechecked on every authenticated request, so role changes and
// disabling take effect without a session store.
func (a Authenticator) IssueUserSession(userID string, sessionVersions ...int64) string {
	return a.issueUserSession(userID, "", sessionVersions...)
}

// IssueUserSessionWithID mints a token linked to a persisted session record.
func (a Authenticator) IssueUserSessionWithID(userID, sessionID string, sessionVersions ...int64) string {
	return a.issueUserSession(userID, sessionID, sessionVersions...)
}

func (a Authenticator) issueUserSession(userID, sessionID string, sessionVersions ...int64) string {
	expiresAt := time.Now().UTC().Add(UserSessionLifetime).Unix()
	identity := []byte(userID)
	if len(sessionVersions) > 0 && sessionVersions[0] > 0 {
		identity, _ = json.Marshal(struct {
			ID      string `json:"i"`
			Version int64  `json:"v"`
			Session string `json:"s,omitempty"`
		}{ID: userID, Version: sessionVersions[0], Session: sessionID})
	} else if sessionID != "" {
		identity, _ = json.Marshal(struct {
			ID      string `json:"i"`
			Session string `json:"s"`
		}{ID: userID, Session: sessionID})
	}
	payload := "us." + base64.RawURLEncoding.EncodeToString(identity) + "." + strconv.FormatInt(expiresAt, 10)
	mac := hmac.New(sha256.New, []byte(a.AdminToken))
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// IssueWebSession mints the HttpOnly browser session used after a successful
// OIDC Authorization Code exchange. It contains the already validated bounded
// identity, never provider access or refresh tokens.

// IssueWebSessionWithID mints a browser session linked to a persisted record.
func (a Authenticator) IssueWebSessionWithID(principal Principal, sessionID string, sessionVersions ...int64) string {
	return a.issueWebSession(principal, sessionID, sessionVersions...)
}

func (a Authenticator) IssueWebSession(principal Principal, sessionVersions ...int64) string {
	return a.issueWebSession(principal, "", sessionVersions...)
}

func (a Authenticator) issueWebSession(principal Principal, sessionID string, sessionVersions ...int64) string {
	expiresAt := time.Now().UTC().Add(UserSessionLifetime).Unix()
	var sessionVersion int64
	if len(sessionVersions) > 0 {
		sessionVersion = sessionVersions[0]
	}
	claims, _ := json.Marshal(struct {
		Actor            string                 `json:"a"`
		Role             Role                   `json:"r,omitempty"`
		Admin            bool                   `json:"d,omitempty"`
		Authentication   AuthenticationKind     `json:"k"`
		OIDCAdminSubject bool                   `json:"s,omitempty"`
		OIDCRoleMappings []OIDCRoleMappingMatch `json:"m,omitempty"`
		SessionVersion   int64                  `json:"v,omitempty"`
		SessionID        string                 `json:"i,omitempty"`
	}{
		Actor: principal.Actor, Role: principal.Role, Admin: principal.Admin,
		Authentication: principal.AuthenticationKind, OIDCAdminSubject: principal.OIDCAdminSubject,
		OIDCRoleMappings: principal.OIDCRoleMappings, SessionVersion: sessionVersion, SessionID: sessionID,
	})
	payload := "ws." + base64.RawURLEncoding.EncodeToString(claims) + "." + strconv.FormatInt(expiresAt, 10)
	mac := hmac.New(sha256.New, []byte(a.AdminToken))
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a Authenticator) webSessionPrincipal(ctx context.Context, token string) (Principal, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "ws" || a.AdminToken == "" {
		return Principal{}, false
	}
	expiresAt, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().UTC().Unix() >= expiresAt {
		return Principal{}, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return Principal{}, false
	}
	mac := hmac.New(sha256.New, []byte(a.AdminToken))
	_, _ = mac.Write([]byte(strings.Join(parts[:3], ".")))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return Principal{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Principal{}, false
	}
	var claims struct {
		Actor            string                 `json:"a"`
		Role             Role                   `json:"r"`
		Admin            bool                   `json:"d"`
		Authentication   AuthenticationKind     `json:"k"`
		OIDCAdminSubject bool                   `json:"s"`
		OIDCRoleMappings []OIDCRoleMappingMatch `json:"m"`
		SessionVersion   int64                  `json:"v"`
		SessionID        string                 `json:"i"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Actor == "" || claims.Authentication != AuthenticationOIDC || claims.Role != "" && !validGatewayRole(claims.Role) || !validOIDCMetadata(claims.Authentication, claims.OIDCAdminSubject, claims.OIDCRoleMappings) {
		return Principal{}, false
	}
	if strings.HasPrefix(claims.Actor, "user:") {
		if a.Users == nil {
			return Principal{}, false
		}
		user, err := a.Users.GetUserByName(ctx, strings.TrimPrefix(claims.Actor, "user:"))
		versionMatches := claims.SessionVersion == user.SessionVersion || claims.SessionVersion == 0 && user.SessionVersion == 1
		if err != nil || user.State != repository.UserActive || !versionMatches || !a.userSessionIsActive(ctx, user.ID, claims.SessionID) {
			return Principal{}, false
		}
		role := Role(user.Role)
		if user.MustChangePassword {
			return Principal{
				Actor: claims.Actor, RepositoryPatterns: a.RepositoryReaders[claims.Actor],
				AuthenticationKind: AuthenticationOIDC, MustChangePassword: true, UserID: user.ID, SessionID: claims.SessionID,
			}, true
		}
		return Principal{
			Actor: claims.Actor, Admin: role == RoleAdmin, Role: role,
			RepositoryPatterns: a.RepositoryReaders[claims.Actor], AuthenticationKind: AuthenticationOIDC,
			OIDCAdminSubject: claims.OIDCAdminSubject, OIDCRoleMappings: claims.OIDCRoleMappings,
			UserID: user.ID, SessionID: claims.SessionID,
		}, true
	}
	return Principal{
		Actor:              claims.Actor,
		Admin:              claims.Admin,
		Role:               claims.Role,
		RepositoryPatterns: a.RepositoryReaders[claims.Actor],
		AuthenticationKind: claims.Authentication,
		OIDCAdminSubject:   claims.OIDCAdminSubject,
		OIDCRoleMappings:   claims.OIDCRoleMappings,
	}, true
}

func (a Authenticator) userSessionClaims(token string) (string, string, int64, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "us" || a.AdminToken == "" {
		return "", "", 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(raw) == 0 {
		return "", "", 0, false
	}
	expiresAt, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().UTC().Unix() >= expiresAt {
		return "", "", 0, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return "", "", 0, false
	}
	mac := hmac.New(sha256.New, []byte(a.AdminToken))
	_, _ = mac.Write([]byte(strings.Join(parts[:3], ".")))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return "", "", 0, false
	}
	var claims struct {
		ID      string `json:"i"`
		Version int64  `json:"v"`
		Session string `json:"s"`
	}
	if raw[0] == '{' {
		if json.Unmarshal(raw, &claims) != nil || claims.ID == "" || claims.Version < 0 || claims.Version == 0 && claims.Session == "" {
			return "", "", 0, false
		}
		return claims.ID, claims.Session, claims.Version, true
	}
	return string(raw), "", 0, true
}

func (a Authenticator) userSessionIsActive(ctx context.Context, userID, sessionID string) bool {
	if sessionID == "" || a.UserSessions == nil {
		return true
	}
	session, err := a.UserSessions.GetUserSession(ctx, userID, sessionID)
	return err == nil && session.RevokedAt == nil && session.ExpiresAt.After(time.Now().UTC())
}

func (a Authenticator) tokenActor(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "v1" || a.ResolverToken == "" {
		return "", false
	}
	actor, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(actor) == 0 {
		return "", false
	}
	expiresAt, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().UTC().Unix() >= expiresAt {
		return "", false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, []byte(a.ResolverToken))
	_, _ = mac.Write([]byte(strings.Join(parts[:3], ".")))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return "", false
	}
	return string(actor), true
}

func (a Authenticator) principalToken(ctx context.Context, token string) (Principal, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "v2" || a.ResolverToken == "" {
		return Principal{}, false
	}
	expiresAt, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().UTC().Unix() >= expiresAt {
		return Principal{}, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return Principal{}, false
	}
	mac := hmac.New(sha256.New, []byte(a.ResolverToken))
	_, _ = mac.Write([]byte(strings.Join(parts[:3], ".")))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return Principal{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Principal{}, false
	}
	var claims struct {
		Actor            string                 `json:"a"`
		Role             Role                   `json:"r"`
		Admin            bool                   `json:"d"`
		Authentication   AuthenticationKind     `json:"k"`
		OIDCAdminSubject bool                   `json:"s"`
		OIDCRoleMappings []OIDCRoleMappingMatch `json:"m"`
		PolicyID         string                 `json:"p"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Actor == "" {
		return Principal{}, false
	}
	if strings.HasPrefix(claims.Actor, "user:") {
		return a.principalForTokenActor(ctx, claims.Actor)
	}
	if claims.Role != "" && !validGatewayRole(claims.Role) {
		return Principal{}, false
	}
	if claims.Authentication == "" {
		claims.Authentication = AuthenticationStaticResolver
	}
	if !validAuthenticationKind(claims.Authentication) || !validOIDCMetadata(claims.Authentication, claims.OIDCAdminSubject, claims.OIDCRoleMappings) {
		return Principal{}, false
	}
	if claims.Authentication == AuthenticationServiceAccountCredential {
		const prefix = "service-account:"
		if a.ServiceAccounts == nil || !strings.HasPrefix(claims.Actor, prefix) {
			return Principal{}, false
		}
		accountID := strings.TrimPrefix(claims.Actor, prefix)
		account, err := a.ServiceAccounts.GetServiceAccount(ctx, accountID)
		if err != nil || account.State != repository.ServiceAccountActive {
			return Principal{}, false
		}
	}
	var credentialID string
	if claims.Authentication == AuthenticationWorkloadFederation {
		// A workload token is revalidated on every use: disabling its policy, issuer or service account ends it
		// within the revocation window, not at token expiry. A deployment that turned workload federation off
		// refuses the tokens it issued earlier.
		if claims.Admin || claims.Role != "" || claims.PolicyID == "" || !a.Workload.Revalidate(ctx, claims.PolicyID, claims.Actor) {
			return Principal{}, false
		}
		credentialID = claims.PolicyID
	}
	return Principal{
		Actor:              claims.Actor,
		Admin:              claims.Admin,
		Role:               claims.Role,
		RepositoryPatterns: a.RepositoryReaders[claims.Actor],
		AuthenticationKind: claims.Authentication,
		OIDCAdminSubject:   claims.OIDCAdminSubject,
		OIDCRoleMappings:   claims.OIDCRoleMappings,
		CredentialID:       credentialID,
	}, true
}

// validGatewayRole reports whether a credential or session role claim names a
// role the Gateway recognises. Every accept-list must route through here, or a
// newly added role silently fails to authenticate the sessions that carry it.
// The removed legacy values "reader" and "writer" are not recognised, so a
// credential or session that carries one is refused rather than downgraded.
func validGatewayRole(role Role) bool {
	switch role {
	case RoleNone, RoleMember, RoleAdmin:
		return true
	}
	return false
}

func validAuthenticationKind(kind AuthenticationKind) bool {
	switch kind {
	case "", AuthenticationStaticAdmin, AuthenticationStaticResolver, AuthenticationLocalSession, AuthenticationAPIKey, AuthenticationServiceAccountCredential, AuthenticationOIDC, AuthenticationWorkloadFederation:
		return true
	default:
		return false
	}
}

func validOIDCMetadata(kind AuthenticationKind, adminSubject bool, mappings []OIDCRoleMappingMatch) bool {
	if kind != AuthenticationOIDC && (adminSubject || len(mappings) != 0) {
		return false
	}
	for _, mapping := range mappings {
		// Only the two assignable levels can be a mapping target. A mapping to
		// a removed legacy role is refused, so it can never be replayed into a
		// global capability it no longer has.
		if mapping.ExternalRole == "" || mapping.GatewayRole != RoleMember && mapping.GatewayRole != RoleAdmin {
			return false
		}
	}
	return true
}

func tokenMatches(value, expected string) bool {
	return expected != "" && len(value) == len(expected) && subtle.ConstantTimeCompare([]byte(value), []byte(expected)) == 1
}

// HashAPIKey returns the database verifier for a generated high-entropy key.
// The plaintext key is never persisted.
func HashAPIKey(token string) string {
	digest := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func containsRole(roles []string, expected string) bool {
	for _, role := range roles {
		if role == expected {
			return true
		}
	}
	return false
}
