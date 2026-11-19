package config

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/aptpublication"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/emailnotification"
	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/localcapacity"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/retention"
)

// NodeRole controls which parts of the Gateway runtime are enabled. The
// standalone role is the default and expands to every role for local use.
type NodeRole string

const (
	NodeRoleStandalone NodeRole = "standalone"
	NodeRoleAPI        NodeRole = "api"
	NodeRoleScheduler  NodeRole = "scheduler"
	NodeRoleWorker     NodeRole = "worker"
)

var supportedNodeRoles = map[NodeRole]struct{}{
	NodeRoleStandalone: {},
	NodeRoleAPI:        {},
	NodeRoleScheduler:  {},
	NodeRoleWorker:     {},
}

var supportedWorkerFormats = func() map[string]struct{} {
	formats := make(map[string]struct{})
	for _, format := range repository.WorkerFormats() {
		formats[string(format)] = struct{}{}
	}
	return formats
}()

var supportedScannerFormats = func() map[string]struct{} {
	formats := make(map[string]struct{})
	for _, format := range repository.SupportedFormats() {
		formats[string(format)] = struct{}{}
	}
	formats[string(repository.FormatCargo)] = struct{}{}
	return formats
}()

var supportedWorkerKinds = map[string]struct{}{
	"promotion": {}, "replication": {}, "retention": {}, "reclaim": {},
	"intelligence": {}, "scan": {}, "deletion": {}, "cache": {}, "audit": {},
	"webhook": {}, "email": {}, "migration": {},
}

type Config struct {
	RepositoryPresetsMode string
	// GroupVisibilityMode is the validated NOTARX_GROUP_VISIBILITY_MODE: strict or lenient.
	GroupVisibilityMode             string
	HelmPreviewEnabled              bool
	NuGetPreviewEnabled             bool
	RubyGemsPreviewEnabled          bool
	MigrationSourcePrivateEndpoints map[string][]netip.Prefix
	MigrationStoragePoolID          string
	Email                           emailnotification.Config
	EmailDefaultLocale              string
	ListenAddress                   string
	ConsoleThemeDir                 string
	NodeRoles                       []NodeRole
	InstanceID                      string
	AccessLogMode                   string
	AccessLogSlowMS                 int
	LogBufferLines                  int
	LogFileDirectory                string
	LogFileMaxBytes                 int64
	LogFileMaxBackups               int
	LogFileMaxAge                   time.Duration
	LocalCapacityMounts             map[localcapacity.Alias]string
	WorkerFormats                   []string
	WorkerKinds                     []string
	ScannerEndpoint                 string
	ScannerHealthEndpoint           string
	ScannerName                     string
	ScannerToken                    string
	ScannerTimeout                  time.Duration
	ScannerHealthTimeout            time.Duration
	ScannerDatabaseMaxAge           time.Duration
	ScannerMaxResponseBytes         int64
	ScannerMaxArtifactBytes         int64
	ScannerFormats                  []string
	APTRestoreTrustedFingerprints   []string
	APTRestoreTrustedPublicKeysFile string
	APTRestoreTrustedPublicKeys     []byte
	APTSignerEndpoint               string
	APTSignerToken                  string
	APTSignerTimeout                time.Duration
	APTSignerTrustedFingerprints    []string
	APTSignerTrustedPublicKeysFile  string
	APTSignerTrustedPublicKeys      []byte
	APTSignerTLSCAFile              string
	APTSignerTLSRootCertificates    []byte
	RuntimeNodeRetention            time.Duration
	RuntimeNodePruneInterval        time.Duration
	// Workload is the JWT verification policy of workload identity federation
	// (NOTARX_WORKLOAD_*, ADR 0019).
	Workload WorkloadConfig
	// SAML is the SAML service provider policy (NOTARX_SAML_*, ADR 0018 section 3).
	SAML SAMLConfig
	// QuotaReconcileInterval is how often the quota reconciler recomputes every
	// repository's usage counter (NOTARX_QUOTA_RECONCILE_INTERVAL, ADR 0023 section 3).
	QuotaReconcileInterval time.Duration
	// PartitionAhead is the number of future partition periods the partition
	// manager creates beyond the current one (ADR 0023 section 2).
	PartitionAhead int
	// Retention configures the retention janitor (ADR 0023 section 2): the profile,
	// the per-table overrides and the run budget.
	Retention retention.Config
	// Audit writer (ADR 0023 section 1). Modes are "queue"|"sync" (denials) and
	// "async"|"sync" (reads); intervals are Go durations or whole seconds.
	AuditBufferSize      int
	AuditBatchSize       int
	AuditFlushInterval   time.Duration
	AuditSyncMaxInflight int
	AuditDenialMode      string
	AuditReadMode        string
	// InlineMaxBytes is the largest payload kept inline in a database row; a larger
	// one goes to the object store meta namespace (ADR 0023 section 9).
	InlineMaxBytes       int
	UsageFlushInterval   time.Duration
	ShutdownFlushTimeout time.Duration
	// AuthzCacheTTL is the revocation window of the read caches (ADR 0023
	// section 6); zero disables every cache.
	AuthzCacheTTL               time.Duration
	APIKeyLastUsedInterval      time.Duration
	OverviewCacheTTL            time.Duration
	DatabaseURL                 string
	DatabaseDriver              string
	DatabaseTLSCAFile           string
	DatabaseMigrateURL          string
	DatabasePollInterval        time.Duration
	DatabaseExperimentalMySQL   bool
	DatabasePool                database.PoolConfig
	DatabaseNotificationPool    database.PoolConfig
	DatabaseCoordinatorPool     database.PoolConfig
	DatabaseArtifactLockPool    database.PoolConfig
	ObjectStore                 objectstore.Config
	OCIProxyAllowedHosts        []string
	MavenProxyAllowedHosts      []string
	RawProxyAllowedHosts        []string
	RawCacheMaxObjectBytes      int64
	RawCacheMaxConcurrentSpools int
	ConanCacheMaxObjectBytes    int64
	OCICacheTTL                 time.Duration
	MavenCacheTTL               time.Duration
	MavenMetadataCacheTTL       time.Duration
	MavenNegativeCacheTTL       time.Duration
	NPMMetadataCacheTTL         time.Duration
	NPMNegativeCacheTTL         time.Duration
	NPMProxyBreakerTTL          time.Duration
	RawCacheTTL                 time.Duration
	ConanCacheTTL               time.Duration
	AdminToken                  string
	ResolverToken               string
	ConanTokenSecret            string
	AdminActor                  string
	ResolverActor               string
	LocalAuthMaxFailedAttempts  int
	LocalAuthLockoutDuration    time.Duration
	RepositoryReaders           map[string][]string
	LegacyReadPermissive        bool
	RepositoryCacheQuotas       map[string]int64
	OIDCIssuer                  string
	OIDCAudience                string
	OIDCJWKSURL                 string
	OIDCClientID                string
	OIDCClientSecret            string
	OIDCRedirectURL             string
	OIDCScopes                  []string
	OIDCAdminSubjects           []string
	OIDCRoles                   authorization.OIDCRoleMapping
	OTLPHTTPEndpoint            string
	OTLPLogsEndpoint            string
	OTLPLogsHeaders             map[string]string
	OTLPLogsQueueSize           int
	OTLPLogsTimeout             time.Duration
	OTLPLogsShutdownTimeout     time.Duration
	OTELSamplingRatio           float64

	// Issuer configures notarx as an OIDC token issuer (ADR 0020).
	Issuer IssuerConfig

	// publicURL is the validated NOTARX_PUBLIC_URL; read it through PublicURL.
	publicURL *url.URL
}

