package authorization

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/jose"
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
	Issuer        string
	Audience      string
	JWKSURL       string
	AdminSubjects []string
	// Realm-role mappings are opt-in. When a token contains several mapped
	// roles, the validator grants the most privileged one.
	Roles OIDCRoleMapping
}

type OIDCIdentity struct {
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
	verifier, err := jose.NewVerifier(jose.Options{
		JWKSTTL:    oidcJWKSCacheTTL,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	})
	if err != nil {
		return nil
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
		Algorithms:          []string{jose.RS256},
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
		Groups        []string `json:"groups"`
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
	identity := OIDCIdentity{
		Issuer: claims.Issuer, Subject: claims.Subject, Email: strings.TrimSpace(claims.Email),
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
	roles = append(roles, claims.Groups...)
	for _, resource := range claims.ResourceAccess {
		roles = append(roles, resource.Roles...)
	}
	identity.RoleMappings = matchedOIDCRoleMappings(roles, v.config.Roles)
	if containsMappedGatewayRole(identity.RoleMappings, RoleAdmin) {
		identity.Admin = true
		identity.Role = RoleAdmin
	} else if containsMappedGatewayRole(identity.RoleMappings, RoleMember) {
		identity.Role = RoleMember
	}
	return identity, true
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
