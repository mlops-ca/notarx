package repository

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mlops-ca/notarx/internal/jose"
)

// Workload identity federation storage (ADR 0019, "Data model"). Issuers say whose
// tokens are verified, trust policies bind verified claims to a service account, and the
// jti table makes single-use policies atomic. A policy confers no grants: the service
// account owns them.

var (
	// ErrWorkloadInvalid reports a field that violates the workload identity model.
	ErrWorkloadInvalid = errors.New("invalid workload identity resource")
	// ErrWorkloadIssuerExists reports a second issuer with the same iss value.
	ErrWorkloadIssuerExists = errors.New("workload issuer URL already exists")
	// ErrWorkloadReference reports a trust policy naming an unknown issuer or service account.
	ErrWorkloadReference = errors.New("workload trust policy references an unknown issuer or service account")
	// ErrWorkloadIssuerInUse reports an issuer that trust policies still reference.
	ErrWorkloadIssuerInUse = errors.New("workload issuer is referenced by trust policies")
)

// WorkloadIssuerKind selects the per-kind mandatory conditions of ADR 0019.
type WorkloadIssuerKind string

const (
	WorkloadKindGitHubActions WorkloadIssuerKind = "github_actions"
	WorkloadKindGitLabCI      WorkloadIssuerKind = "gitlab_ci"
	WorkloadKindKubernetes    WorkloadIssuerKind = "kubernetes"
	WorkloadKindGeneric       WorkloadIssuerKind = "generic"
)

// WorkloadJWKSMode says where an issuer's keys come from.
type WorkloadJWKSMode string

const (
	WorkloadJWKSDiscovery WorkloadJWKSMode = "discovery"
	WorkloadJWKSStatic    WorkloadJWKSMode = "static"
)

// WorkloadState is the lifecycle of an issuer or policy; only active rows are used to authenticate.
type WorkloadState string

const (
	WorkloadActive   WorkloadState = "active"
	WorkloadDisabled WorkloadState = "disabled"
)

const (
	// DefaultWorkloadTokenTTLSeconds is the exchanged-token lifetime cap of a new policy.
	DefaultWorkloadTokenTTLSeconds = 900
	maxWorkloadTokenTTLSeconds     = 86400
	maxWorkloadAudiences           = 16
	maxWorkloadCABundleBytes       = 256 << 10
	// WorkloadBasicUsernameAlias is reserved for the Basic "oidc" username of ADR 0019 slice 4,
	// so no policy may be named after it.
	WorkloadBasicUsernameAlias = "oidc"
)

// WorkloadIssuer is a trusted token issuer (table workload_issuers).
type WorkloadIssuer struct {
	ID   string
	Name string
	Kind WorkloadIssuerKind
	// Issuer is the exact iss value.
	Issuer              string
	Audiences           []string
	AllowedAlgs         []string
	JWKSMode            WorkloadJWKSMode
	JWKSURI             string
	StaticJWKS          json.RawMessage
	CABundlePEM         string
	AllowPrivateNetwork bool
	State               WorkloadState
	Version             string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// WorkloadTrustPolicy binds verified claims to a service account (table workload_trust_policies).
type WorkloadTrustPolicy struct {
	ID               string
	Name             string
	IssuerID         string
	ServiceAccountID string
	// Conditions is the canonical JSON array of {claim, op, values} validated by
	// internal/authorization/workloadpolicy.
	Conditions         json.RawMessage
	MaxTokenTTLSeconds int
	SingleUse          bool
	State              WorkloadState
	Version            string
	ExpiresAt          *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// WorkloadStore persists issuers, policies and single-use jti markers. Mutations take the audit
// records to write in the same transaction (ADR 0023 section 1). Updates replace every mutable
// field and require the version last read. Expired jti rows are deleted by the retention
// janitor (internal/retention), not by this store.
type WorkloadStore interface {
	CreateWorkloadIssuer(context.Context, WorkloadIssuer, ...AuditRecord) (WorkloadIssuer, error)
	GetWorkloadIssuer(context.Context, string) (WorkloadIssuer, error)
	// GetWorkloadIssuerByIssuer finds an issuer, active or not, by its exact iss value.
	GetWorkloadIssuerByIssuer(context.Context, string) (WorkloadIssuer, error)
	ListWorkloadIssuers(ctx context.Context, limit int, afterID string) ([]WorkloadIssuer, error)
	UpdateWorkloadIssuer(ctx context.Context, issuer WorkloadIssuer, expectedVersion string, audit ...AuditRecord) (WorkloadIssuer, error)
	DeleteWorkloadIssuer(ctx context.Context, id, expectedVersion string, audit ...AuditRecord) error

	CreateWorkloadTrustPolicy(context.Context, WorkloadTrustPolicy, ...AuditRecord) (WorkloadTrustPolicy, error)
	GetWorkloadTrustPolicy(context.Context, string) (WorkloadTrustPolicy, error)
	ListWorkloadTrustPolicies(ctx context.Context, limit int, afterID string) ([]WorkloadTrustPolicy, error)
	// ListActiveWorkloadTrustPolicies returns the active policies of an issuer that have not expired at now.
	ListActiveWorkloadTrustPolicies(ctx context.Context, issuerID string, now time.Time) ([]WorkloadTrustPolicy, error)
	UpdateWorkloadTrustPolicy(ctx context.Context, policy WorkloadTrustPolicy, expectedVersion string, audit ...AuditRecord) (WorkloadTrustPolicy, error)
	DeleteWorkloadTrustPolicy(ctx context.Context, id, expectedVersion string, audit ...AuditRecord) error

	// RecordWorkloadJTI inserts (issuer, sha256(jti)) once. It reports false when the pair was
	// already recorded, which is how a replay of a single-use token is detected atomically.
	RecordWorkloadJTI(ctx context.Context, issuerID string, jtiHash []byte, expiresAt time.Time) (bool, error)
}

var workloadSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

func workloadInvalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrWorkloadInvalid, fmt.Sprintf(format, args...))
}

