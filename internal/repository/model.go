package repository

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Format string

const (
	FormatRaw   Format = "raw"
	FormatOCI   Format = "oci"
	FormatMaven Format = "maven"
	FormatNPM   Format = "npm"
	FormatPyPI  Format = "pypi"
	FormatGo    Format = "go"
	FormatCargo Format = "cargo"
	// FormatAPT is a protocol-only Debian repository proxy. APT publication
	// requires trusted Release metadata and signing, so Hosted is intentionally
	// not admitted until that workflow is implemented.
	FormatAPT Format = "apt"
	// FormatConan is a managed authorization target with native Hosted, Proxy,
	// and Group protocol routes.
	FormatConan Format = "conan"
)

type RepositoryState string

const (
	RepositoryActive   RepositoryState = "active"
	RepositoryDeleting RepositoryState = "deleting"
	RepositoryDeleted  RepositoryState = "deleted"
)

type RepositoryType string

const (
	RepositoryTypeHosted RepositoryType = "hosted"
	RepositoryTypeProxy  RepositoryType = "proxy"
)

type HostedRepository struct {
	ID                     string          `json:"id"`
	Name                   string          `json:"name"`
	Format                 Format          `json:"format"`
	Type                   RepositoryType  `json:"type"`
	Endpoint               string          `json:"endpoint,omitempty"`
	AllowedHosts           []string        `json:"allowedHosts,omitempty"`
	EgressProxy            *EgressProxy    `json:"egressProxy,omitempty"`
	UpstreamAuth           *UpstreamAuth   `json:"upstreamAuth,omitempty"`
	OCIBearer              *OCIBearer      `json:"ociBearer,omitempty"`
	Visibility             Visibility      `json:"visibility"`
	MavenStrictPublication bool            `json:"mavenStrictPublication"`
	State                  RepositoryState `json:"state"`
	Version                string          `json:"version"`
	// GrantsVersion counts the changes of the repository grant set. Every grant mutation bumps it in its own
	// transaction, so the read cache of grant sets (ADR 0023 section 6) keys on it and a bump is a miss.
	GrantsVersion int64     `json:"-"`
	CreatedAt     time.Time `json:"-"`
}

// EgressProxyMode selects how a Proxy Repository reaches its upstream.
type EgressProxyMode string

const (
	EgressProxyModeDirect      EgressProxyMode = "direct"
	EgressProxyModeEnvironment EgressProxyMode = "environment"
	EgressProxyModeCustom      EgressProxyMode = "custom"
)

// EgressProxyProtocol is the network protocol of a custom egress proxy.
type EgressProxyProtocol string

const (
	EgressProxyProtocolHTTP   EgressProxyProtocol = "http"
	EgressProxyProtocolSOCKS5 EgressProxyProtocol = "socks5"
)

// EgressProxy is the per-Proxy-Repository egress network proxy configuration.
// Password holds AES-256-GCM ciphertext (base64), never plaintext; it is
// redacted from all management API responses.
type EgressProxy struct {
	Mode      EgressProxyMode     `json:"mode"`
	Protocol  EgressProxyProtocol `json:"protocol,omitempty"`
	Host      string              `json:"host,omitempty"`
	Port      int                 `json:"port,omitempty"`
	Username  string              `json:"username,omitempty"`
	Password  string              `json:"password,omitempty"`
	RemoteDNS bool                `json:"remoteDns,omitempty"`
	NoProxy   []string            `json:"noProxy,omitempty"`
	// CredentialsConfigured is a response-only marker computed at encode time;
	// it is never persisted.
	CredentialsConfigured bool `json:"credentialsConfigured,omitempty"`
}

// Validate enforces the egress proxy invariants documented in
// docs/proxy-egress-design.md.
func (p *EgressProxy) Validate() error {
	if p == nil {
		return nil
	}
	switch p.Mode {
	case EgressProxyModeDirect, EgressProxyModeEnvironment:
		if p.Protocol != "" || p.Host != "" || p.Port != 0 || p.Username != "" || p.Password != "" || p.RemoteDNS || len(p.NoProxy) > 0 {
			return errors.New("egress proxy custom fields require custom mode")
		}
		return nil
	case EgressProxyModeCustom:
		if p.Protocol != EgressProxyProtocolHTTP && p.Protocol != EgressProxyProtocolSOCKS5 {
			return errors.New("egress proxy protocol must be http or socks5")
		}
		if p.Host == "" || strings.ContainsAny(p.Host, "/@") || strings.Contains(p.Host, "://") {
			return errors.New("egress proxy host must be a bare hostname or IP")
		}
		if p.Port < 1 || p.Port > 65535 {
			return errors.New("egress proxy port must be between 1 and 65535")
		}
		if p.RemoteDNS && p.Protocol != EgressProxyProtocolSOCKS5 {
			return errors.New("remoteDns only applies to the socks5 protocol")
		}
		for _, entry := range p.NoProxy {
			if strings.TrimSpace(entry) == "" || strings.Contains(entry, "://") {
				return errors.New("egress proxy noProxy entries must be host suffixes or CIDR ranges")
			}
		}
		return nil
	default:
		return errors.New("egress proxy mode must be direct, environment, or custom")
	}
}

// UpstreamAuthScheme selects how a Proxy Repository authenticates to its
// upstream registry. SchemeNone is a request-side sentinel that removes stored
// credentials; it is normalized away before persistence, so an absent
// UpstreamAuth remains the only stored representation of anonymous reads.
type UpstreamAuthScheme string

const (
	UpstreamAuthSchemeNone   UpstreamAuthScheme = "none"
	UpstreamAuthSchemeBasic  UpstreamAuthScheme = "basic"
	UpstreamAuthSchemeBearer UpstreamAuthScheme = "bearer"
	// UpstreamAuthSchemeEntraWorkload authenticates to Microsoft Entra ID with a short-lived
	// assertion that notarx signs itself (ADR 0020). It carries no secret and is accepted only
	// while the OIDC issuer is enabled.
	UpstreamAuthSchemeEntraWorkload UpstreamAuthScheme = "entra-workload"
)

// RegistryKindACR marks an entra-workload upstream as Azure Container Registry, which needs
// the registry specific token exchange after the Entra token.
const RegistryKindACR = "acr"

