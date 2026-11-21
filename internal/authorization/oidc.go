package authorization

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/repository"
)

const oidcJWKSCacheTTL = 5 * time.Minute

// OIDCRoleMapping maps external realm roles onto the Gateway's account levels.
// Only the two levels an account can hold are targets: Member approves the
// account without granting repository capability, and Admin keeps the existing
// management surface. There is no mapping to editor or reader authority because
// repository reach comes from per-repository grants, never from a mapped role.
type OIDCRoleMapping struct {
	Member []string
	Admin  []string
}

// OIDCConfig supplies the immutable issuer trust boundary. Only RS256 is
// accepted so the configured JWKS cannot negotiate an unexpected algorithm.
// Verification itself is delegated to internal/jose (ADR 0019).
type OIDCConfig struct {
	// ProviderID identifies the identity provider (ADR 0018) the validator was built for.
	ProviderID string
	// ProviderSlug is the slug of the provider, for audit events.
	ProviderSlug  string
	Issuer        string
	Audience      string
	JWKSURL       string
	AdminSubjects []string
	// Algorithms is the per-provider allow-list; empty means RS256.
	Algorithms []string
	// SubjectClaims are the claims whose values, joined with a colon, form the
	// login subject (Entra ID: tid and oid). Empty means the sub claim.
	SubjectClaims []string
	// GroupsClaim names the claim that carries the user's groups; empty means "groups" (ADR 0018 Decision 2).
	GroupsClaim string
	// ExternalIDClaim names the claim that carries the IdP's externalId of the user (attribute_mapping.externalId);
	// empty means the token carries none. A scim_only provider binds a SCIM-created account by it.
	ExternalIDClaim string
	// Realm-role mappings are opt-in. When a token contains several mapped
	// roles, the validator grants the most privileged one.
	Roles OIDCRoleMapping
}

type OIDCIdentity struct {
	ProviderID string
	// ProviderSlug is the slug of the provider, for audit events.
	ProviderSlug  string
	Issuer        string
	Subject       string
	Email         string
	DisplayName   string
	Username      string
	EmailVerified bool
	Admin         bool
	Role          Role
	AdminSubject  bool
	RoleMappings  []OIDCRoleMappingMatch
	// Groups are the group values the token asserts, at most repository.MaxClaimsGroups. They only ever
	// match claims-sourced groups of this provider by external id (ADR 0018 Decision 5).
	Groups []string
	// ExternalID is the value of the configured externalId claim, empty when none is configured or the token lacks it.
	ExternalID string
	// GroupsPresent reports that the token carries the groups claim (or its overage markers). A sign-in ID token is
	// authoritative for the groups of its provider, absent claim included; an API bearer token that does not carry
	// the claim says nothing about groups and leaves the memberships as they are.
	GroupsPresent bool
	// GroupsOverage reports that the token could not carry the full group list (Entra ID's _claim_names and
	// hasgroups, or more values than the bound). Groups is then empty: never a partial list.
	GroupsOverage bool
}

type OIDCValidator struct {
	config   OIDCConfig
	verifier jose.TokenVerifier
}

func NewOIDCValidator(config OIDCConfig) *OIDCValidator {
	if config.Issuer == "" {
		return nil
	}
	admins := make([]string, 0, len(config.AdminSubjects))
	for _, subject := range config.AdminSubjects {
		if subject = strings.TrimSpace(subject); subject != "" {
			admins = append(admins, subject)
		}
	}
	config.Roles.Member = normalizedOIDCRoles(config.Roles.Member)
	config.Roles.Admin = normalizedOIDCRoles(config.Roles.Admin)
	config.AdminSubjects = admins
	// The human IdP is configured by an administrator (ADR 0018 owns its registry), so it
	// keeps the plain client of the original validator: http issuers used by development
	// setups stay reachable. Workload issuers use the SSRF-guarded default of internal/jose.
	options := jose.DefaultOptions()
	options.MaxTokenAge = 0 // an administrator-configured IdP keeps its own token lifetimes
	options.JWKSTTL = oidcJWKSCacheTTL
	options.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	verifier, err := jose.NewVerifier(options)
	if err != nil {
		panic("authorization: invalid OIDC verifier options: " + err.Error()) // the options are constants
	}
	return &OIDCValidator{config: config, verifier: verifier}
}

