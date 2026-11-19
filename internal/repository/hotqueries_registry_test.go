package repository

import (
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

// hotQueries is the registry. Add the next query when a hot statement appears in
// pg_stat_statements; the registry test rejects duplicates and bad placeholders.
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
		indexContains: []string{"native_npm_versions_pkey"},
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
		indexContains: []string{"hosted_repositories_name_key"},
		noSeqScan:     []string{"hosted_repositories"},
	},
	{
		name:          "repository.keysetPage",
		build:         fixed(listHostedRepositoriesQuery, func(f hotQueryFixture) []any { return []any{f.repositoryID, 51} }),
		indexContains: []string{"hosted_repositories_pkey"},
		noSeqScan:     []string{"hosted_repositories"},
	},
	{
		name:          "lifecycle.claimByKind",
		build:         fixed(claimLifecycleCandidatesByKindQuery, func(hotQueryFixture) []any { return []any{string(LifecycleJobKind("scan")), "", []string{}, 10} }),
		indexContains: []string{"lifecycle_jobs_kind_actionable_idx"},
		noSeqScan:     []string{"lifecycle_jobs"},
	},
	{
		name:          "lifecycle.claimAnyKind",
		build:         fixed(claimLifecycleCandidatesAnyKindQuery, func(hotQueryFixture) []any { return []any{"", []string{}, 10} }),
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
}
