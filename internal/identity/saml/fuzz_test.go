package saml

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"testing"
)

// FuzzVerify feeds arbitrary documents to the verifier. The invariants are the security
// properties of the wrapper: it never panics, every rejection is a *Error with a reason (and the
// generic text), and a document is accepted only when it still carries the one valid signed
// assertion, so a mutation can never change who the assertion is about.
func FuzzVerify(f *testing.F) {
	valid, err := base64.StdEncoding.DecodeString(buildResponse(f, responseOptions{}))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	for _, name := range []string{"keycloak-response.xml", "entra-response.xml"} {
		raw, err := os.ReadFile("testdata/" + name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Add([]byte("<!DOCTYPE x [<!ENTITY e \"v\">]><x/>"))
	f.Add([]byte(""))
	v, err := NewVerifier(baseConfig(f))
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, document []byte) {
		claims, err := v.Verify(context.Background(), Request{SAMLResponse: base64.StdEncoding.EncodeToString(document), InResponseTo: testRequestID})
		if err != nil {
			var rejected *Error
			if !errors.As(err, &rejected) || rejected.Reason == "" || err.Error() != "saml response rejected" {
				t.Fatalf("rejection without a reason: %v", err)
			}
			return
		}
		if claims.AssertionID != "_assertion-1" || claims.NameID != "user-1" || claims.Email != "ada@example.test" {
			t.Fatalf("accepted a document that asserts something else: %+v", claims)
		}
	})
}
