package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mlops-ca/notarx/internal/requestcontext"
)

// RecordAudit writes one audit record. With an installed recorder (the audit
// writer) it delegates; without one the record is committed directly, together with
// its usage aggregate and request rollup in one transaction.
func (s *PostgresStore) RecordAudit(ctx context.Context, audit AuditRecord) error {
	if recorder := s.auditRecorder.Load(); recorder != nil {
		return (*recorder).RecordAudit(ctx, audit)
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

// UsageBatchOf is the usage and rollup change one record contributes.
func UsageBatchOf(audit AuditRecord) UsageBatch {
	var batch UsageBatch
	if audit.IsArtifactDownload() {
		inc := audit.UsageIncrement()
		batch.Usage = append(batch.Usage, UsageDelta{Repository: inc.Repository, Format: inc.Format, Resource: inc.Resource,
			Downloads: 1, Bytes: inc.TotalBytes, First: inc.LastDownloadedAt, Last: inc.LastDownloadedAt, LastActor: inc.LastActor})
	}
	if audit.CountsAsRequest() {
		at := audit.OccurredAt
		if at.IsZero() {
			at = time.Now().UTC()
		}
		batch.Rollups = append(batch.Rollups, RollupDelta{Repository: audit.Repository, BucketStart: at.UTC().Truncate(time.Hour),
			Outcome: audit.RequestOutcome(), Requests: 1, Bytes: audit.Bytes})
	}
	return batch
}

// InsertAudit commits one audit row to the table of its class, without usage.
func (s *PostgresStore) InsertAudit(ctx context.Context, audit AuditRecord) error {
	return insertAuditRow(ctx, s.db, withAuditDefaults(ctx, audit))
}

type auditExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// withAuditDefaults fills the request ids and the timestamp the caller left empty.
// The class is not inferred here: the audit writer sets it from Durability and
// insertAudit does the same for audits written inside a store transaction.
func withAuditDefaults(ctx context.Context, audit AuditRecord) AuditRecord {
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
	return audit.Sanitized()
}

// SanitizeAuditText makes client-controlled text storable: PostgreSQL rejects a NUL
// byte and invalid UTF-8 in text columns (SQLSTATE 22021) and U+0000 in jsonb, so
// both become U+FFFD. Clean text is returned unchanged without allocating.
func SanitizeAuditText(text string) string {
	if utf8.ValidString(text) && !strings.ContainsRune(text, 0) {
		return text
	}
	return strings.Map(func(r rune) rune {
		if r == 0 {
			return utf8.RuneError
		}
		return r
	}, strings.ToValidUTF8(text, string(utf8.RuneError)))
}

// Sanitized returns the record with every text field, the evidence keys and values
// included, made storable, so a hostile request path cannot make the record vanish.
func (a AuditRecord) Sanitized() AuditRecord {
	for _, field := range []*string{&a.GroupName, &a.Repository, &a.MemberName, &a.Actor, &a.Format, &a.Resource,
		&a.Representation, &a.MemberType, &a.UpstreamHost, &a.Operation, &a.CacheDisposition,
		&a.AuthorizationSource, &a.AuthorizationReason, &a.RequestID, &a.TraceID} {
		*field = SanitizeAuditText(*field)
	}
	a.Outcome = AuditOutcome(SanitizeAuditText(string(a.Outcome)))
	if len(a.Evidence) > 0 {
		clean := make(map[string]string, len(a.Evidence))
		for key, value := range a.Evidence {
			clean[SanitizeAuditText(key)] = SanitizeAuditText(value)
		}
		a.Evidence = clean
	}
	return a
}

// insertAudit writes the audit row and, in the same transaction when execer is one,
// the usage aggregate and request rollup the record contributes. Store methods
// that audit a mutation inside their own transaction call it.
func insertAudit(ctx context.Context, execer auditExecer, audit AuditRecord) error {
	audit = withAuditDefaults(ctx, audit)
	// ADR 0023 section 1: mutations are sync class and land in security_audit_log.
	audit.Class = audit.Durability()
	if err := insertAuditRow(ctx, execer, audit); err != nil {
		return err
	}
	return flushUsage(ctx, execer, UsageBatchOf(audit))
}

func insertAuditRow(ctx context.Context, execer auditExecer, audit AuditRecord) error {
	evidence := []byte("{}")
	if len(audit.Evidence) > 0 {
		encoded, err := json.Marshal(audit.Evidence)
		if err != nil {
			return err
		}
		evidence = encoded
	}
	_, err := execer.ExecContext(ctx, `INSERT INTO `+AuditTable(audit.Class)+` (group_name, repository, member_name, outcome, actor, occurred_at, format, resource, representation, member_type, upstream_host, operation, http_status, cache_disposition, bytes, authorization_source, authorization_reason, request_id, trace_id, evidence) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20::jsonb)`, audit.GroupName, audit.Repository, audit.MemberName, audit.Outcome, audit.Actor, audit.OccurredAt, audit.Format, audit.Resource, audit.Representation, audit.MemberType, audit.UpstreamHost, audit.Operation, audit.Status, audit.CacheDisposition, audit.Bytes, audit.AuthorizationSource, audit.AuthorizationReason, audit.RequestID, audit.TraceID, evidence)
	return err
}

func (s *PostgresStore) ListAudits(ctx context.Context, query AuditQuery) ([]AuditRecord, error) {
	page, err := s.ListAuditPage(ctx, query)
	return page.Items, err
}

// ListRepositoryRequestStatistics reads the hourly request rollups (ADR 0023 section 1)
// instead of scanning 30 days of audit. Windows start at the hour that contains the
// rolling boundary, so counts are exact to the hour; a request is allowed or one of
// the denial reasons, and denied is every outcome but allowed.
func (s *PostgresStore) ListRepositoryRequestStatistics(ctx context.Context, now time.Time) ([]RepositoryRequestStatistics, error) {
	now = now.UTC()
	hour := func(d time.Duration) time.Time { return now.Add(-d).Truncate(time.Hour) }
	rows, err := s.db.QueryContext(ctx, `SELECT n.name,
		COALESCE(SUM(r.requests) FILTER (WHERE r.bucket_start >= $2), 0),
		COALESCE(SUM(r.requests) FILTER (WHERE r.bucket_start >= $3), 0),
		COALESCE(SUM(r.requests), 0),
		COALESCE(SUM(r.requests) FILTER (WHERE r.outcome <> 0 AND r.bucket_start >= $2), 0),
		COALESCE(SUM(r.requests) FILTER (WHERE r.outcome <> 0 AND r.bucket_start >= $3), 0),
		COALESCE(SUM(r.requests) FILTER (WHERE r.outcome <> 0), 0)
		FROM repository_request_rollup r
		JOIN (`+repositoryNames+`) n ON n.id = r.repository_id
		WHERE r.bucket_start >= $4 AND r.bucket_start <= $1
		GROUP BY n.name ORDER BY n.name`, now, hour(24*time.Hour), hour(7*24*time.Hour), hour(30*24*time.Hour))
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
// AuditTable is the audit table a record of the class is stored in.
func AuditTable(class AuditClass) string {
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
			" FROM " + AuditTable(class) + " WHERE " + joinAnd(where) +
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
