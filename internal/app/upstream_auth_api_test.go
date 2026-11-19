package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
)

const upstreamAuthTestKey = "0123456789abcdef0123456789abcdef"

func goProxyBody(upstreamAuth string) string {
	body := `{"name":"go-proxy","format":"go","type":"proxy","endpoint":"https://proxy.golang.org","allowedHosts":["proxy.golang.org"]`
	if upstreamAuth != "" {
		body += `,"upstreamAuth":` + upstreamAuth
	}
	return body + "}"
}

func TestRepositoryUpstreamAuthCreateRedactsAndEncrypts(t *testing.T) {
	t.Setenv("NOTARX_SETTINGS_ENCRYPTION_KEY", upstreamAuthTestKey)
	store := repository.NewMemoryStore()
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})

	r := httptest.NewRequest(http.MethodPost, "/api/v2/repositories", strings.NewReader(goProxyBody(`{"scheme":"bearer","secret":"ghp_s3cret"}`)))
	r.Header.Set("Idempotency-Key", "create-upstream-auth-1")
	authorize(r, "admin-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create=%d body=%s", rec.Code, rec.Body.String())
	}
	var response repository.HostedRepository
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.UpstreamAuth == nil || response.UpstreamAuth.Scheme != repository.UpstreamAuthSchemeBearer {
		t.Fatalf("response upstreamAuth=%+v", response.UpstreamAuth)
	}
	if !response.UpstreamAuth.CredentialsConfigured {
		t.Fatalf("expected credentialsConfigured: %+v", response.UpstreamAuth)
	}
	if response.UpstreamAuth.Secret != "" || strings.Contains(rec.Body.String(), "ghp_s3cret") {
		t.Fatalf("response leaks the upstream secret: %s", rec.Body.String())
	}

	persisted, err := store.GetHostedRepository(context.Background(), response.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.UpstreamAuth == nil || persisted.UpstreamAuth.Secret == "" || persisted.UpstreamAuth.Secret == "ghp_s3cret" {
		t.Fatalf("persisted secret not encrypted: %+v", persisted.UpstreamAuth)
	}
	if persisted.UpstreamAuth.CredentialsConfigured {
		t.Fatalf("response-only marker must not persist: %+v", persisted.UpstreamAuth)
	}
}

