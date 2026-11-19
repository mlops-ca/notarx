package saml

import (
	"crypto/x509"
	"sync"
	"testing"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/mlops-ca/notarx/internal/identity/saml/samltest"
)

const (
	testEntityID   = "https://idp.example.test/metadata"
	testSPEntityID = "https://notarx.example.test/auth/saml/corp/metadata"
	testACS        = "https://notarx.example.test/auth/saml/corp/acs"
	testRequestID  = "_0190d9c0-0000-7000-8000-000000000001"
)

// testNow is inside every fixture's validity window.
var testNow = time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

var (
	keyOnce  sync.Once
	rsaKey   samltest.Key
	otherKey samltest.Key
	ecKey    samltest.Key
)

func testKeys(t testing.TB) (primary, other, ec samltest.Key) {
	t.Helper()
	keyOnce.Do(func() {
		rsaKey = samltest.NewRSAKey(t, "idp")
		otherKey = samltest.NewRSAKey(t, "other-idp")
		ecKey = samltest.NewECKey(t, "ec-idp")
	})
	return rsaKey, otherKey, ecKey
}

// responseOptions keeps the field names of the verifier tests; everything maps onto samltest.Response.
type responseOptions struct {
	key             *samltest.Key
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

func (o responseOptions) response(t testing.TB) samltest.Response {
	var key samltest.Key
	if o.key != nil {
		key = *o.key
	} else if t != nil {
		key, _, _ = testKeys(t)
	}
	return samltest.Response{
		Key: key, SignMethod: o.signMethod, SignAssertion: o.signAssertion, SignResponse: o.signResponse,
		ResponseID: o.responseID, AssertionID: o.assertionID, InResponseTo: o.inResponseTo, ConfirmationTo: o.confirmationTo,
		Destination: o.destination, Recipient: o.recipient, Audience: o.audience, Issuer: o.issuer, Status: o.status,
		NotBefore: o.notBefore, NotOnOrAfter: o.notOnOrAfter, NameID: o.nameID, NameIDFormat: o.nameIDFormat,
		OmitConditions: o.omitConditions, Attributes: o.attributes, ExtraInResponse: o.extraInResponse, Mutate: o.mutate,
		EntityID: testEntityID, SPEntityID: testSPEntityID, ACSURL: testACS, RequestID: testRequestID, Now: testNow,
	}
}

func attribute(name string, values ...string) string { return samltest.Attribute(name, values...) }

func assertionXML(o responseOptions) string { return o.response(nil).AssertionXML() }

func responseXML(o responseOptions, assertion string) string {
	return o.response(nil).ResponseXML(assertion)
}

func signer(t testing.TB, key samltest.Key, method string) *dsig.SigningContext {
	return samltest.Signer(t, key, method)
}

func sign(t testing.TB, ctx *dsig.SigningContext, el *etree.Element) *etree.Element {
	return samltest.Sign(t, ctx, el)
}

func parseElement(t testing.TB, xml string) *etree.Element { return samltest.ParseElement(t, xml) }

// buildResponse returns the base64 POST body of a response built from o.
func buildResponse(t testing.TB, o responseOptions) string { return o.response(t).Encode(t) }

func baseConfig(t testing.TB) Config {
	t.Helper()
	primary, _, ec := testKeys(t)
	return Config{
		Certificates: []*x509.Certificate{primary.Cert, ec.Cert},
		EntityID:     testEntityID, ACSURL: testACS, SPEntityID: testSPEntityID,
		ClockSkew: time.Minute, NameIDFormat: NameIDPersistent,
		Now: func() time.Time { return testNow },
	}
}
