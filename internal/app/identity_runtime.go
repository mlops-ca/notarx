package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/identity/saml"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
)

const identityCacheTTL = 5 * time.Second

var (
	errOIDCNotEnabled   = errors.New("OIDC authentication is not enabled")
	errProviderNotFound = errors.New("identity provider not found")
)

// OIDCRuntimeConfig is the environment seed (NOTARX_OIDC_*) of the registry.
type OIDCRuntimeConfig struct {
	Enabled       bool
	Issuer        string
	Audience      string
	JWKSURL       string
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	Scopes        []string
	AdminSubjects []string
	Roles         authorization.OIDCRoleMapping
}

// IdentityProviderInput is the administrator-editable part of a provider.
type IdentityProviderInput struct {
	Slug        string
	Kind        identity.Kind
	DisplayName string
	Enabled     bool
	OIDC        identity.OIDCSettings
	// SAML is the settings of a SAML provider; it is set exactly when Kind is saml.
	SAML                *identity.SAMLSettings
	ClientSecret        *string
	ClearClientSecret   bool
	AttributeMapping    identity.AttributeMapping
	ProvisioningMode    identity.ProvisioningMode
	EmailLinkingEnabled bool
	JITDefaultRole      string
}

// PublicIdentityProvider is what the login chooser may learn about a provider.
type PublicIdentityProvider struct {
	Slug        string        `json:"slug"`
	DisplayName string        `json:"displayName"`
	Kind        identity.Kind `json:"kind"`
}

type OIDCConnectionTest struct {
	Reachable             bool      `json:"reachable"`
	Issuer                string    `json:"issuer"`
	AuthorizationEndpoint string    `json:"authorizationEndpoint,omitempty"`
	TokenEndpoint         string    `json:"tokenEndpoint,omitempty"`
	JWKSURL               string    `json:"jwksUrl,omitempty"`
	LatencyMs             int64     `json:"latencyMs"`
	CheckedAt             time.Time `json:"checkedAt"`
}

// oidcProviderState is the runtime of one OIDC provider at one version: the
// discovery and browser clients and the validators. It is cached by
// (provider id, version), so an edit builds a new one and the old one is dropped.
type oidcProviderState struct {
	provider        identity.Provider
	settings        identity.OIDCSettings
	discoveryClient *authorization.OIDCClient
	browserClient   *authorization.OIDCClient
	mu              sync.Mutex
	apiValidator    *authorization.OIDCValidator
	loginValidator  *authorization.OIDCValidator
}

type stateKey struct {
	id      string
	version int64
}

// IdentityRuntime serves the provider registry: management writes, the login
// chooser, per-provider browser sign-in and the API bearer validators.
type IdentityRuntime struct {
	store repository.IdentityProviderStore

	mu         sync.Mutex
	providers  []identity.Provider
	expires    time.Time
	generation uint64 // bumped by invalidate so an in-flight refresh cannot store a stale list
	states     map[stateKey]*oidcProviderState
	saml       samlRuntime
	spKeySets  map[stateKey]saml.SPKeySet
}

func NewIdentityRuntime(store repository.IdentityProviderStore) *IdentityRuntime {
	return &IdentityRuntime{store: store, states: map[stateKey]*oidcProviderState{}}
}

func (o *IdentityRuntime) invalidate() {
	o.mu.Lock()
	o.expires = time.Time{}
	o.generation++
	o.mu.Unlock()
}

// List returns every provider, fresh from the store (management reads).
func (o *IdentityRuntime) List(ctx context.Context) ([]identity.Provider, error) {
	if o.store == nil {
		return nil, nil
	}
	return o.store.ListIdentityProviders(ctx)
}

func (o *IdentityRuntime) Get(ctx context.Context, id string) (identity.Provider, error) {
	if o.store == nil {
		return identity.Provider{}, repository.ErrNotFound
	}
	return o.store.GetIdentityProvider(ctx, id)
}