var (
	entraWorkloadEnabled atomic.Bool
	entraTenantPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)
	entraClientIDPattern = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	entraSubjectPattern  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,256}$`)
)

// AllowEntraWorkload records whether the OIDC issuer is enabled; UpstreamAuth.Validate rejects
// the entra-workload scheme otherwise. The runtime sets it once at startup.
func AllowEntraWorkload(enabled bool) { entraWorkloadEnabled.Store(enabled) }

func (a *UpstreamAuth) validateEntraWorkload() error {
	if !entraWorkloadEnabled.Load() {
		return errors.New("upstream authentication scheme entra-workload requires NOTARX_ISSUER_ENABLED=true")
	}
	if a.Username != "" || a.Secret != "" {
		return errors.New("upstream entra-workload authentication carries no username or secret")
	}
	if !entraTenantPattern.MatchString(a.TenantID) {
		return errors.New("upstream entra-workload authentication requires a tenantId (a tenant GUID or domain)")
	}
	if !entraClientIDPattern.MatchString(a.ClientID) {
		return errors.New("upstream entra-workload authentication requires a clientId that is a GUID")
	}
	if a.Scope == "" || len(a.Scope) > 512 || strings.IndexFunc(a.Scope, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return errors.New("upstream entra-workload authentication requires a single scope without whitespace")
	}
	if a.Subject != "" && !entraSubjectPattern.MatchString(a.Subject) {
		return errors.New("upstream entra-workload subject may only contain letters, digits and ._:-")
	}
	if a.RegistryKind != "" && a.RegistryKind != RegistryKindACR {
		return errors.New("upstream entra-workload registryKind must be acr or empty")
	}
	return nil
}

// UpstreamAuth is the per-Proxy-Repository credential presented to the
// upstream registry. Secret holds AES-256-GCM ciphertext (base64) sealed with
// NOTARX_SETTINGS_ENCRYPTION_KEY, never plaintext; it is redacted from every
// management API response, which carries CredentialsConfigured instead.
type UpstreamAuth struct {
	Scheme   UpstreamAuthScheme `json:"scheme"`
	Username string             `json:"username,omitempty"`
	Secret   string             `json:"secret,omitempty"`
	// The remaining fields configure entra-workload and are not secret (ADR 0020).
	TenantID string `json:"tenantId,omitempty"`
	ClientID string `json:"clientId,omitempty"`
	// Scope is the Entra scope requested for the access token, for example
	// https://storage.azure.com/.default.
	Scope string `json:"scope,omitempty"`
	// Subject is the exact sub claim of the assertion. Empty selects the built-in instance
	// principal notarx:instance:<deployment-id>.
	Subject string `json:"subject,omitempty"`
	// RegistryKind is "acr" for Azure Container Registry, empty otherwise.
	RegistryKind string `json:"registryKind,omitempty"`
	// Repository names the repository the credential belongs to. It is filled when the
	// repository is loaded, never persisted, and only feeds audit records.
	Repository string `json:"-"`
	// CredentialsConfigured is a response-only marker computed at encode time;
	// it is never persisted.
	CredentialsConfigured bool `json:"credentialsConfigured,omitempty"`
}

// Validate enforces the upstream credential invariants. Bearer credentials
// carry a secret and nothing else; basic credentials require both a username
// and a secret. A nil receiver is valid and means anonymous upstream reads.
func (a *UpstreamAuth) Validate() error {
	if a == nil {
		return nil
	}
	if a.Scheme != UpstreamAuthSchemeEntraWorkload && (a.TenantID != "" || a.ClientID != "" || a.Scope != "" || a.Subject != "" || a.RegistryKind != "") {
		return errors.New("tenantId, clientId, scope, subject and registryKind apply only to the entra-workload scheme")
	}
	switch a.Scheme {
	case UpstreamAuthSchemeNone:
		if a.Username != "" || a.Secret != "" {
			return errors.New("upstream authentication scheme none must not carry credentials")
		}
	case UpstreamAuthSchemeEntraWorkload:
		return a.validateEntraWorkload()
	case UpstreamAuthSchemeBasic:
		if strings.TrimSpace(a.Username) == "" {
			return errors.New("upstream basic authentication requires a username")
		}
		if a.Secret == "" {
			return errors.New("upstream basic authentication requires a secret")
		}
	case UpstreamAuthSchemeBearer:
		if a.Username != "" {
			return errors.New("upstream bearer authentication does not accept a username")
		}
		if a.Secret == "" {
			return errors.New("upstream bearer authentication requires a secret")
		}
	default:
		return errors.New("upstream authentication scheme must be basic, bearer or entra-workload")
	}
	return nil
}

func normalizeHostedRepository(repo HostedRepository) HostedRepository {
	if repo.Type == "" {
		repo.Type = RepositoryTypeHosted
	}
	if repo.AllowedHosts == nil {
		repo.AllowedHosts = []string{}
	}
	repo.Visibility = NormalizeVisibility(repo.Visibility)
	if repo.UpstreamAuth != nil {
		// The repository name travels with its credential so exchanges can be audited per
		// repository; a copy keeps the caller's struct untouched.
		attributed := *repo.UpstreamAuth
		attributed.Repository = repo.Name
		repo.UpstreamAuth = &attributed
	}
	return repo
}

// PublicAccessPolicy is the instance kill switch for public visibility (ADR
// 0029). While it is disabled, a repository or group marked public behaves as
// authenticated; the gate never widens access.
type PublicAccessPolicy struct {
	Enabled bool   `json:"enabled"`
	Version string `json:"version"`
}

// RepositoryCapacity is logical usage attributed to one Hosted repository.
// Shared content-addressed bytes are counted once for each visible repository
// reference so quotas remain meaningful after promotion.
type RepositoryCapacity struct {
	RepositoryID       string `json:"repositoryId"`
	Format             Format `json:"format"`
	UsedBytes          int64  `json:"usedBytes"`
	ObjectCount        int64  `json:"objectCount"`
	QuotaBytes         int64  `json:"quotaBytes"` // zero means no configured quota
	PrimaryBytes       int64  `json:"primaryBytes,omitempty"`
	SidecarBytes       int64  `json:"sidecarBytes,omitempty"`
	NegativeCount      int64  `json:"negativeCount,omitempty"`
	ExpiredObjectCount int64  `json:"expiredObjectCount,omitempty"`
	ReclaimableBytes   int64  `json:"reclaimableBytes,omitempty"`
}

type RepositoryCapacityRecord struct {
	Repository HostedRepository
	Capacity   RepositoryCapacity
}

// RequestWindowCounts counts audited protocol requests in rolling windows.
// Windows are inclusive of their lower boundary and end at the snapshot time.
type RequestWindowCounts struct {
	OneDay     int64 `json:"oneDay"`
	SevenDays  int64 `json:"sevenDays"`
	ThirtyDays int64 `json:"thirtyDays"`
}

type RepositoryRequestStatistics struct {
	Repository string
	Requests   RequestWindowCounts
	Denied     RequestWindowCounts
}

type HostedGroup struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Format     Format        `json:"format"`
	Visibility Visibility    `json:"visibility"`
	Members    []GroupMember `json:"members"`
	Version    string        `json:"version"`
	// LessVisibleMembers is computed by the management API when it reads a group,
	// never stored: the members whose own visibility is below the group's.
	LessVisibleMembers []VisibilityRef `json:"lessVisibleMembers,omitempty"`
}

// AsRepository projects a group onto the repository shape that authorization
// and audit code takes, so a group is authorized like a proxy repository of its
// format. The projection keeps the group's visibility.
func (g HostedGroup) AsRepository(format Format) HostedRepository {
	return HostedRepository{ID: g.ID, Name: g.Name, Version: g.Version, Format: format, Type: RepositoryTypeProxy, State: RepositoryActive, Visibility: g.Visibility}
}

type GroupMember struct {
	RepositoryID string `json:"repositoryId"`
	Position     int    `json:"position"`
}

type RepositoryGrant struct {
	Principal      string   `json:"principal"`
	Scopes         []string `json:"scopes"`
	ResourcePrefix string   `json:"resourcePrefix,omitempty"`
}

type RepositoryGrantSet struct {
	Version string
	Grants  []RepositoryGrant
}

type RepositoryGrantRecord struct {
	RepositoryID   string
	RepositoryName string
	Format         Format
	Grant          RepositoryGrant
}

// AuthorizationTemplate is a reusable, repository-scoped set of grant rules.
// The rules are validated against the target repository format when applied.
type AuthorizationTemplate struct {
	ID          string
	Name        string
	Description string
	Grants      []RepositoryGrant
	Version     string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// AuthorizationRole is a reusable repository permission set. Grants and
// templates copy its scopes when edited so later role changes never mutate
// already-persisted authorization decisions.
type AuthorizationRole struct {
	ID          string
	Name        string
	Description string
	Scopes      []string
	Version     string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type RepositoryRetentionPolicy struct {
	Version            string   `json:"version"`
	Enabled            bool     `json:"enabled"`
	KeepDays           int      `json:"keepDays"`
	SnapshotKeepDays   int      `json:"snapshotKeepDays"`
	MinimumVersions    int      `json:"minimumVersions"`
	MaximumVersions    int      `json:"maximumVersions"`
	KeepDownloadedDays int      `json:"keepDownloadedDays"`
	CoordinatePatterns []string `json:"coordinatePatterns"`
	ProtectedPatterns  []string `json:"protectedPatterns"`
}

// RepositorySecurityPolicy defines the admission rules applied when an
// immutable artifact is promoted into this repository. It intentionally does
// not affect ordinary reads or writes in the source repository.
type RepositorySecurityPolicy struct {
	Version                  string   `json:"version"`
	Enabled                  bool     `json:"enabled"`
	AutoScanOnPublish        bool     `json:"autoScanOnPublish"`
	RequireSignature         bool     `json:"requireSignature"`
	RequireVerifiedSignature bool     `json:"requireVerifiedSignature"`
	RequireSBOM              bool     `json:"requireSbom"`
	RequireProvenance        bool     `json:"requireProvenance"`
	RequireVulnerabilityScan bool     `json:"requireVulnerabilityScan"`
	MaxAllowedSeverity       string   `json:"maxAllowedSeverity"`
	FailOnScanError          bool     `json:"failOnScanError"`
	AllowedLicenses          []string `json:"allowedLicenses"`
}

// RepositoryQuarantineReadPolicy controls protocol-read enforcement for
// quarantined artifacts in one Hosted repository. It is independent from
// promotion admission and defaults disabled for compatibility.
type RepositoryQuarantineReadPolicy struct {
	Version string `json:"version"`
	Enabled bool   `json:"enabled"`
}

// SecurityPolicyEvaluation is the stable result returned by dry-run checks
// and used by promotion admission. Reasons are machine-readable codes.
type SecurityPolicyEvaluation struct {
	Allowed             bool     `json:"allowed"`
	Enforced            bool     `json:"enforced"`
	PolicyVersion       string   `json:"policyVersion"`
	IntelligencePresent bool     `json:"intelligencePresent"`
	Reasons             []string `json:"reasons"`
}

type RawAsset struct {
	RepositoryID, Path, Digest, ObjectKey, ContentType string
	Size                                               int64
	UpdatedAt                                          time.Time
}
type RawObject struct {
	RepositoryID, Digest, ObjectKey string
	Size                            int64
	CreatedAt, CollectedAt          time.Time
}
type RawUpload struct {
	ID, RepositoryID, Path, ObjectKey, State string
	Offset                                   int64
	ExpiresAt, CollectedAt                   time.Time
}

// NPMPackage is the registry packument identity projected from immutable
// versions and mutable distribution tags.
type NPMPackage struct {
	// SourceVersion binds Proxy cache authorization to one repository configuration.
	SourceVersion string
	RepositoryID  string
	Name          string
	Revision      int64
	DistTags      map[string]string
	Versions      []NPMVersion
	// TombstonedVersions are authority barriers for Group resolution. They
	// never appear in a registry response or permit a tarball read.
	TombstonedVersions []NPMVersion
	SourceEndpoint     string
	UpstreamETag       string
	UpstreamModified   string
	MetadataExpiresAt  time.Time
	NegativeExpiresAt  time.Time
	Negative           bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// NPMVersion is one immutable package publication. Manifest retains the
// protocol-native version document; dist URLs and integrity fields are
// overwritten from trusted server metadata when the packument is served.
type NPMVersion struct {
	// SourceVersion is required only as CacheNPMProxyTarball input. The caller
	// supplies the repository version captured before fetching; adapters never
	// infer it or persist/restore it as publication provenance.
	SourceVersion   string `json:"-"`
	RepositoryID    string
	PackageName     string
	Version         string
	Digest          string
	Integrity       string
	Shasum          string
	TarballName     string
	UpstreamTarball string
	ObjectKey       string
	Size            int64
	Manifest        []byte
	Deprecated      string
	Publisher       string
	State           string
	CachedAt        time.Time
	DeletedAt       time.Time
	CollectingAt    time.Time
	CollectedAt     time.Time
	CreatedAt       time.Time
}

// NPMDistTagMutation names one Hosted dist-tag change. Version is ignored by
// DeleteNPMDistTag. Audit is committed in the same transaction as a successful
// mutation; the store adds distTag, version, previous, and changed evidence.
type NPMDistTagMutation struct {
	RepositoryID string
	PackageName  string
	Tag          string
	Version      string
	Audit        AuditRecord
}

// NPMDistTagChange is the committed result of one dist-tag mutation. Previous
// is the version the tag named before the mutation (empty for a new tag), and
// Changed is false when the request repeated the current state.
type NPMDistTagChange struct {
	DistTags map[string]string
	Previous string
	Changed  bool
}

// AuditRecord returns the mutation audit with the committed change as evidence.
func (m NPMDistTagMutation) AuditRecord(change NPMDistTagChange, version string) AuditRecord {
	record := m.Audit
	record.Evidence = make(map[string]string, len(m.Audit.Evidence)+4)
	for key, value := range m.Audit.Evidence {
		record.Evidence[key] = value
	}
	record.Evidence["distTag"] = m.Tag
	record.Evidence["version"] = version
	record.Evidence["previous"] = change.Previous
	record.Evidence["changed"] = strconv.FormatBool(change.Changed)
	return record
}

type NPMObject struct {
	RepositoryID string
	ObjectKey    string
	Digest       string
	Size         int64
	DeletedAt    time.Time
}

// PyPIFile is one immutable wheel or source distribution exposed through the
// PEP 503 Simple Repository API.
type PyPIFile struct {
	// SourceVersion binds Proxy cache authorization to one repository configuration.
	SourceVersion  string
	RepositoryID   string
	Project        string
	Version        string
	Filename       string
	FileType       string
	PythonVersion  string
	RequiresPython string
	Digest         string
	ObjectKey      string
	Size           int64
	Publisher      string
	SourceURL      string
	State          string
	CachedAt       time.Time
	DeletedAt      time.Time
	CollectedAt    time.Time
	CreatedAt      time.Time
	Yanked         bool
	YankReason     string
	YankRevision   int64
}

type PyPIProjectSummary struct {
	RepositoryID string
	Project      string
	Latest       PyPIFile
	VersionCount int
	FileCount    int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type PyPIObject struct {
	RepositoryID string
	ObjectKey    string
	Digest       string
	Size         int64
	DeletedAt    time.Time
}

// GoModuleVersion is one immutable module version known by a Go Repository.
// Proxy metadata may exist before every representation is cached; Hosted
// publication commits all required representations atomically.
type GoModuleVersion struct {
	RepositoryID string
	Module       string
	Version      string
	PublishedAt  time.Time
	Publisher    string
	CachedAt     time.Time
	CreatedAt    time.Time
}

// GoModuleAsset is one protocol representation for a module version. Kind is
// one of info, mod, or zip.
type GoModuleAsset struct {
	RepositoryID string
	Module       string
	Version      string
	Kind         string
	Digest       string
	ObjectKey    string
	Size         int64
	SourceURL    string
	CachedAt     time.Time
	CreatedAt    time.Time
	CollectingAt time.Time
	CollectedAt  time.Time
}

type GoModuleObject struct {
	RepositoryID string
	ObjectKey    string
	Digest       string
	Size         int64
	TombstonedAt time.Time
}

// GoModulePublication is the atomic Hosted visibility boundary for one module
// version. Assets must contain exactly one info, mod, and zip representation.
type GoModulePublication struct {
	Version GoModuleVersion
	Assets  []GoModuleAsset
}

// APTAsset is an immutable byte representation cached from a Debian
// repository. The path is the protocol identity (for example
// dists/bookworm/InRelease or pool/main/h/hello/hello_2.10_amd64.deb).
type APTAsset struct {
	// SourceVersion binds Proxy cache authorization to one repository configuration.
	SourceVersion    string
	RepositoryID     string
	Path             string
	Digest           string
	ObjectKey        string
	Size             int64
	ContentType      string
	SourceURL        string
	UpstreamETag     string
	UpstreamModified string
	CachedAt         time.Time
	CreatedAt        time.Time
}

type APTPublicationSessionState string

const (
	APTPublicationSessionOpen      APTPublicationSessionState = "open"
	APTPublicationSessionUploading APTPublicationSessionState = "uploading"
	APTPublicationSessionStaged    APTPublicationSessionState = "staged"
	APTPublicationSessionAborted   APTPublicationSessionState = "aborted"
)

// APTPublicationSession reserves one immutable .deb upload for a target suite
// and component. Canonical package identity is not accepted as authority: it
// is populated only after parsing the uploaded Debian control file.
type APTPublicationSession struct {
	ID                 string
	RepositoryID       string
	Suite              string
	Component          string
	Publisher          string
	ObjectName         string
	DeclaredDigest     string
	DeclaredSize       int64
	ExpectedIdentity   string
	ObjectKey          string
	PackageRevisionID  string
	State              APTPublicationSessionState
	ExpiresAt          time.Time
	CreatedAt          time.Time
	ReclaimScheduledAt time.Time
	CollectedAt        time.Time
}

// APTPackageRevision is immutable package metadata derived from the .deb
// bytes. It is management-visible but not protocol-visible until referenced by
// a signed, atomically published repository snapshot.
type APTPackageRevision struct {
	ID                string
	RepositoryID      string
	Package           string
	Version           string
	Architecture      string
	CanonicalIdentity string
	Digest            string
	ObjectKey         string
	Size              int64
	ObjectName        string
	Publisher         string
	CreatedAt         time.Time
}

type APTRepositorySnapshotState string

const (
	APTRepositorySnapshotBuilding APTRepositorySnapshotState = "building"
	APTRepositorySnapshotSigned   APTRepositorySnapshotState = "signed"
	APTRepositorySnapshotVisible  APTRepositorySnapshotState = "visible"
	APTRepositorySnapshotRetired  APTRepositorySnapshotState = "retired"
	APTRepositorySnapshotFailed   APTRepositorySnapshotState = "failed"
	APTRepositorySnapshotPruned   APTRepositorySnapshotState = "pruned"
)

// APTRepositorySnapshot is the single visibility switch for one signed suite.
// Building and failed snapshots are never returned by protocol reads.
type APTRepositorySnapshot struct {
	ID                 string
	RepositoryID       string
	Suite              string
	Sequence           int64
	State              APTRepositorySnapshotState
	ReleaseDigest      string
	InReleaseDigest    string
	SignerIdentity     string
	KeyFingerprint     string
	SignatureAlgorithm string
	CreatedAt          time.Time
	PublishedAt        time.Time
}

type APTSnapshotPackage struct {
	SnapshotID           string
	PublicationSessionID string
	PackageRevisionID    string
	Component            string
	Architecture         string
}

// APTSnapshotAsset binds one client-visible path to immutable object bytes.
// Assets become readable only when their owning snapshot atomically becomes
// visible; generated metadata and package bytes therefore cannot be mixed
// across publication attempts.
type APTSnapshotAsset struct {
	SnapshotID   string
	RepositoryID string
	Path         string
	Digest       string
	ObjectKey    string
	Size         int64
	ContentType  string
}

// APTSnapshotObjectIntent records generated content-addressed bytes before the
// object store is mutated. Failed or abandoned builds can therefore be
// reclaimed only after checking that no durable package or snapshot uses them.
type APTSnapshotObjectIntent struct {
	SnapshotID   string
	RepositoryID string
	ObjectKey    string
	Digest       string
	Size         int64
	CreatedAt    time.Time
	ScheduledAt  time.Time
	CollectedAt  time.Time
}

type APTAbandonedUpload struct {
	SessionID    string
	RepositoryID string
	ObjectKey    string
}

// APTAssetMutable reports whether an upstream path may legitimately change in
// place. Content-addressed by-hash metadata and pool package objects are
// immutable even though they live below otherwise mutable repository trees.
func APTAssetMutable(path string) bool {
	return strings.HasPrefix(path, "dists/") && !strings.Contains(path, "/by-hash/")
}

type NPMPackageSummary struct {
	RepositoryID string
	Name         string
	Latest       NPMVersion
	VersionCount int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type OCIUpload struct {
	ID, RepositoryID, Name, ObjectKey, State string
	Offset                                   int64
	ExpiresAt                                time.Time
	CollectedAt                              time.Time
}

// RuntimeNode is the durable inventory record for one Gateway process session.
// Instance IDs are deployment-owned labels; SessionID fences restarts and
// exposes accidental duplicate instance IDs without collapsing their records.
type RuntimeNode struct {
	InstanceID    string
	SessionID     string
	BuildVersion  string
	BuildRevision string
	Roles         []string
	WorkerFormats []string
	WorkerKinds   []string
	StartedAt     time.Time
	LastSeenAt    time.Time
	StoppedAt     time.Time
}

func (n RuntimeNode) Validate() error {
	if n.InstanceID == "" || n.SessionID == "" || n.LastSeenAt.IsZero() || n.StartedAt.IsZero() {
		return ErrInvalidRuntimeNode
	}
	return nil
}

type OCIBlob struct {
	Digest, ObjectKey string
	Size              int64
}
type OCIObjectIntent struct {
	RepositoryID, ObjectKey, Digest   string
	Size                              int64
	CreatedAt, ClaimedAt, CollectedAt time.Time
}

type OCIManifest struct {
	RepositoryID, Name, Digest, ObjectKey, MediaType string
	SubjectDigest, ArtifactType                      string
	Size                                             int64
	CreatedAt                                        time.Time
	Tags                                             []string
}

// ArtifactTombstone keeps a deleted artifact's identity until its byte objects
// are safely reclaimed by the lifecycle collector.
type ArtifactTombstone struct {
	RepositoryID string
	Format       Format
	Coordinate   string
	Digest       string
	TombstonedAt time.Time
}

// ArtifactQuarantineState is repository-local governance state for one
// immutable artifact identity. It is deliberately independent from the
// visible/tombstoned lifecycle state.
type ArtifactQuarantineState string

const (
	ArtifactQuarantineStateQuarantined ArtifactQuarantineState = "quarantined"
	ArtifactQuarantineStateReleased    ArtifactQuarantineState = "released"
)

// ArtifactQuarantine records the latest explicit quarantine decision for one
// canonical repository, format, coordinate, and digest identity.
type ArtifactQuarantine struct {
	RepositoryID  string
	Format        Format
	Coordinate    string
	Digest        string
	State         ArtifactQuarantineState
	Reason        string
	UpdatedBy     string
	Version       string
	QuarantinedAt time.Time
	ReleasedAt    time.Time
	UpdatedAt     time.Time
}

// ArtifactIntelligence is format-neutral security and build metadata attached
// to one immutable artifact identity. Scanners and CI systems produce the
// evidence represented here; the Gateway stores and serves the summaries.
type ArtifactIntelligence struct {
	RepositoryID  string
	Format        Format
	Coordinate    string
	Digest        string
	Signatures    []ArtifactSignature
	SBOMs         []ArtifactSBOM
	Provenance    *ArtifactProvenance
	Licenses      []ArtifactLicense
	Vulnerability *ArtifactVulnerabilitySummary
	Version       string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	UpdatedBy     string
}

// ArtifactIntelligenceSummary is the bounded, list-friendly projection of
// security metadata. Full evidence remains available from the detail endpoint.
type ArtifactIntelligenceSummary struct {
	SignatureCount      int    `json:"signatureCount"`
	SBOMCount           int    `json:"sbomCount"`
	LicenseCount        int    `json:"licenseCount"`
	VulnerabilityStatus string `json:"vulnerabilityStatus,omitempty"`
	Critical            int    `json:"critical,omitempty"`
	High                int    `json:"high,omitempty"`
	Medium              int    `json:"medium,omitempty"`
	Low                 int    `json:"low,omitempty"`
	Unknown             int    `json:"unknown,omitempty"`
}

type ArtifactSignature struct {
	KeyID      string    `json:"keyId"`
	Algorithm  string    `json:"algorithm"`
	Identity   string    `json:"identity"`
	Signature  string    `json:"signature"`
	Verified   bool      `json:"verified"`
	VerifiedAt time.Time `json:"verifiedAt,omitempty"`
}

type ArtifactSBOM struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	URL       string `json:"url,omitempty"`
	Size      int64  `json:"size,omitempty"`
}

type ArtifactProvenance struct {
	Builder          string    `json:"builder"`
	BuildType        string    `json:"buildType"`
	SourceRepository string    `json:"sourceRepository"`
	SourceCommit     string    `json:"sourceCommit"`
	BuildID          string    `json:"buildId"`
	StartedAt        time.Time `json:"startedAt,omitempty"`
	FinishedAt       time.Time `json:"finishedAt,omitempty"`
}

type ArtifactLicense struct {
	SPDXID string `json:"spdxId"`
	Name   string `json:"name"`
	Source string `json:"source,omitempty"`
}

type ArtifactVulnerabilitySummary struct {
	Scanner   string                         `json:"scanner"`
	ScannedAt time.Time                      `json:"scannedAt,omitempty"`
	Status    string                         `json:"status"`
	Critical  int                            `json:"critical"`
	High      int                            `json:"high"`
	Medium    int                            `json:"medium"`
	Low       int                            `json:"low"`
	Unknown   int                            `json:"unknown"`
	Findings  []ArtifactVulnerabilityFinding `json:"findings,omitempty"`
}

// ArtifactVulnerabilityFinding identifies one affected component reported by
// the scanner. Summary counts remain available for bounded list projections;
// findings carry the evidence required to investigate and remediate a result.
type ArtifactVulnerabilityFinding struct {
	ID           string                        `json:"id"`
	Source       string                        `json:"source,omitempty"`
	Severity     ArtifactVulnerabilitySeverity `json:"severity"`
	Component    string                        `json:"component"`
	Version      string                        `json:"version,omitempty"`
	FixedVersion string                        `json:"fixedVersion,omitempty"`
	Location     string                        `json:"location,omitempty"`
	Title        string                        `json:"title,omitempty"`
	Description  string                        `json:"description,omitempty"`
	URL          string                        `json:"url,omitempty"`
	CVSSScore    *float64                      `json:"cvssScore,omitempty"`
	CVSSVector   string                        `json:"cvssVector,omitempty"`
}

type LifecycleJobKind string

const (
	LifecycleJobRetention    LifecycleJobKind = "retention"
	LifecycleJobPromotion    LifecycleJobKind = "promotion"
	LifecycleJobReplication  LifecycleJobKind = "replication"
	LifecycleJobReclaim      LifecycleJobKind = "reclaim"
	LifecycleJobIntelligence LifecycleJobKind = "intelligence"
	LifecycleJobScan         LifecycleJobKind = "scan"
)

// ArtifactScanPayload identifies one immutable artifact for a scanner worker.
// Asset bytes are resolved from repository metadata at execution time so a
// queued request cannot smuggle arbitrary object-store keys into a scanner.
type ArtifactScanPayload struct {
	Format     Format `json:"format"`
	Coordinate string `json:"coordinate"`
	Digest     string `json:"digest"`
}

// ArtifactScanCandidate is one visible immutable artifact identity that can be
// reconciled against the durable lifecycle queue.
type ArtifactScanCandidate struct {
	Coordinate  string
	Digest      string
	PublishedAt time.Time
}

// ArtifactIdentityPurpose selects the operation whose canonical coordinate
// rules should be applied. Conan package revisions are independently
// scannable, but distribution always operates on their parent recipe revision.
type ArtifactIdentityPurpose string

const (
	ArtifactIdentityScan         ArtifactIdentityPurpose = "scan"
	ArtifactIdentityDistribution ArtifactIdentityPurpose = "distribution"
)

// ArtifactIdentity is one visible protocol-owned coordinate and digest pair
// that can be submitted directly to the requested management operation.
type ArtifactIdentity struct {
	Coordinate   string
	Digest       string
	Size         *int64
	PublishedAt  time.Time
	Intelligence *ArtifactIntelligenceSummary
}

// ScheduledTaskKind is intentionally a closed set. Scheduled tasks may only
// dispatch operations implemented by the gateway; they never execute user
// supplied commands or SQL.
type ScheduledTaskKind string

const (
	ScheduledTaskRepositoryRetention ScheduledTaskKind = "repository-retention"
	ScheduledTaskAuditRetention      ScheduledTaskKind = "audit-retention"
)

type ScheduledTaskState string

const (
	ScheduledTaskSubmitted ScheduledTaskState = "submitted"
	ScheduledTaskFailed    ScheduledTaskState = "failed"
)

type ScheduledTask struct {
	ID              string
	Name            string
	Description     string
	Kind            ScheduledTaskKind
	RepositoryID    string
	IntervalSeconds int
	Enabled         bool
	NextRunAt       time.Time
	LastRunAt       time.Time
	LastRunID       string
	LastRunState    ScheduledTaskState
	LastError       string
	Version         string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type ScheduledTaskRun struct {
	ID          string
	TaskID      string
	Trigger     string
	State       ScheduledTaskState
	ScheduledAt time.Time
	CreatedAt   time.Time
	CompletedAt time.Time
	TargetKind  string
	TargetID    string
	LastError   string
}

type ScheduledTaskClaim struct {
	Task ScheduledTask
	Run  ScheduledTaskRun
}

type LifecycleJobState string

const (
	LifecycleJobPending   LifecycleJobState = "pending"
	LifecycleJobRunning   LifecycleJobState = "running"
	LifecycleJobRetrying  LifecycleJobState = "retrying"
	LifecycleJobCompleted LifecycleJobState = "completed"
	LifecycleJobFailed    LifecycleJobState = "failed"
	LifecycleJobCancelled LifecycleJobState = "cancelled"
)

const DefaultLifecycleJobMaxAttempts = 3

type LifecycleJob struct {
	ID              string
	RepositoryID    string
	Kind            LifecycleJobKind
	IdempotencyKey  string
	Payload         []byte
	State           LifecycleJobState
	CreatedAt       time.Time
	StartedAt       time.Time
	CompletedAt     time.Time
	NextAttemptAt   time.Time
	LeaseExpiresAt  time.Time
	LeaseToken      string
	Attempts        int
	MaxAttempts     int
	ProgressCurrent int
	ProgressTotal   int
	ProgressMessage string
	LastError       string
}

type RepositoryLifecycleJob struct {
	RepositoryName string
	Job            LifecycleJob
}

type ReplicationPlan struct {
	APTTargetSuite                             string
	ID, SourceRepositoryID, TargetRepositoryID string
	Format                                     Format
	Coordinate, Digest, IdempotencyKey, State  string
	LastError                                  string
	CreatedAt, StartedAt, CompletedAt          time.Time
	NextAttemptAt, LeaseExpiresAt              time.Time
	LeaseToken                                 string
	Attempts, MaxAttempts                      int
}

type ReplicationCheckpoint struct {
	PlanID, SourceObjectKey, ObjectKey, Digest, State, LastError string
	Size, ByteOffset                                             int64
	Attempts                                                     int
	VerifiedAt, UpdatedAt                                        time.Time
}

type MavenDeclaredObject struct {
	Name, Digest string
	Size         int64
}

// User is a local account that authenticates with a username and password and
// carries a coarse role (reader/writer/admin). SecretHash is a bcrypt hash and
// is never returned by management responses.
type User struct {
	ID                  string
	Name                string
	DisplayName         string
	Email               string
	Description         string
	SecretHash          string
	Role                string
	State               string
	LastLoginAt         *time.Time
	PasswordChangedAt   *time.Time
	FailedLoginAttempts int
	LockedUntil         *time.Time
	MustChangePassword  bool
	SessionVersion      int64
	// EmailVerifiedByAdmin records that an administrator verified Email. A SAML assertion never carries
	// a provider-verified email, so this is the only basis on which a SAML login may link the account by
	// email (ADR 0018 Decision 3). Changing the email clears it. EmailVerifiedAt and EmailVerifiedBy are
	// the audit trail: when, and the actor that verified it. They are set and cleared together with the flag.
	EmailVerifiedByAdmin bool
	EmailVerifiedAt      *time.Time
	EmailVerifiedBy      string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	Version              string
}

// UserSession is server-side metadata for a signed browser or local login
// token. ID is embedded in the signed token; token material is never stored.
type UserSession struct {
	ID        string
	UserID    string
	Kind      string
	IPAddress string
	UserAgent string
	CreatedAt time.Time
	ExpiresAt time.Time
	RevokedAt *time.Time
}

const (
	UserSessionLocal = "local_session"
	UserSessionOIDC  = "oidc"
	UserSessionSAML  = "saml"
)

// UserIdentity is an external credential linked to a local account. Issuer
// and Subject together form the provider-owned immutable identity key.
type UserIdentity struct {
	ID     string
	UserID string
	Kind   string
	// ProviderID is the identity provider that owns the identity (ADR 0018). The
	// login key is (ProviderID, Subject); Issuer is informational.
	ProviderID     string
	Issuer         string
	Subject        string
	SCIMExternalID string
	Email          string
	DisplayName    string
	EmailVerified  bool
	LastLoginAt    *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

const (
	UserIdentityOIDC = "oidc"
	UserIdentitySAML = "saml"
)

// OIDCIdentityProvision describes verified claims and the explicit
// provisioning policy observed during an OIDC sign-in. Role is the level an
// external role mapping selected, DefaultRole the configured JIT default; both
// are limited to the assignable levels, and an unrecognized value falls back to
// the member level, which carries no repository capability.
type OIDCIdentityProvision struct {
	// Kind is the protocol of the sign-in, oidc or saml; empty means oidc. The subject is the OIDC
	// subject or the SAML NameID (lower-cased for emailAddress), the issuer the IdP entity id.
	Kind              string
	ProviderID        string
	Issuer            string
	Subject           string
	Email             string
	DisplayName       string
	PreferredUsername string
	EmailVerified     bool
	Role              string
	Provision         bool
	MatchEmail        bool
	// LinkAdminVerifiedEmail enables the SAML email-link path (ADR 0018 Decision 3): an unknown subject links
	// the one existing account whose administrator-verified email matches Email, when that account is not an
	// administrator, has no local password and has no identity at this provider. It is set from the
	// provider's email_linking_enabled and never from an asserted email alone.
	LinkAdminVerifiedEmail bool
	DefaultRole            string
	OccurredAt             time.Time
	// EmailLink is an optional out parameter: the store fills it with the decision of the email-link
	// path (linked, or refused and why) so the caller can audit it. It stays nil when the path is off.
	EmailLink *EmailLinkDecision
}

// EmailLinkDecision is the outcome of the administrator-verified email-link path of one sign-in.
type EmailLinkDecision struct {
	Linked bool
	// Reason is a machine-readable code: linked, no_email, no_match, email_not_verified, ambiguous,
	// admin_account, account_disabled, local_password or already_linked.
	Reason string
}

// Reasons of an EmailLinkDecision.
const (
	EmailLinkLinked        = "linked"
	EmailLinkNoEmail       = "no_email"
	EmailLinkNoMatch       = "no_match"
	EmailLinkNotVerified   = "email_not_verified"
	EmailLinkAmbiguous     = "ambiguous"
	EmailLinkAdminAccount  = "admin_account"
	EmailLinkDisabled      = "account_disabled"
	EmailLinkLocalPassword = "local_password"
	EmailLinkAlreadyLinked = "already_linked"
)

// decideEmailLink applies the eligibility rules to the accounts whose email matches the asserted one
// (case-insensitively). hasIdentityAtProvider reports whether a candidate already has an identity at the
// provider. It returns the single eligible account or a refusal; two verified matches are ambiguous.
func decideEmailLink(email string, matches []User, hasIdentityAtProvider func(User) bool) (User, EmailLinkDecision) {
	if strings.TrimSpace(email) == "" {
		return User{}, EmailLinkDecision{Reason: EmailLinkNoEmail}
	}
	if len(matches) == 0 {
		return User{}, EmailLinkDecision{Reason: EmailLinkNoMatch}
	}
	var verified []User
	for _, candidate := range matches {
		if candidate.EmailVerifiedByAdmin {
			verified = append(verified, candidate)
		}
	}
	switch {
	case len(verified) == 0:
		return User{}, EmailLinkDecision{Reason: EmailLinkNotVerified}
	case len(verified) > 1:
		return User{}, EmailLinkDecision{Reason: EmailLinkAmbiguous}
	}
	candidate := verified[0]
	switch {
	case candidate.Role == userRoleAdmin:
		return User{}, EmailLinkDecision{Reason: EmailLinkAdminAccount}
	case candidate.State != UserActive:
		// A disabled account is refused before any write: binding a foreign subject to it would
		// survive a later re-enable.
		return User{}, EmailLinkDecision{Reason: EmailLinkDisabled}
	case candidate.SecretHash != "":
		return User{}, EmailLinkDecision{Reason: EmailLinkLocalPassword}
	case hasIdentityAtProvider(candidate):
		return User{}, EmailLinkDecision{Reason: EmailLinkAlreadyLinked}
	}
	return candidate, EmailLinkDecision{Linked: true, Reason: EmailLinkLinked}
}

const (
	UserActive   = "active"
	UserDisabled = "disabled"
)

type MavenPublishSession struct {
	ID, RepositoryID, Coordinate, Publisher, PomObject, State string
	Objects                                                   []MavenDeclaredObject
	ExpiresAt                                                 time.Time
	ClientTimestamp                                           string `json:"-"`
	ClientBuildNumber                                         int    `json:"-"`
}
type MavenAsset struct {
	RepositoryID, Path, ObjectKey, Digest string
	Size                                  int64
}
type MavenArtifact struct {
	ID           string `json:"id"`
	RepositoryID string `json:"repositoryId"`
	Coordinate   string `json:"coordinate"`
	Digest       string `json:"digest"`
	State        string `json:"state"`
	Publisher    string `json:"publisher,omitempty"`
	// BuildNumber is 0 for immutable releases and the 1-based publish sequence
	// for SNAPSHOT coordinates, which keep one row per timestamped build.
	BuildNumber       int       `json:"buildNumber,omitempty"`
	SourceTimestamp   string    `json:"sourceTimestamp,omitempty"`
	SourceBuildNumber int       `json:"sourceBuildNumber,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
}

