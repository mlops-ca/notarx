// Package samltest builds signed SAML responses for tests: a stub identity provider that signs with
// a throwaway key. It never imports the verifier, so both the verifier's own tests and the HTTP
// handlers' tests can use it.
package samltest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

// Key is a throwaway IdP signing key with its self-signed certificate.
type Key struct {
	Cert *x509.Certificate
	TLS  tls.Certificate
}

func newKey(t testing.TB, signer crypto.Signer, publicKey crypto.PublicKey, name string) Key {
	t.Helper()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return Key{Cert: cert, TLS: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: signer}}
}

// NewRSAKey generates an RSA-2048 key.
func NewRSAKey(t testing.TB, name string) Key {
	t.Helper()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return newKey(t, private, &private.PublicKey, name)
}

// NewECKey generates an ECDSA P-256 key.
func NewECKey(t testing.TB, name string) Key {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return newKey(t, private, &private.PublicKey, name)
}

// Ptr returns a pointer to v, to tell an unset option from an empty one.
func Ptr[T any](v T) *T { return &v }

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// Response describes one generated response. Zero fields select the valid default; the identifiers
// (EntityID, SPEntityID, ACSURL, RequestID) and Now must be set.
type Response struct {
	Key             Key
	SignMethod      string
	SignAssertion   *bool
	SignResponse    bool
	ResponseID      string
	AssertionID     string
	InResponseTo    *string
	ConfirmationTo  *string
	Destination     *string
	Recipient       string
	Audience        string
	Issuer          string
	Status          string
	NotBefore       string
	NotOnOrAfter    string
	NameID          string
	NameIDFormat    string
	OmitConditions  bool
	Attributes      string
	ExtraInResponse string
	Mutate          func(*etree.Document)

	EntityID, SPEntityID, ACSURL, RequestID string
	Now                                     time.Time
}

