package saml

import (
	"encoding/base64"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/beevik/etree"
)

// signFixture loads an unsigned IdP response shaped like the product's real output and signs the
// element picked by target ("Assertion" or "Response") with the test IdP key.
func signFixture(t *testing.T, name, target string) string {
	t.Helper()
	primary, _, _ := testKeys(t)
	doc := etree.NewDocument()
	if err := doc.ReadFromFile("testdata/" + name); err != nil {
		t.Fatal(err)
	}
	ctx := signer(t, primary, "")
	switch target {
	case "Assertion":
		assertion := doc.Root().FindElement("./Assertion")
		if assertion == nil {
			assertion = doc.Root().FindElement("./saml:Assertion")
		}
		signed := sign(t, ctx, assertion)
		index := assertion.Index()
		doc.Root().RemoveChildAt(index)
		doc.Root().InsertChildAt(index, signed)
	case "Response":
		doc.SetRoot(sign(t, ctx, doc.Root()))
	}
	out, err := doc.WriteToString()
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString([]byte(out))
}

func TestKeycloakSAMLClientResponse(t *testing.T) {
	config := baseConfig(t)
	config.EntityID = "https://keycloak.example.test/realms/notarx"
	config.ACSURL = "https://notarx.example.test/auth/saml/keycloak/acs"
	config.SPEntityID = "https://notarx.example.test/auth/saml/keycloak/metadata"
	config.Now = func() time.Time { return time.Date(2026, 11, 19, 10, 0, 30, 0, time.UTC) }
	claims, err := verify(t, config, signFixture(t, "keycloak-response.xml", "Assertion"), func(r *Request) { r.InResponseTo = "_0190d9c0-0000-7000-8000-000000000002" })
	if err != nil {
		t.Fatalf("%v (%s)", err, ReasonOf(err))
	}
	if claims.NameID != "G-5b0a5c64-5f64-4b3f-8d4a-000000000003" || claims.Email != "grace@example.test" || claims.Username != "grace" ||
		!slices.Equal(claims.Groups, []string{"default-roles-notarx", "notarx-admins"}) || claims.SessionIndex == "" {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestEntraIDResponse(t *testing.T) {
	config := baseConfig(t)
	config.EntityID = "https://sts.windows.net/72f988bf-86f1-41af-91ab-2d7cd011db47/"
	config.ACSURL = "https://notarx.example.test/auth/saml/entra/acs"
	config.SPEntityID = "https://notarx.example.test/auth/saml/entra/metadata"
	config.Now = func() time.Time { return time.Date(2026, 11, 19, 10, 0, 30, 0, time.UTC) }
	for _, target := range []string{"Assertion", "Response"} {
		t.Run("signed "+target, func(t *testing.T) {
			claims, err := verify(t, config, signFixture(t, "entra-response.xml", target), func(r *Request) { r.InResponseTo = "_0190d9c0-0000-7000-8000-000000000012" })
			if err != nil {
				t.Fatalf("%v (%s)", err, ReasonOf(err))
			}
			if claims.NameID != "Zq1x9yTn3k2Lw8vCmR5bHs0pUeJ4aG7dQfXoIuYtE6A" || claims.Email != "alan@contoso.test" || claims.Username != "alan@contoso.test" ||
				claims.DisplayName != "Alan Turing" || len(claims.Groups) != 2 {
				t.Fatalf("claims = %+v", claims)
			}
		})
	}
}

func TestFixtureTemplatesAreUnsigned(t *testing.T) {
	for _, name := range []string{"keycloak-response.xml", "entra-response.xml"} {
		raw, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		root, err := parseStructure(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := checkStructure(root); ReasonOf(err) != ReasonUnsigned {
			t.Fatalf("%s: structure check = %v, want unsigned", name, err)
		}
	}
}