func TestRepositoryUpstreamAuthUpdateKeepsClearsAndSwitchesScheme(t *testing.T) {
	t.Setenv("NOTARX_SETTINGS_ENCRYPTION_KEY", upstreamAuthTestKey)
	store := repository.NewMemoryStore()
	repo, err := store.CreateHostedRepository(context.Background(), repository.HostedRepository{
		ID:           uuid.NewString(),
		Name:         "go-proxy",
		Format:       repository.FormatGo,
		Type:         repository.RepositoryTypeProxy,
		Endpoint:     "https://proxy.golang.org",
		AllowedHosts: []string{"proxy.golang.org"},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	patch := func(version, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPatch, "/api/v2/repositories/"+repo.ID, strings.NewReader(body))
		authorize(r, "admin-secret")
		r.Header.Set("If-Match", version)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec
	}

	configured := patch("1", `{"upstreamAuth":{"scheme":"basic","username":"gateway","secret":"s3cret"}}`)
	if configured.Code != http.StatusOK {
		t.Fatalf("configure=%d body=%s", configured.Code, configured.Body.String())
	}
	if strings.Contains(configured.Body.String(), "s3cret") || !strings.Contains(configured.Body.String(), `"credentialsConfigured":true`) {
		t.Fatalf("configure body=%s", configured.Body.String())
	}
	persisted, _ := store.GetHostedRepository(context.Background(), repo.ID)
	ciphertext := persisted.UpstreamAuth.Secret
	if ciphertext == "" || ciphertext == "s3cret" {
		t.Fatalf("stored secret=%q", ciphertext)
	}

	// Omitting the secret must reuse the stored one only while the scheme is
	// unchanged.
	kept := patch("2", `{"upstreamAuth":{"scheme":"basic","username":"gateway"}}`)
	if kept.Code != http.StatusOK {
		t.Fatalf("keep=%d body=%s", kept.Code, kept.Body.String())
	}
	persisted, _ = store.GetHostedRepository(context.Background(), repo.ID)
	if persisted.UpstreamAuth.Secret != ciphertext {
		t.Fatalf("secret not preserved across update: %+v", persisted.UpstreamAuth)
	}

	// Switching scheme without resubmitting the secret must fail loudly rather
	// than silently reuse a credential the operator did not intend.
	switched := patch("3", `{"upstreamAuth":{"scheme":"bearer"}}`)
	if switched.Code != http.StatusBadRequest {
		t.Fatalf("scheme switch without secret=%d body=%s", switched.Code, switched.Body.String())
	}

	// Both schemes require a secret, so removing the credential is expressed by
	// switching to none rather than by clearing the secret in place.
	removed := patch("3", `{"upstreamAuth":{"scheme":"none"}}`)
	if removed.Code != http.StatusOK {
		t.Fatalf("remove=%d body=%s", removed.Code, removed.Body.String())
	}
	if strings.Contains(removed.Body.String(), `"credentialsConfigured":true`) {
		t.Fatalf("remove body=%s", removed.Body.String())
	}
	persisted, _ = store.GetHostedRepository(context.Background(), repo.ID)
	if persisted.UpstreamAuth != nil {
		t.Fatalf("expected no stored upstreamAuth, got %+v", persisted.UpstreamAuth)
	}
}

func TestRepositoryUpstreamAuthFormatAdmission(t *testing.T) {
	t.Setenv("NOTARX_SETTINGS_ENCRYPTION_KEY", upstreamAuthTestKey)
	store := repository.NewMemoryStore()
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})

	// Newly implemented Proxy formats admit credentials and still redact secrets.
	post := func(idempotencyKey, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v2/repositories", strings.NewReader(body))
		r.Header.Set("Idempotency-Key", idempotencyKey)
		authorize(r, "admin-secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec
	}
	pypi := post("admit-pypi", `{"name":"pypi-proxy","format":"pypi","type":"proxy","endpoint":"https://pypi.org","allowedHosts":["pypi.org"],"upstreamAuth":{"scheme":"bearer","secret":"s3cret"}}`)
	if pypi.Code != http.StatusCreated || strings.Contains(pypi.Body.String(), "s3cret") {
		t.Fatalf("pypi upstreamAuth=%d body=%s", pypi.Code, pypi.Body.String())
	}

	// Hosted repositories reject proxy-only settings as before.
	hosted := post("reject-hosted", `{"name":"go-hosted","format":"go","upstreamAuth":{"scheme":"bearer","secret":"s3cret"}}`)
	if hosted.Code != http.StatusBadRequest {
		t.Fatalf("hosted upstreamAuth=%d body=%s", hosted.Code, hosted.Body.String())
	}

	// The same explicit admission applies on update.
	repo, err := store.CreateHostedRepository(context.Background(), repository.HostedRepository{
		ID: uuid.NewString(), Name: "npm-proxy", Format: repository.FormatNPM, Type: repository.RepositoryTypeProxy,
		Endpoint: "https://registry.npmjs.org", AllowedHosts: []string{"registry.npmjs.org"},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPatch, "/api/v2/repositories/"+repo.ID, strings.NewReader(`{"upstreamAuth":{"scheme":"bearer","secret":"s3cret"}}`))
	authorize(r, "admin-secret")
	r.Header.Set("If-Match", "1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatalf("npm update upstreamAuth=%d body=%s", rec.Code, rec.Body.String())
	}
	hostedRepo, err := store.CreateHostedRepository(t.Context(), repository.HostedRepository{ID: uuid.NewString(), Name: "hosted-auth-update", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest(http.MethodPatch, "/api/v2/repositories/"+hostedRepo.ID, strings.NewReader(`{"upstreamAuth":{"scheme":"bearer","secret":"s3cret"}}`))
	authorize(r, "admin-secret")
	r.Header.Set("If-Match", hostedRepo.Version)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Hosted authentication update=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRepositoryUpstreamAuthValidation(t *testing.T) {
	t.Setenv("NOTARX_SETTINGS_ENCRYPTION_KEY", upstreamAuthTestKey)
	store := repository.NewMemoryStore()
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v2/repositories", strings.NewReader(body))
		r.Header.Set("Idempotency-Key", uuid.NewString())
		authorize(r, "admin-secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec
	}
	cases := []struct {
		name string
		body string
	}{
		{"basic without username", goProxyBody(`{"scheme":"basic","secret":"s3cret"}`)},
		{"basic without secret", goProxyBody(`{"scheme":"basic","username":"gateway"}`)},
		{"bearer with username", goProxyBody(`{"scheme":"bearer","username":"gateway","secret":"s3cret"}`)},
		{"unknown scheme", goProxyBody(`{"scheme":"oauth","secret":"s3cret"}`)},
		{"none with credentials", goProxyBody(`{"scheme":"none","secret":"s3cret"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := post(tc.body); rec.Code != http.StatusBadRequest {
				t.Fatalf("post=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRepositoryUpstreamAuthRequiresEncryptionKey(t *testing.T) {
	t.Setenv("NOTARX_SETTINGS_ENCRYPTION_KEY", "")
	store := repository.NewMemoryStore()
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	r := httptest.NewRequest(http.MethodPost, "/api/v2/repositories", strings.NewReader(goProxyBody(`{"scheme":"bearer","secret":"s3cret"}`)))
	r.Header.Set("Idempotency-Key", uuid.NewString())
	authorize(r, "admin-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "encryption key") {
		t.Fatalf("create=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestRepositoryUpstreamAuthPurposeIsBound pins the AAD binding: a ciphertext
// sealed for a different purpose must never open as an upstream credential.
func TestRepositoryUpstreamAuthPurposeIsBound(t *testing.T) {
	t.Setenv("NOTARX_SETTINGS_ENCRYPTION_KEY", upstreamAuthTestKey)
	foreign, err := secrets.Seal("webhook-signing-secret", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Open(upstreamAuthSecretPurpose, foreign); err == nil {
		t.Fatal("expected a foreign-purpose ciphertext to fail to open")
	}
	own, err := secrets.Seal(upstreamAuthSecretPurpose, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := secrets.Open(upstreamAuthSecretPurpose, own)
	if err != nil || opened != "s3cret" {
		t.Fatalf("own-purpose ciphertext must open: value=%q err=%v", opened, err)
	}
}

const entraAuthJSON = `{"scheme":"entra-workload","tenantId":"contoso.onmicrosoft.com","clientId":"11111111-2222-3333-4444-555555555555","scope":"https://storage.example.test/.default"}`

func rawProxyBody(name, upstreamAuth string) string {
	return `{"name":"` + name + `","format":"raw","type":"proxy","endpoint":"https://acct.blob.example.test/files","allowedHosts":["acct.blob.example.test"],"upstreamAuth":` + upstreamAuth + "}"
}

func TestRepositoryUpstreamAuthEntraWorkloadLifecycle(t *testing.T) {
	t.Setenv("NOTARX_SETTINGS_ENCRYPTION_KEY", upstreamAuthTestKey)
	repository.AllowEntraWorkload(true)
	t.Cleanup(func() { repository.AllowEntraWorkload(false) })
	store := repository.NewMemoryStore()
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	send := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if method == http.MethodPost {
			r.Header.Set("Idempotency-Key", uuid.NewString())
		} else {
			r.Header.Set("If-Match", "1")
		}
		authorize(r, "admin-secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec
	}
	created := send(http.MethodPost, "/api/v2/repositories", rawProxyBody("azure-blobs", entraAuthJSON))
	if created.Code != http.StatusCreated {
		t.Fatalf("create=%d body=%s", created.Code, created.Body.String())
	}
	var response repository.HostedRepository
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if auth := response.UpstreamAuth; auth == nil || auth.Scheme != repository.UpstreamAuthSchemeEntraWorkload || auth.TenantID != "contoso.onmicrosoft.com" ||
		auth.ClientID != "11111111-2222-3333-4444-555555555555" || !auth.CredentialsConfigured || auth.Secret != "" {
		t.Fatalf("response upstreamAuth=%+v", response.UpstreamAuth)
	}
	persisted, _ := store.GetHostedRepository(context.Background(), response.ID)
	if persisted.UpstreamAuth.Secret != "" {
		t.Fatal("an entra-workload credential stores no secret")
	}
	// Changing the endpoint must repeat upstreamAuth explicitly: there is no secret to resubmit.
	moved := `{"endpoint":"https://other.blob.example.test/files","allowedHosts":["other.blob.example.test"]}`
	if rec := send(http.MethodPatch, "/api/v2/repositories/"+response.ID, moved); rec.Code != http.StatusBadRequest {
		t.Fatalf("moving the token to a new destination silently: %d %s", rec.Code, rec.Body.String())
	}
	movedWithAuth := `{"endpoint":"https://other.blob.example.test/files","allowedHosts":["other.blob.example.test"],"upstreamAuth":` + entraAuthJSON + "}"
	if rec := send(http.MethodPatch, "/api/v2/repositories/"+response.ID, movedWithAuth); rec.Code != http.StatusOK {
		t.Fatalf("explicit rebind: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRepositoryUpstreamAuthEntraWorkloadAdmission(t *testing.T) {
	t.Setenv("NOTARX_SETTINGS_ENCRYPTION_KEY", upstreamAuthTestKey)
	store := repository.NewMemoryStore()
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v2/repositories", strings.NewReader(body))
		r.Header.Set("Idempotency-Key", uuid.NewString())
		authorize(r, "admin-secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec
	}
	if rec := post(rawProxyBody("a", entraAuthJSON)); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "NOTARX_ISSUER_ENABLED") {
		t.Fatalf("the scheme needs the issuer: %d %s", rec.Code, rec.Body.String())
	}
	repository.AllowEntraWorkload(true)
	t.Cleanup(func() { repository.AllowEntraWorkload(false) })
	for name, body := range map[string]string{
		"secret":                 rawProxyBody("b", `{"scheme":"entra-workload","secret":"s","tenantId":"t","clientId":"11111111-2222-3333-4444-555555555555","scope":"x"}`),
		"missing tenant":         rawProxyBody("c", `{"scheme":"entra-workload","clientId":"11111111-2222-3333-4444-555555555555","scope":"x"}`),
		"entra fields on bearer": rawProxyBody("d", `{"scheme":"bearer","secret":"s","tenantId":"t"}`),
		"conan":                  `{"name":"conan-azure","format":"conan","type":"proxy","endpoint":"https://conan.example.com","allowedHosts":["conan.example.com"],"upstreamAuth":` + entraAuthJSON + "}",
	} {
		if rec := post(body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := post(rawProxyBody("ok", entraAuthJSON)); rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