// cached returns the provider list, refreshed at most every identityCacheTTL
// so the login chooser and the bearer path never read the table per request.
func (o *IdentityRuntime) cached(ctx context.Context) ([]identity.Provider, error) {
	if o.store == nil {
		return nil, nil
	}
	o.mu.Lock()
	if time.Now().Before(o.expires) {
		providers := o.providers
		o.mu.Unlock()
		return providers, nil
	}
	generation := o.generation
	o.mu.Unlock()
	providers, err := o.store.ListIdentityProviders(ctx)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.generation != generation {
		// A write invalidated the cache while this read was in flight: the list may be
		// stale, so serve it once but do not keep it.
		return providers, nil
	}
	o.providers, o.expires = providers, time.Now().Add(identityCacheTTL)
	live := make(map[stateKey]bool, len(providers))
	for _, provider := range providers {
		live[stateKey{provider.ID, provider.Version}] = true
	}
	for key := range o.states {
		if !live[key] {
			delete(o.states, key)
		}
	}
	for key := range o.spKeySets {
		if !live[key] {
			delete(o.spKeySets, key)
		}
	}
	return providers, nil
}

// Public lists the enabled providers for the login chooser.
func (o *IdentityRuntime) Public(ctx context.Context) ([]PublicIdentityProvider, error) {
	providers, err := o.cached(ctx)
	if err != nil {
		return nil, err
	}
	public := make([]PublicIdentityProvider, 0, len(providers))
	for _, provider := range providers {
		if provider.Enabled && canStartBrowserLogin(provider) && (provider.Kind != identity.KindSAML || o.samlReady()) {
			public = append(public, PublicIdentityProvider{Slug: provider.Slug, DisplayName: provider.DisplayName, Kind: provider.Kind})
		}
	}
	return public, nil
}

// canStartBrowserLogin reports whether the chooser may offer the provider: an API-only
// OIDC provider (no client and redirect URL) has no browser flow to start.
func canStartBrowserLogin(provider identity.Provider) bool {
	if provider.Kind != identity.KindOIDC {
		return true
	}
	settings, err := provider.OIDC()
	return err == nil && settings.ClientID != "" && settings.RedirectURL != ""
}

func (o *IdentityRuntime) stateFor(provider identity.Provider) (*oidcProviderState, error) {
	key := stateKey{provider.ID, provider.Version}
	o.mu.Lock()
	if state, ok := o.states[key]; ok {
		o.mu.Unlock()
		return state, nil
	}
	o.mu.Unlock()
	state, err := buildOIDCProviderState(provider)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if existing, ok := o.states[key]; ok {
		return existing, nil
	}
	o.states[key] = state
	return state, nil
}

func buildOIDCProviderState(provider identity.Provider) (*oidcProviderState, error) {
	settings, err := provider.OIDC()
	if err != nil {
		return nil, err
	}
	clientSecret := ""
	if provider.SecretsSealed != "" {
		if clientSecret, err = secrets.Open(identity.SecretPurpose, provider.SecretsSealed); err != nil {
			return nil, err
		}
	}
	state := &oidcProviderState{provider: provider, settings: settings}
	if settings.Issuer == "" {
		return state, nil
	}
	state.discoveryClient = authorization.NewOIDCDiscoveryClient(settings.Issuer)
	state.browserClient = authorization.NewOIDCClient(authorization.OIDCClientConfig{
		Issuer: settings.Issuer, ClientID: settings.ClientID, ClientSecret: clientSecret,
		RedirectURL: settings.RedirectURL, Scopes: settings.Scopes,
	})
	if state.browserClient != nil {
		state.discoveryClient = state.browserClient
	}
	return state, nil
}

