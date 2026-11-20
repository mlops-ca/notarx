package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/mlops-ca/notarx/internal/app"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

// setupWorkload builds the workload authenticator of ADR 0019 slice 4. It returns nil while
// NOTARX_WORKLOAD_ENABLED is false, so JWTs reach only the human OIDC path and nothing is verified.
func setupWorkload(cfg config.WorkloadConfig, store authorization.WorkloadStore, accounts authorization.WorkloadAccounts, audit app.AuditRecorder, registry *readcache.Registry, metrics *app.Metrics) (*authorization.WorkloadAuthenticator, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	verifier, err := jose.NewVerifier(cfg.VerifierOptions())
	if err != nil {
		return nil, fmt.Errorf("workload token verifier: %w", err)
	}
	return authorization.NewWorkloadAuthenticator(authorization.WorkloadOptions{
		Store: store, Accounts: accounts, Verifier: verifier, ClockSkew: cfg.ClockSkew, Cache: registry,
		AllowPrivateNetwork: cfg.AllowPrivateNetwork, Logger: slog.Default(),
		Observer: app.WorkloadObserver{Metrics: metrics, Audit: audit},
	})
}

// identityLister is the human IdP registry as the startup overlap check reads it.
type identityLister interface {
	ListIdentityProviders(context.Context) ([]identity.Provider, error)
}

// workloadIssuerLister lists the workload issuers.
type workloadIssuerLister interface {
	ListWorkloadIssuers(ctx context.Context, limit int, afterID string) ([]repository.WorkloadIssuer, error)
}

// checkWorkloadIssuerOverlap fails startup when an issuer URL is registered both as a human identity
// provider (ADR 0018) and as a workload issuer: the two registries must never overlap, because a token
// of that issuer would be tried by both authentication paths (ADR 0019, "Authentication flow").
func checkWorkloadIssuerOverlap(ctx context.Context, providers identityLister, issuers workloadIssuerLister) error {
	registered, err := providers.ListIdentityProviders(ctx)
	if err != nil {
		return fmt.Errorf("list identity providers: %w", err)
	}
	human := map[string]string{}
	for _, provider := range registered {
		if provider.Kind != identity.KindOIDC {
			continue
		}
		settings, err := provider.OIDC()
		if err != nil {
			return fmt.Errorf("identity provider %s: %w", provider.Slug, err)
		}
		human[trimIssuer(settings.Issuer)] = provider.Slug
	}
	after := ""
	for {
		page, err := issuers.ListWorkloadIssuers(ctx, 200, after)
		if err != nil {
			return fmt.Errorf("list workload issuers: %w", err)
		}
		for _, issuer := range page {
			if slug, clash := human[trimIssuer(issuer.Issuer)]; clash {
				return fmt.Errorf("issuer %s is registered as workload issuer %q and as identity provider %q: remove one of them", issuer.Issuer, issuer.Name, slug)
			}
		}
		if len(page) < 200 {
			return nil
		}
		after = page[len(page)-1].ID
	}
}

func trimIssuer(issuer string) string {
	for len(issuer) > 0 && issuer[len(issuer)-1] == '/' {
		issuer = issuer[:len(issuer)-1]
	}
	return issuer
}