// IsMavenSnapshotCoordinate reports whether the coordinate's version segment
// ends in -SNAPSHOT, making the coordinate eligible for multi-build publishes.
func IsMavenSnapshotCoordinate(coordinate string) bool {
	parts := strings.Split(coordinate, ":")
	return len(parts) >= 3 && strings.HasSuffix(parts[2], "-SNAPSHOT")
}

// MavenArtifactCursor preserves a SNAPSHOT build's position without encoding
// domain fields into an opaque string inside the storage boundary.
type MavenArtifactCursor struct {
	Coordinate  string
	BuildNumber int
}

// MavenArtifactAfterCoordinate resumes after every build of one coordinate.
func MavenArtifactAfterCoordinate(coordinate string) MavenArtifactCursor {
	return MavenArtifactCursor{Coordinate: coordinate, BuildNumber: int(^uint(0) >> 1)}
}

// MavenReplication is the immutable source snapshot and target-owned assets
// that are published after every replication checkpoint is verified.
type MavenReplication struct {
	ID, SourceRepositoryID, TargetRepositoryID, Coordinate, Digest string
	Assets                                                         []MavenReplicationAsset
}
type MavenReplicationAsset struct {
	Path, SourceObjectKey, ObjectKey, Digest string
	Size                                     int64
}

