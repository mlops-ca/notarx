package config

import "fmt"

const (
	// DefaultSCIMMaxPage is the default cap of the SCIM count parameter and the maxResults the
	// ServiceProviderConfig advertises (ADR 0018 section 4).
	DefaultSCIMMaxPage = 200
	// MaxSCIMMaxPage bounds NOTARX_SCIM_MAX_PAGE: a SCIM page is one indexed keyset page, so it stays small.
	MaxSCIMMaxPage = 1000
)

// SCIMConfig holds the SCIM 2.0 server policy of ADR 0018 section 4.
type SCIMConfig struct {
	// Enabled turns the root-mounted /scim/v2/ surface on (NOTARX_SCIM_ENABLED). While false every
	// /scim/v2/ route answers 404.
	Enabled bool
	// MaxPage caps the count of a SCIM list request (NOTARX_SCIM_MAX_PAGE).
	MaxPage int
}

// DefaultSCIMConfig returns the documented defaults: SCIM off, pages of at most 200.
func DefaultSCIMConfig() SCIMConfig {
	return SCIMConfig{MaxPage: DefaultSCIMMaxPage}
}

func scimConfigFromEnv(current SCIMConfig) (SCIMConfig, error) {
	var err error
	if current.Enabled, err = boolEnv("NOTARX_SCIM_ENABLED", current.Enabled); err != nil {
		return current, err
	}
	if current.MaxPage, err = positiveIntEnv("NOTARX_SCIM_MAX_PAGE", current.MaxPage, false); err != nil {
		return current, err
	}
	if current.MaxPage > MaxSCIMMaxPage {
		return current, fmt.Errorf("NOTARX_SCIM_MAX_PAGE must not exceed %d", MaxSCIMMaxPage)
	}
	return current, nil
}
