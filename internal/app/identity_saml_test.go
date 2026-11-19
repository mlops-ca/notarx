package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/identity/saml"
	"github.com/mlops-ca/notarx/internal/identity/saml/samltest"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
)

type stubFetcher struct {
	body []byte
	err  error
	urls []string
}

func (s *stubFetcher) Fetch(_ context.Context, rawURL string) ([]byte, error) {
	s.urls = append(s.urls, rawURL)
	return s.body, s.err
}

func idpMetadata(entityID, sso string, keys ...samltest.Key) []byte {
	var certificates strings.Builder
	for _, key := range keys {
		certificates.WriteString(`<md:KeyDescriptor use="signing"><ds:KeyInfo><ds:X509Data><ds:X509Certificate>` + base64.StdEncoding.EncodeToString(key.Cert.Raw) + `</ds:X509Certificate></ds:X509Data></ds:KeyInfo></md:KeyDescriptor>`)
	}
	return []byte(`<md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" xmlns:ds="` + samltest.NSDSig + `" entityID="` + entityID + `"><md:IDPSSODescriptor>` + certificates.String() +
		`<md:SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="` + sso + `"/></md:IDPSSODescriptor></md:EntityDescriptor>`)
}

func samlManagementGateway(t *testing.T, fetcher samlMetadataFetcher) (http.Handler, *repository.MemoryStore, *IdentityRuntime) {
	t.Helper()
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	store := repository.NewMemoryStore()
	runtime := NewIdentityRuntime(store)
	publicURL, _ := url.Parse("https://notarx.example.test")
	runtime.ConfigureSAML(SAMLRuntimeOptions{Config: config.DefaultSAMLConfig(), PublicURL: publicURL, State: store, Fetcher: fetcher, Now: func() time.Time { return samlTestNow }})
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{IdentityRuntime: runtime}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	return handler, store, runtime
}

