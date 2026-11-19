package saml

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
	"sync"
	"testing"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

const (
	testEntityID   = "https://idp.example.test/metadata"
	testSPEntityID = "https://notarx.example.test/auth/saml/corp/metadata"
	testACS        = "https://notarx.example.test/auth/saml/corp/acs"
	testRequestID  = "_0190d9c0-0000-7000-8000-000000000001"
)

// testNow is inside every fixture's validity window.
var testNow = time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

type testKey struct {
	cert *x509.Certificate
	tls  tls.Certificate
}

var (
	keyOnce  sync.Once
	rsaKey   testKey
	otherKey testKey
	ecKey    testKey
)

func newTestKey(t testing.TB, signer crypto.Signer, publicKey crypto.PublicKey, name string) testKey {
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
	return testKey{cert: cert, tls: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: signer}}
}

func testKeys(t testing.TB) (primary, other, ec testKey) {
	t.Helper()
	keyOnce.Do(func() {
		a, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		b, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		e, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		rsaKey = newTestKey(t, a, &a.PublicKey, "idp")
		otherKey = newTestKey(t, b, &b.PublicKey, "other-idp")
		ecKey = newTestKey(t, e, &e.PublicKey, "ec-idp")
	})
	return rsaKey, otherKey, ecKey
}

// responseOptions describes one generated response; the zero value of a field selects the valid default.
type responseOptions struct {
	key             *testKey
	signMethod      string
	signAssertion   *bool
	signResponse    bool
	responseID      string
	assertionID     string
	inResponseTo    *string
	confirmationTo  *string
	destination     *string
	recipient       string
	audience        string
	issuer          string
	status          string
	notBefore       string
	notOnOrAfter    string
	nameID          string
	nameIDFormat    string
	omitConditions  bool
	attributes      string
	extraInResponse string
	mutate          func(*etree.Document)
}

func ptr[T any](v T) *T { return &v }

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func assertionXML(o responseOptions) string {
	notBefore := orDefault(o.notBefore, testNow.Add(-time.Minute).Format(time.RFC3339))
	notOnOrAfter := orDefault(o.notOnOrAfter, testNow.Add(5*time.Minute).Format(time.RFC3339))
	confirmationTo := testRequestID
	if o.confirmationTo != nil {
		confirmationTo = *o.confirmationTo
	} else if o.inResponseTo != nil {
		confirmationTo = *o.inResponseTo
	}
	confirmationAttr := ""
	if confirmationTo != "" {
		confirmationAttr = fmt.Sprintf(` InResponseTo="%s"`, confirmationTo)
	}
	conditions := fmt.Sprintf(`<saml:Conditions NotBefore="%s" NotOnOrAfter="%s"><saml:AudienceRestriction><saml:Audience>%s</saml:Audience></saml:AudienceRestriction></saml:Conditions>`,
		notBefore, notOnOrAfter, orDefault(o.audience, testSPEntityID))
	if o.omitConditions {
		conditions = ""
	}
	format := orDefault(o.nameIDFormat, "urn:oasis:names:tc:SAML:2.0:nameid-format:persistent")
	attributes := o.attributes
	if attributes == "" {
		attributes = attribute("http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress", "Ada@Example.test") +
			attribute("http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name", "ada") +
			attribute("displayName", "Ada Lovelace") +
			attribute("groups", "engineering", "admins")
	}
	return fmt.Sprintf(`<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xs="http://www.w3.org/2001/XMLSchema" ID="%s" IssueInstant="%s" Version="2.0">`+
		`<saml:Issuer>%s</saml:Issuer>`+
		`<saml:Subject><saml:NameID Format="%s">%s</saml:NameID><saml:SubjectConfirmation Method="urn:oasis:names:tc:SAML:2.0:cm:bearer"><saml:SubjectConfirmationData%s NotOnOrAfter="%s" Recipient="%s"/></saml:SubjectConfirmation></saml:Subject>`+
		`%s`+
		`<saml:AuthnStatement AuthnInstant="%s" SessionIndex="session-1"><saml:AuthnContext><saml:AuthnContextClassRef>urn:oasis:names:tc:SAML:2.0:ac:classes:PasswordProtectedTransport</saml:AuthnContextClassRef></saml:AuthnContext></saml:AuthnStatement>`+
		`<saml:AttributeStatement>%s</saml:AttributeStatement></saml:Assertion>`,
		orDefault(o.assertionID, "_assertion-1"), testNow.Format(time.RFC3339), orDefault(o.issuer, testEntityID), format, orDefault(o.nameID, "user-1"),
		confirmationAttr, notOnOrAfter, orDefault(o.recipient, testACS), conditions, testNow.Format(time.RFC3339), attributes)
}

