package saml

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func metadataXML(t *testing.T, mutate func(string) string) string {
	t.Helper()
	primary, other, _ := testKeys(t)
	encoded := func(der []byte) string { return base64.StdEncoding.EncodeToString(der) }
	document := `<?xml version="1.0"?><md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" xmlns:ds="http://www.w3.org/2000/09/xmldsig#" entityID="https://idp.example.test/metadata">` +
		`<md:IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">` +
		`<md:KeyDescriptor use="signing"><ds:KeyInfo><ds:X509Data><ds:X509Certificate>` + encoded(primary.Cert.Raw) + `</ds:X509Certificate></ds:X509Data></ds:KeyInfo></md:KeyDescriptor>` +
		`<md:KeyDescriptor use="encryption"><ds:KeyInfo><ds:X509Data><ds:X509Certificate>` + encoded(other.Cert.Raw) + `</ds:X509Certificate></ds:X509Data></ds:KeyInfo></md:KeyDescriptor>` +
		`<md:SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" Location="https://idp.example.test/post"/>` +
		`<md:SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp.example.test/sso"/>` +
		`</md:IDPSSODescriptor></md:EntityDescriptor>`
	if mutate != nil {
		document = mutate(document)
	}
	return document
}

func TestParseIdPMetadata(t *testing.T) {
	primary, _, _ := testKeys(t)
	got, err := ParseIdPMetadata([]byte(metadataXML(t, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if got.EntityID != "https://idp.example.test/metadata" || got.SSOURL != "https://idp.example.test/sso" {
		t.Fatalf("metadata = %+v", got)
	}
	if len(got.Certificates) != 1 || !got.Certificates[0].Equal(primary.Cert) {
		t.Fatalf("only the signing certificate is taken, got %d", len(got.Certificates))
	}
	if !strings.Contains(CertificatePEM(got.Certificates[0]), "BEGIN CERTIFICATE") {
		t.Fatal("PEM encoding is wrong")
	}
	// An unspecified use counts as signing, and whitespace inside the base64 is tolerated.
	spaced := metadataXML(t, func(d string) string {
		d = strings.Replace(d, ` use="signing"`, "", 1)
		return strings.Replace(d, "<ds:X509Certificate>", "<ds:X509Certificate>\n  ", 1)
	})
	if got, err := ParseIdPMetadata([]byte(spaced)); err != nil || len(got.Certificates) != 1 {
		t.Fatalf("unspecified use = %+v, %v", got, err)
	}
}

func TestParseIdPMetadataEntitiesDescriptor(t *testing.T) {
	single := metadataXML(t, func(d string) string {
		body := d[strings.Index(d, "<md:EntityDescriptor"):]
		return `<md:EntitiesDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata">` + body + `</md:EntitiesDescriptor>`
	})
	if got, err := ParseIdPMetadata([]byte(single)); err != nil || got.EntityID == "" {
		t.Fatalf("one IdP in a federation = %+v, %v", got, err)
	}
	two := strings.Replace(single, "</md:EntitiesDescriptor>", single[strings.Index(single, "<md:EntityDescriptor"):], 1)
	if _, err := ParseIdPMetadata([]byte(two)); err == nil {
		t.Fatal("two IdPs accepted")
	}
}

func TestParseIdPMetadataRejectsHostileOrIncompleteInput(t *testing.T) {
	cases := map[string]struct {
		raw  func() string
		want string
	}{
		"empty":     {func() string { return "" }, "empty"},
		"too large": {func() string { return strings.Repeat("a", MaxMetadataBytes+1) }, "exceeds"},
		"dtd": {func() string {
			return `<!DOCTYPE x [<!ENTITY e "boom">]>` + metadataXML(t, func(d string) string { return strings.TrimPrefix(d, `<?xml version="1.0"?>`) })
		}, "document type"},
		"not xml":    {func() string { return "<<<" }, "well-formed"},
		"wrong root": {func() string { return `<a xmlns="urn:x"/>` }, "EntityDescriptor"},
		"no entityID": {func() string {
			return metadataXML(t, func(d string) string {
				return strings.Replace(d, ` entityID="https://idp.example.test/metadata"`, "", 1)
			})
		}, "entityID"},
		"no redirect binding": {func() string {
			return metadataXML(t, func(d string) string { return strings.Replace(d, "HTTP-Redirect", "SOAP", 1) })
		}, "HTTP-Redirect"},
		"no certificate": {func() string {
			return metadataXML(t, func(d string) string { return strings.Replace(d, `use="signing"`, `use="encryption"`, 1) })
		}, "signing certificate"},
		"garbage certificate": {func() string {
			return metadataXML(t, func(d string) string {
				i := strings.Index(d, "<ds:X509Certificate>") + len("<ds:X509Certificate>")
				return d[:i] + "bm90IGEgY2VydA==" + d[i+strings.Index(d[i:], "</ds:X509Certificate>"):]
			})
		}, "signing certificate"},
		"no idp descriptor": {func() string {
			return metadataXML(t, func(d string) string { return strings.ReplaceAll(d, "IDPSSODescriptor", "SPSSODescriptor") })
		}, "IDPSSODescriptor"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseIdPMetadata([]byte(tc.raw()))
			metadata, ok := err.(*MetadataError)
			if !ok || !strings.Contains(metadata.Message, tc.want) {
				t.Fatalf("error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestFetcherReadsMetadataOverHTTPS(t *testing.T) {
	body := metadataXML(t, nil)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(body))
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat("a", MaxMetadataBytes+1)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	fetcher := NewFetcherWithClient(server.Client())
	got, err := fetcher.Fetch(context.Background(), server.URL+"/ok")
	if err != nil || string(got) != body {
		t.Fatalf("fetch = %d bytes, %v", len(got), err)
	}
	for name, target := range map[string]string{
		"not found": server.URL + "/missing", "oversized": server.URL + "/big",
		"plain http":  strings.Replace(server.URL, "https://", "http://", 1) + "/ok",
		"credentials": strings.Replace(server.URL, "https://", "https://user:pass@", 1) + "/ok",
		"not a url":   "://", "unreachable": "https://127.0.0.1:1/",
	} {
		if _, err := fetcher.Fetch(context.Background(), target); err == nil {
			t.Fatalf("%s accepted", name)
		} else if _, ok := err.(*MetadataError); !ok {
			t.Fatalf("%s: %T is not a MetadataError", name, err)
		}
	}
}

// TestFetcherRefusesPrivateTargets uses the real SSRF-guarded client: loopback, private, link-local
// and unspecified addresses are refused at dial time before any request is sent.
func TestFetcherRefusesPrivateTargets(t *testing.T) {
	fetcher, err := NewFetcher()
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		"https://127.0.0.1/metadata", "https://[::1]/metadata", "https://10.0.0.5/metadata", "https://192.168.1.10/metadata",
		"https://172.16.0.1/metadata", "https://169.254.169.254/latest/meta-data", "https://[fe80::1]/metadata", "https://0.0.0.0/metadata",
		"https://100.64.0.1/metadata", "https://localhost/metadata",
	} {
		if _, err := fetcher.Fetch(context.Background(), target); err == nil {
			t.Fatalf("%s was fetched", target)
		}
	}
}
