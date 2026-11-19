package app

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/identity/saml"
	"github.com/mlops-ca/notarx/internal/repository"
)

var (
	errNotSAMLProvider   = errors.New("the identity provider is not a SAML provider")
	errSAMLNoPublicURL   = errors.New("NOTARX_PUBLIC_URL is required for SAML")
	errMetadataSourceSet = errors.New("send exactly one of url and xml")
)

// samlMetadataFetcher downloads IdP metadata; the production one is the SSRF-guarded saml.Fetcher.
type samlMetadataFetcher interface {
	Fetch(ctx context.Context, rawURL string) ([]byte, error)
}

// SAMLRuntimeOptions wires the SAML service provider into the identity runtime.
type SAMLRuntimeOptions struct {
	Config    config.SAMLConfig
	PublicURL *url.URL
	State     repository.SAMLStateStore
	Fetcher   samlMetadataFetcher
	Now       func() time.Time
}

type samlRuntime struct {
	config    config.SAMLConfig
	publicURL *url.URL
	state     repository.SAMLStateStore
	fetcher   samlMetadataFetcher
	now       func() time.Time
}

// SAMLSPKeyInfo describes a published service provider key without any private material.
type SAMLSPKeyInfo struct {
	ID       string
	NotAfter time.Time
}

// SAMLView is the derived, read-only part of a SAML provider the management API shows.
type SAMLView struct {
	Endpoints saml.SPEndpoints
	Keys      []SAMLSPKeyInfo
}

// ConfigureSAML enables the SAML routes and the metadata operations.
func (o *IdentityRuntime) ConfigureSAML(options SAMLRuntimeOptions) {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Fetcher == nil {
		if fetcher, err := saml.NewFetcher(); err == nil {
			options.Fetcher = fetcher
		}
	}
	o.saml = samlRuntime{config: options.Config, publicURL: options.PublicURL, state: options.State, fetcher: options.Fetcher, now: options.Now}
}

// SAMLEndpoints returns the service provider URLs of a slug, or false without NOTARX_PUBLIC_URL.
func (o *IdentityRuntime) SAMLEndpoints(slug string) (saml.SPEndpoints, bool) {
	if o.saml.publicURL == nil {
		return saml.SPEndpoints{}, false
	}
	return saml.EndpointsFor(o.saml.publicURL, slug), true
}

// spKeys opens the sealed service provider key set of a provider, cached by (id, version) like the OIDC
// provider state so the decryption and key parsing do not repeat on every login or metadata request.
func (o *IdentityRuntime) spKeys(provider identity.Provider) (saml.SPKeySet, error) {
	key := stateKey{provider.ID, provider.Version}
	o.mu.Lock()
	keys, ok := o.spKeySets[key]
	o.mu.Unlock()
	if ok {
		return keys, nil
	}
	keys, err := saml.OpenSPKeySet(provider.SecretsSealed)
	if err != nil {
		return saml.SPKeySet{}, err
	}
	o.mu.Lock()
	if o.spKeySets == nil {
		o.spKeySets = map[stateKey]saml.SPKeySet{}
	}
	o.spKeySets[key] = keys
	o.mu.Unlock()
	return keys, nil
}

// samlReady reports whether the SAML routes can serve: the public URL derives every endpoint and the
// request-state store makes the flow single use. Without them the chooser must not offer SAML providers.
func (o *IdentityRuntime) samlReady() bool { return o.saml.publicURL != nil && o.saml.state != nil }

// SAMLView returns the derived fields of a SAML provider.
func (o *IdentityRuntime) SAMLView(provider identity.Provider) SAMLView {
	var view SAMLView
	view.Endpoints, _ = o.SAMLEndpoints(provider.Slug)
	if keys, err := o.spKeys(provider); err == nil {
		for _, key := range keys.Keys {
			view.Keys = append(view.Keys, SAMLSPKeyInfo{ID: key.ID, NotAfter: key.NotAfter})
		}
	}
	return view
}

// samlProviderRuntime is one enabled SAML provider ready to sign in or verify.
type samlProviderRuntime struct {
	provider  identity.Provider
	settings  identity.SAMLSettings
	endpoints saml.SPEndpoints
}

