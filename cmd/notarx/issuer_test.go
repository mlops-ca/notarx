package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/upstreamauth"

	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
	"github.com/mlops-ca/notarx/internal/tokenissuer"
)

type fakeIssuerStore struct {
	*tokenissuer.MemoryStore
	settings   map[string]string
	registered map[string]repository.WorkloadIssuer
	lookupErr  error
	audits     []repository.AuditRecord
	auditErr   error
}

func (s *fakeIssuerStore) RecordAudit(_ context.Context, record repository.AuditRecord) error {
	s.audits = append(s.audits, record)
	return s.auditErr
}

func newFakeIssuerStore() *fakeIssuerStore {
	return &fakeIssuerStore{MemoryStore: tokenissuer.NewMemoryStore(), settings: map[string]string{}, registered: map[string]repository.WorkloadIssuer{}}
}

func (s *fakeIssuerStore) DeploymentID(context.Context) (string, error) { return "dep-1", nil }
func (s *fakeIssuerStore) Setting(_ context.Context, key string) (string, error) {
	if value, ok := s.settings[key]; ok {
		return value, nil
	}
	return "", repository.ErrSettingNotFound
}
func (s *fakeIssuerStore) SetSetting(_ context.Context, key, value string) error {
	s.settings[key] = value
	return nil
}
func (s *fakeIssuerStore) GetWorkloadIssuerByIssuer(_ context.Context, issuer string) (repository.WorkloadIssuer, error) {
	if s.lookupErr != nil {
		return repository.WorkloadIssuer{}, s.lookupErr
	}
	if registered, ok := s.registered[issuer]; ok {
		return registered, nil
	}
	return repository.WorkloadIssuer{}, repository.ErrNotFound
}

func enabledIssuer() config.IssuerConfig {
	return config.IssuerConfig{
		Enabled: true, URL: "https://issuer.example.test", Alg: "RS256", EntraAuthorityHost: config.DefaultEntraAuthorityHost,
		KeyRotationInterval: 720 * 3600e9, KeyPublishLead: 24 * 3600e9, MaxTokenLifetime: 600e9,
	}
}

func TestSetupIssuerDisabledIsInert(t *testing.T) {
	store := newFakeIssuerStore()
	runtime, err := setupIssuer(t.Context(), config.IssuerConfig{}, store, nil)
	if runtime != nil || err != nil || len(store.settings) != 0 {
		t.Fatalf("runtime=%v err=%v settings=%v", runtime, err, store.settings)
	}
}

func TestSetupIssuerRequiresTheEncryptionKey(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "")
	if _, err := setupIssuer(t.Context(), enabledIssuer(), newFakeIssuerStore(), nil); err == nil || !strings.Contains(err.Error(), "encryption key") {
		t.Fatalf("err=%v", err)
	}
}

func TestSetupIssuerRecordsURLAndBuildsRuntime(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	t.Cleanup(func() {
		upstreamauth.SetEntra(nil)
		repository.AllowEntraWorkload(false)
	})
	store := newFakeIssuerStore()
	runtime, err := setupIssuer(t.Context(), enabledIssuer(), store, nil)
	if err != nil || runtime == nil || runtime.DeploymentID != "dep-1" {
		t.Fatalf("runtime=%v err=%v", runtime, err)
	}
	if store.settings[repository.SettingIssuerURL] != "https://issuer.example.test" {
		t.Fatalf("settings=%v", store.settings)
	}
}

func TestCheckIssuerURLRejectsRegisteredIssuerUnlessSelfTrust(t *testing.T) {
	store := newFakeIssuerStore()
	store.registered["https://issuer.example.test"] = repository.WorkloadIssuer{Name: "peer"}
	cfg := enabledIssuer()
	if err := checkIssuerURL(t.Context(), cfg, store); err == nil || !strings.Contains(err.Error(), config.IssuerAllowSelfTrustEnv) {
		t.Fatalf("err=%v", err)
	}
	cfg.AllowSelfTrust = true
	if err := checkIssuerURL(t.Context(), cfg, store); err != nil {
		t.Fatalf("explicit self-trust is allowed: %v", err)
	}
}

