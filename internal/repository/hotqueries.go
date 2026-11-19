package repository

// HotQuery is one entry of the plan-regression registry of ADR 0023 section 11: a
// statement the request path runs often, the index its plan must use and the tables
// on which it must never fall back to a sequential scan. The registry is curated from
// pg_stat_statements on the target profile and executed by make explain-check.
type HotQuery struct {
	// Name identifies the query in test output; it is unique.
	Name string
	// SQL is the statement exactly as the store runs it.
	SQL string
	// Args builds the statement arguments from the seeded fixture.
	Args func(HotQueryFixture) []any
	// IndexContains, when set, must be a substring of an index name in the plan.
	// Leave it empty where the planner may pick between equivalent indexes (for
	// example partitioned audit tables, whose partition indexes carry their own names).
	IndexContains string
	// NoSeqScan lists the tables on which the plan must not contain a Seq Scan.
	NoSeqScan []string
}

// HotQueryFixture names rows that the datagen seed always produces.
type HotQueryFixture struct {
	RepositoryID   string
	RepositoryName string
	PackageName    string
	Version        string
	Digest         string
	Actor          string
}

// HotQueries is the registry. Add the next query here when a hot statement appears in
// pg_stat_statements; the registry test fails on duplicate names and empty entries.
var HotQueries = []HotQuery{
	{
		Name: "npm.package.get",
		SQL: `SELECT repository_id::text,name,dist_tags,source_endpoint,upstream_etag,upstream_modified,
		       metadata_expires_at,negative_expires_at,negative,created_at,updated_at,revision,source_version
		FROM native_npm_packages WHERE repository_id=$1::uuid AND name=$2`,
		Args:          func(f HotQueryFixture) []any { return []any{f.RepositoryID, f.PackageName} },
		IndexContains: "native_npm_packages_pkey",
		NoSeqScan:     []string{"native_npm_packages"},
	},
	{
		Name:      "npm.package.namePrefix",
		SQL:       `SELECT name FROM native_npm_packages WHERE repository_id=$1::uuid AND name LIKE $2 ORDER BY name LIMIT 50`,
		Args:      func(f HotQueryFixture) []any { return []any{f.RepositoryID, "perf-pkg-0000%"} },
		NoSeqScan: []string{"native_npm_packages"},
	},
	{
		Name: "npm.versions.list",
		SQL: `SELECT repository_id::text,package_name,version,digest,integrity,shasum,
		       tarball_name,upstream_tarball,object_key,size,manifest,publisher,state,cached_at,deleted_at,collected_at,created_at,deprecated,collecting_at
		FROM native_npm_versions
		WHERE repository_id=$1::uuid AND package_name=$2 AND state='visible'
		ORDER BY created_at DESC,version DESC`,
		Args:          func(f HotQueryFixture) []any { return []any{f.RepositoryID, f.PackageName} },
		IndexContains: "native_npm_versions_pkey",
		NoSeqScan:     []string{"native_npm_versions"},
	},
	{
		Name: "npm.version.get",
		SQL: `SELECT repository_id::text,package_name,version,digest,integrity,shasum,
		       tarball_name,upstream_tarball,object_key,size,manifest,publisher,state,cached_at,deleted_at,collected_at,created_at,deprecated,collecting_at
		FROM native_npm_versions
		WHERE repository_id=$1::uuid AND package_name=$2 AND version=$3 AND state='visible'`,
		Args:          func(f HotQueryFixture) []any { return []any{f.RepositoryID, f.PackageName, f.Version} },
		IndexContains: "native_npm_versions_pkey",
		NoSeqScan:     []string{"native_npm_versions"},
	},
	{
		Name: "npm.version.tarball",
		SQL:  `SELECT object_key FROM native_npm_versions WHERE repository_id=$1::uuid AND package_name=$2 AND tarball_name=$3`,
		Args: func(f HotQueryFixture) []any {
			return []any{f.RepositoryID, f.PackageName, f.PackageName + "-" + f.Version + ".tgz"}
		},
		IndexContains: "native_npm_versions_repository_id_package_name_tarball_name_key",
		NoSeqScan:     []string{"native_npm_versions"},
	},
	{
		Name:          "repository.byName",
		SQL:           `SELECT id::text,format,state FROM hosted_repositories WHERE name=$1`,
		Args:          func(f HotQueryFixture) []any { return []any{f.RepositoryName} },
		IndexContains: "hosted_repositories_name_key",
		NoSeqScan:     []string{"hosted_repositories"},
	},
	{
		Name:          "repository.keysetPage",
		SQL:           `SELECT id::text,name FROM hosted_repositories WHERE (created_at,id) > ($1::timestamptz,$2::uuid) ORDER BY created_at,id LIMIT 50`,
		Args:          func(f HotQueryFixture) []any { return []any{"2000-01-01T00:00:00Z", f.RepositoryID} },
		IndexContains: "hosted_repositories_created_at_idx",
		NoSeqScan:     []string{"hosted_repositories"},
	},
	{
		Name: "audit.byRepository",
		SQL: `SELECT id,occurred_at,actor,outcome FROM resolver_audit_log
		WHERE repository=$1 AND occurred_at >= $2::timestamptz ORDER BY occurred_at DESC, id DESC LIMIT 50`,
		Args:      func(f HotQueryFixture) []any { return []any{f.RepositoryName, "2000-01-01T00:00:00Z"} },
		NoSeqScan: []string{"resolver_audit_log", "resolver_audit_log_default"},
	},
	{
		Name: "audit.byActor",
		SQL: `SELECT id,occurred_at,repository,outcome FROM resolver_audit_log
		WHERE actor=$1 AND occurred_at >= $2::timestamptz ORDER BY occurred_at DESC, id DESC LIMIT 50`,
		Args:      func(f HotQueryFixture) []any { return []any{f.Actor, "2000-01-01T00:00:00Z"} },
		NoSeqScan: []string{"resolver_audit_log", "resolver_audit_log_default"},
	},
	{
		Name: "audit.keysetPage",
		SQL: `SELECT id,occurred_at,repository,actor,outcome FROM resolver_audit_log
		WHERE (occurred_at,id) < ($1::timestamptz,$2::bigint) ORDER BY occurred_at DESC, id DESC LIMIT 50`,
		Args:      func(HotQueryFixture) []any { return []any{"2100-01-01T00:00:00Z", int64(1 << 62)} },
		NoSeqScan: []string{"resolver_audit_log", "resolver_audit_log_default"},
	},
	{
		Name:      "lifecycle.pendingClaim",
		SQL:       `SELECT id::text FROM lifecycle_jobs WHERE state='pending' ORDER BY created_at LIMIT 10`,
		Args:      func(HotQueryFixture) []any { return nil },
		NoSeqScan: []string{"lifecycle_jobs"},
	},
}
