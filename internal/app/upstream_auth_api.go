package app

import (
	"errors"

	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
)

// upstreamAuthSecretPurpose binds every stored upstream credential ciphertext
// to this feature. Because internal/secrets uses the purpose as AEAD
// associated data, a ciphertext sealed for another purpose (a webhook signing
// key, an OIDC client secret) can never be replayed as an upstream credential.
const upstreamAuthSecretPurpose = "repository-upstream-auth"

// upstreamAuthRequest is the write shape for per-repository upstream registry
// credentials. Secret arrives as plaintext over TLS and is encrypted before
// storage; it never appears in responses. There is no separate "clear" flag:
// both supported schemes require a secret, so `scheme: none` is the only
// coherent way to remove a stored credential.
type upstreamAuthRequest struct {
	Scheme   repository.UpstreamAuthScheme `json:"scheme"`
	Username string                        `json:"username,omitempty"`
	Secret   string                        `json:"secret,omitempty"`
	// The entra-workload fields are not secret and are stored as given (ADR 0020).
	TenantID     string `json:"tenantId,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Subject      string `json:"subject,omitempty"`
	RegistryKind string `json:"registryKind,omitempty"`
}

// upstreamAuthSupported reports whether a format/type pair consumes upstream
// credentials. The explicitly implemented native Proxy formats consume them. Rejecting the field
// everywhere else is deliberate: accepting a credential that the fetch path
// never reads would let an operator configure authentication that silently
// does nothing.
func upstreamAuthSupported(format repository.Format, repoType repository.RepositoryType) bool {
	if repoType != repository.RepositoryTypeProxy {
		return false
	}
	switch format {
	case repository.FormatGo, repository.FormatMaven, repository.FormatOCI, repository.FormatNPM, repository.FormatPyPI, repository.FormatCargo, repository.FormatRaw, repository.FormatAPT, repository.FormatConan, repository.FormatHuggingFace:
		return true
	default:
		return false
	}
}

// resolveUpstreamAuth validates the request shape and encrypts the secret.
// existing carries the currently stored credential on update (nil on create);
// its secret survives only while the scheme is unchanged, so switching scheme
// requires resubmitting the secret instead of silently reusing the old one.
func resolveUpstreamAuth(request *upstreamAuthRequest, existing *repository.UpstreamAuth) (*repository.UpstreamAuth, error) {
	if request == nil {
		return existing, nil
	}
	if request.Scheme == repository.UpstreamAuthSchemeNone {
		if request.Username != "" || request.Secret != "" {
			return nil, errors.New("upstream authentication scheme none must not carry credentials")
		}
		return nil, nil
	}
	if request.Scheme == repository.UpstreamAuthSchemeEntraWorkload {
		if request.Secret != "" {
			return nil, errors.New("upstream entra-workload authentication carries no username or secret")
		}
		resolved := &repository.UpstreamAuth{Scheme: request.Scheme, Username: request.Username, TenantID: request.TenantID, ClientID: request.ClientID,
			Scope: request.Scope, Subject: request.Subject, RegistryKind: request.RegistryKind}
		if err := resolved.Validate(); err != nil {
			return nil, err
		}
		return resolved, nil
	}
	if request.TenantID != "" || request.ClientID != "" || request.Scope != "" || request.Subject != "" || request.RegistryKind != "" {
		return nil, errors.New("tenantId, clientId, scope, subject and registryKind apply only to the entra-workload scheme")
	}
	resolved := &repository.UpstreamAuth{Scheme: request.Scheme, Username: request.Username}
	switch {
	case request.Secret != "":
		encrypted, err := secrets.Seal(upstreamAuthSecretPurpose, request.Secret)
		if err != nil {
			return nil, err
		}
		resolved.Secret = encrypted
	case existing != nil && existing.Scheme == request.Scheme:
		resolved.Secret = existing.Secret
	}
	if err := resolved.Validate(); err != nil {
		return nil, err
	}
	return resolved, nil
}

// upstreamCredentialBindingChanged treats the complete configured endpoint and
// OCI issuer/audience as the authority boundary for a stored source credential.
func upstreamCredentialBindingChanged(existing, next repository.HostedRepository) bool {
	if existing.Endpoint != next.Endpoint {
		return true
	}
	if existing.Format != repository.FormatOCI {
		return false
	}
	if existing.OCIBearer == nil || next.OCIBearer == nil {
		return (existing.OCIBearer == nil) != (next.OCIBearer == nil)
	}
	return *existing.OCIBearer != *next.OCIBearer
}

// upstreamAuthSchemeSupported rejects the entra-workload scheme for Conan, whose proxy exchanges
// a Basic credential for its own source token and has no use for an Entra access token.
func upstreamAuthSchemeSupported(format repository.Format, auth *repository.UpstreamAuth) bool {
	return auth == nil || auth.Scheme != repository.UpstreamAuthSchemeEntraWorkload || format != repository.FormatConan
}

func upstreamCredentialRebindNeedsSecret(existing, next repository.HostedRepository, request *upstreamAuthRequest) bool {
	if existing.UpstreamAuth == nil || existing.UpstreamAuth.Scheme == repository.UpstreamAuthSchemeNone || next.UpstreamAuth == nil || !upstreamCredentialBindingChanged(existing, next) {
		return false
	}
	if next.UpstreamAuth.Scheme == repository.UpstreamAuthSchemeEntraWorkload {
		// There is no secret to resubmit: moving the token to a new destination must be an explicit,
		// repeated upstreamAuth in the same request.
		return request == nil
	}
	return request == nil || request.Secret == ""
}

// redactUpstreamAuth strips the stored secret from a repository about to be
// encoded in a management response, replacing it with a configured marker. The
// copy keeps the stored value intact for the protocol paths.
func redactUpstreamAuth(repo repository.HostedRepository) repository.HostedRepository {
	if repo.UpstreamAuth == nil {
		return repo
	}
	redacted := *repo.UpstreamAuth
	redacted.CredentialsConfigured = redacted.Secret != "" || redacted.Scheme == repository.UpstreamAuthSchemeEntraWorkload
	redacted.Secret = ""
	repo.UpstreamAuth = &redacted
	return repo
}

// redactRepositorySecrets applies every credential redactor. Management
// responses must never be encoded without it.
func redactRepositorySecrets(repo repository.HostedRepository) repository.HostedRepository {
	return redactUpstreamAuth(redactEgressProxy(repo))
}