// Attribute renders one attribute statement entry.
func Attribute(name string, values ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<saml:Attribute Name="%s">`, name)
	for _, v := range values {
		fmt.Fprintf(&b, `<saml:AttributeValue xsi:type="xs:string">%s</saml:AttributeValue>`, v)
	}
	b.WriteString("</saml:Attribute>")
	return b.String()
}

// AssertionXML renders the unsigned assertion.
func (o Response) AssertionXML() string {
	notBefore := orDefault(o.NotBefore, o.Now.Add(-time.Minute).Format(time.RFC3339))
	notOnOrAfter := orDefault(o.NotOnOrAfter, o.Now.Add(5*time.Minute).Format(time.RFC3339))
	confirmationTo := o.RequestID
	if o.ConfirmationTo != nil {
		confirmationTo = *o.ConfirmationTo
	} else if o.InResponseTo != nil {
		confirmationTo = *o.InResponseTo
	}
	confirmationAttr := ""
	if confirmationTo != "" {
		confirmationAttr = fmt.Sprintf(` InResponseTo="%s"`, confirmationTo)
	}
	conditions := fmt.Sprintf(`<saml:Conditions NotBefore="%s" NotOnOrAfter="%s"><saml:AudienceRestriction><saml:Audience>%s</saml:Audience></saml:AudienceRestriction></saml:Conditions>`,
		notBefore, notOnOrAfter, orDefault(o.Audience, o.SPEntityID))
	if o.OmitConditions {
		conditions = ""
	}
	format := orDefault(o.NameIDFormat, "urn:oasis:names:tc:SAML:2.0:nameid-format:persistent")
	attributes := o.Attributes
	if attributes == "" {
		attributes = Attribute("http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress", "Ada@Example.test") +
			Attribute("http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name", "ada") +
			Attribute("displayName", "Ada Lovelace") +
			Attribute("groups", "engineering", "admins")
	}
	return fmt.Sprintf(`<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xs="http://www.w3.org/2001/XMLSchema" ID="%s" IssueInstant="%s" Version="2.0">`+
		`<saml:Issuer>%s</saml:Issuer>`+
		`<saml:Subject><saml:NameID Format="%s">%s</saml:NameID><saml:SubjectConfirmation Method="urn:oasis:names:tc:SAML:2.0:cm:bearer"><saml:SubjectConfirmationData%s NotOnOrAfter="%s" Recipient="%s"/></saml:SubjectConfirmation></saml:Subject>`+
		`%s`+
		`<saml:AuthnStatement AuthnInstant="%s" SessionIndex="session-1"><saml:AuthnContext><saml:AuthnContextClassRef>urn:oasis:names:tc:SAML:2.0:ac:classes:PasswordProtectedTransport</saml:AuthnContextClassRef></saml:AuthnContext></saml:AuthnStatement>`+
		`<saml:AttributeStatement>%s</saml:AttributeStatement></saml:Assertion>`,
		orDefault(o.AssertionID, "_assertion-1"), o.Now.Format(time.RFC3339), orDefault(o.Issuer, o.EntityID), format, orDefault(o.NameID, "user-1"),
		confirmationAttr, notOnOrAfter, orDefault(o.Recipient, o.ACSURL), conditions, o.Now.Format(time.RFC3339), attributes)
}

// ResponseXML renders the unsigned response around assertion.
func (o Response) ResponseXML(assertion string) string {
	inResponseTo := o.RequestID
	if o.InResponseTo != nil {
		inResponseTo = *o.InResponseTo
	}
	inAttr := ""
	if inResponseTo != "" {
		inAttr = fmt.Sprintf(` InResponseTo="%s"`, inResponseTo)
	}
	destination := o.ACSURL
	if o.Destination != nil {
		destination = *o.Destination
	}
	destAttr := ""
	if destination != "" {
		destAttr = fmt.Sprintf(` Destination="%s"`, destination)
	}
	return fmt.Sprintf(`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="%s"%s%s IssueInstant="%s" Version="2.0">`+
		`<saml:Issuer>%s</saml:Issuer><samlp:Status><samlp:StatusCode Value="%s"/></samlp:Status>%s%s</samlp:Response>`,
		orDefault(o.ResponseID, "_response-1"), inAttr, destAttr, o.Now.Format(time.RFC3339), orDefault(o.Issuer, o.EntityID),
		orDefault(o.Status, "urn:oasis:names:tc:SAML:2.0:status:Success"), o.ExtraInResponse, assertion)
}

// Signer returns a signing context for key that uses exclusive canonicalization, as SAML IdPs do.
func Signer(t testing.TB, key Key, method string) *dsig.SigningContext {
	t.Helper()
	ctx, err := dsig.NewSigningContext(key.TLS.PrivateKey.(crypto.Signer), key.TLS.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	// Exclusive canonicalization keeps the digest independent of the namespaces declared on the
	// ancestors a signed element is later embedded in.
	ctx.Canonicalizer = dsig.MakeC14N10ExclusiveCanonicalizerWithPrefixList("")
	if method != "" {
		if err := ctx.SetSignatureMethod(method); err != nil {
			t.Fatal(err)
		}
	}
	return ctx
}

// Sign signs el (which carries its own xmlns declarations) and returns the signed copy.
func Sign(t testing.TB, ctx *dsig.SigningContext, el *etree.Element) *etree.Element {
	t.Helper()
	signed, err := ctx.SignEnveloped(el)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// ParseElement parses an XML fragment.
func ParseElement(t testing.TB, xml string) *etree.Element {
	t.Helper()
	doc := etree.NewDocument()
	if err := doc.ReadFromString(xml); err != nil {
		t.Fatal(err)
	}
	return doc.Root()
}

// Encode returns the base64 POST body of the response.
func (o Response) Encode(t testing.TB) string {
	t.Helper()
	ctx := Signer(t, o.Key, o.SignMethod)
	assertion := ParseElement(t, o.AssertionXML())
	if o.SignAssertion == nil || *o.SignAssertion {
		assertion = Sign(t, ctx, assertion)
	}
	doc := etree.NewDocument()
	doc.SetRoot(ParseElement(t, o.ResponseXML("")))
	doc.Root().AddChild(assertion)
	if o.SignResponse {
		doc.SetRoot(Sign(t, ctx, doc.Root()))
	}
	if o.Mutate != nil {
		o.Mutate(doc)
	}
	out, err := doc.WriteToString()
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString([]byte(out))
}
