package saml

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/identity/saml/samltest"
)

func verify(t *testing.T, config Config, body string, mutate ...func(*Request)) (Claims, error) {
	t.Helper()
	v, err := NewVerifier(config)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{SAMLResponse: body, InResponseTo: testRequestID}
	for _, m := range mutate {
		m(&request)
	}
	return v.Verify(context.Background(), request)
}

func wantReason(t *testing.T, err error, reason Reason) {
	t.Helper()
	if err == nil {
		t.Fatalf("accepted; want rejection %s", reason)
	}
	if got := ReasonOf(err); got != reason {
		t.Fatalf("reason = %q, want %q (%v)", got, reason, err)
	}
	if strings.Contains(err.Error(), testEntityID) || err.Error() != "saml response rejected" {
		t.Fatalf("error text is not generic: %q", err.Error())
	}
}

func TestVerifyAcceptsSignedAssertion(t *testing.T) {
	claims, err := verify(t, baseConfig(t), buildResponse(t, responseOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	if claims.AssertionID != "_assertion-1" || claims.NameID != "user-1" || claims.SessionIndex != "session-1" {
		t.Fatalf("claims = %+v", claims)
	}
	if claims.Email != "ada@example.test" || claims.Username != "ada" || claims.DisplayName != "Ada Lovelace" || !slices.Equal(claims.Groups, []string{"engineering", "admins"}) {
		t.Fatalf("mapped attributes = %+v", claims)
	}
	if !strings.HasSuffix(claims.AuthnContext, "PasswordProtectedTransport") {
		t.Fatalf("authn context = %q", claims.AuthnContext)
	}
	// ReplayUntil is the later NotOnOrAfter plus the skew.
	if want := testNow.Add(5*time.Minute + time.Minute); !claims.ReplayUntil.Equal(want) {
		t.Fatalf("ReplayUntil = %s, want %s", claims.ReplayUntil, want)
	}
}

func TestVerifyAcceptedShapes(t *testing.T) {
	_, _, ec := testKeys(t)
	for name, options := range map[string]responseOptions{
		"signed response, unsigned assertion": {signAssertion: ptr(false), signResponse: true},
		"signed response and assertion":       {signResponse: true},
		"rsa-sha384":                          {signMethod: dsig.RSASHA384SignatureMethod},
		"rsa-sha512":                          {signMethod: dsig.RSASHA512SignatureMethod},
		"ecdsa-sha256":                        {key: &ec, signMethod: dsig.ECDSASHA256SignatureMethod},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := verify(t, baseConfig(t), buildResponse(t, options)); err != nil {
				t.Fatalf("rejected: %v (%s)", err, ReasonOf(err))
			}
		})
	}
}

func TestVerifyClockSkew(t *testing.T) {
	// Expired 30s ago, inside the 60s skew: accepted. Expired 2m ago: refused.
	inside := responseOptions{notOnOrAfter: testNow.Add(-30 * time.Second).Format(time.RFC3339), confirmationTo: nil}
	if _, err := verify(t, baseConfig(t), buildResponse(t, inside)); err != nil {
		t.Fatalf("inside the skew: %v", err)
	}
	outside := responseOptions{notOnOrAfter: testNow.Add(-2 * time.Minute).Format(time.RFC3339)}
	_, err := verify(t, baseConfig(t), buildResponse(t, outside))
	wantReason(t, err, ReasonExpired)
	// Valid from 30s in the future is inside the skew; 5m ahead is not.
	if _, err := verify(t, baseConfig(t), buildResponse(t, responseOptions{notBefore: testNow.Add(30 * time.Second).Format(time.RFC3339)})); err != nil {
		t.Fatalf("not-before inside the skew: %v", err)
	}
	_, err = verify(t, baseConfig(t), buildResponse(t, responseOptions{notBefore: testNow.Add(5 * time.Minute).Format(time.RFC3339), notOnOrAfter: testNow.Add(10 * time.Minute).Format(time.RFC3339)}))
	wantReason(t, err, ReasonNotYetValid)
}

