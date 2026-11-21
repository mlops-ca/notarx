// Package retention bounds every append-only or churning table with a throttled,
// leased janitor (ADR 0023 section 2).
//
// A registry lists each table with its terminal-row predicate, the time column the
// age is measured on and the default keep time of the standard and free-tier
// profiles. The Janitor deletes terminal rows older than the keep time in small
// batches (PostgreSQL: ctid with FOR UPDATE SKIP LOCKED; MySQL: ORDER BY LIMIT),
// sleeping between batches so the deletes never compete with request traffic. One
// node at a time runs a pass, elected through a lease row in scheduled_tasks.
//
// Tables that carry provenance or aggregates (artifact_promotions,
// artifact_usage_stats, artifact_intelligence) are deliberately absent from the
// registry; a test pins that. PostgreSQL audit tables that are partitioned are
// pruned by dropping partitions (internal/partitions), so their entries only run
// on MySQL.
package retention

import (
	"fmt"
	"regexp"
	"time"
)

const day = 24 * time.Hour

// DefaultBatch is the number of rows deleted per statement. MaxBatch bounds a batch size
// taken from a stored job so one statement can never hold locks and write WAL for an
// unbounded number of rows.
const (
	DefaultBatch = 2000
	MaxBatch     = 50000
)

// Entry describes how one class of rows of one table is pruned.
type Entry struct {
	// Table is an unquoted lower-case identifier; it is also the metric label and the
	// key of the NOTARX_RETENTION_<TABLE> override, which applies to every entry
	// of the table.
	Table string
	// Label distinguishes several entries of one table in logs; it defaults to Table.
	Label string
	// TimeColumn is the column the age is measured on; rows with a value older than
	// now minus the keep time are eligible. Rows where it is NULL are never deleted.
	TimeColumn string
	// Terminal is a SQL boolean expression, written against the table's own name,
	// that selects only rows that may be deleted ("TRUE" when every row may).
	Terminal string
	// KeyColumns is the primary key; the MySQL form deletes in key order.
	KeyColumns []string
	// Batch is the rows per statement; zero selects DefaultBatch.
	Batch int
	// Standard and FreeTier are the default keep times of the two profiles.
	Standard, FreeTier time.Duration
	// Drain lets the loop run past the run budget until the table is empty, for
	// tables whose backlog must not outlive a pass (user_sessions).
	Drain bool
	// PartitionedOnPostgres marks audit tables that PostgreSQL prunes by dropping
	// partitions; the janitor only runs the entry on MySQL.
	PartitionedOnPostgres bool
	// Optional marks tables created by a later ADR; the entry is skipped while the
	// table does not exist.
	Optional bool
}

// Name returns the label used in logs.
func (e Entry) Name() string {
	if e.Label != "" {
		return e.Label
	}
	return e.Table
}

// BatchSize returns the rows per statement.
func (e Entry) BatchSize() int {
	if e.Batch > 0 {
		return e.Batch
	}
	return DefaultBatch
}

var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// Validate rejects entries that could not be used safely in generated SQL.
func (e Entry) Validate() error {
	for _, name := range append([]string{e.Table, e.TimeColumn}, e.KeyColumns...) {
		if !identifier.MatchString(name) {
			return fmt.Errorf("retention: %q is not a lower-case identifier", name)
		}
	}
	if len(e.KeyColumns) == 0 {
		return fmt.Errorf("retention: %s has no key columns", e.Name())
	}
	if e.Terminal == "" {
		return fmt.Errorf("retention: %s has no terminal predicate", e.Name())
	}
	if e.Standard < 0 || e.FreeTier < 0 {
		return fmt.Errorf("retention: %s has a negative default keep time", e.Name())
	}
	return nil
}

