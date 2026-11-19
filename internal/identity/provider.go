// Package identity holds the human identity provider registry of ADR 0018: the
// Provider model, the kind-specific settings and their validation. Storage lives
// in internal/repository (the registry is one more PostgreSQL-backed store) and
// the sign-in flows in internal/app; this package has no dependency on either.
package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/jose"
)

// Kind is the protocol of a provider.
type Kind string

const (
	KindOIDC Kind = "oidc"
	KindSAML Kind = "saml"
)

// ProvisioningMode says how a first login of an unknown subject is handled.
type ProvisioningMode string

const (
	ProvisioningDisabled ProvisioningMode = "disabled"
	ProvisioningJIT      ProvisioningMode = "jit"
	ProvisioningSCIMOnly ProvisioningMode = "scim_only"
)

// Valid reports whether the mode is one of the stored values.
func (m ProvisioningMode) Valid() bool {
	return m == ProvisioningDisabled || m == ProvisioningJIT || m == ProvisioningSCIMOnly
}

// JIT default roles. An IdP-driven first login never mints an administrator, so
// "admin" is deliberately absent (ADR 0018 decision 1).
const (
	JITRoleNone   = "none"
	JITRoleMember = "member"
)

// SecretPurpose is the AAD purpose the provider secret is sealed under. It is a
// fixed purpose, like the other sealed settings, so the sealed secret copied by
// the registry migration from oidc_settings stays readable.
const SecretPurpose = "oidc.client-secret"

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// Errors reported by validation. They are safe to show to an administrator.
var ErrInvalid = errors.New("invalid identity provider")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Provider is one row of the registry. SecretsSealed is ciphertext and must
// never be serialized by an API response.
type Provider struct {
	ID                  string
	Slug                string
	Kind                Kind
	DisplayName         string
	Enabled             bool
	Settings            json.RawMessage
	SecretsSealed       string
	AttributeMapping    AttributeMapping
	ProvisioningMode    ProvisioningMode
	EmailLinkingEnabled bool
	JITDefaultRole      string
	Version             int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// AttributeMapping names where each user attribute comes from: a claim of the ID
// token (OIDC) or an attribute of the assertion (SAML). Empty fields select the
// protocol default. Subject may join two claims with a colon, which is how Entra
// ID keeps its stable key: "tid:oid".
type AttributeMapping struct {
	Subject     string `json:"subject,omitempty"`
	Username    string `json:"username,omitempty"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Groups      string `json:"groups,omitempty"`
	ExternalID  string `json:"externalId,omitempty"`
}

// SubjectClaims returns the claims whose values, joined with a colon, form the
// login subject. It defaults to the standard "sub" claim.
func (m AttributeMapping) SubjectClaims() []string {
	var claims []string
	for _, part := range strings.Split(m.Subject, ":") {
		if part = strings.TrimSpace(part); part != "" {
			claims = append(claims, part)
		}
	}
	if len(claims) == 0 {
		return []string{"sub"}
	}
	return claims
}

// OIDCSettings is the kind-specific configuration of an OIDC provider.
type OIDCSettings struct {
	Issuer        string   `json:"issuer"`
	Audience      string   `json:"audience"`
	JWKSURL       string   `json:"jwksUrl,omitempty"`
	ClientID      string   `json:"clientId"`
	RedirectURL   string   `json:"redirectUrl"`
	Scopes        []string `json:"scopes"`
	AdminSubjects []string `json:"adminSubjects"`
	MemberRoles   []string `json:"memberRoles"`
	AdminRoles    []string `json:"adminRoles"`
	// Algorithms is the allow-list of ID token signature algorithms. The default
	// is RS256; ES256 and EdDSA are selectable.
	Algorithms []string `json:"algorithms"`
}

// DefaultAlgorithms is the per-provider default allow-list.
func DefaultAlgorithms() []string { return []string{jose.RS256} }

// OIDC decodes the settings of an OIDC provider.
func (p Provider) OIDC() (OIDCSettings, error) {
	var settings OIDCSettings
	if len(p.Settings) > 0 {
		if err := json.Unmarshal(p.Settings, &settings); err != nil {
			return OIDCSettings{}, invalid("settings are not valid OIDC settings")
		}
	}
	if len(settings.Algorithms) == 0 {
		settings.Algorithms = DefaultAlgorithms()
	}
	return settings, nil
}

// WithOIDC returns the provider with its settings replaced by the encoding of settings.
func (p Provider) WithOIDC(settings OIDCSettings) (Provider, error) {
	encoded, err := json.Marshal(settings)
	if err != nil {
		return p, err
	}
	p.Settings = encoded
	return p, nil
}

// NormalizeAndValidate trims and defaults the provider and checks it. An enabled
// OIDC provider needs an issuer, an audience and a consistent browser client.
func (p Provider) NormalizeAndValidate() (Provider, error) {
	p.Slug = strings.TrimSpace(p.Slug)
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	if !slugPattern.MatchString(p.Slug) {
		return p, invalid("slug must match %s", slugPattern)
	}
	if n := len([]rune(p.DisplayName)); n < 1 || n > 120 {
		return p, invalid("displayName must be 1 to 120 characters")
	}
	if p.ProvisioningMode == "" {
		p.ProvisioningMode = ProvisioningDisabled
	}
	if !p.ProvisioningMode.Valid() {
		return p, invalid("provisioningMode must be disabled, jit or scim_only")
	}
	if p.JITDefaultRole == "" {
		p.JITDefaultRole = JITRoleNone
	}
	if p.JITDefaultRole != JITRoleNone && p.JITDefaultRole != JITRoleMember {
		return p, invalid("jitDefaultRole must be none or member")
	}
	p.AttributeMapping = p.AttributeMapping.normalized()
	if len(strings.Split(p.AttributeMapping.Subject, ":")) > 2 {
		return p, invalid("attributeMapping.subject may name one claim or two joined with a colon")
	}
	switch p.Kind {
	case KindOIDC:
		settings, err := p.OIDC()
		if err != nil {
			return p, err
		}
		settings, err = settings.normalizeAndValidate(p.Enabled)
		if err != nil {
			return p, err
		}
		return p.WithOIDC(settings)
	case KindSAML:
		if p.AttributeMapping.Subject != "" {
			return p, invalid("attributeMapping.subject does not apply to SAML: the identity key is the NameID")
		}
		settings, err := p.SAML()
		if err != nil {
			return p, err
		}
		settings, err = settings.normalizeAndValidate(p.Enabled)
		if err != nil {
			return p, err
		}
		return p.WithSAML(settings)
	default:
		return p, invalid("kind must be oidc or saml")
	}
}

func (m AttributeMapping) normalized() AttributeMapping {
	m.Subject, m.Username, m.Email = strings.TrimSpace(m.Subject), strings.TrimSpace(m.Username), strings.TrimSpace(m.Email)
	m.DisplayName, m.Groups, m.ExternalID = strings.TrimSpace(m.DisplayName), strings.TrimSpace(m.Groups), strings.TrimSpace(m.ExternalID)
	return m
}

func (s OIDCSettings) normalizeAndValidate(enabled bool) (OIDCSettings, error) {
	s.Issuer = strings.TrimRight(strings.TrimSpace(s.Issuer), "/")
	s.Audience = strings.TrimSpace(s.Audience)
	s.JWKSURL = strings.TrimSpace(s.JWKSURL)
	s.ClientID = strings.TrimSpace(s.ClientID)
	s.RedirectURL = strings.TrimSpace(s.RedirectURL)
	s.Scopes = NormalizeStrings(append([]string{"openid"}, s.Scopes...))
	s.AdminSubjects = NormalizeStrings(s.AdminSubjects)
	s.MemberRoles = NormalizeStrings(s.MemberRoles)
	s.AdminRoles = NormalizeStrings(s.AdminRoles)
	algorithms := NormalizeStrings(s.Algorithms)
	if len(algorithms) == 0 {
		algorithms = DefaultAlgorithms()
	}
	for _, algorithm := range algorithms {
		if !slices.Contains(jose.DefaultAlgorithms(), algorithm) {
			return s, invalid("algorithms may only list RS256, ES256 and EdDSA")
		}
	}
	s.Algorithms = algorithms
	if !enabled {
		return s, nil
	}
	if s.Issuer == "" || s.Audience == "" {
		return s, invalid("issuer and audience are required when the provider is enabled")
	}
	if (s.ClientID == "") != (s.RedirectURL == "") {
		return s, invalid("clientId and redirectUrl must be configured together")
	}
	if !SecureURL(s.Issuer) {
		return s, invalid("issuer must use HTTPS outside localhost")
	}
	if s.JWKSURL != "" && !SecureURL(s.JWKSURL) {
		return s, invalid("jwksUrl must use HTTPS outside localhost")
	}
	if s.RedirectURL != "" && !SecureURL(s.RedirectURL) {
		return s, invalid("redirectUrl must use HTTPS outside localhost")
	}
	return s, nil
}

// SecureURL reports whether value is an absolute HTTPS URL, or an HTTP URL on a
// loopback host (development setups).
func SecureURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}
	return slices.Contains([]string{"localhost", "127.0.0.1", "::1"}, parsed.Hostname())
}

// NormalizeStrings trims values and drops empty and duplicate entries, keeping order.
func NormalizeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		normalized = append(normalized, value)
	}
	return normalized
}
