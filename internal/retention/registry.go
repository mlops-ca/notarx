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

// DefaultBatch is the number of rows deleted per statement.
const DefaultBatch = 2000

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
//
// Tables of ADR 0018 (saml_*), ADR 0019 (workload_jti_seen) are optional until
// their migrations land. promotion_evaluations, domain_events, import sessions
// and the hf_* tables are added by the slices that create them, because their
// columns are not part of the schema yet.
func DefaultRegistry() []Entry {
	return []Entry{
		{Table: "resolver_audit_log", TimeColumn: "occurred_at", Terminal: "TRUE", KeyColumns: []string{"id"}, Standard: 90 * day, FreeTier: 7 * day, PartitionedOnPostgres: true},
		{Table: "security_audit_log", TimeColumn: "occurred_at", Terminal: "TRUE", KeyColumns: []string{"id"}, Standard: 730 * day, FreeTier: 90 * day, PartitionedOnPostgres: true},
		{Table: "repository_request_rollup", TimeColumn: "bucket_start", Terminal: "TRUE", KeyColumns: []string{"repository_id", "bucket_start", "outcome"}, Standard: 400 * day, FreeTier: 400 * day},
		{Table: "webhook_deliveries", Label: "webhook_deliveries_succeeded", TimeColumn: "updated_at", Terminal: "state = 'succeeded'", KeyColumns: []string{"id"}, Standard: 14 * day, FreeTier: 3 * day},
		{Table: "webhook_deliveries", Label: "webhook_deliveries_dead", TimeColumn: "updated_at", Terminal: "state = 'dead'", KeyColumns: []string{"id"}, Standard: 90 * day, FreeTier: 30 * day},
		{Table: "lifecycle_jobs", Label: "lifecycle_jobs_done", TimeColumn: "completed_at", Terminal: "state IN ('completed','cancelled')", KeyColumns: []string{"id"}, Standard: 7 * day, FreeTier: 1 * day},
		{Table: "lifecycle_jobs", Label: "lifecycle_jobs_failed", TimeColumn: "completed_at", Terminal: "state = 'failed'", KeyColumns: []string{"id"}, Standard: 30 * day, FreeTier: 7 * day},
		{Table: "replication_plans", TimeColumn: "completed_at", Terminal: "state IN ('completed','failed','cancelled')", KeyColumns: []string{"id"}, Standard: 30 * day, FreeTier: 7 * day},
		// Checkpoints are deleted when their plan completes; the plan row keeps the
		// high-water mark (digest, coordinate and state).
		{Table: "replication_checkpoints", TimeColumn: "updated_at", Terminal: "EXISTS (SELECT 1 FROM replication_plans p WHERE p.id = replication_checkpoints.plan_id AND p.state = 'completed')", KeyColumns: []string{"plan_id", "object_key"}},
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
		{Table: "workload_jti_seen", TimeColumn: "expires_at", Terminal: "TRUE", KeyColumns: []string{"jti"}, Optional: true},
		{Table: "saml_assertion_ids", TimeColumn: "expires_at", Terminal: "TRUE", KeyColumns: []string{"assertion_id"}, Optional: true},
		{Table: "saml_request_state", TimeColumn: "expires_at", Terminal: "TRUE", KeyColumns: []string{"request_id"}, Optional: true},
	}
}

// Provenance lists tables that must never be pruned: they hold provenance or
// aggregates, not logs.
var Provenance = []string{"artifact_promotions", "artifact_usage_stats", "artifact_intelligence"}