// OCIReplicationPublication atomically rechecks the source manifest and makes
// its copied manifest and referenced blobs visible in the target Repository.
type OCIReplicationPublication struct {
	SourceRepositoryID, TargetRepositoryID, SourceObjectKey string
	Manifest                                                OCIManifest
	BlobDigests                                             []string
}

// MavenPromotion snapshots a visible coordinate for immutable promotion into a
// second Maven Hosted repository.
type MavenPromotion struct {
	ID, SourceRepositoryID, TargetRepositoryID, Coordinate, Digest string
}
type MavenObjectIntent struct{ RepositoryID, ObjectKey, ClaimToken string }

type ConanObjectIntent struct {
	RepositoryID, ObjectKey, Digest                 string
	Size                                            int64
	CreatedAt, ClaimedAt, CollectingAt, CollectedAt time.Time
}

type ConanAsset struct {
	RepositoryID, Reference, RecipeRevision, PackageID, PackageRevision string
	Path, ObjectKey, Digest                                             string
	Size                                                                int64
}

type ConanRecipeRevision struct {
	RepositoryID, Reference, Revision, Digest, State string
	CreatedAt                                        time.Time
}

// ConanReference is a visible recipe reference together with the publisher of
// its most recent committed publish session. Publisher is empty when no
// publish session was recorded (replicated, promoted, or pre-session data).
type ConanReference struct {
	Reference, Publisher string
}

