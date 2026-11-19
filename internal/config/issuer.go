package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/tokenissuer"
)

// Environment variables of the OIDC issuer (ADR 0020).
const (
	IssuerEnabledEnv          = "NOTARX_ISSUER_ENABLED"
	IssuerURLEnv              = "NOTARX_ISSUER_URL"
	IssuerAlgEnv              = "NOTARX_ISSUER_ALG"
	IssuerKeyRotationEnv      = "NOTARX_ISSUER_KEY_ROTATION_INTERVAL"
	IssuerKeyPublishLeadEnv   = "NOTARX_ISSUER_KEY_PUBLISH_LEAD"
	IssuerMaxTokenLifetimeEnv = "NOTARX_ISSUER_MAX_TOKEN_LIFETIME"
	IssuerAllowSelfTrustEnv   = "NOTARX_ISSUER_ALLOW_SELF_TRUST"
	EntraAuthorityHostEnv     = "NOTARX_ENTRA_AUTHORITY_HOST"
	EntraAudienceEnv          = "NOTARX_ENTRA_AUDIENCE"

	// DefaultEntraAuthorityHost is the public Entra cloud.
	DefaultEntraAuthorityHost = "https://login.microsoftonline.com"
)

// IssuerConfig configures notarx as an OIDC token issuer. Everything is inert while Enabled
// is false: no endpoints, no rotation worker, no entra-workload upstream scheme.
type IssuerConfig struct {
	Enabled bool
	// URL is the issuer string of tokens and discovery: NOTARX_ISSUER_URL, defaulting to
	// NOTARX_PUBLIC_URL, with the trailing slash removed. Empty while disabled.
	URL                 string
	Alg                 string
	KeyRotationInterval time.Duration
	KeyPublishLead      time.Duration
	MaxTokenLifetime    time.Duration
	// AllowSelfTrust permits an issuer URL that is also registered as an ADR 0019 issuer.
	AllowSelfTrust bool
	// EntraAuthorityHost is the base URL of the Entra login host (sovereign clouds differ).
	EntraAuthorityHost string
	// EntraAudience overrides the audience derived from the authority host.
	EntraAudience string
	// Warnings are startup notices (for example plain http on loopback) for the caller to log.
	Warnings []string
}

// Lifecycle converts the key settings for internal/tokenissuer.
func (c IssuerConfig) Lifecycle() tokenissuer.LifecycleConfig {
	return tokenissuer.LifecycleConfig{
		Alg: c.Alg, RotationInterval: c.KeyRotationInterval, PublishLead: c.KeyPublishLead, MaxTokenLifetime: c.MaxTokenLifetime,
	}
}

// defaultIssuerConfig holds the ADR 0020 defaults.
func defaultIssuerConfig() IssuerConfig {
	defaults := tokenissuer.DefaultLifecycleConfig()
	return IssuerConfig{
		Alg: defaults.Alg, KeyRotationInterval: defaults.RotationInterval, KeyPublishLead: defaults.PublishLead,
		MaxTokenLifetime: defaults.MaxTokenLifetime, EntraAuthorityHost: DefaultEntraAuthorityHost,
	}
}

// ParseIssuerURL validates NOTARX_ISSUER_URL. raw falls back to publicURL when empty. The
// scheme must be https; plain http is accepted for loopback hosts only and returns a warning.
// There must be no user information, query or fragment, and a trailing slash is removed.
func ParseIssuerURL(raw string, publicURL *url.URL) (issuer string, warnings []string, err error) {
	raw = strings.TrimSpace(raw)
	source := IssuerURLEnv
	if raw == "" {
		if publicURL == nil {
			return "", nil, fmt.Errorf("%s (or %s) is required while %s is true", IssuerURLEnv, PublicURLEnv, IssuerEnabledEnv)
		}
		raw, source = publicURL.String(), PublicURLEnv
	}
	parsed, err := parseBaseURL(source, strings.TrimRight(raw, "/"))
	if err != nil {
		return "", nil, err
	}
	if parsed == nil {
		return "", nil, fmt.Errorf("%s must not be empty while %s is true", IssuerURLEnv, IssuerEnabledEnv)
	}
	if parsed.Scheme == "http" {
		warnings = append(warnings, fmt.Sprintf("%s uses plain http on a loopback host; only use this for local development", IssuerURLEnv))
	}
	if parsed.Path != "" {
		warnings = append(warnings, fmt.Sprintf("%s has a path: discovery and the key set are served at the server root, so a reverse proxy must map %s/.well-known/ to them", IssuerURLEnv, parsed.String()))
	}
	return parsed.String(), warnings, nil
}

// loadIssuerConfig reads the issuer settings. Validation of URL and lifecycle values runs only
// while the issuer is enabled, so an air-gapped install never needs them.
func loadIssuerConfig(publicURL *url.URL) (IssuerConfig, error) {
	cfg := defaultIssuerConfig()
	var err error
	if cfg.Enabled, err = boolEnv(IssuerEnabledEnv, false); err != nil {
		return cfg, err
	}
	if cfg.AllowSelfTrust, err = boolEnv(IssuerAllowSelfTrustEnv, false); err != nil {
		return cfg, err
	}
	if alg := strings.TrimSpace(os.Getenv(IssuerAlgEnv)); alg != "" {
		cfg.Alg = alg
	}
	if cfg.KeyRotationInterval, err = durationEnv(IssuerKeyRotationEnv, cfg.KeyRotationInterval); err != nil {
		return cfg, err
	}
	if cfg.KeyPublishLead, err = durationEnv(IssuerKeyPublishLeadEnv, cfg.KeyPublishLead); err != nil {
		return cfg, err
	}
	if cfg.MaxTokenLifetime, err = durationEnv(IssuerMaxTokenLifetimeEnv, cfg.MaxTokenLifetime); err != nil {
		return cfg, err
	}
	if host := strings.TrimSpace(os.Getenv(EntraAuthorityHostEnv)); host != "" {
		cfg.EntraAuthorityHost = host
	}
	cfg.EntraAudience = strings.TrimSpace(os.Getenv(EntraAudienceEnv))
	if !cfg.Enabled {
		return cfg, nil
	}
	if cfg.Alg != jose.RS256 && cfg.Alg != jose.ES256 {
		return cfg, fmt.Errorf("%s must be RS256 or ES256", IssuerAlgEnv)
	}
	if err := cfg.Lifecycle().Validate(); err != nil {
		return cfg, fmt.Errorf("%s/%s/%s: %w", IssuerKeyRotationEnv, IssuerKeyPublishLeadEnv, IssuerMaxTokenLifetimeEnv, err)
	}
	if cfg.URL, cfg.Warnings, err = ParseIssuerURL(os.Getenv(IssuerURLEnv), publicURL); err != nil {
		return cfg, err
	}
	if cfg.EntraAuthorityHost, err = ParseEntraAuthorityHost(cfg.EntraAuthorityHost); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// ParseEntraAuthorityHost validates the Entra authority host: an https origin with no path,
// query, fragment or user information. Token requests go only to this host.
func ParseEntraAuthorityHost(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", fmt.Errorf("%s must be an https origin without path, query or credentials", EntraAuthorityHostEnv)
	}
	return parsed.Scheme + "://" + strings.ToLower(parsed.Host), nil
}
