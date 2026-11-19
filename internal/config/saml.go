package config

import (
	"fmt"
	"time"
)

const (
	// DefaultSAMLClockSkew is the default leeway for SAML validity windows.
	DefaultSAMLClockSkew = time.Minute
	// MaxSAMLClockSkew caps NOTARX_SAML_CLOCK_SKEW (ADR 0018 section 3).
	MaxSAMLClockSkew = 5 * time.Minute
	// DefaultSAMLRequestTTL is how long an AuthnRequest id stays redeemable.
	DefaultSAMLRequestTTL = 10 * time.Minute
	// DefaultSAMLMetadataRefreshInterval is the default refresh cadence of opted-in providers.
	DefaultSAMLMetadataRefreshInterval = 24 * time.Hour
	// DefaultSAMLMaxResponseBytes caps the decoded SAML response payload (1 MiB).
	DefaultSAMLMaxResponseBytes int64 = 1 << 20
)

// SAMLConfig holds the SAML service provider limits of ADR 0018 section 3.
type SAMLConfig struct {
	// ClockSkew is the leeway applied to NotBefore and NotOnOrAfter and added to the
	// assertion replay window (NOTARX_SAML_CLOCK_SKEW, max 5m).
	ClockSkew time.Duration
	// RequestTTL is the lifetime of a stored AuthnRequest id (NOTARX_SAML_REQUEST_TTL).
	RequestTTL time.Duration
	// MaxResponseBytes caps the SAML response payload (NOTARX_SAML_MAX_RESPONSE_BYTES).
	MaxResponseBytes int64
	// MetadataRefreshInterval is how often providers that opted in re-import URL-imported IdP
	// metadata (NOTARX_SAML_METADATA_REFRESH_INTERVAL). The refresh itself is off per provider.
	MetadataRefreshInterval time.Duration
}

// DefaultSAMLConfig returns the documented defaults.
func DefaultSAMLConfig() SAMLConfig {
	return SAMLConfig{ClockSkew: DefaultSAMLClockSkew, RequestTTL: DefaultSAMLRequestTTL, MaxResponseBytes: DefaultSAMLMaxResponseBytes, MetadataRefreshInterval: DefaultSAMLMetadataRefreshInterval}
}

func samlConfigFromEnv(current SAMLConfig) (SAMLConfig, error) {
	var err error
	if current.ClockSkew, err = durationEnv("NOTARX_SAML_CLOCK_SKEW", current.ClockSkew); err != nil {
		return current, err
	}
	if current.ClockSkew > MaxSAMLClockSkew {
		return current, fmt.Errorf("NOTARX_SAML_CLOCK_SKEW must not exceed %s", MaxSAMLClockSkew)
	}
	if current.RequestTTL, err = durationEnv("NOTARX_SAML_REQUEST_TTL", current.RequestTTL); err != nil {
		return current, err
	}
	if current.MaxResponseBytes, err = positiveInt64Env("NOTARX_SAML_MAX_RESPONSE_BYTES", current.MaxResponseBytes); err != nil {
		return current, err
	}
	if current.MetadataRefreshInterval, err = durationEnv("NOTARX_SAML_METADATA_REFRESH_INTERVAL", current.MetadataRefreshInterval); err != nil {
		return current, err
	}
	return current, nil
}