func Load() (Config, error) {
	if err := RefuseLegacyEnv(os.Environ()); err != nil {
		return Config{}, err
	}
	presetsMode, presetsErr := loadRepositoryPresetsMode()
	if presetsErr != nil {
		return Config{}, presetsErr
	}
	groupVisibilityMode, groupVisibilityErr := loadGroupVisibilityMode()
	if groupVisibilityErr != nil {
		return Config{}, groupVisibilityErr
	}
	cfg := Config{
		RepositoryPresetsMode:           presetsMode,
		GroupVisibilityMode:             groupVisibilityMode,
		MigrationStoragePoolID:          strings.TrimSpace(os.Getenv("NOTARX_MIGRATION_STORAGE_POOL_ID")),
		ListenAddress:                   value("NOTARX_LISTEN_ADDRESS", ":8080"),
		ConsoleThemeDir:                 strings.TrimSpace(os.Getenv("NOTARX_CONSOLE_THEME_DIR")),
		NodeRoles:                       parseNodeRoles(os.Getenv("NOTARX_NODE_ROLES")),
		InstanceID:                      value("NOTARX_INSTANCE_ID", "gateway-"+hostname()),
		AccessLogMode:                   value("NOTARX_ACCESS_LOG", "limited"),
		AccessLogSlowMS:                 1000,
		LogBufferLines:                  1000,
		WorkerFormats:                   parseFilter(os.Getenv("NOTARX_WORKER_FORMATS"), supportedWorkerFormats),
		WorkerKinds:                     parseFilter(os.Getenv("NOTARX_WORKER_KINDS"), supportedWorkerKinds),
		ScannerEndpoint:                 strings.TrimSpace(os.Getenv("NOTARX_SCANNER_ENDPOINT")),
		ScannerHealthEndpoint:           strings.TrimSpace(os.Getenv("NOTARX_SCANNER_HEALTH_ENDPOINT")),
		ScannerName:                     strings.TrimSpace(os.Getenv("NOTARX_SCANNER_NAME")),
		ScannerToken:                    os.Getenv("NOTARX_SCANNER_TOKEN"),
		ScannerTimeout:                  2 * time.Minute,
		ScannerHealthTimeout:            2 * time.Second,
		ScannerDatabaseMaxAge:           24 * time.Hour,
		ScannerMaxResponseBytes:         512 << 10,
		ScannerMaxArtifactBytes:         20 << 30,
		APTRestoreTrustedFingerprints:   splitCSV(os.Getenv("NOTARX_APT_RESTORE_TRUSTED_FINGERPRINTS")),
		APTRestoreTrustedPublicKeysFile: strings.TrimSpace(os.Getenv("NOTARX_APT_RESTORE_TRUSTED_PUBLIC_KEYS_FILE")),
		APTSignerEndpoint:               strings.TrimSpace(os.Getenv("NOTARX_APT_SIGNER_ENDPOINT")),
		APTSignerToken:                  os.Getenv("NOTARX_APT_SIGNER_TOKEN"),
		APTSignerTimeout:                15 * time.Second,
		APTSignerTrustedFingerprints:    splitCSV(os.Getenv("NOTARX_APT_SIGNER_TRUSTED_FINGERPRINTS")),
		APTSignerTrustedPublicKeysFile:  strings.TrimSpace(os.Getenv("NOTARX_APT_SIGNER_TRUSTED_PUBLIC_KEYS_FILE")),
		APTSignerTLSCAFile:              strings.TrimSpace(os.Getenv("NOTARX_APT_SIGNER_TLS_CA_FILE")),
		RuntimeNodeRetention:            7 * 24 * time.Hour,
		RuntimeNodePruneInterval:        time.Hour,
		Workload: WorkloadConfig{
			ClockSkew: jose.DefaultClockSkew, MaxTokenAge: jose.DefaultMaxTokenAge,
			JWKSTTL: jose.DefaultJWKSTTL, JWKSStaleMax: jose.DefaultJWKSStaleMax,
		},
		SAML:                        DefaultSAMLConfig(),
		QuotaReconcileInterval:      6 * time.Hour,
		PartitionAhead:              3,
		Retention:                   retention.DefaultConfig(),
		AuditBufferSize:             65536,
		AuditBatchSize:              1000,
		AuditFlushInterval:          time.Second,
		AuditSyncMaxInflight:        64,
		AuditDenialMode:             "queue",
		AuditReadMode:               "async",
		InlineMaxBytes:              DefaultInlineMaxBytes,
		UsageFlushInterval:          5 * time.Second,
		ShutdownFlushTimeout:        10 * time.Second,
		AuthzCacheTTL:               5 * time.Second,
		APIKeyLastUsedInterval:      time.Minute,
		OverviewCacheTTL:            30 * time.Second,
		DatabaseURL:                 os.Getenv("NOTARX_DATABASE_URL"),
		DatabasePool:                database.DefaultPoolConfig(),
		DatabaseCoordinatorPool:     database.DefaultCoordinatorPoolConfig(),
		DatabaseArtifactLockPool:    database.DefaultArtifactLockPoolConfig(),
		OCIProxyAllowedHosts:        splitCSV(os.Getenv("NOTARX_OCI_PROXY_ALLOWED_HOSTS")),
		MavenProxyAllowedHosts:      splitCSV(os.Getenv("NOTARX_MAVEN_PROXY_ALLOWED_HOSTS")),
		RawProxyAllowedHosts:        splitCSV(os.Getenv("NOTARX_RAW_PROXY_ALLOWED_HOSTS")),
		RawCacheMaxObjectBytes:      1 << 30,
		RawCacheMaxConcurrentSpools: 4,
		ConanCacheMaxObjectBytes:    1 << 30,
		OCICacheTTL:                 15 * time.Minute,
		MavenCacheTTL:               24 * time.Hour,
		MavenMetadataCacheTTL:       15 * time.Minute,
		MavenNegativeCacheTTL:       10 * time.Minute,
		NPMMetadataCacheTTL:         15 * time.Minute,
		NPMNegativeCacheTTL:         10 * time.Minute,
		NPMProxyBreakerTTL:          30 * time.Second,
		RawCacheTTL:                 15 * time.Minute,
		ConanCacheTTL:               15 * time.Minute,
		AdminToken:                  os.Getenv("NOTARX_ADMIN_TOKEN"),
		ResolverToken:               os.Getenv("NOTARX_RESOLVER_TOKEN"),
		ConanTokenSecret:            os.Getenv("NOTARX_CONAN_TOKEN_SECRET"),
		AdminActor:                  value("NOTARX_ADMIN_ACTOR", "gateway-admin"),
		ResolverActor:               value("NOTARX_RESOLVER_ACTOR", "gateway-resolver"),
		LocalAuthMaxFailedAttempts:  5,
		LocalAuthLockoutDuration:    15 * time.Minute,
		RepositoryReaders:           repositoryReaders(os.Getenv("NOTARX_REPOSITORY_READERS")),
		LegacyReadPermissive:        legacyReadPermissive(os.Getenv("NOTARX_LEGACY_READ_DEFAULT")),
		RepositoryCacheQuotas:       repositoryCacheQuotas(os.Getenv("NOTARX_REPOSITORY_CACHE_QUOTAS")),
		OIDCIssuer:                  strings.TrimRight(strings.TrimSpace(os.Getenv("NOTARX_OIDC_ISSUER")), "/"),
		OIDCAudience:                strings.TrimSpace(os.Getenv("NOTARX_OIDC_AUDIENCE")),
		OIDCJWKSURL:                 strings.TrimSpace(os.Getenv("NOTARX_OIDC_JWKS_URL")),
		OIDCClientID:                strings.TrimSpace(os.Getenv("NOTARX_OIDC_CLIENT_ID")),
		OIDCClientSecret:            strings.TrimSpace(os.Getenv("NOTARX_OIDC_CLIENT_SECRET")),
		OIDCRedirectURL:             strings.TrimSpace(os.Getenv("NOTARX_OIDC_REDIRECT_URL")),
		OIDCScopes:                  oidcScopes(os.Getenv("NOTARX_OIDC_SCOPES")),
		OIDCAdminSubjects:           splitCSV(os.Getenv("NOTARX_OIDC_ADMIN_SUBJECTS")),
		OIDCRoles: authorization.OIDCRoleMapping{
			Member: splitCSV(os.Getenv("NOTARX_OIDC_MEMBER_ROLES")),
			Admin:  splitCSV(os.Getenv("NOTARX_OIDC_ADMIN_ROLES")),
		},
		OTLPHTTPEndpoint:  strings.TrimSpace(os.Getenv("NOTARX_OTLP_HTTP_ENDPOINT")),
		OTELSamplingRatio: 1,
	}
	if raw := strings.TrimSpace(os.Getenv("NOTARX_HELM_PREVIEW_ENABLED")); raw != "" {
		if raw != "true" && raw != "false" {
			return Config{}, errors.New("NOTARX_HELM_PREVIEW_ENABLED must be true or false")
		}
		cfg.HelmPreviewEnabled = raw == "true"
	}
	if cfg.HelmPreviewEnabled && strings.TrimSpace(os.Getenv("NOTARX_WORKER_FORMATS")) == "" {
		cfg.WorkerFormats = append(cfg.WorkerFormats, "helm")
	}
	if raw := strings.TrimSpace(os.Getenv("NOTARX_NUGET_PREVIEW_ENABLED")); raw != "" {
		if raw != "true" && raw != "false" {
			return Config{}, errors.New("NOTARX_NUGET_PREVIEW_ENABLED must be true or false")
		}
		cfg.NuGetPreviewEnabled = raw == "true"
	}
	if cfg.NuGetPreviewEnabled && strings.TrimSpace(os.Getenv("NOTARX_WORKER_FORMATS")) == "" {
		cfg.WorkerFormats = append(cfg.WorkerFormats, "nuget")
	}
	if raw := strings.TrimSpace(os.Getenv("NOTARX_RUBYGEMS_PREVIEW_ENABLED")); raw != "" {
		if raw != "true" && raw != "false" {
			return Config{}, errors.New("NOTARX_RUBYGEMS_PREVIEW_ENABLED must be true or false")
		}
		cfg.RubyGemsPreviewEnabled = raw == "true"
	}
	if cfg.RubyGemsPreviewEnabled && strings.TrimSpace(os.Getenv("NOTARX_WORKER_FORMATS")) == "" {
		cfg.WorkerFormats = append(cfg.WorkerFormats, "rubygems")
	}
	if err := validateRuntimeConfig(cfg); err != nil {
		return Config{}, err
	}
	if cfg.AccessLogMode != "limited" && cfg.AccessLogMode != "full" {
		return Config{}, fmt.Errorf("NOTARX_ACCESS_LOG must be limited or full")
	}
	if slowMS, err := positiveIntEnv("NOTARX_ACCESS_LOG_SLOW_MS", cfg.AccessLogSlowMS, false); err != nil {
		return Config{}, err
	} else if slowMS > 60_000 {
		return Config{}, fmt.Errorf("NOTARX_ACCESS_LOG_SLOW_MS must be at most 60000")
	} else {
		cfg.AccessLogSlowMS = slowMS
	}
	if lines, err := positiveIntEnv("NOTARX_LOG_BUFFER_LINES", cfg.LogBufferLines, true); err != nil {
		return Config{}, err
	} else if lines > 5000 {
		return Config{}, fmt.Errorf("NOTARX_LOG_BUFFER_LINES must be at most 5000")
	} else {
		cfg.LogBufferLines = lines
	}

	if err := configureLogFile(&cfg); err != nil {
		return Config{}, err
	}
	capacityMounts, err := localcapacity.ParsePaths(os.Getenv("NOTARX_LOCAL_CAPACITY_MOUNTS"))
	if err != nil {
		return Config{}, fmt.Errorf("NOTARX_LOCAL_CAPACITY_MOUNTS is invalid")
	}
	cfg.LocalCapacityMounts = capacityMounts
	cfg.Email, err = emailnotification.Load(strings.TrimSpace(os.Getenv("NOTARX_EMAIL_CONFIG_FILE")))
	if err != nil {
		return Config{}, err
	}
	if cfg.EmailDefaultLocale, err = emailnotification.ParseDefaultLocale(os.Getenv("NOTARX_EMAIL_DEFAULT_LOCALE")); err != nil {
		return Config{}, err
	}
	if err := configureOTLPLogs(&cfg); err != nil {
		return Config{}, err
	}
	if cfg.ConanTokenSecret != "" && (len(cfg.ConanTokenSecret) < 32 || cfg.ConanTokenSecret == cfg.ResolverToken || cfg.ConanTokenSecret == cfg.AdminToken) {
		return Config{}, fmt.Errorf("NOTARX_CONAN_TOKEN_SECRET requires at least 32 bytes and must differ from login credentials")
	}
	if cfg.DatabaseURL == "" || cfg.AdminToken == "" || cfg.ResolverToken == "" {
		return Config{}, fmt.Errorf("NOTARX_DATABASE_URL, NOTARX_ADMIN_TOKEN, and NOTARX_RESOLVER_TOKEN are required")
	}
	if err := configureDatabase(&cfg); err != nil {
		return Config{}, err
	}
	objectConfig, err := loadObjectStore()
	if err != nil {
		return Config{}, err
	}
	cfg.ObjectStore = objectConfig
	if err := configureScanner(&cfg); err != nil {
		return Config{}, err
	}
	if err := configureAPTRestore(&cfg); err != nil {
		return Config{}, err
	}
	if err := configureAPTSigner(&cfg); err != nil {
		return Config{}, err
	}
	if attempts, err := positiveIntEnv("NOTARX_LOCAL_AUTH_MAX_FAILED_ATTEMPTS", cfg.LocalAuthMaxFailedAttempts, false); err != nil {
		return Config{}, err
	} else {
		cfg.LocalAuthMaxFailedAttempts = attempts
	}
	if cfg.LocalAuthMaxFailedAttempts > 100 {
		return Config{}, fmt.Errorf("NOTARX_LOCAL_AUTH_MAX_FAILED_ATTEMPTS must not exceed 100")
	}
	if lockout, err := durationEnv("NOTARX_LOCAL_AUTH_LOCKOUT_DURATION", cfg.LocalAuthLockoutDuration); err != nil {
		return Config{}, err
	} else {
		cfg.LocalAuthLockoutDuration = lockout
	}
	if cfg.LocalAuthLockoutDuration < time.Minute || cfg.LocalAuthLockoutDuration > 24*time.Hour {
		return Config{}, fmt.Errorf("NOTARX_LOCAL_AUTH_LOCKOUT_DURATION must be between 1m and 24h")
	}
	if cfg.OIDCIssuer != "" {
		if cfg.OIDCAudience == "" {
			return Config{}, fmt.Errorf("NOTARX_OIDC_AUDIENCE is required when NOTARX_OIDC_ISSUER is set")
		}
		if parsed, err := url.ParseRequestURI(cfg.OIDCIssuer); err != nil || parsed.Host == "" || !secureOrLoopbackHTTP(parsed) {
			return Config{}, fmt.Errorf("NOTARX_OIDC_ISSUER must use HTTPS outside localhost")
		}
		if cfg.OIDCJWKSURL != "" {
			if parsed, err := url.ParseRequestURI(cfg.OIDCJWKSURL); err != nil || parsed.Host == "" || !secureOrLoopbackHTTP(parsed) {
				return Config{}, fmt.Errorf("NOTARX_OIDC_JWKS_URL must use HTTPS outside localhost")
			}
		}
		if cfg.OIDCClientID != "" {
			if cfg.OIDCRedirectURL == "" {
				return Config{}, fmt.Errorf("NOTARX_OIDC_REDIRECT_URL is required when NOTARX_OIDC_CLIENT_ID is set")
			}
			parsed, err := url.ParseRequestURI(cfg.OIDCRedirectURL)
			if err != nil || parsed.Host == "" || parsed.Scheme != "https" && parsed.Scheme != "http" {
				return Config{}, fmt.Errorf("NOTARX_OIDC_REDIRECT_URL must be an HTTP or HTTPS URL")
			}
			if parsed.Scheme == "http" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1" {
				return Config{}, fmt.Errorf("NOTARX_OIDC_REDIRECT_URL must use HTTPS outside localhost")
			}
		} else if cfg.OIDCClientSecret != "" || cfg.OIDCRedirectURL != "" {
			return Config{}, fmt.Errorf("NOTARX_OIDC_CLIENT_ID is required when OIDC browser login is configured")
		}
	} else if cfg.OIDCClientID != "" || cfg.OIDCClientSecret != "" || cfg.OIDCRedirectURL != "" {
		return Config{}, fmt.Errorf("NOTARX_OIDC_ISSUER is required for OIDC browser login")
	}
	if raw := strings.TrimSpace(os.Getenv("NOTARX_OTEL_SAMPLING_RATIO")); raw != "" {
		ratio, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 || ratio > 1 {
			return Config{}, fmt.Errorf("NOTARX_OTEL_SAMPLING_RATIO must be between 0 and 1")
		}
		cfg.OTELSamplingRatio = ratio
	}
	if value, err := positiveIntEnv("NOTARX_DATABASE_MAX_OPEN_CONNS", cfg.DatabasePool.MaxOpenConns, false); err != nil {
		return Config{}, err
	} else {
		cfg.DatabasePool.MaxOpenConns = value
	}
	if value, err := positiveIntEnv("NOTARX_DATABASE_MAX_IDLE_CONNS", cfg.DatabasePool.MaxIdleConns, true); err != nil {
		return Config{}, err
	} else {
		cfg.DatabasePool.MaxIdleConns = value
	}
	if ttl, err := durationEnv("NOTARX_DATABASE_CONN_MAX_LIFETIME", cfg.DatabasePool.ConnMaxLifetime); err != nil {
		return Config{}, err
	} else {
		cfg.DatabasePool.ConnMaxLifetime = ttl
	}
	if ttl, err := durationEnv("NOTARX_DATABASE_CONN_MAX_IDLE_TIME", cfg.DatabasePool.ConnMaxIdleTime); err != nil {
		return Config{}, err
	} else {
		cfg.DatabasePool.ConnMaxIdleTime = ttl
	}
	if value, err := positiveIntEnv("NOTARX_DATABASE_COORDINATOR_MAX_OPEN_CONNS", cfg.DatabaseCoordinatorPool.MaxOpenConns, false); err != nil {
		return Config{}, err
	} else {
		cfg.DatabaseCoordinatorPool.MaxOpenConns = value
	}
	if value, err := positiveIntEnv("NOTARX_DATABASE_COORDINATOR_MAX_IDLE_CONNS", cfg.DatabaseCoordinatorPool.MaxIdleConns, true); err != nil {
		return Config{}, err
	} else {
		cfg.DatabaseCoordinatorPool.MaxIdleConns = value
	}
	if value, err := positiveIntEnv("NOTARX_DATABASE_ARTIFACT_LOCK_MAX_OPEN_CONNS", cfg.DatabaseArtifactLockPool.MaxOpenConns, false); err != nil {
		return Config{}, err
	} else {
		cfg.DatabaseArtifactLockPool.MaxOpenConns = value
	}
	if value, err := positiveIntEnv("NOTARX_DATABASE_ARTIFACT_LOCK_MAX_IDLE_CONNS", cfg.DatabaseArtifactLockPool.MaxIdleConns, true); err != nil {
		return Config{}, err
	} else {
		cfg.DatabaseArtifactLockPool.MaxIdleConns = value
	}
	cfg.DatabaseCoordinatorPool.ConnMaxLifetime = cfg.DatabasePool.ConnMaxLifetime
	cfg.DatabaseCoordinatorPool.ConnMaxIdleTime = cfg.DatabasePool.ConnMaxIdleTime
	cfg.DatabaseArtifactLockPool.ConnMaxLifetime = cfg.DatabasePool.ConnMaxLifetime
	cfg.DatabaseArtifactLockPool.ConnMaxIdleTime = cfg.DatabasePool.ConnMaxIdleTime
	if err := cfg.DatabasePool.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid database pool configuration: %w", err)
	}
	if err := cfg.DatabaseCoordinatorPool.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid database coordinator pool configuration: %w", err)
	}
	if err := cfg.DatabaseArtifactLockPool.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid database artifact lock pool configuration: %w", err)
	}
	if cfg.DatabaseDriver != database.DriverMySQL {
		if err := cfg.DatabaseNotificationPool.Validate(); err != nil {
			return Config{}, fmt.Errorf("invalid database notification pool configuration: %w", err)
		}
	}
	if raw := strings.TrimSpace(os.Getenv("NOTARX_RAW_CACHE_MAX_OBJECT_BYTES")); raw != "" {
		bytes, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || bytes <= 0 {
			return Config{}, fmt.Errorf("NOTARX_RAW_CACHE_MAX_OBJECT_BYTES must be a positive integer")
		}
		cfg.RawCacheMaxObjectBytes = bytes
	}
	if value, err := positiveIntEnv("NOTARX_RAW_CACHE_MAX_CONCURRENT_SPOOLS", cfg.RawCacheMaxConcurrentSpools, false); err != nil {
		return Config{}, err
	} else {
		cfg.RawCacheMaxConcurrentSpools = value
	}
	if raw := strings.TrimSpace(os.Getenv("NOTARX_CONAN_CACHE_MAX_OBJECT_BYTES")); raw != "" {
		bytes, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || bytes <= 0 {
			return Config{}, fmt.Errorf("NOTARX_CONAN_CACHE_MAX_OBJECT_BYTES must be a positive integer")
		}
		cfg.ConanCacheMaxObjectBytes = bytes
	}
	if ttl, err := durationEnv("NOTARX_OCI_CACHE_TTL", cfg.OCICacheTTL); err != nil {
		return Config{}, err
	} else {
		cfg.OCICacheTTL = ttl
	}
	if ttl, err := durationEnv("NOTARX_MAVEN_CACHE_TTL", cfg.MavenCacheTTL); err != nil {
		return Config{}, err
	} else {
		cfg.MavenCacheTTL = ttl
	}
	if ttl, err := durationEnv("NOTARX_MAVEN_METADATA_CACHE_TTL", cfg.MavenMetadataCacheTTL); err != nil {
		return Config{}, err
	} else {
		cfg.MavenMetadataCacheTTL = ttl
	}
	if ttl, err := durationEnv("NOTARX_MAVEN_NEGATIVE_CACHE_TTL", cfg.MavenNegativeCacheTTL); err != nil {
		return Config{}, err
	} else {
		cfg.MavenNegativeCacheTTL = ttl
	}
	if ttl, err := durationEnv("NOTARX_NPM_METADATA_CACHE_TTL", cfg.NPMMetadataCacheTTL); err != nil {
		return Config{}, err
	} else {
		cfg.NPMMetadataCacheTTL = ttl
	}
	if ttl, err := durationEnv("NOTARX_NPM_NEGATIVE_CACHE_TTL", cfg.NPMNegativeCacheTTL); err != nil {
		return Config{}, err
	} else {
		cfg.NPMNegativeCacheTTL = ttl
	}
	if ttl, err := durationEnv("NOTARX_NPM_PROXY_BREAKER_TTL", cfg.NPMProxyBreakerTTL); err != nil {
		return Config{}, err
	} else {
		cfg.NPMProxyBreakerTTL = ttl
	}
	if ttl, err := durationEnv("NOTARX_RAW_CACHE_TTL", cfg.RawCacheTTL); err != nil {
		return Config{}, err
	} else {
		cfg.RawCacheTTL = ttl
	}
	if ttl, err := durationEnv("NOTARX_CONAN_CACHE_TTL", cfg.ConanCacheTTL); err != nil {
		return Config{}, err
	} else {
		cfg.ConanCacheTTL = ttl
	}
	if ttl, err := durationEnv("NOTARX_RUNTIME_NODE_RETENTION", cfg.RuntimeNodeRetention); err != nil {
		return Config{}, err
	} else {
		cfg.RuntimeNodeRetention = ttl
	}
	if workload, err := workloadConfigFromEnv(cfg.Workload); err != nil {
		return Config{}, err
	} else {
		cfg.Workload = workload
	}
	if saml, err := samlConfigFromEnv(cfg.SAML); err != nil {
		return Config{}, err
	} else {
		cfg.SAML = saml
	}
	if interval, err := durationEnv("NOTARX_RUNTIME_NODE_PRUNE_INTERVAL", cfg.RuntimeNodePruneInterval); err != nil {
		return Config{}, err
	} else {
		cfg.RuntimeNodePruneInterval = interval
	}
	if interval, err := durationEnv("NOTARX_QUOTA_RECONCILE_INTERVAL", cfg.QuotaReconcileInterval); err != nil {
		return Config{}, err
	} else {
		cfg.QuotaReconcileInterval = interval
	}
	if ahead, err := positiveIntEnv("NOTARX_PARTITION_AHEAD", cfg.PartitionAhead, false); err != nil {
		return Config{}, err
	} else {
		cfg.PartitionAhead = ahead
	}
	if loaded, err := retention.LoadConfig(os.LookupEnv); err != nil {
		return Config{}, err
	} else {
		cfg.Retention = loaded
	}
	if err := loadReadCacheConfig(&cfg); err != nil {
		return Config{}, err
	}
	if err := loadAuditWriterConfig(&cfg); err != nil {
		return Config{}, err
	}
	if cfg.InlineMaxBytes, err = positiveIntEnv("NOTARX_INLINE_MAX_BYTES", cfg.InlineMaxBytes, true); err != nil {
		return Config{}, err
	}
	if cfg.MigrationStoragePoolID != "" && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`).MatchString(cfg.MigrationStoragePoolID) {
		return Config{}, fmt.Errorf("invalid migration storage pool ID")
	}
	cfg.MigrationSourcePrivateEndpoints, err = parseMigrationSourcePrivateEndpoints(os.Getenv("NOTARX_MIGRATION_SOURCE_PRIVATE_ENDPOINTS"))
	if err != nil {
		return Config{}, err
	}
	cfg.publicURL, err = ParsePublicURL(os.Getenv(PublicURLEnv))
	if err != nil {
		return Config{}, err
	}
	if cfg.Issuer, err = loadIssuerConfig(cfg.publicURL); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func secureOrLoopbackHTTP(parsed *url.URL) bool {
	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}
	return isLoopbackHost(parsed.Hostname())
}

func configureScanner(cfg *Config) error {
	requestedWithoutEndpoint := cfg.ScannerHealthEndpoint != "" || cfg.ScannerName != "" || cfg.ScannerToken != "" || strings.TrimSpace(os.Getenv("NOTARX_SCANNER_FORMATS")) != "" || strings.TrimSpace(os.Getenv("NOTARX_SCANNER_TIMEOUT")) != "" || strings.TrimSpace(os.Getenv("NOTARX_SCANNER_HEALTH_TIMEOUT")) != "" || strings.TrimSpace(os.Getenv("NOTARX_SCANNER_DATABASE_MAX_AGE")) != "" || strings.TrimSpace(os.Getenv("NOTARX_SCANNER_MAX_RESPONSE_BYTES")) != "" || strings.TrimSpace(os.Getenv("NOTARX_SCANNER_MAX_ARTIFACT_BYTES")) != ""
	if cfg.ScannerEndpoint == "" {
		if requestedWithoutEndpoint {
			return fmt.Errorf("NOTARX_SCANNER_ENDPOINT is required when artifact scanner settings are configured")
		}
		cfg.ScannerName = ""
		cfg.ScannerToken = ""
		cfg.ScannerFormats = []string{}
		return nil
	}
	parsed, err := url.ParseRequestURI(cfg.ScannerEndpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !secureOrLoopbackHTTP(parsed) {
		return fmt.Errorf("NOTARX_SCANNER_ENDPOINT must be an HTTPS URL without credentials, query, or fragment outside localhost")
	}
	if cfg.ScannerHealthEndpoint != "" {
		parsed, err = url.ParseRequestURI(cfg.ScannerHealthEndpoint)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !secureOrLoopbackHTTP(parsed) {
			return fmt.Errorf("NOTARX_SCANNER_HEALTH_ENDPOINT must be an HTTPS URL without credentials, query, or fragment outside localhost")
		}
	}
	if cfg.ScannerName == "" {
		cfg.ScannerName = "artifact-scanner"
	}
	if len(cfg.ScannerName) > 128 || strings.ContainsAny(cfg.ScannerName, "\r\n\x00") || strings.ContainsAny(cfg.ScannerToken, "\r\n") {
		return fmt.Errorf("artifact scanner name or token is invalid")
	}
	if cfg.ScannerTimeout, err = durationEnv("NOTARX_SCANNER_TIMEOUT", cfg.ScannerTimeout); err != nil {
		return err
	}
	if cfg.ScannerTimeout < time.Second || cfg.ScannerTimeout > 30*time.Minute {
		return fmt.Errorf("NOTARX_SCANNER_TIMEOUT must be between 1s and 30m")
	}
	if cfg.ScannerHealthTimeout, err = durationEnv("NOTARX_SCANNER_HEALTH_TIMEOUT", cfg.ScannerHealthTimeout); err != nil {
		return err
	}
	if cfg.ScannerHealthTimeout < time.Second || cfg.ScannerHealthTimeout > 30*time.Second {
		return fmt.Errorf("NOTARX_SCANNER_HEALTH_TIMEOUT must be between 1s and 30s")
	}
	if cfg.ScannerDatabaseMaxAge, err = durationEnv("NOTARX_SCANNER_DATABASE_MAX_AGE", cfg.ScannerDatabaseMaxAge); err != nil {
		return err
	}
	if cfg.ScannerDatabaseMaxAge < time.Minute || cfg.ScannerDatabaseMaxAge > 30*24*time.Hour {
		return fmt.Errorf("NOTARX_SCANNER_DATABASE_MAX_AGE must be between 1m and 720h")
	}
	if cfg.ScannerMaxResponseBytes, err = positiveInt64Env("NOTARX_SCANNER_MAX_RESPONSE_BYTES", cfg.ScannerMaxResponseBytes); err != nil {
		return err
	}
	if cfg.ScannerMaxResponseBytes < 1024 || cfg.ScannerMaxResponseBytes > 8<<20 {
		return fmt.Errorf("NOTARX_SCANNER_MAX_RESPONSE_BYTES must be between 1024 and 8388608")
	}
	if cfg.ScannerMaxArtifactBytes, err = positiveInt64Env("NOTARX_SCANNER_MAX_ARTIFACT_BYTES", cfg.ScannerMaxArtifactBytes); err != nil {
		return err
	}
	if cfg.ScannerMaxArtifactBytes > 1<<40 {
		return fmt.Errorf("NOTARX_SCANNER_MAX_ARTIFACT_BYTES must not exceed 1099511627776")
	}
	cfg.ScannerFormats = parseFilter(os.Getenv("NOTARX_SCANNER_FORMATS"), supportedScannerFormats)
	for _, format := range cfg.ScannerFormats {
		if _, ok := supportedScannerFormats[format]; !ok {
			return fmt.Errorf("NOTARX_SCANNER_FORMATS contains unsupported format %q", format)
		}
	}
	return nil
}

func configureAPTSigner(cfg *Config) error {
	requestedWithoutEndpoint := cfg.APTSignerToken != "" || strings.TrimSpace(os.Getenv("NOTARX_APT_SIGNER_TIMEOUT")) != "" ||
		len(cfg.APTSignerTrustedFingerprints) > 0 || cfg.APTSignerTrustedPublicKeysFile != "" || cfg.APTSignerTLSCAFile != ""
	if cfg.APTSignerEndpoint == "" {
		if requestedWithoutEndpoint {
			return fmt.Errorf("NOTARX_APT_SIGNER_ENDPOINT is required when APT signer settings are configured")
		}
		cfg.APTSignerToken = ""
		cfg.APTSignerTrustedFingerprints = nil
		cfg.APTSignerTrustedPublicKeysFile = ""
		cfg.APTSignerTrustedPublicKeys = nil
		cfg.APTSignerTLSCAFile = ""
		cfg.APTSignerTLSRootCertificates = nil
		return nil
	}
	parsed, err := url.ParseRequestURI(cfg.APTSignerEndpoint)
	if err != nil || parsed.Host == "" || parsed.Path == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !secureOrLoopbackHTTP(parsed) {
		return fmt.Errorf("NOTARX_APT_SIGNER_ENDPOINT must be an HTTPS URL without credentials, query, or fragment outside localhost")
	}
	if len(cfg.APTSignerToken) < 32 || len(cfg.APTSignerToken) > 256 || strings.ContainsAny(cfg.APTSignerToken, "\x00\r\n") {
		return fmt.Errorf("NOTARX_APT_SIGNER_TOKEN must contain between 32 and 256 safe bytes")
	}
	if cfg.APTSignerTimeout, err = durationEnv("NOTARX_APT_SIGNER_TIMEOUT", cfg.APTSignerTimeout); err != nil {
		return err
	}
	if cfg.APTSignerTimeout < time.Second || cfg.APTSignerTimeout > time.Minute {
		return fmt.Errorf("NOTARX_APT_SIGNER_TIMEOUT must be between 1s and 1m")
	}
	fingerprints, err := aptpublication.NormalizeTrustedSignerFingerprints(cfg.APTSignerTrustedFingerprints)
	if err != nil {
		return fmt.Errorf("NOTARX_APT_SIGNER_TRUSTED_FINGERPRINTS: %w", err)
	}
	remote := !aptpublication.IsLoopbackSignerHost(parsed.Hostname())
	if remote && (len(fingerprints) == 0 || cfg.APTSignerTrustedPublicKeysFile == "") {
		return fmt.Errorf("NOTARX_APT_SIGNER_TRUSTED_FINGERPRINTS and NOTARX_APT_SIGNER_TRUSTED_PUBLIC_KEYS_FILE are required for a remote APT signer")
	}
	if len(fingerprints) > 0 || cfg.APTSignerTrustedPublicKeysFile != "" {
		if len(fingerprints) == 0 || cfg.APTSignerTrustedPublicKeysFile == "" {
			return fmt.Errorf("NOTARX_APT_SIGNER_TRUSTED_FINGERPRINTS and NOTARX_APT_SIGNER_TRUSTED_PUBLIC_KEYS_FILE must be configured together")
		}
		publicKeys, loadErr := aptpublication.LoadTrustedSignerPublicKeys(cfg.APTSignerTrustedPublicKeysFile)
		if loadErr != nil {
			return fmt.Errorf("NOTARX_APT_SIGNER_TRUSTED_PUBLIC_KEYS_FILE could not be loaded")
		}
		if validateErr := aptpublication.ValidateTrustedSignerPublicKeys(fingerprints, publicKeys); validateErr != nil {
			return fmt.Errorf("APT signer trusted public-key policy is invalid")
		}
		cfg.APTSignerTrustedPublicKeys = publicKeys
	}
	if cfg.APTSignerTLSCAFile != "" {
		if parsed.Scheme != "https" {
			return fmt.Errorf("NOTARX_APT_SIGNER_TLS_CA_FILE requires an HTTPS signer endpoint")
		}
		rootCertificates, loadErr := aptpublication.LoadHTTPSignerTLSRootCertificates(cfg.APTSignerTLSCAFile)
		if loadErr != nil {
			return fmt.Errorf("NOTARX_APT_SIGNER_TLS_CA_FILE could not be loaded")
		}
		cfg.APTSignerTLSRootCertificates = rootCertificates
	}
	cfg.APTSignerTrustedFingerprints = fingerprints
	return nil
}

func oidcScopes(raw string) []string {
	values := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
	seen := map[string]bool{"openid": true}
	scopes := []string{"openid"}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		scopes = append(scopes, value)
	}
	if len(scopes) == 1 {
		scopes = append(scopes, "profile", "email")
	}
	return scopes
}

func (c Config) HasRole(role NodeRole) bool {
	for _, configured := range c.NodeRoles {
		if configured == NodeRoleStandalone || configured == role {
			return true
		}
	}
	return false
}

func (c Config) WorkerEnabled(format, kind string) bool {
	if !c.HasRole(NodeRoleWorker) {
		return false
	}
	return c.WorkerFormatEnabled(format) && c.WorkerKindEnabled(kind)
}

func (c Config) WorkerFormatEnabled(format string) bool {
	return contains(c.WorkerFormats, format)
}

func (c Config) WorkerKindEnabled(kind string) bool {
	return contains(c.WorkerKinds, kind)
}

func (c Config) ScannerEnabled() bool {
	return c.ScannerEndpoint != ""
}

func (c Config) APTSignerEnabled() bool {
	return c.APTSignerEndpoint != ""
}

func (c Config) ScannerFormatEnabled(format string) bool {
	return c.ScannerEnabled() && contains(c.ScannerFormats, format)
}

func validateRuntimeConfig(cfg Config) error {
	if strings.TrimSpace(cfg.InstanceID) == "" {
		return fmt.Errorf("NOTARX_INSTANCE_ID must not be empty")
	}
	if len(cfg.NodeRoles) == 0 {
		return fmt.Errorf("NOTARX_NODE_ROLES must include at least one role")
	}
	standalone := false
	for _, role := range cfg.NodeRoles {
		if _, ok := supportedNodeRoles[role]; !ok {
			return fmt.Errorf("NOTARX_NODE_ROLES contains unsupported role %q", role)
		}
		if role == NodeRoleStandalone {
			standalone = true
		}
	}
	if standalone && len(cfg.NodeRoles) != 1 {
		return fmt.Errorf("NOTARX_NODE_ROLES=standalone cannot be combined with other roles")
	}
	for _, format := range cfg.WorkerFormats {
		if format == "helm" && cfg.HelmPreviewEnabled {
			continue
		}
		if format == "nuget" && cfg.NuGetPreviewEnabled {
			continue
		}
		if format == "rubygems" && cfg.RubyGemsPreviewEnabled {
			continue
		}
		if _, ok := supportedWorkerFormats[format]; !ok {
			return fmt.Errorf("NOTARX_WORKER_FORMATS contains unsupported format %q", format)
		}
	}
	for _, kind := range cfg.WorkerKinds {
		if _, ok := supportedWorkerKinds[kind]; !ok {
			return fmt.Errorf("NOTARX_WORKER_KINDS contains unsupported kind %q", kind)
		}
	}
	return nil
}

func parseNodeRoles(raw string) []NodeRole {
	values := splitCSV(raw)
	if len(values) == 0 {
		return []NodeRole{NodeRoleStandalone}
	}
	roles := make([]NodeRole, 0, len(values))
	seen := make(map[NodeRole]struct{}, len(values))
	for _, value := range values {
		role := NodeRole(strings.ToLower(value))
		if _, exists := seen[role]; exists {
			continue
		}
		seen[role] = struct{}{}
		roles = append(roles, role)
	}
	return roles
}

func parseFilter(raw string, supported map[string]struct{}) []string {
	values := splitCSV(raw)
	if len(values) == 0 {
		for value := range supported {
			values = append(values, value)
		}
		// Map iteration is intentionally normalized so the effective config is
		// deterministic in logs and tests.
		sort.Strings(values)
		return values
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(value)
		if _, ok := supported[value]; !ok {
			result = append(result, value)
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return "local"
	}
	return strings.TrimSpace(name)
}

func positiveIntEnv(name string, fallback int, allowZero bool) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || !allowZero && value == 0 {
		if allowZero {
			return 0, fmt.Errorf("%s must be a non-negative integer", name)
		}
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func positiveInt64Env(name string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

// durationEnv parses a cache TTL override. The value is either a Go duration
// string ("30m", "1h30m") or a positive integer number of seconds ("900").
// An unset variable retains the fallback.
func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if seconds <= 0 {
			return 0, fmt.Errorf("%s must be a positive duration", name)
		}
		return time.Duration(seconds) * time.Second, nil
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil || ttl <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return ttl, nil
}

func splitCSV(raw string) []string {
	var values []string
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// repositoryReaders parses semicolon-separated actor grants. Each grant is
// actor=repository-pattern|repository-pattern, where a trailing /* matches a
// repository prefix. An omitted variable retains the local-development
// unrestricted resolver token behavior.
func repositoryReaders(raw string) map[string][]string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	readers := make(map[string][]string)
	for _, grant := range strings.Split(raw, ";") {
		actor, patterns, found := strings.Cut(strings.TrimSpace(grant), "=")
		if !found || strings.TrimSpace(actor) == "" {
			continue
		}
		for _, pattern := range strings.Split(patterns, "|") {
			if pattern = strings.TrimSpace(pattern); pattern != "" {
				readers[strings.TrimSpace(actor)] = append(readers[strings.TrimSpace(actor)], pattern)
			}
		}
	}
	return readers
}

// legacyReadPermissive reports whether a deployment that has configured no
// reader patterns admits an otherwise-unmatched caller. Denying is the default
// and the only recognized opt-out is allow, so a typo fails closed rather than
// silently widening access.
func legacyReadPermissive(raw string) bool {
	return strings.EqualFold(strings.TrimSpace(raw), "allow")
}

func repositoryCacheQuotas(raw string) map[string]int64 {
	quotas := make(map[string]int64)
	for _, entry := range strings.Split(raw, ";") {
		repository, limit, found := strings.Cut(strings.TrimSpace(entry), "=")
		if !found || strings.TrimSpace(repository) == "" {
			continue
		}
		bytes, err := strconv.ParseInt(strings.TrimSpace(limit), 10, 64)
		if err == nil && bytes > 0 {
			quotas[strings.TrimSpace(repository)] = bytes
		}
	}
	return quotas
}

func value(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

// Restore trust is independent of the signer service: recovery never requests signatures.
func configureAPTRestore(cfg *Config) error {
	if len(cfg.APTRestoreTrustedFingerprints) == 0 && cfg.APTRestoreTrustedPublicKeysFile == "" {
		return nil
	}
	if len(cfg.APTRestoreTrustedFingerprints) == 0 || cfg.APTRestoreTrustedPublicKeysFile == "" {
		return fmt.Errorf("NOTARX_APT_RESTORE_TRUSTED_FINGERPRINTS and NOTARX_APT_RESTORE_TRUSTED_PUBLIC_KEYS_FILE must be configured together")
	}
	fingerprints, err := aptpublication.NormalizeTrustedSignerFingerprints(cfg.APTRestoreTrustedFingerprints)
	if err != nil {
		return fmt.Errorf("NOTARX_APT_RESTORE_TRUSTED_FINGERPRINTS: %w", err)
	}
	publicKeys, err := aptpublication.LoadTrustedSignerPublicKeys(cfg.APTRestoreTrustedPublicKeysFile)
	if err != nil {
		return fmt.Errorf("NOTARX_APT_RESTORE_TRUSTED_PUBLIC_KEYS_FILE could not be loaded")
	}
	if err = aptpublication.ValidateTrustedSignerPublicKeys(fingerprints, publicKeys); err != nil {
		return fmt.Errorf("APT restore trusted public-key policy is invalid")
	}
	cfg.APTRestoreTrustedFingerprints, cfg.APTRestoreTrustedPublicKeys = fingerprints, publicKeys
	return nil
}