func (s *oidcProviderState) validatorConfig(audience, discoveredJWKS string) authorization.OIDCConfig {
	jwksURL := s.settings.JWKSURL
	if jwksURL == "" {
		jwksURL = discoveredJWKS
	}
	return authorization.OIDCConfig{
		ProviderID: s.provider.ID, Issuer: s.settings.Issuer, Audience: audience, JWKSURL: jwksURL,
		AdminSubjects: s.settings.AdminSubjects, Algorithms: s.settings.Algorithms,
		SubjectClaims: s.provider.AttributeMapping.SubjectClaims(),
		Roles:         authorization.OIDCRoleMapping{Member: s.settings.MemberRoles, Admin: s.settings.AdminRoles},
	}
}

// Browser resolves the enabled OIDC provider of a slug for the sign-in flow. A
// disabled provider is reported exactly like an unknown one.
func (o *IdentityRuntime) Browser(ctx context.Context, slug string) (*oidcProviderState, *authorization.OIDCValidator, error) {
	providers, err := o.cached(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, provider := range providers {
		if provider.Slug != slug {
			continue
		}
		if !provider.Enabled || provider.Kind != identity.KindOIDC {
			return nil, nil, errProviderNotFound
		}
		state, err := o.stateFor(provider)
		if err != nil {
			return nil, nil, err
		}
		if state.browserClient == nil {
			return nil, nil, errOIDCNotEnabled
		}
		metadata, err := state.browserClient.Metadata(ctx)
		if err != nil {
			return nil, nil, err
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.loginValidator == nil {
			state.loginValidator = authorization.NewOIDCValidator(state.validatorConfig(state.settings.ClientID, metadata.JWKSURI))
		}
		return state, state.loginValidator, nil
	}
	return nil, nil, errProviderNotFound
}

// OIDCValidators returns the API bearer validators of the enabled OIDC providers that
// have an audience and whose issuer is the token's (unverified) iss. Selecting by issuer
// keeps discovery of an unrelated, slow or unreachable provider off the request path of
// every other provider's tokens. A provider whose discovery fails is skipped.
func (o *IdentityRuntime) OIDCValidators(ctx context.Context, token string) ([]*authorization.OIDCValidator, error) {
	issuer := authorization.UnverifiedIssuer(token)
	if issuer == "" {
		return nil, nil
	}
	providers, err := o.cached(ctx)
	if err != nil {
		return nil, err
	}
	var validators []*authorization.OIDCValidator
	for _, provider := range providers {
		if !provider.Enabled || provider.Kind != identity.KindOIDC {
			continue
		}
		state, err := o.stateFor(provider)
		if err != nil || state.discoveryClient == nil || state.settings.Audience == "" || state.settings.Issuer != issuer {
			continue
		}
		metadata, err := state.discoveryClient.Metadata(ctx)
		if err != nil {
			continue
		}
		state.mu.Lock()
		if state.apiValidator == nil {
			state.apiValidator = authorization.NewOIDCValidator(state.validatorConfig(state.settings.Audience, metadata.JWKSURI))
		}
		validators = append(validators, state.apiValidator)
		state.mu.Unlock()
	}
	return validators, nil
}

// Test checks that discovery of the stored provider works. The provider need not be enabled.
func (o *IdentityRuntime) Test(ctx context.Context, id string) (OIDCConnectionTest, error) {
	provider, err := o.Get(ctx, id)
	if err != nil {
		return OIDCConnectionTest{}, err
	}
	if provider.Kind != identity.KindOIDC {
		return OIDCConnectionTest{}, errOIDCNotEnabled
	}
	settings, err := provider.OIDC()
	if err != nil || settings.Issuer == "" {
		return OIDCConnectionTest{}, errOIDCNotEnabled
	}
	started := time.Now()
	metadata, err := authorization.NewOIDCDiscoveryClient(settings.Issuer).Metadata(ctx)
	result := OIDCConnectionTest{Issuer: settings.Issuer, LatencyMs: time.Since(started).Milliseconds(), CheckedAt: time.Now().UTC()}
	if err != nil {
		return result, err
	}
	result.Reachable = true
	result.AuthorizationEndpoint, result.TokenEndpoint, result.JWKSURL = metadata.AuthorizationEndpoint, metadata.TokenEndpoint, metadata.JWKSURI
	return result, nil
}

func (in IdentityProviderInput) provider(current identity.Provider) (identity.Provider, error) {
	provider := current
	provider.Slug, provider.Kind = in.Slug, in.Kind
	if current.ID != "" {
		provider.Slug, provider.Kind = current.Slug, current.Kind
	}
	provider.DisplayName, provider.Enabled = in.DisplayName, in.Enabled
	provider.AttributeMapping, provider.ProvisioningMode = in.AttributeMapping, in.ProvisioningMode
	provider.EmailLinkingEnabled, provider.JITDefaultRole = in.EmailLinkingEnabled, in.JITDefaultRole
	var err error
	switch provider.Kind {
	case identity.KindOIDC:
		if in.SAML != nil {
			return provider, fmt.Errorf("%w: saml settings do not apply to an OIDC provider", identity.ErrInvalid)
		}
		if provider, err = provider.WithOIDC(in.OIDC); err != nil {
			return provider, err
		}
	case identity.KindSAML:
		return in.samlProvider(provider)
	default:
		return provider, fmt.Errorf("%w: kind must be oidc or saml", identity.ErrInvalid)
	}
	switch {
	case in.ClientSecret != nil && *in.ClientSecret != "":
		sealed, err := secrets.Seal(identity.SecretPurpose, *in.ClientSecret)
		if err != nil {
			return provider, err
		}
		provider.SecretsSealed = sealed
	case in.ClearClientSecret:
		provider.SecretsSealed = ""
	}
	provider, err = provider.NormalizeAndValidate()
	if err != nil {
		return provider, err
	}
	if provider.SecretsSealed != "" && in.OIDC.ClientID == "" {
		return provider, fmt.Errorf("%w: clientId is required when a client secret is configured", identity.ErrInvalid)
	}
	return provider, nil
}

// samlProvider applies the SAML settings. The sealed service provider key set is server-owned: it is
// kept across edits, generated the first time signed requests are switched on, and never taken from a client.
func (in IdentityProviderInput) samlProvider(provider identity.Provider) (identity.Provider, error) {
	if in.SAML == nil {
		return provider, fmt.Errorf("%w: saml settings are required for a SAML provider", identity.ErrInvalid)
	}
	if in.ClientSecret != nil || in.ClearClientSecret {
		return provider, fmt.Errorf("%w: a SAML provider has no client secret", identity.ErrInvalid)
	}
	provider, err := provider.WithSAML(*in.SAML)
	if err != nil {
		return provider, err
	}
	if provider, err = provider.NormalizeAndValidate(); err != nil {
		return provider, err
	}
	if in.SAML.SignRequests && provider.SecretsSealed == "" {
		keys, err := saml.SPKeySet{}.Rotated(provider.Slug, time.Now().UTC())
		if err != nil {
			return provider, err
		}
		if provider.SecretsSealed, err = keys.Seal(); err != nil {
			return provider, err
		}
	}
	return provider, nil
}

// Create registers a provider.
func (o *IdentityRuntime) Create(ctx context.Context, in IdentityProviderInput, audit ...repository.AuditRecord) (identity.Provider, error) {
	if o.store == nil {
		return identity.Provider{}, errors.New("identity provider storage is unavailable")
	}
	provider, err := in.provider(identity.Provider{})
	if err != nil {
		return identity.Provider{}, err
	}
	provider.ID = ids.New()
	created, err := o.store.CreateIdentityProvider(ctx, provider, audit...)
	if err == nil {
		o.invalidate()
	}
	return created, err
}

// Replace is a version-checked replace of the editable part. Slug and kind never change.
func (o *IdentityRuntime) Replace(ctx context.Context, id string, in IdentityProviderInput, expectedVersion int64, audit ...repository.AuditRecord) (identity.Provider, error) {
	current, err := o.Get(ctx, id)
	if err != nil {
		return identity.Provider{}, err
	}
	provider, err := in.provider(current)
	if err != nil {
		return identity.Provider{}, err
	}
	replaced, err := o.store.ReplaceIdentityProvider(ctx, provider, expectedVersion, audit...)
	if err == nil {
		o.invalidate()
	}
	return replaced, err
}

func (o *IdentityRuntime) Delete(ctx context.Context, id string, expectedVersion int64, audit ...repository.AuditRecord) error {
	if o.store == nil {
		return repository.ErrNotFound
	}
	err := o.store.DeleteIdentityProvider(ctx, id, expectedVersion, audit...)
	if err == nil {
		o.invalidate()
	}
	return err
}

// Seed applies the NOTARX_OIDC_* environment once: it registers the provider
// "default" while the registry is empty. A "default" provider that the registry
// migration created only to keep stored identities bound (issuer known, nothing
// else configured) is completed from the environment the same way. There is no
// ongoing merge: later environment changes are ignored.
func (o *IdentityRuntime) Seed(ctx context.Context, config OIDCRuntimeConfig) error {
	if o.store == nil || config.Issuer == "" {
		return nil
	}
	provider, err := seedProvider(config)
	if err != nil {
		return err
	}
	provider.ID = ids.New()
	seeded, err := o.store.SeedIdentityProvider(ctx, provider)
	if err != nil || seeded {
		o.invalidate()
		return err
	}
	// The environment never overrides a registry that already exists; say so, because an
	// operator who changed the variables would otherwise see no effect and no reason.
	ignored := func(reason string) {
		slog.Warn("NOTARX_OIDC_* ignored: it only seeds an empty identity provider registry; manage providers through the management API", "reason", reason, "issuer", config.Issuer)
	}
	stub, err := o.store.GetIdentityProviderBySlug(ctx, provider.Slug)
	if errors.Is(err, repository.ErrNotFound) {
		ignored("the registry already has providers")
		return nil
	}
	if err != nil {
		return err
	}
	settings, err := stub.OIDC()
	if err != nil || stub.Enabled || stub.Kind != identity.KindOIDC || stub.SecretsSealed != "" || settings.Audience != "" || settings.ClientID != "" {
		ignored("the default provider is already configured")
		return nil
	}
	want, _ := provider.OIDC()
	if settings.Issuer != want.Issuer {
		ignored("the default provider belongs to another issuer")
		return nil
	}
	provider.ID, provider.Version = stub.ID, stub.Version
	if _, err := o.store.ReplaceIdentityProvider(ctx, provider, stub.Version); err != nil && !errors.Is(err, repository.ErrVersionConflict) {
		return err
	}
	o.invalidate()
	return nil
}

func seedProvider(config OIDCRuntimeConfig) (identity.Provider, error) {
	provider := identity.Provider{
		Slug: "default", Kind: identity.KindOIDC, DisplayName: "Single sign-on", Enabled: config.Enabled,
		ProvisioningMode: identity.ProvisioningDisabled, JITDefaultRole: identity.JITRoleMember,
	}
	provider, err := provider.WithOIDC(identity.OIDCSettings{
		Issuer: config.Issuer, Audience: config.Audience, JWKSURL: config.JWKSURL, ClientID: config.ClientID,
		RedirectURL: config.RedirectURL, Scopes: config.Scopes, AdminSubjects: config.AdminSubjects,
		MemberRoles: config.Roles.Member, AdminRoles: config.Roles.Admin,
	})
	if err != nil {
		return provider, err
	}
	if config.ClientSecret != "" {
		if provider.SecretsSealed, err = secrets.Seal(identity.SecretPurpose, config.ClientSecret); err != nil {
			return provider, err
		}
	}
	return provider.NormalizeAndValidate()
}