type ConanPackageRevision struct {
	RepositoryID, Reference, RecipeRevision, PackageID, Revision, Digest, State string
	CreatedAt                                                                   time.Time
}

// ConanReplicationPublication is the complete target-owned recipe subtree.
// Publishing it is all-or-nothing so retries never expose a partial revision.
type ConanReplicationPublication struct {
	SourceRepositoryID string
	Recipe             ConanRecipeRevision
	Packages           []ConanPackageRevision
	SourceAssets       []ConanAsset
	TargetAssets       []ConanAsset
}

// ConanPromotion snapshots a visible recipe revision together with every
// visible package revision beneath it into another Hosted repository.
type ConanPromotion struct {
	SourceRepositoryID, TargetRepositoryID, Reference, Revision, Digest string
}

// ConanPublishSession keeps uploads unaddressable until a complete recipe or
// package revision is atomically promoted to visible metadata.
type ConanPublishSession struct {
	ID, RepositoryID, Publisher, Kind string
	Reference, RecipeRevision         string
	PackageID, PackageRevision        string
	State                             string
	Objects                           []MavenDeclaredObject
	ExpiresAt                         time.Time
	Native                            bool
	Version                           int64
}

type MemberType string

const (
	MemberHosted MemberType = "hosted"
	MemberProxy  MemberType = "proxy"
)

