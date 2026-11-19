package app

import (
	"time"

	migrationapi "github.com/mlops-ca/notarx/internal/admin/nexusmigration"
	sourceapi "github.com/mlops-ca/notarx/internal/admin/nexussource"
	"github.com/mlops-ca/notarx/internal/aptpublication"
	"github.com/mlops-ca/notarx/internal/consoletheme"
	"github.com/mlops-ca/notarx/internal/operationalog"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/scanning"
)

// generatedRepositoryAPIAdapter temporarily owns the domains not yet extracted
// under #280. Migrated operations live only on their domain handler.
type generatedRepositoryAPIAdapter struct {
	*sourceapi.Adapter
	*migrationapi.BatchAdapter
	managementScope
	store                  repository.HostedRepositoryStore
	sessions               nativeMavenHandler
	npmRestore             repository.NPMRestoreStore
	aptPublication         *aptpublication.Manager
	aptSnapshotPublisher   *aptpublication.Publisher
	aptSnapshotImporter    *aptpublication.SnapshotArchiveImporter
	aptSnapshotExporter    aptpublication.SnapshotArchiveExporter
	aptPublications        repository.NativeAPTPublicationStore
	retentionPolicies      repository.RepositoryRetentionPolicyStore
	securityPolicies       repository.RepositorySecurityPolicyStore
	quarantineReadPolicies repository.RepositoryQuarantineReadPolicyStore
	capacities             repository.RepositoryCapacityStore
	tombstones             repository.ArtifactTombstoneStore
	lifecycleJobs          repository.LifecycleJobStore
	auditRetention         repository.AuditRetentionStore
	publicAccess           repository.PublicAccessPolicyStore
	siteSettings           repository.SiteSettingsStore
	overview               *overviewCache
	authzCacheTTL          time.Duration
	consoleThemePackages   repository.ConsoleThemeStore
	consoleThemes          *consoletheme.Registry
	replication            repository.ReplicationStore
	oci                    repository.NativeOCIStore
	cargo                  repository.NativeCargoStore
	conan                  repository.NativeConanStore
	audit                  repository.Store
	maintenance            *CacheMaintenance
	searchProjection       repository.ArtifactSearchStore
	intelligence           repository.ArtifactIntelligenceStore
	quarantine             repository.ArtifactQuarantineStore
	runtimeNodes           repository.RuntimeNodeStore
	scheduledTasks         repository.ScheduledTaskStore
	emailTargets           repository.EmailStore
	quotaAlerts            repository.RepositoryQuotaAlertStore
	webhooks               repository.WebhookStore
	queueStats             repository.BackgroundOperationQueueStore
	diagnostics            Dependencies
	objects                ObjectStores
	logBuffer              *operationalog.Buffer
	artifactScanner        scanning.Scanner
	artifactScanFormats    []repository.Format
}
