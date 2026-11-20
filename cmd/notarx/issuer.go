package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/mlops-ca/notarx/internal/app"
	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
	"github.com/mlops-ca/notarx/internal/tokenissuer"
	"github.com/mlops-ca/notarx/internal/upstreamauth"
)

// issuerStore is what startup needs besides the key store: the shared deployment id, the
// settings table and the ADR 0019 issuer registry.
type issuerStore interface {
	tokenissuer.Store
	DeploymentID(ctx context.Context) (string, error)
	Setting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
	GetWorkloadIssuerByIssuer(ctx context.Context, issuer string) (repository.WorkloadIssuer, error)
	auditRecorder
}

// setupIssuer validates the issuer against the database and builds its runtime. It returns nil
// while the issuer is disabled, so air-gapped installs run nothing and touch nothing.
func setupIssuer(ctx context.Context, cfg config.IssuerConfig, store issuerStore, registry *readcache.Registry) (*app.IssuerRuntime, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if err := secrets.ValidateRing(); err != nil {
		return nil, fmt.Errorf("the OIDC issuer seals its signing keys with the settings encryption key: %w", err)
	}
	for _, warning := range cfg.Warnings {
		slog.Warn(warning)
	}
	if err := checkIssuerURL(ctx, cfg, store); err != nil {
		return nil, err
	}
	deploymentID, err := store.DeploymentID(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the deployment id: %w", err)
	}
	runtime, err := app.NewIssuerRuntime(cfg, store, deploymentID, registry)
	if err != nil {
		return nil, err
	}
	enableEntraWorkload(cfg, runtime, store)
	return runtime, nil
}

// auditRecorder is the part of the store the entra-workload audit hook needs.
type auditRecorder interface {
	RecordAudit(ctx context.Context, record repository.AuditRecord) error
}

// enableEntraWorkload installs the entra-workload upstream scheme: the process accepts the scheme
// in repository configuration only from here on, and token exchanges are audited with subject,
// tenant, client and repository (never a token).
func enableEntraWorkload(cfg config.IssuerConfig, runtime *app.IssuerRuntime, audit auditRecorder) {
	repository.AllowEntraWorkload(true)
	upstreamauth.SetEntra(&upstreamauth.Entra{
		Signer: runtime.Signer, IssuerURL: cfg.URL, DeploymentID: runtime.DeploymentID,
		AuthorityHost: cfg.EntraAuthorityHost, Audience: cfg.EntraAudience, MaxLifetime: cfg.MaxTokenLifetime,
		Do: app.EntraHTTPDoer(app.UpstreamClient{}),
		Audit: func(ctx context.Context, event upstreamauth.EntraExchange) {
			record := entraAuditRecord(event, time.Now().UTC())
			if err := audit.RecordAudit(context.WithoutCancel(ctx), record); err != nil {
				slog.Error("record entra-workload audit event", "error", err)
			}
		},
	})
}

// entraAuditRecord maps one exchange to an audit record. Evidence carries the resolved subject,
// tenant and client; the repository is the record's repository.
func entraAuditRecord(event upstreamauth.EntraExchange, now time.Time) repository.AuditRecord {
	evidence := map[string]string{"subject": event.Subject, "tenantId": event.TenantID, "clientId": event.ClientID, "scope": event.Scope}
	if event.KeyID != "" {
		evidence["kid"], evidence["jti"] = event.KeyID, event.JTI
	}
	if event.Registry != "" {
		evidence["registry"] = event.Registry
	}
	outcome, status := repository.AuditResolved, http.StatusOK
	if !event.Succeeded {
		outcome, status = repository.AuditUpstreamError, http.StatusBadGateway
	}
	return repository.AuditRecord{
		GroupName: event.Repository, Repository: event.Repository, Actor: "notarx-instance", Outcome: outcome,
		OccurredAt: now, Resource: event.TenantID + "/" + event.ClientID, Operation: "upstream." + event.Operation,
		Status: status, CacheDisposition: "bypass", AuthorizationSource: "entra_workload", Evidence: evidence,
	}
}

// checkIssuerURL rejects an issuer URL that is also an ADR 0019 trusted issuer unless
// self-trust is configured, and warns when the URL differs from the last one this deployment
// used: a changed issuer invalidates every federated credential that names the old one.
func checkIssuerURL(ctx context.Context, cfg config.IssuerConfig, store interface {
	Setting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
	GetWorkloadIssuerByIssuer(ctx context.Context, issuer string) (repository.WorkloadIssuer, error)
}) error {
	registered, err := store.GetWorkloadIssuerByIssuer(ctx, cfg.URL)
	switch {
	case err == nil:
		if !cfg.AllowSelfTrust {
			return fmt.Errorf("%s %q is registered as trusted issuer %q (ADR 0019); set %s=true to trust this instance's own tokens deliberately",
				config.IssuerURLEnv, cfg.URL, registered.Name, config.IssuerAllowSelfTrustEnv)
		}
		slog.Warn("the issuer URL is also a trusted workload issuer: this instance accepts its own assertions", "issuer", cfg.URL, "trusted_issuer", registered.Name)
	case !errors.Is(err, repository.ErrNotFound):
		return fmt.Errorf("check registered issuers: %w", err)
	}
	last, err := store.Setting(ctx, repository.SettingIssuerURL)
	switch {
	case errors.Is(err, repository.ErrSettingNotFound):
	case err != nil:
		return fmt.Errorf("read the last issuer URL: %w", err)
	case last != cfg.URL:
		slog.Warn("the issuer URL changed since the last start: federated credentials that name the old issuer stop working until they are recreated",
			"previous", last, "current", cfg.URL)
	}
	if last != cfg.URL {
		if err := store.SetSetting(ctx, repository.SettingIssuerURL, cfg.URL); err != nil {
			return fmt.Errorf("record the issuer URL: %w", err)
		}
	}
	return nil
}