func (v *OIDCValidator) Validate(ctx context.Context, token string) (OIDCIdentity, bool) {
	return v.validate(ctx, token, "")
}

// ValidateNonce validates a browser OIDC ID token and binds it to the nonce
// issued for the corresponding Authorization Code flow.
func (v *OIDCValidator) ValidateNonce(ctx context.Context, token, expectedNonce string) (OIDCIdentity, bool) {
	if expectedNonce == "" {
		return OIDCIdentity{}, false
	}
	return v.validate(ctx, token, expectedNonce)
}

func (v *OIDCValidator) validate(ctx context.Context, token, expectedNonce string) (OIDCIdentity, bool) {
	verified, err := v.verifier.Verify(ctx, token, jose.Issuer{
		Issuer:              v.config.Issuer,
		Audiences:           []string{v.config.Audience},
		Algorithms:          v.algorithms(),
		Mode:                jose.ModeDiscovery,
		JWKSURI:             v.config.JWKSURL,
		AllowPrivateNetwork: true,
	})
	if err != nil {
		return OIDCIdentity{}, false
	}
	var claims struct {
		Issuer        string   `json:"iss"`
		Subject       string   `json:"sub"`
		Email         string   `json:"email"`
		Name          string   `json:"name"`
		Username      string   `json:"preferred_username"`
		EmailVerified bool     `json:"email_verified"`
		Nonce         string   `json:"nonce"`
		Roles         []string `json:"roles"`
		RealmAccess   struct {
			Roles []string `json:"roles"`
		} `json:"realm_access"`
		ResourceAccess map[string]struct {
			Roles []string `json:"roles"`
		} `json:"resource_access"`
	}
	// The verified claim map is re-encoded so the typed view below reads the same
	// fields as before; a claim of an unexpected shape fails closed.
	encoded, err := json.Marshal(verified.Claims)
	if err != nil || json.Unmarshal(encoded, &claims) != nil || expectedNonce != "" && claims.Nonce != expectedNonce {
		return OIDCIdentity{}, false
	}
	subject, ok := v.subject(verified.Claims, claims.Subject)
	if !ok {
		return OIDCIdentity{}, false
	}
	identity := OIDCIdentity{
		ProviderID: v.config.ProviderID, ProviderSlug: v.config.ProviderSlug,
		Issuer: claims.Issuer, Subject: subject, Email: strings.TrimSpace(claims.Email),
		DisplayName: strings.TrimSpace(claims.Name), Username: strings.TrimSpace(claims.Username),
		EmailVerified: claims.EmailVerified,
	}
	for _, subject := range v.config.AdminSubjects {
		if subject == identity.Subject {
			identity.Admin = true
			identity.Role = RoleAdmin
			identity.AdminSubject = true
			break
		}
	}
	roles := append([]string(nil), claims.RealmAccess.Roles...)
	roles = append(roles, claims.Roles...)
	roles = append(roles, tolerantStrings(verified.Claims["groups"])...)
	for _, resource := range claims.ResourceAccess {
		roles = append(roles, resource.Roles...)
	}
	if v.config.ExternalIDClaim != "" {
		if value, ok := verified.Claims[v.config.ExternalIDClaim].(string); ok {
			identity.ExternalID = strings.TrimSpace(value)
		}
	}
	identity.Groups, identity.GroupsOverage, identity.GroupsPresent = claimGroupsPresent(verified.Claims, v.config.GroupsClaim)
	identity.RoleMappings = matchedOIDCRoleMappings(roles, v.config.Roles)
	if containsMappedGatewayRole(identity.RoleMappings, RoleAdmin) {
		identity.Admin = true
		identity.Role = RoleAdmin
	} else if containsMappedGatewayRole(identity.RoleMappings, RoleMember) {
		identity.Role = RoleMember
	}
	return identity, true
}

// claimGroups reads the group values of a verified token. Microsoft Entra ID replaces the groups claim with
// _claim_names (and hasgroups) when a user is in too many groups; resolving them through Microsoft Graph is out of
// scope, so an overage yields no groups at all and the caller audits it. A claim longer than
// repository.MaxClaimsGroups is an overage too: never a partial list. A claim of an unexpected shape (not a list
// or a string, a non-string member) asserts no groups and is not reported as an overage.
func claimGroups(claims map[string]any, name string) (groups []string, overage bool) {
	groups, overage, _ = claimGroupsPresent(claims, name)
	return groups, overage
}