// OCIBearer opts an OCI Proxy into bound upstream Bearer challenge exchange.
// Realm and Service bind challenges to this repository's configured issuer and
// audience. UpstreamAuth, when configured, authenticates only to that issuer.
// This binding grants no egress permission; every request still requires the
// repository's egress policy.
type OCIBearer struct {
	Realm   string `json:"realm"`
	Service string `json:"service"`
}

type Member struct {
	Name         string       `json:"name"`
	Type         MemberType   `json:"type"`
	Endpoint     string       `json:"endpoint"`
	Position     int          `json:"position"`
	AllowedHosts []string     `json:"allowedHosts,omitempty"`
	EgressProxy  *EgressProxy `json:"egressProxy,omitempty"`
	// OCIBearer is projected from V2 repository configuration at runtime. It is
	// not a configuration field on the legacy Group management surface.
	OCIBearer *OCIBearer `json:"-"`
	// UpstreamAuth and RepositoryVersion are runtime projections of the current
	// repository configuration. They never enter a Group response or export.
	UpstreamAuth      *UpstreamAuth `json:"-"`
	RepositoryVersion string        `json:"-"`
	RepositoryID      string        `json:"repositoryId,omitempty"`
	// Visibility is the visibility of the member repository, projected per
	// request by access.BindMember so a group decision can filter members. It is
	// derived, never stored (ADR 0029).
	Visibility Visibility `json:"-"`
}