func validWorkloadState(state WorkloadState) bool {
	return state == WorkloadActive || state == WorkloadDisabled
}

func httpsURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.Fragment == "" && parsed.User == nil
}

// normalizeWorkloadIssuer applies defaults and validates an issuer before it is stored.
func normalizeWorkloadIssuer(issuer WorkloadIssuer) (WorkloadIssuer, error) {
	if !workloadSlug.MatchString(issuer.Name) {
		return issuer, workloadInvalid("name must be a lower-case slug of at most 63 characters")
	}
	switch issuer.Kind {
	case WorkloadKindGitHubActions, WorkloadKindGitLabCI, WorkloadKindKubernetes, WorkloadKindGeneric:
	default:
		return issuer, workloadInvalid("unknown issuer kind %q", issuer.Kind)
	}
	if !httpsURL(issuer.Issuer) {
		return issuer, workloadInvalid("issuer must be an https URL")
	}
	issuer.Audiences = slices.Clone(issuer.Audiences)
	if len(issuer.Audiences) == 0 || len(issuer.Audiences) > maxWorkloadAudiences {
		return issuer, workloadInvalid("between 1 and %d audiences are required", maxWorkloadAudiences)
	}
	for _, audience := range issuer.Audiences {
		if strings.TrimSpace(audience) == "" || len(audience) > 512 {
			return issuer, workloadInvalid("audiences must be non-empty strings of at most 512 bytes")
		}
	}
	if len(issuer.AllowedAlgs) == 0 {
		issuer.AllowedAlgs = jose.DefaultAlgorithms()
	}
	issuer.AllowedAlgs = slices.Clone(issuer.AllowedAlgs)
	for _, alg := range issuer.AllowedAlgs {
		if !slices.Contains(jose.DefaultAlgorithms(), alg) {
			return issuer, workloadInvalid("algorithm %q is not allowed", alg)
		}
	}
	switch issuer.JWKSMode {
	case WorkloadJWKSDiscovery:
		if len(issuer.StaticJWKS) != 0 {
			return issuer, workloadInvalid("static_jwks is only valid with jwks_mode static")
		}
		if issuer.JWKSURI != "" && !httpsURL(issuer.JWKSURI) {
			return issuer, workloadInvalid("jwks_uri must be an https URL")
		}
	case WorkloadJWKSStatic:
		if issuer.JWKSURI != "" {
			return issuer, workloadInvalid("jwks_uri is not used with jwks_mode static")
		}
		if _, err := jose.ParseJWKS(issuer.StaticJWKS); err != nil {
			return issuer, workloadInvalid("static_jwks: %v", err)
		}
		issuer.StaticJWKS = slices.Clone(issuer.StaticJWKS)
	default:
		return issuer, workloadInvalid("unknown jwks_mode %q", issuer.JWKSMode)
	}
	if issuer.CABundlePEM != "" {
		if len(issuer.CABundlePEM) > maxWorkloadCABundleBytes || !x509.NewCertPool().AppendCertsFromPEM([]byte(issuer.CABundlePEM)) {
			return issuer, workloadInvalid("ca_bundle_pem must hold PEM certificates")
		}
	}
	if issuer.State == "" {
		issuer.State = WorkloadActive
	}
	if !validWorkloadState(issuer.State) {
		return issuer, workloadInvalid("unknown state %q", issuer.State)
	}
	return issuer, nil
}

// normalizeWorkloadTrustPolicy applies defaults and validates a policy before it is stored. The
// condition language itself is validated by internal/authorization/workloadpolicy.
func normalizeWorkloadTrustPolicy(policy WorkloadTrustPolicy) (WorkloadTrustPolicy, error) {
	if !workloadSlug.MatchString(policy.Name) || policy.Name == WorkloadBasicUsernameAlias {
		return policy, workloadInvalid("name must be a lower-case slug of at most 63 characters and not %q", WorkloadBasicUsernameAlias)
	}
	if _, err := uuid.Parse(policy.IssuerID); err != nil {
		return policy, workloadInvalid("issuer_id must be a UUID")
	}
	if _, err := uuid.Parse(policy.ServiceAccountID); err != nil {
		return policy, workloadInvalid("service_account_id must be a UUID")
	}
	var conditions []json.RawMessage
	if err := json.Unmarshal(policy.Conditions, &conditions); err != nil || len(conditions) == 0 {
		return policy, workloadInvalid("conditions must be a non-empty JSON array")
	}
	policy.Conditions = slices.Clone(policy.Conditions)
	if policy.MaxTokenTTLSeconds == 0 {
		policy.MaxTokenTTLSeconds = DefaultWorkloadTokenTTLSeconds
	}
	if policy.MaxTokenTTLSeconds < 1 || policy.MaxTokenTTLSeconds > maxWorkloadTokenTTLSeconds {
		return policy, workloadInvalid("max_token_ttl_seconds must be between 1 and %d", maxWorkloadTokenTTLSeconds)
	}
	if policy.State == "" {
		policy.State = WorkloadActive
	}
	if !validWorkloadState(policy.State) {
		return policy, workloadInvalid("unknown state %q", policy.State)
	}
	return policy, nil
}

func checkJTIArguments(issuerID string, jtiHash []byte) error {
	if _, err := uuid.Parse(issuerID); err != nil {
		return workloadInvalid("issuer id must be a UUID")
	}
	if len(jtiHash) != 32 {
		return workloadInvalid("jti hash must be a SHA-256 digest")
	}
	return nil
}