// claimGroupsPresent is claimGroups that also reports whether the token carries the claim at all.
func claimGroupsPresent(claims map[string]any, name string) (groups []string, overage, present bool) {
	if name = strings.TrimSpace(name); name == "" {
		name = "groups"
	}
	if names, ok := claims["_claim_names"].(map[string]any); ok {
		if _, marked := names[name]; marked {
			return nil, true, true
		}
	}
	if has, _ := claims["hasgroups"].(bool); has {
		return nil, true, true
	}
	raw, present := claims[name]
	if !present {
		return nil, false, false
	}
	var values []any
	switch typed := raw.(type) {
	case []any:
		values = typed
	case string:
		// An IdP that sends one group as a plain string.
		values = []any{typed}
	default:
		return nil, false, true
	}
	if len(values) > repository.MaxClaimsGroups {
		return nil, true, true
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		text, isString := value.(string)
		if !isString {
			// A claim of an unexpected shape asserts no groups; it is not an overflow, so it is not audited as one.
			return nil, false, true
		}
		if text = strings.TrimSpace(text); text != "" {
			if _, duplicate := seen[text]; !duplicate {
				seen[text] = struct{}{}
				groups = append(groups, text)
			}
		}
	}
	return groups, false, true
}

// UnverifiedIssuer reads the iss claim of a JWT without verifying anything. It is only
// for choosing which provider's validator to try: a forged value can at most select a
// validator that then rejects the token.
func UnverifiedIssuer(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Issuer string `json:"iss"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(claims.Issuer), "/")
}

func (v *OIDCValidator) algorithms() []string {
	if len(v.config.Algorithms) == 0 {
		return []string{jose.RS256}
	}
	return v.config.Algorithms
}

// subject builds the login subject. A configured composite key (Entra ID tid:oid)
// needs every claim to be a non-empty string, otherwise the token is refused.
func (v *OIDCValidator) subject(verified map[string]any, standard string) (string, bool) {
	if len(v.config.SubjectClaims) == 0 || len(v.config.SubjectClaims) == 1 && v.config.SubjectClaims[0] == "sub" {
		return standard, standard != ""
	}
	parts := make([]string, 0, len(v.config.SubjectClaims))
	for _, name := range v.config.SubjectClaims {
		value, _ := verified[name].(string)
		if strings.TrimSpace(value) == "" {
			return "", false
		}
		parts = append(parts, value)
	}
	return strings.Join(parts, ":"), true
}

func normalizedOIDCRoles(values []string) []string {
	roles := make([]string, 0, len(values))
	for _, role := range values {
		if role = strings.TrimSpace(role); role != "" {
			roles = append(roles, role)
		}
	}
	return roles
}

func matchedOIDCRoleMappings(values []string, mapping OIDCRoleMapping) []OIDCRoleMappingMatch {
	matches := make([]OIDCRoleMappingMatch, 0)
	seen := make(map[OIDCRoleMappingMatch]struct{})
	for _, value := range values {
		for _, candidate := range []struct {
			roles []string
			role  Role
		}{{mapping.Admin, RoleAdmin}, {mapping.Member, RoleMember}} {
			match := OIDCRoleMappingMatch{ExternalRole: value, GatewayRole: candidate.role}
			if !containsRole(candidate.roles, value) {
				continue
			}
			if _, ok := seen[match]; ok {
				continue
			}
			seen[match] = struct{}{}
			matches = append(matches, match)
		}
	}
	return matches
}

func containsMappedGatewayRole(mappings []OIDCRoleMappingMatch, expected Role) bool {
	for _, mapping := range mappings {
		if mapping.GatewayRole == expected {
			return true
		}
	}
	return false
}

// tolerantStrings reads a claim that should be a list of strings. A string is one value and anything else, or a
// non-string member, is ignored: an unexpected shape must not make the whole token unreadable.
func tolerantStrings(raw any) []string {
	switch typed := raw.(type) {
	case string:
		return []string{typed}
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
		return values
	}
	return nil
}