type Group struct {
	Name            string    `json:"name"`
	Enabled         bool      `json:"enabled"`
	Anonymous       bool      `json:"anonymous"`
	CacheQuotaBytes int64     `json:"cacheQuotaBytes,omitempty"`
	Members         []Member  `json:"members"`
	CreatedAt       time.Time `json:"createdAt"`
}

type AuditOutcome string

const (
	AuditResolved          AuditOutcome = "resolved"
	AuditInternalPreferred AuditOutcome = "internal_preferred"
	AuditNotFound          AuditOutcome = "not_found"
	AuditGroupDisabled     AuditOutcome = "group_disabled"
	AuditStorageError      AuditOutcome = "storage_error"
	AuditUpstreamError     AuditOutcome = "upstream_error"
	AuditAccessDenied      AuditOutcome = "access_denied"
	AuditProxyDenied       AuditOutcome = "proxy_denied"
)

// AuditClass is the durability class of an audit record (ADR 0023 section 1). It
// also names the table that stores the record: sync-class records live in
// security_audit_log and async-class records in resolver_audit_log.
type AuditClass string

const (
	// AuditClassSync marks evidence of a security decision or state change.
	AuditClassSync AuditClass = "sync"
	// AuditClassAsync marks high-volume reads and downloads. It is the default.
	AuditClassAsync AuditClass = "async"
)

