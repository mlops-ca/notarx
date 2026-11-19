package identity

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"time"
)

// SAML certificate states. Any listed certificate verifies a response; "next" marks a
// certificate added ahead of an IdP cut-over (ADR 0018 section 3, certificate rollover).
const (
	SAMLCertificateActive = "active"
	SAMLCertificateNext   = "next"
)

// SAML NameID formats selectable per provider; transient is refused at login.
const (
	SAMLNameIDPersistent   = "persistent"
	SAMLNameIDEmailAddress = "emailAddress"
	SAMLNameIDUnspecified  = "unspecified"
)

// MaxSAMLCertificates bounds the pinned certificate list so a rollover cannot grow without limit.
const MaxSAMLCertificates = 8

// SAMLCertificate is one pinned IdP signing certificate. NotAfter is derived from the PEM on
// every save; a value supplied by a client is ignored.
type SAMLCertificate struct {
	PEM      string    `json:"pem"`
	NotAfter time.Time `json:"notAfter"`
	State    string    `json:"state"`
}

// SAMLSettings is the kind-specific configuration of a SAML provider. The sealed service
// provider key never appears here; it lives in the provider's secrets_sealed column.
type SAMLSettings struct {
	EntityID     string            `json:"entityId"`
	SSOURL       string            `json:"ssoUrl"`
	Certificates []SAMLCertificate `json:"certificates"`
	NameIDFormat string            `json:"nameIdFormat"`
	// AllowIdPInitiated accepts a response without InResponseTo; it needs RelayStateAllowlist.
	AllowIdPInitiated   bool     `json:"allowIdpInitiated"`
	RelayStateAllowlist []string `json:"relayStateAllowlist"`
	// SignRequests signs the AuthnRequest with the notarx-generated service provider key.
	SignRequests bool `json:"signRequests"`
	// MetadataURL is the HTTPS URL the settings were imported from; refresh needs it.
	MetadataURL string `json:"metadataUrl,omitempty"`
	// MetadataRefresh turns the periodic refresh of URL-imported metadata on (off by default).
	MetadataRefresh bool `json:"metadataRefresh"`
}

// SAML decodes the settings of a SAML provider.
func (p Provider) SAML() (SAMLSettings, error) {
	var settings SAMLSettings
	if len(p.Settings) > 0 {
		if err := json.Unmarshal(p.Settings, &settings); err != nil {
			return SAMLSettings{}, invalid("settings are not valid SAML settings")
		}
	}
	if settings.NameIDFormat == "" {
		settings.NameIDFormat = SAMLNameIDPersistent
	}
	return settings, nil
}

// WithSAML returns the provider with its settings replaced by the encoding of settings.
func (p Provider) WithSAML(settings SAMLSettings) (Provider, error) {
	if settings.Certificates == nil {
		settings.Certificates = []SAMLCertificate{}
	}
	if settings.RelayStateAllowlist == nil {
		settings.RelayStateAllowlist = []string{}
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return p, err
	}
	p.Settings = encoded
	return p, nil
}

// ParseSAMLCertificate decodes one PEM certificate. Anything other than exactly one
// CERTIFICATE block is refused, so a pinned entry never smuggles a second block.
func ParseSAMLCertificate(value string) (*x509.Certificate, string, error) {
	value = strings.TrimSpace(value)
	block, rest := pem.Decode([]byte(value))
	if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, "", invalid("each certificate must be exactly one PEM CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, "", invalid("a certificate cannot be parsed")
	}
	return cert, string(pem.EncodeToMemory(block)), nil
}

// Certificates parses the pinned certificates of the settings.
func (s SAMLSettings) ParsedCertificates() ([]*x509.Certificate, error) {
	certs := make([]*x509.Certificate, 0, len(s.Certificates))
	for _, entry := range s.Certificates {
		cert, _, err := ParseSAMLCertificate(entry.PEM)
		if err != nil {
			return nil, err
		}
		certs = append(certs, cert)
	}
	return certs, nil
}

// ExpiringCertificates returns the certificates whose NotAfter falls within the window of now
// (already expired ones included), for the console warning.
func (s SAMLSettings) ExpiringCertificates(now time.Time, window time.Duration) []SAMLCertificate {
	var expiring []SAMLCertificate
	for _, entry := range s.Certificates {
		if entry.NotAfter.Before(now.Add(window)) {
			expiring = append(expiring, entry)
		}
	}
	return expiring
}

func (s SAMLSettings) normalizeAndValidate(enabled bool) (SAMLSettings, error) {
	s.EntityID = strings.TrimSpace(s.EntityID)
	s.SSOURL = strings.TrimSpace(s.SSOURL)
	s.MetadataURL = strings.TrimSpace(s.MetadataURL)
	s.RelayStateAllowlist = NormalizeStrings(s.RelayStateAllowlist)
	if s.NameIDFormat == "" {
		s.NameIDFormat = SAMLNameIDPersistent
	}
	switch s.NameIDFormat {
	case SAMLNameIDPersistent, SAMLNameIDEmailAddress, SAMLNameIDUnspecified:
	default:
		return s, invalid("nameIdFormat must be persistent, emailAddress or unspecified")
	}
	if len(s.Certificates) > MaxSAMLCertificates {
		return s, invalid("at most %d certificates can be pinned", MaxSAMLCertificates)
	}
	seen := make(map[string]bool, len(s.Certificates))
	certificates := make([]SAMLCertificate, 0, len(s.Certificates))
	for _, entry := range s.Certificates {
		cert, canonical, err := ParseSAMLCertificate(entry.PEM)
		if err != nil {
			return s, err
		}
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		switch entry.State {
		case "":
			entry.State = SAMLCertificateActive
		case SAMLCertificateActive, SAMLCertificateNext:
		default:
			return s, invalid("a certificate state must be active or next")
		}
		certificates = append(certificates, SAMLCertificate{PEM: canonical, NotAfter: cert.NotAfter.UTC(), State: entry.State})
	}
	s.Certificates = certificates
	if s.MetadataURL != "" && !SecureURL(s.MetadataURL) {
		return s, invalid("metadataUrl must use HTTPS outside localhost")
	}
	if s.SSOURL != "" && !SecureURL(s.SSOURL) {
		return s, invalid("ssoUrl must use HTTPS outside localhost")
	}
	if s.AllowIdPInitiated && len(s.RelayStateAllowlist) == 0 {
		return s, invalid("allowIdpInitiated requires a relayStateAllowlist")
	}
	if !s.AllowIdPInitiated {
		s.RelayStateAllowlist = []string{}
	}
	if !enabled {
		return s, nil
	}
	if s.EntityID == "" || s.SSOURL == "" {
		return s, invalid("entityId and ssoUrl are required when the provider is enabled")
	}
	if len(s.Certificates) == 0 {
		return s, invalid("at least one certificate is required when the provider is enabled")
	}
	return s, nil
}
