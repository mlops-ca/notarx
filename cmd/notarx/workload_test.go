package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/repository"
)

type fakeProviders []identity.Provider

func (f fakeProviders) ListIdentityProviders(context.Context) ([]identity.Provider, error) {
	return f, nil
}

type fakeIssuers []repository.WorkloadIssuer

func (f fakeIssuers) ListWorkloadIssuers(_ context.Context, limit int, afterID string) ([]repository.WorkloadIssuer, error) {
	start := 0
	if afterID != "" {
		for i, issuer := range f {
			if issuer.ID == afterID {
				start = i + 1
			}
		}
	}
	end := min(start+limit, len(f))
	return f[start:end], nil
}

func oidcProvider(t *testing.T, slug, issuer string) identity.Provider {
	t.Helper()
	settings, err := json.Marshal(identity.OIDCSettings{Issuer: issuer})
	if err != nil {
		t.Fatal(err)
	}
	return identity.Provider{Slug: slug, Kind: identity.KindOIDC, Settings: settings}
}

func TestWorkloadIssuerOverlapWithIdentityProvidersFailsStartup(t *testing.T) {
	providers := fakeProviders{oidcProvider(t, "keycloak", "https://sso.example.test/realms/acme/")}
	disjoint := fakeIssuers{{ID: "1", Name: "gh", Issuer: "https://token.actions.example.test"}}
	if err := checkWorkloadIssuerOverlap(context.Background(), providers, disjoint); err != nil {
		t.Fatalf("disjoint registries: %v", err)
	}
	overlapping := fakeIssuers{{ID: "1", Name: "gh", Issuer: "https://token.actions.example.test"}, {ID: "2", Name: "sso", Issuer: "https://sso.example.test/realms/acme"}}
	err := checkWorkloadIssuerOverlap(context.Background(), providers, overlapping)
	if err == nil || !strings.Contains(err.Error(), "keycloak") || !strings.Contains(err.Error(), "sso") {
		t.Fatalf("overlap error = %v", err)
	}
}

func TestWorkloadIssuerOverlapReadsEveryPage(t *testing.T) {
	providers := fakeProviders{oidcProvider(t, "keycloak", "https://sso.example.test")}
	var many fakeIssuers
	for i := range 250 {
		many = append(many, repository.WorkloadIssuer{ID: string(rune('a'+i/26)) + string(rune('a'+i%26)), Issuer: "https://issuer" + string(rune('a'+i/26)) + string(rune('a'+i%26)) + ".example.test"})
	}
	many[249].Issuer = "https://sso.example.test"
	if err := checkWorkloadIssuerOverlap(context.Background(), providers, many); err == nil {
		t.Fatal("an overlap on the last page was missed")
	}
}

func configWorkload(enabled bool) config.WorkloadConfig {
	return config.WorkloadConfig{Enabled: enabled}
}

func TestSetupWorkloadIsOffUnlessEnabled(t *testing.T) {
	workload, err := setupWorkload(configWorkload(false), nil, nil, nil, nil, nil)
	if err != nil || workload != nil {
		t.Fatalf("disabled: %v %v", workload, err)
	}
}