// Valid reports whether c is one of the two classes.
func (c AuditClass) Valid() bool { return c == AuditClassSync || c == AuditClassAsync }

// effective maps the zero value to the default class.
func (c AuditClass) effective() AuditClass {
	if c == "" {
		return AuditClassAsync
	}
	return c
}

// rank orders the classes inside a merged page when two records share both
// occurred_at and id, which two independent tables can produce.
func (c AuditClass) rank() int {
	if c.effective() == AuditClassSync {
		return 1
	}
	return 0
}

type AuditRecord struct {
	// ID is an internal stable tie-breaker for cursor pagination. It is never
	// serialized as part of the public audit representation.
	ID int64 `json:"-"`
	// Class selects the audit table on write; an empty class means async. Reads
	// report the class of the table the record came from.
	Class                                                                                   AuditClass `json:",omitempty"`
	GroupName                                                                               string
	Repository                                                                              string
	MemberName                                                                              string
	Outcome                                                                                 AuditOutcome
	Actor                                                                                   string
	OccurredAt                                                                              time.Time
	Format, Resource, Representation, MemberType, UpstreamHost, Operation, CacheDisposition string
	AuthorizationSource, AuthorizationReason                                                string
	RequestID, TraceID                                                                      string
	Evidence                                                                                map[string]string `json:"-"`
	Status                                                                                  int
	Bytes                                                                                   int64
}

type AuditQuery struct {
	GroupName  string
	Repository string
	Outcome    string
	Format     string
	Operation  string
	Actor      string
	// Class restricts the page to one table; empty merges both.
	Class  AuditClass
	Limit  int
	From   time.Time
	To     time.Time
	Before AuditCursor
}

// AuditCursor identifies the last item in a descending audit page.
type AuditCursor struct {
	OccurredAt time.Time
	ID         int64
	// Class completes the keyset: ids come from one sequence per table.
	Class AuditClass
}

// AuditPage is the storage-level result for cursor-paginated audit queries.
// The API layer signs Next before returning it to callers.
type AuditPage struct {
	Items []AuditRecord
	Next  *AuditCursor
}

// AuditRetentionPolicy governs global resolver audit cleanup. It is disabled by
// default so enabling deletion is always an explicit administrative action.
type AuditRetentionPolicy struct {
	Version  string `json:"version"`
	Enabled  bool   `json:"enabled"`
	KeepDays int    `json:"keepDays"`
}

// SiteSettings is the versioned, deployment-wide Console identity. LogoURL
// may be an HTTPS URL or a bounded raster-image data URL; BrandMark is the
// short textual fallback shown when no logo is configured or it cannot load.
type SiteSettings struct {
	Version         string    `json:"version"`
	SiteName        string    `json:"siteName"`
	LogoURL         string    `json:"logoUrl,omitempty"`
	BrandMark       string    `json:"brandMark"`
	EnabledThemeIDs []string  `json:"enabledThemeIds"`
	DefaultThemeID  string    `json:"defaultThemeId"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// ConsoleThemePackage is the durable representation of one validated Theme
// Package. Payload contains canonical JSON produced after strict parsing; the
// API never serves an unvalidated stored payload to the Console.
type ConsoleThemePackage struct {
	ID        string    `json:"id"`
	Version   string    `json:"version"`
	Payload   []byte    `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type AuditCleanupJob struct {
	ID             string
	IdempotencyKey string
	PolicyVersion  string
	CutoffAt       time.Time
	BatchSize      int
	Deleted        int
	State          LifecycleJobState
	CreatedAt      time.Time
	StartedAt      time.Time
	CompletedAt    time.Time
	LastError      string
}

// WebhookEventType is a stable consumer-facing operational event name.
type WebhookEventType string

const (
	WebhookEventArtifactQuarantined       WebhookEventType = "artifact.quarantined"
	WebhookEventArtifactReleased          WebhookEventType = "artifact.released"
	WebhookEventCargoPromotionCompleted   WebhookEventType = "cargo.promotion.completed"
	WebhookEventCargoPromotionFailed      WebhookEventType = "cargo.promotion.failed"
	WebhookEventCargoReplicationCompleted WebhookEventType = "cargo.replication.completed"
	WebhookEventCargoReplicationFailed    WebhookEventType = "cargo.replication.failed"
)

type WebhookSubscription struct {
	ID               string
	Name             string
	EndpointURL      string
	SecretCiphertext string `json:"-"`
	EventTypes       []WebhookEventType
	Enabled          bool
	Version          string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type WebhookEvent struct {
	ID         string
	Type       WebhookEventType
	OccurredAt time.Time
	Data       []byte
}

type WebhookDeliveryState string

const (
	WebhookDeliveryPending    WebhookDeliveryState = "pending"
	WebhookDeliveryDelivering WebhookDeliveryState = "delivering"
	WebhookDeliveryRetrying   WebhookDeliveryState = "retrying"
	WebhookDeliverySucceeded  WebhookDeliveryState = "succeeded"
	WebhookDeliveryDead       WebhookDeliveryState = "dead"
)

type WebhookDelivery struct {
	ID             string
	EventID        string
	EventType      WebhookEventType
	SubscriptionID string
	// SubscriptionName is filled by the delivery listing only.
	SubscriptionName string
	State            WebhookDeliveryState
	Attempts         int
	NextAttemptAt    time.Time
	LeaseOwner       string
	LeaseToken       string
	LeaseExpiresAt   time.Time
	LastStatus       int
	LastError        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeliveredAt      time.Time
}

type WebhookDeliveryClaim struct {
	Delivery     WebhookDelivery
	Event        WebhookEvent
	Subscription WebhookSubscription
}

// WebhookDeliveryQuery pages deliveries newest first; PageToken is the NextPageToken of the previous page and
// is bound to the subscription and state filters.
type WebhookDeliveryQuery struct {
	SubscriptionID string
	State          WebhookDeliveryState
	Limit          int
	PageToken      string
}

// WebhookDeliveryPage is one page of deliveries.
type WebhookDeliveryPage struct {
	Items         []WebhookDelivery
	NextPageToken string
}

// WebhookSubscriptionQuery pages subscriptions by name.
type WebhookSubscriptionQuery struct {
	Limit     int
	PageToken string
}

// WebhookSubscriptionPage is one page of subscriptions.
type WebhookSubscriptionPage struct {
	Items         []WebhookSubscription
	NextPageToken string
}

type ArtifactQuarantineWebhookData struct {
	RepositoryID string                  `json:"repositoryId"`
	Format       Format                  `json:"format"`
	Coordinate   string                  `json:"coordinate"`
	Digest       string                  `json:"digest"`
	State        ArtifactQuarantineState `json:"state"`
	Reason       string                  `json:"reason"`
	Actor        string                  `json:"actor"`
	Version      string                  `json:"version"`
}

// CargoDistributionWebhookData describes a durable operation state change.
// Failure details remain in the authenticated management API rather than the
// outbound payload, where they could contain dependency or storage context.
type CargoDistributionWebhookData struct {
	OperationID        string `json:"operationId"`
	Kind               string `json:"kind"`
	Format             Format `json:"format"`
	SourceRepositoryID string `json:"sourceRepositoryId"`
	TargetRepositoryID string `json:"targetRepositoryId"`
	Coordinate         string `json:"coordinate"`
	Digest             string `json:"digest"`
	State              string `json:"state"`
	Attempts           int    `json:"attempts"`
}
