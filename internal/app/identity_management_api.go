package app

import (
	"context"
	"net/http"

	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/repository"
)

// Identity management does not receive authentication lookups, login mutation,
// session creation, or expiry jobs merely because their records share a store.
type identityUserStore interface {
	CreateUser(context.Context, repository.User, ...repository.AuditRecord) (repository.User, error)
	ListUsers(context.Context, repository.UserListQuery) (repository.UserPage, error)
	GetUser(context.Context, string) (repository.User, error)
	UpdateUser(context.Context, repository.UserUpdate, string, ...repository.AuditRecord) (repository.User, error)
	DeleteUser(context.Context, string, ...repository.AuditRecord) error
	UpdateUserPassword(context.Context, string, string, string, bool, ...repository.AuditRecord) (repository.User, error)
	RevokeUserSessions(context.Context, string, string, ...repository.AuditRecord) (repository.User, error)
	ListUserSessions(context.Context, string, bool) ([]repository.UserSession, error)
	RevokeUserSession(context.Context, string, string, ...repository.AuditRecord) (repository.UserSession, error)
	ListUserIdentities(context.Context, string) ([]repository.UserIdentity, error)
	CreateUserIdentity(context.Context, repository.UserIdentity, ...repository.AuditRecord) (repository.UserIdentity, error)
	DeleteUserIdentity(context.Context, string, string, ...repository.AuditRecord) error
}

type identityAPIKeyStore interface {
	CreateAPIKey(context.Context, repository.APIKey) (repository.APIKey, error)
	ListAPIKeys(context.Context) ([]repository.APIKey, error)
	RevokeAPIKey(context.Context, string) (repository.APIKey, error)
}

type identityServiceAccountStore interface {
	CreateServiceAccount(context.Context, repository.ServiceAccount, ...repository.AuditRecord) (repository.ServiceAccount, error)
	ListServiceAccounts(context.Context, int, string) ([]repository.ServiceAccount, error)
	UpdateServiceAccount(context.Context, repository.ServiceAccountUpdate, string, ...repository.AuditRecord) (repository.ServiceAccount, error)
	CreateServiceAccountCredential(context.Context, repository.APIKey, ...repository.AuditRecord) (repository.APIKey, error)
	ListServiceAccountCredentials(context.Context, string, int, string) ([]repository.APIKey, error)
	RevokeServiceAccountCredential(context.Context, string, string, ...repository.AuditRecord) (repository.APIKey, error)
}

// identityProviderRuntime is the registry service of the identity management API.
type identityProviderRuntime interface {
	List(context.Context) ([]identity.Provider, error)
	Get(context.Context, string) (identity.Provider, error)
	Create(context.Context, IdentityProviderInput, ...repository.AuditRecord) (identity.Provider, error)
	Replace(ctx context.Context, id string, in IdentityProviderInput, expectedVersion int64, audit ...repository.AuditRecord) (identity.Provider, error)
	Delete(ctx context.Context, id string, expectedVersion int64, audit ...repository.AuditRecord) error
	Test(context.Context, string) (OIDCConnectionTest, error)
	ImportSAMLMetadata(ctx context.Context, id string, source SAMLMetadataSource, audit ...repository.AuditRecord) (identity.Provider, error)
	RotateSAMLSPKey(ctx context.Context, id string, audit ...repository.AuditRecord) (identity.Provider, error)
	SAMLView(identity.Provider) SAMLView
}

// workloadIssuerLookup finds a workload issuer (ADR 0019) by its exact iss value; the identity provider API uses it
// to keep the human and workload registries from overlapping.
type workloadIssuerLookup interface {
	GetWorkloadIssuerByIssuer(context.Context, string) (repository.WorkloadIssuer, error)
}

type identityManagementOptions struct {
	WorkloadIssuers   workloadIssuerLookup
	Scope             managementScope
	Users             identityUserStore
	APIKeys           identityAPIKeyStore
	ServiceAccounts   identityServiceAccountStore
	IdentityProviders identityProviderRuntime
}

// identityManagementHandler owns identity administration; repository content,
// lifecycle, operations and site persistence are not its dependencies.
type identityManagementHandler struct {
	managementScope
	users             identityUserStore
	apiKeys           identityAPIKeyStore
	serviceAccounts   identityServiceAccountStore
	identityProviders identityProviderRuntime
	workloadIssuers   workloadIssuerLookup
}

func newIdentityManagementHandler(options identityManagementOptions) identityManagementHandler {
	return identityManagementHandler{
		managementScope:   options.Scope,
		users:             options.Users,
		apiKeys:           options.APIKeys,
		serviceAccounts:   options.ServiceAccounts,
		identityProviders: options.IdentityProviders,
		workloadIssuers:   options.WorkloadIssuers,
	}
}

func (h identityManagementHandler) GetCurrentIdentity(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	writeNativeMavenJSON(w, http.StatusOK, currentIdentityResponse(principal))
}