// DefaultRegistry lists every table the janitor prunes.
// promotion_evaluations and import sessions are added by the slices that create them,
// because their columns are not part of the schema yet.
func DefaultRegistry() []Entry {
	return []Entry{
		{Table: "resolver_audit_log", TimeColumn: "occurred_at", Terminal: "TRUE", KeyColumns: []string{"id"}, Standard: 90 * day, FreeTier: 7 * day, PartitionedOnPostgres: true},
		{Table: "security_audit_log", TimeColumn: "occurred_at", Terminal: "TRUE", KeyColumns: []string{"id"}, Standard: 730 * day, FreeTier: 90 * day, PartitionedOnPostgres: true},
		{Table: "artifact_scan_history", TimeColumn: "scanned_at", Terminal: "TRUE", KeyColumns: []string{"scanned_at", "id"}, Standard: 180 * day, FreeTier: 180 * day, PartitionedOnPostgres: true},
		// The event log of ADR 0021. NOTARX_EVENTS_RETENTION is the documented key of the table; PostgreSQL drops
		// whole partitions (internal/partitions), MySQL deletes in batches.
		{Table: "domain_events", TimeColumn: "occurred_at", Terminal: "TRUE", KeyColumns: []string{"occurred_at", "seq"}, Standard: 14 * day, FreeTier: 3 * day, PartitionedOnPostgres: true},
		{Table: "repository_request_rollup", TimeColumn: "bucket_start", Terminal: "TRUE", KeyColumns: []string{"repository_id", "bucket_start", "outcome"}, Standard: 400 * day, FreeTier: 400 * day},
		{Table: "webhook_deliveries", Label: "webhook_deliveries_succeeded", TimeColumn: "updated_at", Terminal: "state = 'succeeded'", KeyColumns: []string{"id"}, Standard: 14 * day, FreeTier: 3 * day},
		// A dead delivery is inspectable and replayable while its domain event is kept (14 days, 3 on the free tier), so
		// it is pruned on the same schedule; a longer keep time would list rows whose event is gone.
		{Table: "webhook_deliveries", Label: "webhook_deliveries_dead", TimeColumn: "updated_at", Terminal: "state = 'dead'", KeyColumns: []string{"id"}, Standard: 14 * day, FreeTier: 3 * day},
		{Table: "lifecycle_jobs", Label: "lifecycle_jobs_done", TimeColumn: "completed_at", Terminal: "state IN ('completed','cancelled')", KeyColumns: []string{"id"}, Standard: 7 * day, FreeTier: 1 * day},
		{Table: "lifecycle_jobs", Label: "lifecycle_jobs_failed", TimeColumn: "completed_at", Terminal: "state = 'failed'", KeyColumns: []string{"id"}, Standard: 30 * day, FreeTier: 7 * day},
		{Table: "replication_plans", TimeColumn: "completed_at", Terminal: "state IN ('completed','failed','cancelled')", KeyColumns: []string{"id"}, Standard: 30 * day, FreeTier: 7 * day},
		// Checkpoints are deleted an hour after their plan completes; the plan row keeps
		// the high-water mark (digest, coordinate and state).
		{Table: "replication_checkpoints", TimeColumn: "updated_at", Terminal: "EXISTS (SELECT 1 FROM replication_plans p WHERE p.id = replication_checkpoints.plan_id AND p.state = 'completed')", KeyColumns: []string{"plan_id", "object_key"}, Standard: 1 * time.Hour, FreeTier: 1 * time.Hour},
		{Table: "cache_tasks", TimeColumn: "completed_at", Terminal: "completed_at IS NOT NULL", KeyColumns: []string{"id"}, Standard: 3 * day, FreeTier: 1 * day},
		{Table: "audit_cleanup_jobs", TimeColumn: "completed_at", Terminal: "state IN ('completed','failed')", KeyColumns: []string{"id"}, Standard: 30 * day, FreeTier: 7 * day},
		{Table: "scheduled_task_runs", TimeColumn: "created_at", Terminal: "state IN ('submitted','failed')", KeyColumns: []string{"id"}, Standard: 30 * day, FreeTier: 7 * day},
		{Table: "user_sessions", TimeColumn: "expires_at", Terminal: "TRUE", KeyColumns: []string{"id"}, Standard: 7 * day, FreeTier: 1 * day, Drain: true},
		{Table: "runtime_node_sessions", TimeColumn: "stopped_at", Terminal: "stopped_at IS NOT NULL", KeyColumns: []string{"session_id"}, Standard: 7 * day, FreeTier: 1 * day},
		{Table: "repository_quota_alert_events", TimeColumn: "created_at", Terminal: "TRUE", KeyColumns: []string{"id"}, Standard: 365 * day, FreeTier: 30 * day},
		// Alert events reference their delivery without a cascade, so a delivery is kept while an event points at it.
		{Table: "email_test_deliveries", TimeColumn: "updated_at", Terminal: "state IN ('accepted','dead') AND NOT EXISTS (SELECT 1 FROM repository_quota_alert_events e WHERE e.delivery_id = email_test_deliveries.id)", KeyColumns: []string{"id"}, Standard: 7 * day, FreeTier: 1 * day},
		{Table: "nexus_migration_entries", TimeColumn: "updated_at", Terminal: "EXISTS (SELECT 1 FROM nexus_migration_batches b WHERE b.id = nexus_migration_entries.batch_id AND b.state IN ('completed','failed','cancelled'))", KeyColumns: []string{"id"}, Standard: 30 * day, FreeTier: 7 * day},
		{Table: "hosted_repository_idempotency", TimeColumn: "expires_at", Terminal: "TRUE", KeyColumns: []string{"actor", "target", "key"}, Standard: 1 * day, FreeTier: 1 * day},
		{Table: "hosted_group_idempotency", TimeColumn: "expires_at", Terminal: "TRUE", KeyColumns: []string{"actor", "key"}, Standard: 1 * day, FreeTier: 1 * day},
		{Table: "native_apt_publication_idempotency", TimeColumn: "expires_at", Terminal: "TRUE", KeyColumns: []string{"actor", "target", "key"}, Standard: 1 * day, FreeTier: 1 * day},
		{Table: "native_maven_commit_idempotency", TimeColumn: "expires_at", Terminal: "TRUE", KeyColumns: []string{"session_id"}, Standard: 1 * day, FreeTier: 1 * day},
		{Table: "native_maven_publish_idempotency", TimeColumn: "expires_at", Terminal: "TRUE", KeyColumns: []string{"actor", "target", "key"}, Standard: 1 * day, FreeTier: 1 * day},
		// A single-use jti row is only needed until its token can no longer verify (expires_at is exp
		// plus the clock skew); the hour is a grace period, and the predicate keeps live rows out.
		{Table: "workload_jti_seen", TimeColumn: "expires_at", Terminal: "expires_at < now()", KeyColumns: []string{"issuer_id", "jti_hash"}, Standard: 1 * time.Hour, FreeTier: 1 * time.Hour},
		// SAML replay and request state (ADR 0018 section 3, ADR 0023 section 2). expires_at already
		// is NotOnOrAfter plus the clock skew for an assertion id and the request TTL for a request,
		// so a row is terminal once it is past expires_at. The keep time is the default clock skew
		// (NOTARX_SAML_CLOCK_SKEW, 60s) as the grace period, equal for both profiles.
		{Table: "saml_assertion_ids", TimeColumn: "expires_at", Terminal: "expires_at < now()", KeyColumns: []string{"provider_id", "assertion_id"}, Standard: time.Minute, FreeTier: time.Minute},
		// Hugging Face metadata (ADR 0027). A ref row is terminal once it is past expires_at; the keep
		// time is added to that, so a stale ref stays servable while upstream is down. An upstream commit
		// is terminal when no ref points at it and it is not one of the three newest commits of its
		// repository; its age is its last read. Hosted and promoted commits are content, never pruned.
		// The ranking sits in a derived table, which MySQL materializes, so the DELETE may read the table
		// it deletes from. It reads every commit row once per batch (hf_commits_repo_committed_idx serves the
		// ordering); the table holds only commits somebody read, and this entry is what keeps it bounded.
		{Table: "hf_refs", TimeColumn: "expires_at", Terminal: "TRUE", KeyColumns: []string{"hf_repo_id", "ref_kind", "ref_name"}, Standard: 30 * day, FreeTier: 7 * day},
		{Table: "hf_commits", TimeColumn: "last_read_at", Terminal: "source = 'upstream' AND NOT EXISTS (SELECT 1 FROM hf_refs r WHERE r.hf_repo_id = hf_commits.hf_repo_id AND r.commit_sha = hf_commits.commit_sha) AND hf_commits.id NOT IN (SELECT k.id FROM (SELECT c.id AS id, ROW_NUMBER() OVER (PARTITION BY c.hf_repo_id ORDER BY c.committed_at DESC, c.id DESC) AS rn FROM hf_commits c) k WHERE k.rn <= 3)", KeyColumns: []string{"id"}, Standard: 30 * day, FreeTier: 7 * day},
		{Table: "saml_request_state", TimeColumn: "expires_at", Terminal: "expires_at < now()", KeyColumns: []string{"id"}, Standard: time.Minute, FreeTier: time.Minute},
	}
}

// Provenance lists tables that must never be pruned: they hold provenance or
// aggregates, not logs.
var Provenance = []string{"artifact_promotions", "artifact_usage_stats", "artifact_intelligence"}