func TestVerifyNegativeSuite(t *testing.T) {
	primary, other, _ := testKeys(t)
	cases := []struct {
		name    string
		options responseOptions
		reason  Reason
		mutate  []func(*Request)
	}{
		{name: "unsigned", options: responseOptions{signAssertion: ptr(false)}, reason: ReasonUnsigned},
		{name: "wrong audience", options: responseOptions{audience: "https://other.example.test"}, reason: ReasonAudience},
		{name: "wrong destination", options: responseOptions{destination: ptr("https://evil.example.test/acs"), signResponse: true}, reason: ReasonDestination},
		{name: "missing destination", options: responseOptions{destination: ptr("")}, reason: ReasonDestination},
		{name: "wrong recipient", options: responseOptions{recipient: "https://evil.example.test/acs"}, reason: ReasonRecipient},
		{name: "expired", options: responseOptions{notOnOrAfter: testNow.Add(-time.Hour).Format(time.RFC3339)}, reason: ReasonExpired},
		{name: "replayed response against a new request", options: responseOptions{}, reason: ReasonInResponseTo, mutate: []func(*Request){func(r *Request) { r.InResponseTo = "_another-request" }}},
		{name: "response InResponseTo differs", options: responseOptions{inResponseTo: ptr("_other"), confirmationTo: ptr(testRequestID)}, reason: ReasonInResponseTo},
		{name: "confirmation InResponseTo differs", options: responseOptions{confirmationTo: ptr("_other")}, reason: ReasonInResponseTo},
		{name: "idp initiated", options: responseOptions{inResponseTo: ptr(""), confirmationTo: ptr("")}, reason: ReasonUnsolicited, mutate: []func(*Request){func(r *Request) { r.InResponseTo = "" }}},
		{name: "sha-1 signature", options: responseOptions{signMethod: dsig.RSASHA1SignatureMethod}, reason: ReasonSignatureAlgorithm},
		{name: "unknown certificate", options: responseOptions{key: &other}, reason: ReasonSignatureInvalid},
		{name: "wrong issuer", options: responseOptions{issuer: "https://evil.example.test/idp"}, reason: ReasonIssuer},
		{name: "failed status", options: responseOptions{status: "urn:oasis:names:tc:SAML:2.0:status:Requester"}, reason: ReasonStatus},
		{name: "transient name id", options: responseOptions{nameIDFormat: "urn:oasis:names:tc:SAML:2.0:nameid-format:transient"}, reason: ReasonTransientNameID},
		{name: "no conditions", options: responseOptions{omitConditions: true}, reason: ReasonConditions},
		{name: "bearer method replaced", options: responseOptions{mutate: replaceInDoc("urn:oasis:names:tc:SAML:2.0:cm:bearer", "urn:oasis:names:tc:SAML:2.0:cm:holder-of-key")}, reason: ReasonSignatureInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := verify(t, baseConfig(t), buildResponse(t, tc.options), tc.mutate...)
			wantReason(t, err, tc.reason)
		})
	}
	_ = primary
}

// replaceInDoc rewrites a text fragment of the serialised document through attribute edits.
func replaceInDoc(old, replacement string) func(*etree.Document) {
	return func(doc *etree.Document) {
		for _, e := range doc.Root().FindElements("//*") {
			for i := range e.Attr {
				if e.Attr[i].Value == old {
					e.Attr[i].Value = replacement
				}
			}
		}
	}
}

func TestVerifyRejectsAWrappedAssertion(t *testing.T) {
	// Classic wrapping: the legitimately signed assertion is moved into Extensions and a forged
	// unsigned assertion for another user takes its place as the direct child of the Response.
	forged := assertionXML(responseOptions{nameID: "admin", assertionID: "_forged"})
	wrapped := responseOptions{mutate: func(doc *etree.Document) {
		root := doc.Root()
		signed := root.FindElement("./Assertion")
		root.RemoveChild(signed)
		extensions := etree.NewElement("samlp:Extensions")
		extensions.AddChild(signed)
		root.AddChild(extensions)
		root.AddChild(parseElement(t, forged))
	}}
	_, err := verify(t, baseConfig(t), buildResponse(t, wrapped))
	wantReason(t, err, ReasonAssertionCount)

	// Same shape but only the signed copy remains, inside Extensions: no direct-child assertion.
	moved := responseOptions{mutate: func(doc *etree.Document) {
		root := doc.Root()
		signed := root.FindElement("./Assertion")
		root.RemoveChild(signed)
		extensions := etree.NewElement("samlp:Extensions")
		extensions.AddChild(signed)
		root.AddChild(extensions)
	}}
	_, err = verify(t, baseConfig(t), buildResponse(t, moved))
	wantReason(t, err, ReasonAssertionPlacement)

	// A signature stored where the profile does not allow one.
	misplaced := responseOptions{signAssertion: ptr(false), mutate: func(doc *etree.Document) {
		primary, _, _ := testKeys(t)
		root := doc.Root()
		signedResponse := sign(t, signer(t, primary, ""), parseElement(t, responseXML(responseOptions{}, "")))
		signature := signedResponse.FindElement("./Signature")
		extensions := etree.NewElement("samlp:Extensions")
		extensions.AddChild(signature)
		root.AddChild(extensions)
	}}
	_, err = verify(t, baseConfig(t), buildResponse(t, misplaced))
	wantReason(t, err, ReasonSignaturePlacement)
}