func TestCheckIssuerURLTracksChangesAndSurfacesStoreErrors(t *testing.T) {
	store := newFakeIssuerStore()
	store.settings[repository.SettingIssuerURL] = "https://old.example.test"
	if err := checkIssuerURL(t.Context(), enabledIssuer(), store); err != nil {
		t.Fatal(err)
	}
	if store.settings[repository.SettingIssuerURL] != "https://issuer.example.test" {
		t.Fatalf("the new URL must be recorded: %v", store.settings)
	}
	store.lookupErr = errors.New("database down")
	if err := checkIssuerURL(t.Context(), enabledIssuer(), store); err == nil {
		t.Fatal("a failing registry lookup must fail startup, not skip the check")
	}
}

func TestEntraAuditRecordCarriesSubjectTenantClientAndRepositoryButNoToken(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	record := entraAuditRecord(upstreamauth.EntraExchange{
		Operation: "entra.token", Subject: "notarx:instance:dep", TenantID: "tenant", ClientID: "client", Repository: "azure-blobs",
		Scope: "scope", KeyID: "kid-1", JTI: "jti-1", Succeeded: true,
	}, now)
	if record.Repository != "azure-blobs" || record.Operation != "upstream.entra.token" || record.Outcome != repository.AuditResolved || !record.OccurredAt.Equal(now) {
		t.Fatalf("%+v", record)
	}
	for key, want := range map[string]string{"subject": "notarx:instance:dep", "tenantId": "tenant", "clientId": "client", "kid": "kid-1", "jti": "jti-1"} {
		if record.Evidence[key] != want {
			t.Errorf("evidence[%s] = %q want %q", key, record.Evidence[key], want)
		}
	}
	failed := entraAuditRecord(upstreamauth.EntraExchange{Operation: "entra.token", Repository: "azure-blobs"}, now)
	if failed.Outcome != repository.AuditUpstreamError || failed.Status != http.StatusBadGateway {
		t.Fatalf("a rejected exchange must not be recorded as resolved: %+v", failed)
	}
	acr := entraAuditRecord(upstreamauth.EntraExchange{Operation: "acr.access", Registry: "r.azurecr.io"}, now)
	if acr.Evidence["registry"] != "r.azurecr.io" || acr.Evidence["jti"] != "" {
		t.Fatalf("%+v", acr.Evidence)
	}
}

func TestSetupIssuerInstallsTheEntraSchemeAndAuditsExchanges(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	t.Cleanup(func() {
		upstreamauth.SetEntra(nil)
		repository.AllowEntraWorkload(false)
	})
	store := newFakeIssuerStore()
	cfg := enabledIssuer()
	if _, err := setupIssuer(t.Context(), cfg, store, nil); err != nil {
		t.Fatal(err)
	}
	auth := &repository.UpstreamAuth{Scheme: repository.UpstreamAuthSchemeEntraWorkload, TenantID: "contoso.onmicrosoft.com",
		ClientID: "11111111-2222-3333-4444-555555555555", Scope: "https://storage.example.test/.default"}
	if err := auth.Validate(); err != nil {
		t.Fatalf("the scheme is accepted once the issuer runs: %v", err)
	}
	// Validate checks configuration only, so it passes before the rotation worker created a key.
	if err := upstreamauth.Validate(auth); err != nil {
		t.Fatalf("configuration is valid: %v", err)
	}
	// Without a signing key an exchange fails closed and sends nothing.
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://blob.example.test/x", nil)
	if err := upstreamauth.Apply(request, auth); !errors.Is(err, upstreamauth.ErrCredential) {
		t.Fatalf("err=%v", err)
	}
	if len(store.audits) != 0 {
		t.Fatalf("nothing is audited when no assertion was signed: %v", store.audits)
	}
}
