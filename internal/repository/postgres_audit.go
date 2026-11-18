package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"github.com/mlops-ca/notarx/internal/requestcontext"
)

func (s *PostgresStore) RecordAudit(ctx context.Context, audit AuditRecord) error {
	if !audit.IsArtifactDownload() {
		return insertAudit(ctx, s.db, audit)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

type auditExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertAudit(ctx context.Context, execer auditExecer, audit AuditRecord) error {
	if ids, ok := requestcontext.FromContext(ctx); ok {
		if audit.RequestID == "" {
			audit.RequestID = ids.RequestID
		}
		if audit.TraceID == "" {
			audit.TraceID = ids.TraceID
		}
	}
	if audit.OccurredAt.IsZero() {
		audit.OccurredAt = time.Now().UTC()
	}
	evidence := []byte("{}")
	if len(audit.Evidence) > 0 {
		encoded, err := json.Marshal(audit.Evidence)
		if err != nil {
			return err
		}
		evidence = encoded
	}
	_, err := execer.ExecContext(ctx, `INSERT INTO `+auditTable(audit.Class)+` (group_name, repository, member_name, outcome, actor, occurred_at, format, resource, representation, member_type, upstream_host, operation, http_status, cache_disposition, bytes, authorization_source, authorization_reason, request_id, trace_id, evidence) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20::jsonb)`, audit.GroupName, audit.Repository, audit.MemberName, audit.Outcome, audit.Actor, audit.OccurredAt, audit.Format, audit.Resource, audit.Representation, audit.MemberType, audit.UpstreamHost, audit.Operation, audit.Status, audit.CacheDisposition, audit.Bytes, audit.AuthorizationSource, audit.AuthorizationReason, audit.RequestID, audit.TraceID, evidence)
	if err != nil {
		return err
	}
	if audit.IsArtifactDownload() {
		// Keep the aggregate and audit in the caller's transaction so a
		// download cannot leave a durable audit but missing retention evidence.
		increment := audit.UsageIncrement()
		_, err = execer.ExecContext(ctx, artifactUsageUpsertSQL,
			increment.Repository, increment.Format, increment.Resource,
			increment.TotalBytes, increment.LastDownloadedAt, increment.LastActor)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) ListAudits(ctx context.Context, query AuditQuery) ([]AuditRecord, error) {
	page, err := s.ListAuditPage(ctx, query)
	return page.Items, err
}

func (s *PostgresStore) ListRepositoryRequestStatistics(ctx context.Context, now time.Time) ([]RepositoryRequestStatistics, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repository,
		COUNT(*) FILTER (WHERE occurred_at >= $1::timestamptz - INTERVAL '1 day'),
		COUNT(*) FILTER (WHERE occurred_at >= $1::timestamptz - INTERVAL '7 days'),
		COUNT(*),
		COUNT(*) FILTER (WHERE outcome IN ('access_denied', 'proxy_denied', 'denied') AND occurred_at >= $1::timestamptz - INTERVAL '1 day'),
		COUNT(*) FILTER (WHERE outcome IN ('access_denied', 'proxy_denied', 'denied') AND occurred_at >= $1::timestamptz - INTERVAL '7 days'),
		COUNT(*) FILTER (WHERE outcome IN ('access_denied', 'proxy_denied', 'denied'))
		FROM (
		  SELECT repository, format, outcome, occurred_at FROM resolver_audit_log
		  WHERE occurred_at >= $1::timestamptz - INTERVAL '30 days' AND occurred_at <= $1
		  UNION ALL
		  SELECT repository, format, outcome, occurred_at FROM security_audit_log
		  WHERE occurred_at >= $1::timestamptz - INTERVAL '30 days' AND occurred_at <= $1
		) audit
		WHERE repository <> '' AND format IS DISTINCT FROM 'management'
		GROUP BY repository ORDER BY repository`, now.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []RepositoryRequestStatistics{}
	for rows.Next() {
		var item RepositoryRequestStatistics
		if err := rows.Scan(&item.Repository,
			&item.Requests.OneDay, &item.Requests.SevenDays, &item.Requests.ThirtyDays,
			&item.Denied.OneDay, &item.Denied.SevenDays, &item.Denied.ThirtyDays); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// auditTable maps a durability class to its table. Only these two constants are
// ever interpolated into SQL.
func auditTable(class AuditClass) string {
	if class == AuditClassSync {
		return "security_audit_log"
	}
	return "resolver_audit_log"
}

// auditClassWindow is the default lower time bound of a page that supplies no
// From: the table's default retention, so older rows no longer exist and the bound
// only lets the planner prune partitions (ADR 0023 section 2).
func auditClassWindow(class AuditClass) time.Duration {
	if class == AuditClassSync {
		return 730 * 24 * time.Hour
	}
	return 90 * 24 * time.Hour
}

const auditPageColumns = `id, group_name, repository, member_name, outcome, actor, occurred_at,
		COALESCE(format, ''), COALESCE(resource, ''), COALESCE(representation, ''), COALESCE(member_type, ''), COALESCE(upstream_host, ''), COALESCE(operation, ''),
		COALESCE(http_status, 0), COALESCE(cache_disposition, ''), COALESCE(bytes, 0), COALESCE(authorization_source, ''), COALESCE(authorization_reason, ''), COALESCE(request_id, ''), COALESCE(trace_id, ''), COALESCE(evidence, '{}'::jsonb)`

// buildAuditPageQuery assembles the page statement from the filters that were
// supplied only, so every predicate can use an index and partition pruning
// applies. Each requested table contributes one ordered, limited branch; two
// branches are merged by (occurred_at, id, class rank), which is the keyset.
func buildAuditPageQuery(query AuditQuery, now time.Time) (string, []any, int) {
	limit := query.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var args []any
	add := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}
	var conditions []string
	for _, filter := range []struct {
		column, value string
	}{
		{"group_name", query.GroupName}, {"repository", query.Repository}, {"outcome", query.Outcome},
		{"format", query.Format}, {"operation", query.Operation}, {"actor", query.Actor},
	} {
		if filter.value != "" {
			conditions = append(conditions, filter.column+" = "+add(filter.value))
		}
	}
	if !query.To.IsZero() {
		conditions = append(conditions, "occurred_at <= "+add(query.To))
	}
	var before, beforeID string
	if !query.Before.OccurredAt.IsZero() {
		before, beforeID = add(query.Before.OccurredAt), add(query.Before.ID)
		// The plain bound lets the planner prune; the row comparison is the keyset.
		conditions = append(conditions, "occurred_at <= "+before)
	}
	var explicitFrom string
	if !query.From.IsZero() {
		explicitFrom = add(query.From)
	}
	classes := []AuditClass{AuditClassSync, AuditClassAsync}
	if query.Class.Valid() {
		classes = []AuditClass{query.Class}
	}
	limitPlaceholder := add(limit + 1)
	branches := make([]string, 0, len(classes))
	for _, class := range classes {
		where := append([]string(nil), conditions...)
		from := explicitFrom
		if from == "" {
			from = add(now.Add(-auditClassWindow(class)))
		}
		where = append(where, "occurred_at >= "+from)
		if before != "" {
			// A record of a lower class rank may share the cursor's (occurred_at, id).
			operator := "<"
			if class.rank() < query.Before.Class.rank() {
				operator = "<="
			}
			where = append(where, "(occurred_at, id) "+operator+" ("+before+", "+beforeID+")")
		}
		branch := "SELECT '" + string(class) + "'::text AS audit_class, " + strconv.Itoa(class.rank()) + " AS class_rank, " + auditPageColumns +
			" FROM " + auditTable(class) + " WHERE " + joinAnd(where) +
			" ORDER BY occurred_at DESC, id DESC LIMIT " + limitPlaceholder
		branches = append(branches, "("+branch+")")
	}
	if len(branches) == 1 {
		return branches[0], args, limit
	}
	return "SELECT * FROM (" + branches[0] + " UNION ALL " + branches[1] + ") merged ORDER BY occurred_at DESC, id DESC, class_rank DESC LIMIT " + limitPlaceholder, args, limit
}

func joinAnd(conditions []string) string {
	result := conditions[0]
	for _, condition := range conditions[1:] {
		result += " AND " + condition
	}
	return result
}

// ListAuditPage returns one keyset page, newest first, merged across
// security_audit_log and resolver_audit_log unless query.Class selects one.
func (s *PostgresStore) ListAuditPage(ctx context.Context, query AuditQuery) (AuditPage, error) {
	statement, args, limit := buildAuditPageQuery(query, time.Now().UTC())
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return AuditPage{}, err
	}
	defer func() { _ = rows.Close() }()
	page := AuditPage{Items: make([]AuditRecord, 0, limit)}
	for rows.Next() {
		var audit AuditRecord
		var class string
		var rank int
		var evidence []byte
		if err := rows.Scan(&class, &rank, &audit.ID, &audit.GroupName, &audit.Repository, &audit.MemberName, &audit.Outcome, &audit.Actor, &audit.OccurredAt, &audit.Format, &audit.Resource, &audit.Representation, &audit.MemberType, &audit.UpstreamHost, &audit.Operation, &audit.Status, &audit.CacheDisposition, &audit.Bytes, &audit.AuthorizationSource, &audit.AuthorizationReason, &audit.RequestID, &audit.TraceID, &evidence); err != nil {
			return AuditPage{}, err
		}
		audit.Class = AuditClass(class)
		if err := json.Unmarshal(evidence, &audit.Evidence); err != nil {
			return AuditPage{}, err
		}
		page.Items = append(page.Items, audit)
	}
	if err := rows.Err(); err != nil {
		return AuditPage{}, err
	}
	if len(page.Items) > limit {
		last := page.Items[limit-1]
		page.Items = page.Items[:limit]
		page.Next = &AuditCursor{OccurredAt: last.OccurredAt, ID: last.ID, Class: last.Class}
	}
	return page, nil
}
