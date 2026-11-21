package repository

import (
	"reflect"
	"sort"
)

// MySQLStore is the MySQL 8.4 store set of ADR 0022. It starts empty: each
// store slice adds the methods of one port, and a port only counts as
// implemented once *MySQLStore satisfies its interface, so a partial backend
// is reported by MissingMySQLStorePorts and refused at start-up.
type MySQLStore struct{}

// gatewayStorePorts are the store interfaces the gateway composes
// (internal/app.GatewayStore). A test keeps the list in step with the
// architecture baseline.
var gatewayStorePorts = map[string]reflect.Type{
	"APIKeyStore":                         reflect.TypeFor[APIKeyStore](),
	"APTArtifactStore":                    reflect.TypeFor[APTArtifactStore](),
	"ArtifactBrowseStore":                 reflect.TypeFor[ArtifactBrowseStore](),
	"ArtifactCatalogStore":                reflect.TypeFor[ArtifactCatalogStore](),
	"ArtifactIntelligenceStore":           reflect.TypeFor[ArtifactIntelligenceStore](),
	"ArtifactQuarantineStore":             reflect.TypeFor[ArtifactQuarantineStore](),
	"ArtifactTombstoneStore":              reflect.TypeFor[ArtifactTombstoneStore](),
	"ArtifactUsageStore":                  reflect.TypeFor[ArtifactUsageStore](),
	"AuditRetentionStore":                 reflect.TypeFor[AuditRetentionStore](),
	"AuthorizationRoleStore":              reflect.TypeFor[AuthorizationRoleStore](),
	"AuthorizationTemplateStore":          reflect.TypeFor[AuthorizationTemplateStore](),
	"BackgroundOperationQueueStore":       reflect.TypeFor[BackgroundOperationQueueStore](),
	"CargoGroupStore":                     reflect.TypeFor[CargoGroupStore](),
	"CargoProxyStore":                     reflect.TypeFor[CargoProxyStore](),
	"ConanStore":                          reflect.TypeFor[ConanStore](),
	"ConsoleThemeStore":                   reflect.TypeFor[ConsoleThemeStore](),
	"EmailStore":                          reflect.TypeFor[EmailStore](),
	"HostedGroupStore":                    reflect.TypeFor[HostedGroupStore](),
	"HostedRepositoryStore":               reflect.TypeFor[HostedRepositoryStore](),
	"IdentityProviderStore":               reflect.TypeFor[IdentityProviderStore](),
	"LifecycleJobStore":                   reflect.TypeFor[LifecycleJobStore](),
	"MavenStore":                          reflect.TypeFor[MavenStore](),
	"NativeAPTPublicationStore":           reflect.TypeFor[NativeAPTPublicationStore](),
	"NativeAPTStore":                      reflect.TypeFor[NativeAPTStore](),
	"NativeCargoStore":                    reflect.TypeFor[NativeCargoStore](),
	"NativeConanStore":                    reflect.TypeFor[NativeConanStore](),
	"NativeGoStore":                       reflect.TypeFor[NativeGoStore](),
	"NativeMavenStore":                    reflect.TypeFor[NativeMavenStore](),
	"NativeNPMStore":                      reflect.TypeFor[NativeNPMStore](),
	"NativeOCIStore":                      reflect.TypeFor[NativeOCIStore](),
	"NativePyPIStore":                     reflect.TypeFor[NativePyPIStore](),
	"NativeRawStore":                      reflect.TypeFor[NativeRawStore](),
	"PublicAccessPolicyStore":             reflect.TypeFor[PublicAccessPolicyStore](),
	"RawStore":                            reflect.TypeFor[RawStore](),
	"ReplicationStore":                    reflect.TypeFor[ReplicationStore](),
	"RepositoryCapacityStore":             reflect.TypeFor[RepositoryCapacityStore](),
	"PromotionRuleStore":                  reflect.TypeFor[PromotionRuleStore](),
	"RemoteInstanceStore":                 reflect.TypeFor[RemoteInstanceStore](),
	"RepositoryGrantStore":                reflect.TypeFor[RepositoryGrantStore](),
	"RepositoryQuarantineReadPolicyStore": reflect.TypeFor[RepositoryQuarantineReadPolicyStore](),
	"RepositoryQuotaAlertStore":           reflect.TypeFor[RepositoryQuotaAlertStore](),
	"RepositoryRetentionPolicyStore":      reflect.TypeFor[RepositoryRetentionPolicyStore](),
	"RepositorySecurityPolicyStore":       reflect.TypeFor[RepositorySecurityPolicyStore](),
	"RuntimeNodeStore":                    reflect.TypeFor[RuntimeNodeStore](),
	"SCIMStore":                           reflect.TypeFor[SCIMStore](),
	"SCIMTokenStore":                      reflect.TypeFor[SCIMTokenStore](),
	"ScheduledTaskStore":                  reflect.TypeFor[ScheduledTaskStore](),
	"ServiceAccountStore":                 reflect.TypeFor[ServiceAccountStore](),
	"SiteSettingsStore":                   reflect.TypeFor[SiteSettingsStore](),
	"Store":                               reflect.TypeFor[Store](),
	"UserGroupStore":                      reflect.TypeFor[UserGroupStore](),
	"UserIdentityLinker":                  reflect.TypeFor[UserIdentityLinker](),
	"UserIdentityStore":                   reflect.TypeFor[UserIdentityStore](),
	"UserSessionStore":                    reflect.TypeFor[UserSessionStore](),
	"UserStore":                           reflect.TypeFor[UserStore](),
	"WebhookStore":                        reflect.TypeFor[WebhookStore](),
}

// MissingMySQLStorePorts lists, sorted, the store ports that *MySQLStore does not implement yet.
func MissingMySQLStorePorts() []string {
	implementation := reflect.TypeFor[*MySQLStore]()
	missing := []string{}
	for name, port := range gatewayStorePorts {
		if !implementation.Implements(port) {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}
