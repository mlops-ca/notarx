package saml

import (
	"encoding/base64"
	"encoding/xml"
	"errors"
	"net/url"
	"strings"

	saml2 "github.com/russellhaering/gosaml2"
	dsig "github.com/russellhaering/goxmldsig"
)

// SPEndpoints are the URLs of this service provider for one provider slug. They derive from
// NOTARX_PUBLIC_URL; the SP entity id is the metadata URL.
type SPEndpoints struct {
	EntityID    string
	ACSURL      string
	MetadataURL string
	LoginURL    string
}

// EndpointsFor builds the URLs for slug under the public base URL.
func EndpointsFor(publicURL *url.URL, slug string) SPEndpoints {
	base := strings.TrimRight(publicURL.String(), "/") + "/auth/saml/" + url.PathEscape(slug)
	return SPEndpoints{EntityID: base + "/metadata", MetadataURL: base + "/metadata", ACSURL: base + "/acs", LoginURL: base + "/login"}
}

// LoginRequest is what BuildLoginURL needs to start one sign-in.
type LoginRequest struct {
	IdPEntityID  string
	SSOURL       string
	NameIDFormat string
	SP           SPEndpoints
	// RequestID is the AuthnRequest ID; the ACS requires it back as InResponseTo.
	RequestID  string
	RelayState string
	// SignKey signs the request (HTTP-Redirect query signature) when non-nil.
	SignKey *SPKey
}

// BuildLoginURL returns the IdP URL of an HTTP-Redirect AuthnRequest: deflated, base64 encoded and,
// when a signing key is given, signed over the query string.
func BuildLoginURL(request LoginRequest) (string, error) {
	format, ok := NameIDFormatURI(request.NameIDFormat)
	if !ok {
		return "", errors.New("saml: unsupported NameID format")
	}
	provider := &saml2.SAMLServiceProvider{
		IdentityProviderSSOURL: request.SSOURL, IdentityProviderIssuer: request.IdPEntityID,
		ServiceProviderIssuer: request.SP.EntityID, AssertionConsumerServiceURL: request.SP.ACSURL,
		AudienceURI: request.SP.EntityID, NameIdFormat: format,
	}
	if request.SignKey != nil {
		private, err := request.SignKey.signer()
		if err != nil {
			return "", err
		}
		der, err := request.SignKey.Certificate()
		if err != nil {
			return "", err
		}
		if err := provider.SetSPSigningKeyStore(&saml2.KeyStore{Signer: private, Cert: der}); err != nil {
			return "", err
		}
		provider.SignAuthnRequests = true
		provider.SignAuthnRequestsAlgorithm = dsig.RSASHA256SignatureMethod
	}
	document, err := provider.BuildAuthRequestDocumentNoSig()
	if err != nil {
		return "", err
	}
	document.Root().CreateAttr("ID", request.RequestID)
	return provider.BuildAuthURLRedirect(request.RelayState, document)
}

// RequestIDFor returns the AuthnRequest ID for a stored request-state id.
func RequestIDFor(stateID string) string { return "_" + stateID }

// StateIDFrom is the inverse of RequestIDFor; it reports false for an ID notarx did not issue.
func StateIDFrom(requestID string) (string, bool) {
	state, ok := strings.CutPrefix(requestID, "_")
	return state, ok && state != ""
}

type spMetadata struct {
	XMLName         xml.Name `xml:"md:EntityDescriptor"`
	NSMetadata      string   `xml:"xmlns:md,attr"`
	NSDSig          string   `xml:"xmlns:ds,attr"`
	EntityID        string   `xml:"entityID,attr"`
	SPSSODescriptor spSSODescriptor
}

type spSSODescriptor struct {
	XMLName                    xml.Name `xml:"md:SPSSODescriptor"`
	AuthnRequestsSigned        bool     `xml:"AuthnRequestsSigned,attr"`
	WantAssertionsSigned       bool     `xml:"WantAssertionsSigned,attr"`
	ProtocolSupportEnumeration string   `xml:"protocolSupportEnumeration,attr"`
	KeyDescriptors             []spKeyDescriptor
	NameIDFormats              []string `xml:"md:NameIDFormat"`
	ACS                        spACS
}

type spKeyDescriptor struct {
	XMLName xml.Name `xml:"md:KeyDescriptor"`
	Use     string   `xml:"use,attr"`
	KeyInfo spKeyInfo
}

type spKeyInfo struct {
	XMLName  xml.Name `xml:"ds:KeyInfo"`
	X509Data struct {
		XMLName     xml.Name `xml:"ds:X509Data"`
		Certificate string   `xml:"ds:X509Certificate"`
	}
}

type spACS struct {
	XMLName   xml.Name `xml:"md:AssertionConsumerService"`
	Binding   string   `xml:"Binding,attr"`
	Location  string   `xml:"Location,attr"`
	Index     int      `xml:"index,attr"`
	IsDefault bool     `xml:"isDefault,attr"`
}

// SPMetadata renders the service provider metadata of one provider: the entity id, the ACS endpoint
// (HTTP-POST), the requested NameID format and, for each service provider key, a signing certificate.
// Two active keys are published while a rotation is in progress. Private keys never appear.
func SPMetadata(endpoints SPEndpoints, nameIDFormat string, signRequests bool, keys SPKeySet) ([]byte, error) {
	format, ok := NameIDFormatURI(nameIDFormat)
	if !ok {
		return nil, errors.New("saml: unsupported NameID format")
	}
	descriptor := spMetadata{NSMetadata: nsMetadata, NSDSig: nsDSig, EntityID: endpoints.EntityID}
	descriptor.SPSSODescriptor = spSSODescriptor{
		AuthnRequestsSigned: signRequests && len(keys.Keys) > 0, WantAssertionsSigned: true,
		ProtocolSupportEnumeration: nsProtocol, NameIDFormats: []string{format},
		ACS: spACS{Binding: bindingPost, Location: endpoints.ACSURL, Index: 0, IsDefault: true},
	}
	for _, key := range keys.Keys {
		der, err := key.Certificate()
		if err != nil {
			return nil, err
		}
		entry := spKeyDescriptor{Use: "signing"}
		entry.KeyInfo.X509Data.Certificate = base64.StdEncoding.EncodeToString(der)
		descriptor.SPSSODescriptor.KeyDescriptors = append(descriptor.SPSSODescriptor.KeyDescriptors, entry)
	}
	body, err := xml.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}