func callManagement(t *testing.T, handler http.Handler, method, path, body, ifMatch string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/api/v2"+path, strings.NewReader(body))
	authorize(request, "admin-secret")
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestSAMLProviderManagementLifecycle(t *testing.T) {
	old, next := samltest.NewRSAKey(t, "old"), samltest.NewRSAKey(t, "next")
	fetcher := &stubFetcher{body: idpMetadata("https://idp.example.test/new", "https://idp.example.test/new-sso", old, next)}
	handler, store, _ := samlManagementGateway(t, fetcher)

	body := `{"slug":"corp","kind":"saml","displayName":"Corp","enabled":true,"saml":{"entityId":"https://idp.example.test/metadata","ssoUrl":"https://idp.example.test/sso","certificates":[{"pem":` +
		jsonString(saml.CertificatePEM(old.Cert)) + `}],"signRequests":true},"provisioningMode":"jit"}`
	created := callManagement(t, handler, http.MethodPost, "/authentication/providers", body, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	if text := created.Body.String(); strings.Contains(text, "PRIVATE") || strings.Contains(strings.ToLower(text), "privatekey") || strings.Contains(text, "secretsSealed") {
		t.Fatalf("the response leaks key material: %s", text)
	}
	var view struct {
		ID   string `json:"id"`
		Saml struct {
			EntityID     string `json:"entityId"`
			SpEntityID   string `json:"spEntityId"`
			ACSURL       string `json:"acsUrl"`
			NameIDFormat string `json:"nameIdFormat"`
			Certificates []struct {
				NotAfter time.Time `json:"notAfter"`
				State    string    `json:"state"`
			} `json:"certificates"`
			SPKeys []struct {
				ID string `json:"id"`
			} `json:"spKeys"`
		} `json:"saml"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &view); err != nil || view.ID == "" {
		t.Fatalf("created = %+v, %v", view, err)
	}
	if view.Saml.SpEntityID != samlTestSP || view.Saml.ACSURL != samlTestACS || view.Saml.NameIDFormat != "persistent" ||
		len(view.Saml.Certificates) != 1 || view.Saml.Certificates[0].State != "active" || !view.Saml.Certificates[0].NotAfter.Equal(old.Cert.NotAfter.UTC()) || len(view.Saml.SPKeys) != 1 {
		t.Fatalf("view = %+v", view.Saml)
	}
	stored, err := store.GetIdentityProvider(t.Context(), view.ID)
	if err != nil || stored.SecretsSealed == "" || strings.Contains(stored.SecretsSealed, "PRIVATE") {
		t.Fatalf("the key set must be stored sealed: %v", err)
	}
	firstKey := view.Saml.SPKeys[0].ID

	// Import from a URL: the endpoint is replaced, the pinned certificate stays active, the new one is next.
	imported := callManagement(t, handler, http.MethodPost, "/authentication/providers/"+view.ID+":import-metadata", `{"url":"https://idp.example.test/metadata.xml"}`, "")
	if imported.Code != http.StatusOK || len(fetcher.urls) != 1 {
		t.Fatalf("import = %d %s", imported.Code, imported.Body.String())
	}
	if err := json.Unmarshal(imported.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Saml.EntityID != "https://idp.example.test/new" || len(view.Saml.Certificates) != 2 || view.Saml.Certificates[0].State != "active" || view.Saml.Certificates[1].State != "next" {
		t.Fatalf("after import = %+v", view.Saml)
	}
	settings, _ := mustSAML(t, store, view.ID)
	if settings.SSOURL != "https://idp.example.test/new-sso" || settings.MetadataURL != "https://idp.example.test/metadata.xml" {
		t.Fatalf("settings = %+v", settings)
	}
	// Importing the same metadata again changes no certificate.
	callManagement(t, handler, http.MethodPost, "/authentication/providers/"+view.ID+":import-metadata", `{"url":"https://idp.example.test/metadata.xml"}`, "")
	if settings, _ := mustSAML(t, store, view.ID); len(settings.Certificates) != 2 {
		t.Fatalf("a repeated import duplicated certificates: %d", len(settings.Certificates))
	}

	rotated := callManagement(t, handler, http.MethodPost, "/authentication/providers/"+view.ID+":rotate-sp-key", "", "")
	if rotated.Code != http.StatusOK || strings.Contains(rotated.Body.String(), "PRIVATE") {
		t.Fatalf("rotate = %d %s", rotated.Code, rotated.Body.String())
	}
	if err := json.Unmarshal(rotated.Body.Bytes(), &view); err != nil || len(view.Saml.SPKeys) != 2 || view.Saml.SPKeys[0].ID != firstKey {
		t.Fatalf("after rotation both keys are published: %+v %v", view.Saml.SPKeys, err)
	}

	// Replace keeps the sealed keys, and refuses the OIDC object.
	etag := rotated.Header().Get("ETag")
	replace := `{"displayName":"Corp","enabled":true,"saml":{"entityId":"https://idp.example.test/new","ssoUrl":"https://idp.example.test/new-sso","certificates":[{"pem":` + jsonString(saml.CertificatePEM(next.Cert)) + `,"state":"active"}],"signRequests":true},"attributeMapping":{},"provisioningMode":"jit","emailLinkingEnabled":false,"jitDefaultRole":"none"}`
	replaced := callManagement(t, handler, http.MethodPut, "/authentication/providers/"+view.ID, replace, etag)
	if replaced.Code != http.StatusOK {
		t.Fatalf("replace = %d %s", replaced.Code, replaced.Body.String())
	}
	if err := json.Unmarshal(replaced.Body.Bytes(), &view); err != nil || len(view.Saml.SPKeys) != 2 || len(view.Saml.Certificates) != 1 {
		t.Fatalf("replace must keep the keys and drop the removed certificate: %+v %v", view.Saml, err)
	}
	wrong := strings.Replace(replace, `"saml":`, `"oidc":{"issuer":"https://x","audience":"a","clientId":"","redirectUrl":"","scopes":[],"adminSubjects":[],"memberRoles":[],"adminRoles":[]},"saml":`, 1)
	if response := callManagement(t, handler, http.MethodPut, "/authentication/providers/"+view.ID, wrong, replaced.Header().Get("ETag")); response.Code != http.StatusBadRequest {
		t.Fatalf("both settings objects accepted: %d", response.Code)
	}

	// Validation errors.
	for name, bad := range map[string]string{
		"no saml object":        `{"slug":"a-saml","kind":"saml","displayName":"A","enabled":false}`,
		"bad certificate":       `{"slug":"a-saml","kind":"saml","displayName":"A","enabled":false,"saml":{"entityId":"e","ssoUrl":"https://idp.example.test/sso","certificates":[{"pem":"nope"}]}}`,
		"http sso":              `{"slug":"a-saml","kind":"saml","displayName":"A","enabled":false,"saml":{"entityId":"e","ssoUrl":"http://idp.example.test/sso","certificates":[]}}`,
		"idp initiated no list": `{"slug":"a-saml","kind":"saml","displayName":"A","enabled":false,"saml":{"entityId":"e","ssoUrl":"https://idp.example.test/sso","certificates":[],"allowIdpInitiated":true}}`,
		"transient":             `{"slug":"a-saml","kind":"saml","displayName":"A","enabled":false,"saml":{"entityId":"e","ssoUrl":"https://idp.example.test/sso","certificates":[],"nameIdFormat":"transient"}}`,
		"enabled incomplete":    `{"slug":"a-saml","kind":"saml","displayName":"A","enabled":true,"saml":{"entityId":"e","ssoUrl":"https://idp.example.test/sso","certificates":[]}}`,
	} {
		if response := callManagement(t, handler, http.MethodPost, "/authentication/providers", bad, ""); response.Code != http.StatusBadRequest {
			t.Fatalf("%s accepted: %d %s", name, response.Code, response.Body.String())
		}
	}
	// Metadata import and rotation refuse non-SAML providers and bad sources.
	oidc := callManagement(t, handler, http.MethodPost, "/authentication/providers", `{"slug":"oidc-one","kind":"oidc","displayName":"O","enabled":false,"oidc":{"issuer":"","audience":"","clientId":"","redirectUrl":"","scopes":[],"adminSubjects":[],"memberRoles":[],"adminRoles":[]}}`, "")
	var oidcView struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(oidc.Body.Bytes(), &oidcView)
	for name, request := range map[string][3]string{
		"oidc import":    {oidcView.ID + ":import-metadata", `{"xml":"<x/>"}`, "400"},
		"oidc rotate":    {oidcView.ID + ":rotate-sp-key", "", "400"},
		"neither":        {view.ID + ":import-metadata", `{}`, "400"},
		"both":           {view.ID + ":import-metadata", `{"url":"https://a.example.test/","xml":"<x/>"}`, "400"},
		"bad xml":        {view.ID + ":import-metadata", `{"xml":"<<"}`, "400"},
		"unknown":        {"00000000-0000-7000-8000-000000000999:import-metadata", `{"xml":"<x/>"}`, "404"},
		"unknown rotate": {"00000000-0000-7000-8000-000000000999:rotate-sp-key", "", "404"},
	} {
		response := callManagement(t, handler, http.MethodPost, "/authentication/providers/"+request[0], request[1], "")
		if want := map[string]int{"400": 400, "404": 404}[request[2]]; response.Code != want {
			t.Fatalf("%s = %d, want %d: %s", name, response.Code, want, response.Body.String())
		}
	}
	fetcher.err = &saml.MetadataError{Message: "the metadata could not be fetched"}
	if response := callManagement(t, handler, http.MethodPost, "/authentication/providers/"+view.ID+":import-metadata", `{"url":"https://blocked.example.test/"}`, ""); response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "could not be fetched") {
		t.Fatalf("fetch failure = %d %s", response.Code, response.Body.String())
	}
	fetcher.err = errors.New("network down")
	if response := callManagement(t, handler, http.MethodPost, "/authentication/providers/"+view.ID+":import-metadata", `{"url":"https://blocked.example.test/"}`, ""); response.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected fetch failure = %d", response.Code)
	}
}

func mustSAML(t *testing.T, store *repository.MemoryStore, id string) (identity.SAMLSettings, identity.Provider) {
	t.Helper()
	provider, err := store.GetIdentityProvider(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := provider.SAML()
	if err != nil {
		t.Fatal(err)
	}
	return settings, provider
}

func TestSAMLSPKeySurvivesAReseal(t *testing.T) {
	const oldKey, newKey = "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"
	t.Setenv(secrets.KeyEnv, oldKey)
	keys, err := saml.SPKeySet{}.Rotated("corp", samlTestNow)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := keys.Seal()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(secrets.KeyEnv, newKey)
	t.Setenv(secrets.KeyPreviousEnv, oldKey)
	resealed, changed, err := secrets.Reseal(identity.SecretPurpose, sealed)
	if err != nil || !changed {
		t.Fatalf("reseal = %v, %v", changed, err)
	}
	t.Setenv(secrets.KeyPreviousEnv, "")
	opened, err := saml.OpenSPKeySet(resealed)
	if err != nil || len(opened.Keys) != 1 || opened.Keys[0].ID != keys.Keys[0].ID || opened.Keys[0].PrivateKeyPEM != keys.Keys[0].PrivateKeyPEM {
		t.Fatalf("after the reseal the key set is %+v, %v", opened, err)
	}
	if _, err := saml.OpenSPKeySet(sealed); err == nil {
		t.Fatal("the old ciphertext must no longer open once the old key is retired")
	}
}

func TestSAMLMetadataRefreshAddsNextCertificates(t *testing.T) {
	old, next := samltest.NewRSAKey(t, "old"), samltest.NewRSAKey(t, "next")
	fetcher := &stubFetcher{body: idpMetadata(samlTestEntityID, "https://idp.example.test/sso", old, next)}
	_, store, runtime := samlManagementGateway(t, fetcher)
	create := func(slug string, refresh bool) identity.Provider {
		provider, err := runtime.Create(t.Context(), IdentityProviderInput{
			Slug: slug, Kind: identity.KindSAML, DisplayName: slug,
			SAML: &identity.SAMLSettings{EntityID: samlTestEntityID, SSOURL: "https://idp.example.test/sso", Certificates: []identity.SAMLCertificate{{PEM: saml.CertificatePEM(old.Cert)}},
				MetadataURL: "https://idp.example.test/metadata.xml", MetadataRefresh: refresh},
		})
		if err != nil {
			t.Fatal(err)
		}
		return provider
	}
	on, off := create("refresh-on", true), create("refresh-off", false)
	runtime.RefreshSAMLMetadata(t.Context())
	if settings, _ := mustSAML(t, store, on.ID); len(settings.Certificates) != 2 || settings.Certificates[1].State != identity.SAMLCertificateNext {
		t.Fatalf("refresh on: %+v", settings.Certificates)
	}
	if settings, _ := mustSAML(t, store, off.ID); len(settings.Certificates) != 1 {
		t.Fatalf("refresh is off by default: %+v", settings.Certificates)
	}
	if len(fetcher.urls) != 1 {
		t.Fatalf("fetched %d times, want only the opted-in provider", len(fetcher.urls))
	}
	// A failing fetch is logged and leaves the provider untouched.
	fetcher.err = errors.New("down")
	runtime.RefreshSAMLMetadata(t.Context())
	// A zero interval never starts the loop; a cancelled context stops it.
	runtime.RunSAMLMetadataRefresh(t.Context(), 0)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	runtime.RunSAMLMetadataRefresh(ctx, time.Hour)
}

func TestSAMLEndpointsNeedAPublicURL(t *testing.T) {
	runtime := NewIdentityRuntime(repository.NewMemoryStore())
	if _, ok := runtime.SAMLEndpoints("corp"); ok {
		t.Fatal("endpoints without NOTARX_PUBLIC_URL")
	}
	if view := runtime.SAMLView(identity.Provider{Slug: "corp"}); view.Endpoints.ACSURL != "" || len(view.Keys) != 0 {
		t.Fatalf("view = %+v", view)
	}
}

type auditCapturingStore struct {
	*repository.MemoryStore
	audits []repository.AuditRecord
}

func (s *auditCapturingStore) ReplaceIdentityProvider(ctx context.Context, provider identity.Provider, version int64, audit ...repository.AuditRecord) (identity.Provider, error) {
	s.audits = append(s.audits, audit...)
	return s.MemoryStore.ReplaceIdentityProvider(ctx, provider, version, audit...)
}

func TestSAMLMetadataRefreshIsAuditedAndSurvivesTheCertificateCap(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	store := &auditCapturingStore{MemoryStore: repository.NewMemoryStore()}
	runtime := NewIdentityRuntime(store)
	next := samltest.NewRSAKey(t, "next")
	fetcher := &stubFetcher{body: idpMetadata(samlTestEntityID, "https://idp.example.test/sso", next)}
	runtime.ConfigureSAML(SAMLRuntimeOptions{Config: config.DefaultSAMLConfig(), State: store, Fetcher: fetcher, Now: func() time.Time { return samlTestNow }})
	// Eight pinned certificates, seven of them already expired: the new one must still fit.
	var pinned []identity.SAMLCertificate
	for i := range identity.MaxSAMLCertificates {
		key := samltest.NewRSAKey(t, "old")
		entry := identity.SAMLCertificate{PEM: saml.CertificatePEM(key.Cert)}
		if i > 0 {
			entry.NotAfter = samlTestNow.Add(-time.Hour)
		}
		pinned = append(pinned, entry)
	}
	provider, err := runtime.Create(t.Context(), IdentityProviderInput{
		Slug: "full", Kind: identity.KindSAML, DisplayName: "Full",
		SAML: &identity.SAMLSettings{EntityID: samlTestEntityID, SSOURL: "https://idp.example.test/sso", Certificates: pinned, MetadataURL: "https://idp.example.test/m", MetadataRefresh: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Normalisation derives notAfter from the PEM (ten years out), so mark seven as expired in the stored settings.
	settings, _ := provider.SAML()
	for i := 1; i < len(settings.Certificates); i++ {
		settings.Certificates[i].NotAfter = samlTestNow.Add(-time.Hour)
	}
	stored, _ := provider.WithSAML(settings)
	if _, err := store.MemoryStore.ReplaceIdentityProvider(t.Context(), stored, provider.Version); err != nil {
		t.Fatal(err)
	}
	runtime.invalidate()
	runtime.RefreshSAMLMetadata(t.Context())
	if len(store.audits) != 1 || store.audits[0].Operation != "authentication.provider.refresh-metadata" || store.audits[0].Actor != "system:saml-metadata-refresh" || store.audits[0].Class != repository.AuditClassSync {
		t.Fatalf("audits = %+v", store.audits)
	}
	refreshed, _ := runtime.Get(t.Context(), provider.ID)
	after, _ := refreshed.SAML()
	if len(after.Certificates) != 2 || after.Certificates[1].State != identity.SAMLCertificateNext {
		t.Fatalf("certificates after refresh = %d", len(after.Certificates))
	}
}

func TestSAMLChooserHidesProvidersUntilSAMLIsReady(t *testing.T) {
	f := newSAMLFixture(t, nil)
	public, err := f.runtime.Public(t.Context())
	if err != nil || len(public) != 1 || public[0].Kind != identity.KindSAML {
		t.Fatalf("ready = %+v, %v", public, err)
	}
	f.runtime.saml.publicURL = nil
	f.runtime.invalidate()
	if public, _ := f.runtime.Public(t.Context()); len(public) != 0 {
		t.Fatalf("without NOTARX_PUBLIC_URL the chooser lists %+v", public)
	}
}

func TestSAMLKeySetsAreCachedPerProviderVersion(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	runtime := NewIdentityRuntime(repository.NewMemoryStore())
	keys, err := saml.SPKeySet{}.Rotated("corp", samlTestNow)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := keys.Seal()
	provider := identity.Provider{ID: "p", Version: 1, SecretsSealed: sealed}
	first, err := runtime.spKeys(provider)
	if err != nil || len(first.Keys) != 1 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	// The same (id, version) is served from the cache even if the ciphertext is no longer readable.
	t.Setenv(secrets.KeyEnv, "fedcba9876543210fedcba9876543210")
	if again, err := runtime.spKeys(provider); err != nil || again.Keys[0].ID != first.Keys[0].ID {
		t.Fatalf("cached = %+v, %v", again, err)
	}
	provider.Version = 2
	if _, err := runtime.spKeys(provider); err == nil {
		t.Fatal("a new version must reopen the sealed set, which the changed key cannot read")
	}
}

func TestMergeSAMLMetadataRefusesBadMetadata(t *testing.T) {
	provider, err := identity.Provider{Slug: "corp", Kind: identity.KindSAML, DisplayName: "Corp"}.WithSAML(identity.SAMLSettings{})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"<<", "", `<a xmlns="urn:x"/>`} {
		if _, err := mergeSAMLMetadata(provider, []byte(raw), "", samlTestNow); !errors.Is(err, identity.ErrInvalid) {
			t.Fatalf("%q = %v, want an invalid-provider error", raw, err)
		}
	}
}

func TestSAMLAdminLinkLetsAnExistingAccountSignIn(t *testing.T) {
	f := newSAMLFixture(t, func(in *IdentityProviderInput) { in.ProvisioningMode = identity.ProvisioningDisabled })
	user, err := f.store.CreateUser(t.Context(), repository.User{ID: ids.New(), Name: "grace", Email: "grace@example.test", Role: "member", State: repository.UserActive})
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{IdentityRuntime: f.runtime}, Store: f.store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	link := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v2/users/"+user.ID+"/identities", strings.NewReader(body))
		authorize(request, "admin-secret")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	if response := link(`{"issuer":"https://elsewhere.example.test/","subject":"G-1","providerId":"` + f.provider.ID + `"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("an issuer that is not the entity id = %d %s", response.Code, response.Body.String())
	}
	response := link(`{"issuer":"` + samlTestEntityID + `","subject":"G-1"}`)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"kind":"saml"`) {
		t.Fatalf("link by entity id = %d %s", response.Code, response.Body.String())
	}
	login := f.begin("/")
	params := redirectParams(t, f.post(&login, f.response(samltest.Response{RequestID: login.requestID, NameID: "G-1"}), login.relay))
	if params.Get("oidc") != "success" {
		t.Fatalf("the linked account must sign in: %v", params)
	}
	if page, _ := f.store.ListUsers(t.Context(), repository.UserListQuery{Limit: 10}); page.Total != 1 {
		t.Fatalf("users = %d, the sign-in must reuse the linked account", page.Total)
	}
}

func TestSAMLMetadataImportAcceptsLargePastedXML(t *testing.T) {
	key := samltest.NewRSAKey(t, "idp")
	handler, _, _ := samlManagementGateway(t, &stubFetcher{})
	create := `{"slug":"big","kind":"saml","displayName":"Big","enabled":false,"saml":{"entityId":"","ssoUrl":"","certificates":[]}}`
	created := callManagement(t, handler, http.MethodPost, "/authentication/providers", create, "")
	var view struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(created.Body.Bytes(), &view)
	padded := strings.Replace(string(idpMetadata(samlTestEntityID, "https://idp.example.test/sso", key)), "<md:IDPSSODescriptor>", "<!-- "+strings.Repeat("x", 200<<10)+" --><md:IDPSSODescriptor>", 1)
	body, _ := json.Marshal(map[string]string{"xml": padded})
	if response := callManagement(t, handler, http.MethodPost, "/authentication/providers/"+view.ID+":import-metadata", string(body), ""); response.Code != http.StatusOK {
		t.Fatalf("200 KiB of metadata = %d %s", response.Code, response.Body.String())
	}
}

type failingStateStore struct{ repository.SAMLStateStore }

func (failingStateStore) ConsumeSAMLRequestState(context.Context, string, string, time.Time) (bool, error) {
	return false, errors.New("database down")
}

func TestSAMLStoreOutagesAreNotAuditedAsDenials(t *testing.T) {
	f := newSAMLFixture(t, nil)
	login := f.begin("/")
	f.runtime.saml.state = failingStateStore{SAMLStateStore: f.store}
	params := redirectParams(t, f.post(&login, f.response(samltest.Response{RequestID: login.requestID}), login.relay))
	if params.Get("oidc_error") != "identity_unavailable" {
		t.Fatalf("params = %v", params)
	}
	record := f.audit.records[len(f.audit.records)-1]
	if record.Outcome != repository.AuditStorageError || record.AuthorizationReason != "state_store_unavailable" {
		t.Fatalf("record = %+v", record)
	}
}

func TestSAMLRateLimiter(t *testing.T) {
	limiter := &samlRateLimiter{}
	now := samlTestNow
	for i := range samlRateBurst {
		if !limiter.allow("a", now) {
			t.Fatalf("call %d refused inside the burst", i)
		}
	}
	if limiter.allow("a", now) {
		t.Fatal("the burst was exceeded")
	}
	if !limiter.allow("b", now) {
		t.Fatal("clients have separate budgets")
	}
	if !limiter.allow("a", now.Add(samlRateWindow/samlRateBurst+time.Millisecond)) {
		t.Fatal("tokens refill over time")
	}
	// A full table refuses new clients but keeps serving the tracked ones, and idle entries make room.
	full := &samlRateLimiter{buckets: map[string]samlBucket{}}
	for i := range samlRateMaxClients {
		full.buckets[strings.Repeat("x", 1)+string(rune('a'+i%26))+strings.Repeat("y", i/26)] = samlBucket{tokens: 5, seen: now}
	}
	if full.allow("new-client", now) {
		t.Fatal("a new client was admitted to a full table of active clients")
	}
	if !full.allow("new-client", now.Add(2*samlRateWindow)) {
		t.Fatal("idle entries must be evicted to admit a new client")
	}
}

func TestSAMLLoginIsRateLimitedPerPeer(t *testing.T) {
	f := newSAMLFixture(t, nil)
	f.handler.limiter = &samlRateLimiter{}
	call := func() int {
		request := httptest.NewRequest(http.MethodGet, "/auth/saml/corp/login", nil)
		request.SetPathValue("slug", "corp")
		request.RemoteAddr = "203.0.113.7:4444"
		// A forged forwarding header must not buy a fresh budget.
		request.Header.Set("X-Forwarded-For", "198.51.100."+strings.Repeat("1", 1))
		recorder := httptest.NewRecorder()
		f.handler.start(recorder, request)
		return recorder.Code
	}
	for i := range samlRateBurst {
		if code := call(); code != http.StatusFound {
			t.Fatalf("call %d = %d", i, code)
		}
	}
	if code := call(); code != http.StatusTooManyRequests {
		t.Fatalf("over the budget = %d", code)
	}
	// The ACS refuses with the console error code instead of a JSON problem.
	form := url.Values{"SAMLResponse": {"x"}}
	request := httptest.NewRequest(http.MethodPost, "/auth/saml/corp/acs", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetPathValue("slug", "corp")
	request.RemoteAddr = "203.0.113.7:4444"
	recorder := httptest.NewRecorder()
	f.handler.acs(recorder, request)
	if params := redirectParams(t, recorder); params.Get("oidc_error") != "rate_limited" {
		t.Fatalf("acs = %v", params)
	}
}
