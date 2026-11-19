package repository

import (
	"strings"
	"testing"
)

func validEntraAuth() *UpstreamAuth {
	return &UpstreamAuth{
		Scheme: UpstreamAuthSchemeEntraWorkload, TenantID: "contoso.onmicrosoft.com",
		ClientID: "11111111-2222-3333-4444-555555555555", Scope: "https://storage.example.test/.default",
	}
}

func TestEntraWorkloadUpstreamAuthValidation(t *testing.T) {
	AllowEntraWorkload(true)
	t.Cleanup(func() { AllowEntraWorkload(false) })
	if err := validEntraAuth().Validate(); err != nil {
		t.Fatal(err)
	}
	full := validEntraAuth()
	full.Subject, full.RegistryKind = "notarx:service-account:acr-puller", RegistryKindACR
	if err := full.Validate(); err != nil {
		t.Fatal(err)
	}
	guid := validEntraAuth()
	guid.TenantID = "72f988bf-86f1-41af-91ab-2d7cd011db47"
	if err := guid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*UpstreamAuth){
		"secret":          func(a *UpstreamAuth) { a.Secret = "x" },
		"username":        func(a *UpstreamAuth) { a.Username = "u" },
		"no tenant":       func(a *UpstreamAuth) { a.TenantID = "" },
		"tenant path":     func(a *UpstreamAuth) { a.TenantID = "a/b" },
		"tenant dotdot":   func(a *UpstreamAuth) { a.TenantID = "../x" },
		"tenant too long": func(a *UpstreamAuth) { a.TenantID = strings.Repeat("a", 254) },
		"client not guid": func(a *UpstreamAuth) { a.ClientID = "not-a-guid" },
		"no scope":        func(a *UpstreamAuth) { a.Scope = "" },
		"scope space":     func(a *UpstreamAuth) { a.Scope = "a b" },
		"scope control":   func(a *UpstreamAuth) { a.Scope = "a\x01b" },
		"scope too long":  func(a *UpstreamAuth) { a.Scope = strings.Repeat("a", 513) },
		"subject chars":   func(a *UpstreamAuth) { a.Subject = "has space" },
		"registry kind":   func(a *UpstreamAuth) { a.RegistryKind = "ecr" },
	} {
		auth := validEntraAuth()
		mutate(auth)
		if auth.Validate() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestEntraWorkloadIsRejectedWhileTheIssuerIsDisabled(t *testing.T) {
	AllowEntraWorkload(false)
	err := validEntraAuth().Validate()
	if err == nil || !strings.Contains(err.Error(), "NOTARX_ISSUER_ENABLED") {
		t.Fatalf("err=%v", err)
	}
}

func TestEntraFieldsAreRejectedForOtherSchemes(t *testing.T) {
	for _, auth := range []*UpstreamAuth{
		{Scheme: UpstreamAuthSchemeBearer, Secret: "s", TenantID: "t"},
		{Scheme: UpstreamAuthSchemeBasic, Username: "u", Secret: "s", ClientID: "c"},
		{Scheme: UpstreamAuthSchemeNone, Scope: "x"},
	} {
		if auth.Validate() == nil {
			t.Errorf("%+v was accepted", auth)
		}
	}
	if err := (&UpstreamAuth{Scheme: "oauth"}).Validate(); err == nil || !strings.Contains(err.Error(), "entra-workload") {
		t.Fatalf("err=%v", err)
	}
}

func TestMemoryStoreFillsTheRepositoryNameOfUpstreamAuth(t *testing.T) {
	AllowEntraWorkload(true)
	t.Cleanup(func() { AllowEntraWorkload(false) })
	store := NewMemoryStore()
	created, err := store.CreateHostedRepository(t.Context(), HostedRepository{
		ID: "r1", Name: "azure-blobs", Format: FormatRaw, Type: RepositoryTypeProxy, Endpoint: "https://x.blob.example.test", UpstreamAuth: validEntraAuth(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.UpstreamAuth == nil || created.UpstreamAuth.Repository != "azure-blobs" {
		t.Fatalf("%+v", created.UpstreamAuth)
	}
}