func TestVerifyRejectsBadReferenceURI(t *testing.T) {
	bad := responseOptions{mutate: func(doc *etree.Document) {
		doc.Root().FindElement("//Reference").CreateAttr("URI", "#_something-else")
	}}
	_, err := verify(t, baseConfig(t), buildResponse(t, bad))
	wantReason(t, err, ReasonReferenceURI)

	whole := responseOptions{mutate: func(doc *etree.Document) {
		doc.Root().FindElement("//Reference").CreateAttr("URI", "")
	}}
	_, err = verify(t, baseConfig(t), buildResponse(t, whole))
	wantReason(t, err, ReasonReferenceURI)
}

func TestVerifyRejectsStructuralAttacks(t *testing.T) {
	encode := func(xml string) string { return base64.StdEncoding.EncodeToString([]byte(xml)) }
	valid := func() string {
		raw, _ := base64.StdEncoding.DecodeString(buildResponse(t, responseOptions{}))
		return string(raw)
	}
	cases := map[string]struct {
		body   string
		reason Reason
	}{
		"dtd with entity": {encode("<!DOCTYPE foo [<!ENTITY xxe \"boom\">]>" + valid()), ReasonDTD},
		"billion laughs":  {encode("<!DOCTYPE lolz [<!ENTITY lol \"lol\"><!ENTITY lol2 \"&lol;&lol;\">]>" + valid()), ReasonDTD},
		"encrypted assertion": {buildResponse(t, responseOptions{mutate: func(doc *etree.Document) {
			root := doc.Root()
			root.RemoveChild(root.FindElement("./Assertion"))
			root.AddChild(parseElement(t, `<saml:EncryptedAssertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion"/>`))
		}}), ReasonEncryptedAssertion},
		"not xml":                {encode("not xml at all"), ReasonMalformed},
		"truncated":              {encode(valid()[:200]), ReasonMalformed},
		"wrong root":             {encode(`<a xmlns="urn:x"/>`), ReasonMalformed},
		"processing instruction": {encode(`<?php evil ?>` + valid()), ReasonMalformed},
		"not base64":             {"@@@@", ReasonMalformed},
		"duplicate ids":          {buildResponse(t, responseOptions{responseID: "_assertion-1"}), ReasonDuplicateID},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := verify(t, baseConfig(t), tc.body)
			wantReason(t, err, tc.reason)
		})
	}
}

func TestVerifyRejectsOversizedResponses(t *testing.T) {
	config := baseConfig(t)
	config.MaxResponseBytes = 1024
	_, err := verify(t, config, buildResponse(t, responseOptions{}))
	wantReason(t, err, ReasonTooLarge)
	_, err = verify(t, config, base64.StdEncoding.EncodeToString(make([]byte, 2048)))
	wantReason(t, err, ReasonTooLarge)
}

func TestVerifyAcceptsIdPInitiatedOnlyWhenAllowed(t *testing.T) {
	body := buildResponse(t, responseOptions{inResponseTo: ptr(""), confirmationTo: ptr("")})
	claims, err := verify(t, baseConfig(t), body, func(r *Request) { r.InResponseTo, r.AllowUnsolicited = "", true })
	if err != nil || claims.NameID != "user-1" {
		t.Fatalf("allowed unsolicited = %+v, %v", claims, err)
	}
	// An allowed unsolicited login still refuses a response that does carry an InResponseTo.
	_, err = verify(t, baseConfig(t), buildResponse(t, responseOptions{}), func(r *Request) { r.InResponseTo, r.AllowUnsolicited = "", true })
	wantReason(t, err, ReasonInResponseTo)
}

