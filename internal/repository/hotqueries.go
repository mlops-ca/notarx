package repository

import (
	"strings"
	"time"
)

// hotQuery is one entry of the plan-regression registry of ADR 0023 section 11: a
// statement the request path runs often, built from the store's own query text so a
// change to the store is planned too, the index its plan must use and the tables on
// which it must never fall back to a sequential scan. The registry is curated from
// pg_stat_statements on the target profile and executed by TestHotQueriesKeepTheirPlans
// (make explain-check).
type hotQuery struct {
	// name identifies the query in test output; it is unique.
	name string
	// build returns the statement and its arguments for the seeded fixture.
	build func(hotQueryFixture) (string, []any)
	// indexContains, when set, lists substrings of index names; the plan must scan an
	// index matching at least one. List every index the planner may legitimately pick
	// between equivalent ones. When empty the plan must still scan some index; leave it
	// empty for partitioned audit tables, whose partition indexes carry their own names.
	indexContains []string
	// noSeqScan lists the tables on which the plan must not contain a Seq Scan; a
	// partition of a listed table counts as the table.
	noSeqScan []string
}

// hotQueryFixture names rows that the datagen seed always produces.
type hotQueryFixture struct {
	repositoryID   string
	repositoryName string
	packageName    string
	version        string
	actor          string
}

func fixed(query string, args func(hotQueryFixture) []any) func(hotQueryFixture) (string, []any) {
	return func(f hotQueryFixture) (string, []any) { return query, args(f) }
}

// auditPage builds the real audit page statement for the filters of one request.
func auditPage(query func(hotQueryFixture) AuditQuery) func(hotQueryFixture) (string, []any) {
	return func(f hotQueryFixture) (string, []any) {
		sql, args, _ := buildAuditPageQuery(query(f), time.Now())
		return sql, args
	}
}

// The statement shapes of the catalogue search of ADR 0023 section 8: the keyset page of a repository and format in
// coordinate order, one row per coordinate, and the same page filtered by a substring of the coordinate, which the
// trigram index artifact_catalog_coordinate_trgm_idx serves. No store method runs them yet: the search rewrite
// (SearchArtifactProjection reading artifact_catalog instead of the projection view) must keep these shapes, and
// this registry is where its plans are pinned before and after it lands.
const (
	catalogPageQuery = `
	SELECT coordinate, latest_version, latest_digest, latest_published_at, publisher, version_count, size_bytes
	FROM artifact_catalog
	WHERE repository_id=$1::uuid AND format=$2 AND coordinate>$3
	ORDER BY coordinate
	LIMIT $4`
	catalogContainsQuery = `
	SELECT coordinate, latest_version, latest_digest, latest_published_at, publisher, version_count, size_bytes
	FROM artifact_catalog
	WHERE repository_id=$1::uuid AND format=$2 AND coordinate ILIKE $3 ESCAPE '\'
	  AND coordinate>$4
	ORDER BY coordinate
	LIMIT $5`
)

// hotQueryCount is the size of the registry: the top 30 statements of ADR 0023 section 11 and the two catalogue
// search plans of section 8.
const hotQueryCount = 32

