package saml

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/jose"
)

const (
	nsMetadata = "urn:oasis:names:tc:SAML:2.0:metadata"

	// MaxMetadataBytes caps imported metadata, whether fetched or pasted.
	MaxMetadataBytes = 1 << 20
	// MetadataFetchTimeout is the total timeout of a metadata fetch.
	MetadataFetchTimeout = 5 * time.Second

	bindingRedirect = "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect"
	bindingPost     = "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST"
)

// MetadataError is an import failure that is safe to show to an administrator.
type MetadataError struct{ Message string }

func (e *MetadataError) Error() string { return e.Message }

func metadataError(format string, args ...any) error {
	return &MetadataError{Message: fmt.Sprintf(format, args...)}
}

// IdPMetadata is what notarx takes from IdP metadata: the entity id, the HTTP-Redirect SSO endpoint
// (the AuthnRequest binding) and the signing certificates.
type IdPMetadata struct {
	EntityID     string
	SSOURL       string
	Certificates []*x509.Certificate
}

var whitespace = regexp.MustCompile(`\s+`)

// ParseIdPMetadata reads IdP metadata with DTD and entity expansion off and a size cap. It accepts
// one EntityDescriptor, or an EntitiesDescriptor that contains exactly one IdP.
func ParseIdPMetadata(raw []byte) (IdPMetadata, error) {
	if len(raw) == 0 {
		return IdPMetadata{}, metadataError("the metadata is empty")
	}
	if len(raw) > MaxMetadataBytes {
		return IdPMetadata{}, metadataError("the metadata exceeds 1 MiB")
	}
	root, err := parseStructure(raw)
	if err != nil {
		if ReasonOf(err) == ReasonDTD {
			return IdPMetadata{}, metadataError("document type declarations are not allowed in metadata")
		}
		return IdPMetadata{}, metadataError("the metadata is not well-formed XML")
	}
	var descriptor *element
	switch {
	case root.is(nsMetadata, "EntityDescriptor"):
		descriptor = root
	case root.is(nsMetadata, "EntitiesDescriptor"):
		var idps []*element
		root.walk(func(e *element) {
			if e.is(nsMetadata, "EntityDescriptor") && len(e.child(nsMetadata, "IDPSSODescriptor")) > 0 {
				idps = append(idps, e)
			}
		})
		if len(idps) != 1 {
			return IdPMetadata{}, metadataError("the metadata must describe exactly one identity provider")
		}
		descriptor = idps[0]
	default:
		return IdPMetadata{}, metadataError("the metadata root must be an EntityDescriptor")
	}
	idp := descriptor.child(nsMetadata, "IDPSSODescriptor")
	if len(idp) != 1 {
		return IdPMetadata{}, metadataError("the metadata must contain exactly one IDPSSODescriptor")
	}
	result := IdPMetadata{EntityID: strings.TrimSpace(descriptor.attr("entityID"))}
	if result.EntityID == "" {
		return IdPMetadata{}, metadataError("the metadata has no entityID")
	}
	for _, service := range idp[0].child(nsMetadata, "SingleSignOnService") {
		if service.attr("Binding") == bindingRedirect {
			result.SSOURL = strings.TrimSpace(service.attr("Location"))
			break
		}
	}
	if result.SSOURL == "" {
		return IdPMetadata{}, metadataError("the metadata has no HTTP-Redirect SingleSignOnService")
	}
	for _, key := range idp[0].child(nsMetadata, "KeyDescriptor") {
		if use := key.attr("use"); use != "" && use != "signing" {
			continue
		}
		key.walk(func(e *element) {
			if e.is(nsDSig, "X509Certificate") {
				if cert := parseMetadataCertificate(e.text); cert != nil && !slices.ContainsFunc(result.Certificates, cert.Equal) {
					result.Certificates = append(result.Certificates, cert)
				}
			}
		})
	}
	if len(result.Certificates) == 0 {
		return IdPMetadata{}, metadataError("the metadata has no signing certificate")
	}
	return result, nil
}

func parseMetadataCertificate(value string) *x509.Certificate {
	der, err := base64.StdEncoding.DecodeString(whitespace.ReplaceAllString(value, ""))
	if err != nil {
		return nil
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil
	}
	return cert
}

// CertificatePEM encodes a certificate as one PEM CERTIFICATE block.
func CertificatePEM(cert *x509.Certificate) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
}

// Fetcher downloads metadata from an HTTPS URL through the SSRF guard of internal/jose: no private,
// loopback or link-local address, https only, a 5 second timeout, a 1 MiB cap, and redirects that stay
// on the original host and are re-checked.
type Fetcher struct{ client *http.Client }

// NewFetcher builds a Fetcher on the guarded client.
func NewFetcher() (*Fetcher, error) {
	client, err := jose.NewGuardedClient(MetadataFetchTimeout)
	if err != nil {
		return nil, err
	}
	return &Fetcher{client: client}, nil
}

// NewFetcherWithClient builds a Fetcher on a given client; tests use it to reach a local server.
func NewFetcherWithClient(client *http.Client) *Fetcher { return &Fetcher{client: client} }

// Fetch returns the body at rawURL.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, metadataError("the metadata URL must be an HTTPS URL without credentials")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, metadataError("the metadata URL is invalid")
	}
	request.Header.Set("Accept", "application/samlmetadata+xml, application/xml, text/xml")
	response, err := f.client.Do(request)
	if err != nil {
		return nil, metadataError("the metadata could not be fetched")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, metadataError("the metadata server answered with status %d", response.StatusCode)
	}
	body, err := jose.ReadLimited(response.Body, MaxMetadataBytes)
	if err != nil {
		return nil, metadataError("the metadata exceeds 1 MiB or could not be read")
	}
	return body, nil
}