func TestVerifyNameIDRules(t *testing.T) {
	config := baseConfig(t)
	claims, err := verify(t, config, buildResponse(t, responseOptions{nameID: "Ada@Example.TEST", nameIDFormat: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress"}))
	if err != nil || claims.NameID != "ada@example.test" {
		t.Fatalf("emailAddress key = %+v, %v", claims, err)
	}
	claims, err = verify(t, config, buildResponse(t, responseOptions{nameID: "CaseSensitive"}))
	if err != nil || claims.NameID != "CaseSensitive" {
		t.Fatalf("persistent key = %+v, %v", claims, err)
	}
	_, err = verify(t, config, buildResponse(t, responseOptions{nameID: strings.Repeat("a", maxNameIDLength+1)}))
	wantReason(t, err, ReasonNameID)
}

func TestVerifyAttributeMappingOverrides(t *testing.T) {
	config := baseConfig(t)
	config.Mapping = identity.AttributeMapping{Username: "uid", Email: "mail", DisplayName: "cn", Groups: "memberOf", ExternalID: "employeeId"}
	attributes := attribute("uid", "u1") + attribute("mail", "U1@Example.test") + attribute("cn", "U One") + attribute("memberOf", "g1", "g1", "g2") + attribute("employeeId", "e-9")
	claims, err := verify(t, config, buildResponse(t, responseOptions{attributes: attributes}))
	if err != nil {
		t.Fatal(err)
	}
	if claims.Username != "u1" || claims.Email != "u1@example.test" || claims.DisplayName != "U One" || claims.ExternalID != "e-9" || !slices.Equal(claims.Groups, []string{"g1", "g2"}) {
		t.Fatalf("claims = %+v", claims)
	}
	// A configured name never falls back to the defaults.
	claims, err = verify(t, config, buildResponse(t, responseOptions{}))
	if err != nil || claims.Username != "" || len(claims.Groups) != 0 {
		t.Fatalf("configured mapping fell back to defaults: %+v, %v", claims, err)
	}
}

func TestNewVerifierValidatesConfig(t *testing.T) {
	good := baseConfig(t)
	for name, mutate := range map[string]func(*Config){
		"no certificates": func(c *Config) { c.Certificates = nil },
		"no entity":       func(c *Config) { c.EntityID = "" },
		"no acs":          func(c *Config) { c.ACSURL = "" },
		"no sp entity":    func(c *Config) { c.SPEntityID = "" },
		"skew too large":  func(c *Config) { c.ClockSkew = 6 * time.Minute },
		"negative skew":   func(c *Config) { c.ClockSkew = -1 },
		"transient":       func(c *Config) { c.NameIDFormat = "transient" },
	} {
		t.Run(name, func(t *testing.T) {
			config := good
			config.Certificates = slices.Clone(good.Certificates)
			mutate(&config)
			if _, err := NewVerifier(config); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	config := good
	config.Now, config.MaxResponseBytes, config.NameIDFormat = nil, 0, ""
	if _, err := NewVerifier(config); err != nil {
		t.Fatal(err)
	}
	for format, uri := range map[string]string{"": nameIDURIPersistent, NameIDPersistent: nameIDURIPersistent, NameIDEmailAddress: nameIDURIEmail, NameIDUnspecified: nameIDURIUnspecific} {
		if got, ok := NameIDFormatURI(format); !ok || got != uri {
			t.Fatalf("NameIDFormatURI(%q) = %q, %v", format, got, ok)
		}
	}
	if _, ok := NameIDFormatURI("transient"); ok {
		t.Fatal("transient accepted")
	}
}

func TestReasonOf(t *testing.T) {
	if ReasonOf(errors.New("x")) != "" || ReasonOf(nil) != "" {
		t.Fatal("non-rejection has a reason")
	}
	if ReasonOf(reject(ReasonUnsigned)) != ReasonUnsigned {
		t.Fatal("reason lost")
	}
}

var _ = x509.Certificate{}

// TestVerifyCertificateRollover pins two certificates: a response signed by either verifies, and once
// one is removed from the pinned list a response signed by it is refused.
func TestVerifyCertificateRollover(t *testing.T) {
	primary, other, _ := testKeys(t)
	both := baseConfig(t)
	both.Certificates = []*x509.Certificate{primary.Cert, other.Cert}
	for name, key := range map[string]*samltest.Key{"current": &primary, "next": &other} {
		if _, err := verify(t, both, buildResponse(t, responseOptions{key: key})); err != nil {
			t.Fatalf("signed by the %s certificate: %v (%s)", name, err, ReasonOf(err))
		}
	}
	retired := baseConfig(t)
	retired.Certificates = []*x509.Certificate{primary.Cert}
	_, err := verify(t, retired, buildResponse(t, responseOptions{key: &other}))
	wantReason(t, err, ReasonSignatureInvalid)
	if _, err := verify(t, retired, buildResponse(t, responseOptions{key: &primary})); err != nil {
		t.Fatalf("the remaining certificate must keep working: %v", err)
	}
}