// SAMLBrowser resolves the enabled SAML provider of a slug. A disabled provider is reported
// exactly like an unknown one.
func (o *IdentityRuntime) SAMLBrowser(ctx context.Context, slug string) (samlProviderRuntime, error) {
	providers, err := o.cached(ctx)
	if err != nil {
		return samlProviderRuntime{}, err
	}
	for _, provider := range providers {
		if provider.Slug != slug {
			continue
		}
		if !provider.Enabled || provider.Kind != identity.KindSAML {
			return samlProviderRuntime{}, errProviderNotFound
		}
		endpoints, ok := o.SAMLEndpoints(slug)
		if !ok {
			return samlProviderRuntime{}, errSAMLNoPublicURL
		}
		settings, err := provider.SAML()
		if err != nil {
			return samlProviderRuntime{}, err
		}
		return samlProviderRuntime{provider: provider, settings: settings, endpoints: endpoints}, nil
	}
	return samlProviderRuntime{}, errProviderNotFound
}

// verifier builds the response verifier of the provider at its current version.
func (p samlProviderRuntime) verifier(config config.SAMLConfig, now func() time.Time) (saml.Verifier, error) {
	certificates, err := p.settings.ParsedCertificates()
	if err != nil {
		return nil, err
	}
	return saml.NewVerifier(saml.Config{
		Certificates: certificates, EntityID: p.settings.EntityID, ACSURL: p.endpoints.ACSURL, SPEntityID: p.endpoints.EntityID,
		ClockSkew: config.ClockSkew, MaxResponseBytes: config.MaxResponseBytes, NameIDFormat: p.settings.NameIDFormat,
		Mapping: p.provider.AttributeMapping, Now: now,
	})
}

// RotateSAMLSPKey appends a fresh service provider key; the previous one stays published.
func (o *IdentityRuntime) RotateSAMLSPKey(ctx context.Context, id string, audit ...repository.AuditRecord) (identity.Provider, error) {
	current, err := o.Get(ctx, id)
	if err != nil {
		return identity.Provider{}, err
	}
	if current.Kind != identity.KindSAML {
		return identity.Provider{}, fmt.Errorf("%w: only SAML providers have service provider keys", identity.ErrInvalid)
	}
	keys, err := saml.OpenSPKeySet(current.SecretsSealed)
	if err != nil {
		return identity.Provider{}, err
	}
	rotated, err := keys.Rotated(current.Slug, o.saml.clock())
	if err != nil {
		return identity.Provider{}, err
	}
	if current.SecretsSealed, err = rotated.Seal(); err != nil {
		return identity.Provider{}, err
	}
	replaced, err := o.store.ReplaceIdentityProvider(ctx, current, current.Version, audit...)
	if err == nil {
		o.invalidate()
	}
	return replaced, err
}

func (s samlRuntime) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

// SAMLMetadataSource is exactly one of a URL and pasted XML.
type SAMLMetadataSource struct{ URL, XML string }

// ImportSAMLMetadata merges IdP metadata into a SAML provider: the entity id and SSO endpoint are
// replaced, certificates that are not pinned yet are added (as next when one is already pinned, so
// a rollover verifies both), and a URL import remembers its URL for the optional refresh. Nothing is
// removed; an administrator drops a retired certificate by replacing the provider.
func (o *IdentityRuntime) ImportSAMLMetadata(ctx context.Context, id string, source SAMLMetadataSource, audit ...repository.AuditRecord) (identity.Provider, error) {
	if (source.URL == "") == (source.XML == "") {
		return identity.Provider{}, fmt.Errorf("%w: %v", identity.ErrInvalid, errMetadataSourceSet)
	}
	current, err := o.Get(ctx, id)
	if err != nil {
		return identity.Provider{}, err
	}
	if current.Kind != identity.KindSAML {
		return identity.Provider{}, fmt.Errorf("%w: %v", identity.ErrInvalid, errNotSAMLProvider)
	}
	raw := []byte(source.XML)
	if source.URL != "" {
		if o.saml.fetcher == nil {
			return identity.Provider{}, errors.New("metadata fetching is unavailable")
		}
		if raw, err = o.saml.fetcher.Fetch(ctx, source.URL); err != nil {
			return identity.Provider{}, metadataInvalid(err)
		}
	}
	merged, err := mergeSAMLMetadata(current, raw, source.URL, o.saml.clock())
	if err != nil {
		return identity.Provider{}, err
	}
	replaced, err := o.store.ReplaceIdentityProvider(ctx, merged, current.Version, audit...)
	if err == nil {
		o.invalidate()
	}
	return replaced, err
}