func attribute(name string, values ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<saml:Attribute Name="%s">`, name)
	for _, v := range values {
		fmt.Fprintf(&b, `<saml:AttributeValue xsi:type="xs:string">%s</saml:AttributeValue>`, v)
	}
	b.WriteString("</saml:Attribute>")
	return b.String()
}

func responseXML(o responseOptions, assertion string) string {
	inResponseTo := testRequestID
	if o.inResponseTo != nil {
		inResponseTo = *o.inResponseTo
	}
	inAttr := ""
	if inResponseTo != "" {
		inAttr = fmt.Sprintf(` InResponseTo="%s"`, inResponseTo)
	}
	destination := testACS
	if o.destination != nil {
		destination = *o.destination
	}
	destAttr := ""
	if destination != "" {
		destAttr = fmt.Sprintf(` Destination="%s"`, destination)
	}
	return fmt.Sprintf(`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="%s"%s%s IssueInstant="%s" Version="2.0">`+
		`<saml:Issuer>%s</saml:Issuer><samlp:Status><samlp:StatusCode Value="%s"/></samlp:Status>%s%s</samlp:Response>`,
		orDefault(o.responseID, "_response-1"), inAttr, destAttr, testNow.Format(time.RFC3339), orDefault(o.issuer, testEntityID),
		orDefault(o.status, "urn:oasis:names:tc:SAML:2.0:status:Success"), o.extraInResponse, assertion)
}

func signer(t testing.TB, key testKey, method string) *dsig.SigningContext {
	t.Helper()
	ctx, err := dsig.NewSigningContext(key.tls.PrivateKey.(crypto.Signer), key.tls.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	// SAML IdPs sign with exclusive canonicalization, which keeps the digest independent of the
	// namespaces declared on the ancestors a signed element is later embedded in.
	ctx.Canonicalizer = dsig.MakeC14N10ExclusiveCanonicalizerWithPrefixList("")
	if method != "" {
		if err := ctx.SetSignatureMethod(method); err != nil {
			t.Fatal(err)
		}
	}
	return ctx
}

// sign signs el (which carries its own xmlns declarations) and returns the signed copy.
func sign(t testing.TB, ctx *dsig.SigningContext, el *etree.Element) *etree.Element {
	t.Helper()
	signed, err := ctx.SignEnveloped(el)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func parseElement(t testing.TB, xml string) *etree.Element {
	t.Helper()
	doc := etree.NewDocument()
	if err := doc.ReadFromString(xml); err != nil {
		t.Fatal(err)
	}
	return doc.Root()
}

// buildResponse returns the base64 POST body of a response built from o.
func buildResponse(t testing.TB, o responseOptions) string {
	t.Helper()
	primary, _, _ := testKeys(t)
	key := primary
	if o.key != nil {
		key = *o.key
	}
	ctx := signer(t, key, o.signMethod)
	assertion := parseElement(t, assertionXML(o))
	if o.signAssertion == nil || *o.signAssertion {
		assertion = sign(t, ctx, assertion)
	}
	doc := etree.NewDocument()
	doc.SetRoot(parseElement(t, responseXML(o, "")))
	doc.Root().AddChild(assertion)
	if o.signResponse {
		doc.SetRoot(sign(t, ctx, doc.Root()))
	}
	if o.mutate != nil {
		o.mutate(doc)
	}
	out, err := doc.WriteToString()
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString([]byte(out))
}

func baseConfig(t testing.TB) Config {
	t.Helper()
	primary, _, ec := testKeys(t)
	return Config{
		Certificates: []*x509.Certificate{primary.cert, ec.cert},
		EntityID:     testEntityID, ACSURL: testACS, SPEntityID: testSPEntityID,
		ClockSkew: time.Minute, NameIDFormat: NameIDPersistent,
		Now: func() time.Time { return testNow },
	}
}