// hotQueries is the registry. Add or swap a query when a hot statement appears in
// pg_stat_statements; the registry test pins the count at hotQueryCount and rejects
// duplicates and bad placeholders.
//
// A query on a table the seed leaves empty names no index: on an empty or small table the
// planner may pick any of several equivalent indexes, and which one changes with the rows
// other tests leave behind, so such an entry asserts what must hold everywhere, that the plan
// scans some index and never reads a listed table sequentially. A query on a seeded table, or
// one that only a single index can serve, names its index.
var hotQueries = []hotQuery{
	{
		name:          "npm.package.get",
		build:         fixed(getNPMPackageQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID, f.packageName} }),
		indexContains: []string{"native_npm_packages_pkey", "native_npm_packages_name_idx"},
		noSeqScan:     []string{"native_npm_packages"},
	},
	{
		name:          "npm.versions.list",
		build:         fixed(listNPMVersionsQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID, f.packageName} }),
		indexContains: []string{"native_npm_versions_pkey", "native_npm_versions_repository_id_package_name_tarball_name_key"},
		noSeqScan:     []string{"native_npm_versions"},
	},
	{
		name:          "npm.version.get",
		build:         fixed(getNPMVersionQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID, f.packageName, f.version} }),
		indexContains: []string{"native_npm_versions_pkey"},
		noSeqScan:     []string{"native_npm_versions"},
	},
	{
		name: "npm.version.byTarball",
		build: fixed(getNPMVersionByTarballQuery, func(f hotQueryFixture) []any {
			return []any{f.repositoryID, f.packageName, f.packageName + "-" + f.version + ".tgz"}
		}),
		indexContains: []string{"native_npm_versions_repository_id_package_name_tarball_name_key"},
		noSeqScan:     []string{"native_npm_versions"},
	},
	{
		name:          "repository.byName",
		build:         fixed(getHostedRepositoryByNameQuery, func(f hotQueryFixture) []any { return []any{f.repositoryName} }),
		indexContains: []string{"hosted_repositories_name_key", "hosted_repositories_visibility_name_idx"},
		noSeqScan:     []string{"hosted_repositories"},
	},
	{
		name:          "repository.keysetPage",
		build:         fixed(listHostedRepositoriesQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID, 51} }),
		indexContains: []string{"hosted_repositories_pkey", "hosted_repositories_created_at_idx"},
		noSeqScan:     []string{"hosted_repositories"},
	},
	{
		name: "lifecycle.claimByKind",
		build: fixed(claimLifecycleCandidatesByKindQuery, func(hotQueryFixture) []any {
			return []any{string(LifecycleJobKind("scan")), []string{}, []string{}, 10}
		}),
		indexContains: []string{"lifecycle_jobs_kind_actionable_idx"},
		noSeqScan:     []string{"lifecycle_jobs"},
	},
	{
		name:          "lifecycle.claimAnyKind",
		build:         fixed(claimLifecycleCandidatesAnyKindQuery, func(hotQueryFixture) []any { return []any{[]string{}, []string{}, 10} }),
		indexContains: []string{"lifecycle_jobs_actionable_idx"},
		noSeqScan:     []string{"lifecycle_jobs"},
	},
	{
		name:      "audit.byRepository",
		build:     auditPage(func(f hotQueryFixture) AuditQuery { return AuditQuery{Repository: f.repositoryName, Limit: 50} }),
		noSeqScan: []string{"resolver_audit_log", "security_audit_log"},
	},
	{
		name:      "audit.byActor",
		build:     auditPage(func(f hotQueryFixture) AuditQuery { return AuditQuery{Actor: f.actor, Limit: 50} }),
		noSeqScan: []string{"resolver_audit_log", "security_audit_log"},
	},
	{
		name: "audit.keysetPage",
		build: auditPage(func(hotQueryFixture) AuditQuery {
			return AuditQuery{Limit: 50, Before: AuditCursor{OccurredAt: time.Now(), ID: 1 << 40, Class: AuditClassAsync}}
		}),
		noSeqScan: []string{"resolver_audit_log", "security_audit_log"},
	},
	{
		name:          "apiKey.bySecretHash",
		build:         fixed(getActiveAPIKeyBySecretHashQuery, func(hotQueryFixture) []any { return []any{"perf-secret-hash"} }),
		indexContains: []string{"api_keys_secret_hash_key"},
		// service_accounts is a small configuration table that the hashed sub-plan of the
		// "active service account" condition reads whole, so only api_keys is listed.
		noSeqScan: []string{"api_keys"},
	},
	{
		name:      "session.byID",
		build:     fixed(getUserSessionQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID, f.repositoryID} }),
		noSeqScan: []string{"user_sessions"},
	},
	{
		name:      "grants.byRepository",
		build:     fixed(listRepositoryGrantsQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID} }),
		noSeqScan: []string{"repository_grants"},
	},
	{
		name:      "quota.counters",
		build:     fixed(getRepositoryUsageCountersQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID} }),
		noSeqScan: []string{"repository_usage_counters"},
	},
	{
		name:      "repository.byID",
		build:     fixed(getHostedRepositoryQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID} }),
		noSeqScan: []string{"hosted_repositories"},
	},
	{
		name:      "lifecycle.listByRepository",
		build:     fixed(listLifecycleJobsQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID, 50} }),
		noSeqScan: []string{"lifecycle_jobs"},
	},
	{
		name:      "webhook.deliveryByID",
		build:     fixed(getWebhookDeliveryQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID} }),
		noSeqScan: []string{"webhook_deliveries", "domain_events"},
	},
	{
		name:          "webhook.claim",
		build:         fixed(claimWebhookDeliveriesQuery, func(hotQueryFixture) []any { return []any{"perf-node", "5 minutes", 20} }),
		indexContains: []string{"webhook_deliveries_actionable_idx"},
		noSeqScan:     []string{"webhook_deliveries"},
	},
	{
		name:          "replication.claim",
		build:         fixed(claimReplicationPlansQuery, func(hotQueryFixture) []any { return []any{[]string{}, 10} }),
		indexContains: []string{"replication_plans_claim_idx", "replication_plans_retry_idx"},
		noSeqScan:     []string{"replication_plans"},
	},
	{
		name:      "usage.byRepository",
		build:     fixed(walkArtifactUsageQuery, func(f hotQueryFixture) []any { return []any{f.repositoryName} }),
		noSeqScan: []string{"artifact_usage_stats", "hosted_repositories", "hosted_groups"},
	},
	{
		name:      "maven.coordinate.latest",
		build:     fixed(latestMavenArtifactByCoordinateQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID, "perf.group:artifact:1.0.0"} }),
		noSeqScan: []string{"native_maven_artifacts"},
	},
	{
		name: "oci.manifest.byDigest",
		build: fixed(getOCIManifestByDigestQuery, func(f hotQueryFixture) []any {
			return []any{f.repositoryID, "perf/image", "sha256:" + strings.Repeat("0", 64)}
		}),
		noSeqScan: []string{"native_oci_manifests"},
	},
	{
		name:      "oci.manifest.byTag",
		build:     fixed(getOCIManifestByTagQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID, "perf/image", "latest"} }),
		noSeqScan: []string{"native_oci_tags", "native_oci_manifests"},
	},
	{
		name:      "pypi.file.byFilename",
		build:     fixed(getPyPIFileQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID, "perf-1.0.0.tar.gz"} }),
		noSeqScan: []string{"native_pypi_files"},
	},
	{
		name:      "raw.asset.get",
		build:     fixed(getRawAssetQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID, "perf/file.bin"} }),
		noSeqScan: []string{"native_raw_assets", "native_raw_objects"},
	},
	{
		name:      "go.version.get",
		build:     fixed(getGoModuleVersionQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID, "example.com/perf/module", "v1.0.0"} }),
		noSeqScan: []string{"native_go_versions", "artifact_tombstones"},
	},
	{
		name: "conan.revision.get",
		build: fixed(getConanRecipeRevisionQuery, func(f hotQueryFixture) []any {
			return []any{f.repositoryID, "perf/1.0@_/_", "0123456789abcdef"}
		}),
		noSeqScan: []string{"native_conan_recipe_revisions"},
	},
	{
		name:      "apt.asset.get",
		build:     fixed(getAPTAssetQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID, "pool/main/p/perf/perf_1.0_amd64.deb"} }),
		noSeqScan: []string{"native_apt_assets"},
	},
	{
		name:          "catalog.search.page",
		build:         fixed(catalogPageQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID, "npm", "", 51} }),
		indexContains: []string{"artifact_catalog_pkey"},
		noSeqScan:     []string{"artifact_catalog"},
	},
	{
		name: "catalog.search.contains",
		build: fixed(catalogContainsQuery, func(f hotQueryFixture) []any {
			return []any{f.repositoryID, "npm", "%" + escapeLikePrefix(f.packageName) + "%", "", 51}
		}),
		indexContains: []string{"artifact_catalog_pkey", "artifact_catalog_coordinate_trgm_idx"},
		noSeqScan:     []string{"artifact_catalog"},
	},
	{
		name:          "npm.reference.byObjectKey",
		build:         fixed(npmObjectHasVisibleReferenceQuery, func(hotQueryFixture) []any { return []any{"native/npm/sha256/" + strings.Repeat("0", 64)} }),
		indexContains: []string{"native_npm_versions_object_key_idx"},
		noSeqScan:     []string{"native_npm_versions"},
	},
}