func countNewCertificates(pinned map[string]bool, parsed []*x509.Certificate) int {
	count := 0
	for _, cert := range parsed {
		if !pinned[saml.CertificatePEM(cert)] {
			count++
		}
	}
	return count
}

func metadataInvalid(err error) error {
	var metadata *saml.MetadataError
	if errors.As(err, &metadata) {
		return fmt.Errorf("%w: %s", identity.ErrInvalid, metadata.Message)
	}
	return err
}

func mergeSAMLMetadata(current identity.Provider, raw []byte, sourceURL string, now time.Time) (identity.Provider, error) {
	parsed, err := saml.ParseIdPMetadata(raw)
	if err != nil {
		return identity.Provider{}, metadataInvalid(err)
	}
	settings, err := current.SAML()
	if err != nil {
		return identity.Provider{}, err
	}
	settings.EntityID, settings.SSOURL = parsed.EntityID, parsed.SSOURL
	if sourceURL != "" {
		settings.MetadataURL = sourceURL
	}
	pinned := map[string]bool{}
	for _, entry := range settings.Certificates {
		if cert, canonical, err := identity.ParseSAMLCertificate(entry.PEM); err == nil {
			_ = cert
			pinned[canonical] = true
		}
	}
	state := identity.SAMLCertificateActive
	if len(settings.Certificates) > 0 {
		state = identity.SAMLCertificateNext
	}
	// Rollovers add certificates and never remove one the administrator pinned; expired ones are the
	// exception, dropped only when the list would otherwise pass its cap, so a refresh cannot get stuck.
	if fresh := countNewCertificates(pinned, parsed.Certificates); len(settings.Certificates)+fresh > identity.MaxSAMLCertificates {
		settings.Certificates = slices.DeleteFunc(settings.Certificates, func(entry identity.SAMLCertificate) bool { return entry.NotAfter.Before(now) })
	}
	for _, cert := range parsed.Certificates {
		if canonical := saml.CertificatePEM(cert); !pinned[canonical] {
			settings.Certificates = append(settings.Certificates, identity.SAMLCertificate{PEM: canonical, NotAfter: cert.NotAfter.UTC(), State: state})
			pinned[canonical] = true
		}
	}
	current, err = current.WithSAML(settings)
	if err != nil {
		return current, err
	}
	return current.NormalizeAndValidate()
}

// RefreshSAMLMetadata re-imports the metadata of every SAML provider that opted in. A new
// certificate is added as next before the IdP cuts over; a failure is logged and never stops the others.
func (o *IdentityRuntime) RefreshSAMLMetadata(ctx context.Context) {
	providers, err := o.List(ctx)
	if err != nil {
		slog.Warn("list identity providers for the SAML metadata refresh failed", "error", err)
		return
	}
	for _, provider := range providers {
		if provider.Kind != identity.KindSAML {
			continue
		}
		settings, err := provider.SAML()
		if err != nil || !settings.MetadataRefresh || settings.MetadataURL == "" {
			continue
		}
		// The refresh changes the trusted certificates, the entity id and the SSO URL, so it is audited like an
		// administrator's import, as the system actor.
		audit := repository.AuditRecord{
			Class: repository.AuditClassSync, Actor: "system:saml-metadata-refresh", Outcome: repository.AuditResolved, OccurredAt: o.saml.clock(),
			Format: "management", Resource: providerResourcePrefix + provider.ID, Operation: "authentication.provider.refresh-metadata",
			Status: http.StatusOK, CacheDisposition: "bypass",
		}
		_, err = o.ImportSAMLMetadata(ctx, provider.ID, SAMLMetadataSource{URL: settings.MetadataURL}, audit)
		switch {
		case err == nil:
		case errors.Is(err, repository.ErrVersionConflict):
			// Another node (or an administrator) changed the provider first; the next tick retries.
			slog.Debug("SAML metadata refresh skipped after a concurrent change", "provider", provider.Slug)
		default:
			slog.Warn("SAML metadata refresh failed", "provider", provider.Slug, "error", err)
		}
	}
}

// RunSAMLMetadataRefresh refreshes on every interval until ctx ends. A non-positive interval disables it.
func (o *IdentityRuntime) RunSAMLMetadataRefresh(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.RefreshSAMLMetadata(ctx)
		}
	}
}
