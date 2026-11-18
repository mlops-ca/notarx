package repository

import (
	"context"
	"encoding/json"
	"sort"
	"time"
)

// repositoryNames maps a repository or group name to its surrogate id. Audit,
// usage and rollup records carry the name; the aggregates are keyed by id.
const repositoryNames = `SELECT id, name FROM hosted_repositories UNION ALL SELECT id, name FROM hosted_groups`

// RequestRollupRetention is how long hourly request rollups are kept.
const RequestRollupRetention = 400 * 24 * time.Hour

// InsertAuditBatch writes records with one INSERT ... SELECT unnest(...) per table
// (the sync class is routed to security_audit_log, async to resolver_audit_log).
func (s *PostgresStore) InsertAuditBatch(ctx context.Context, records []AuditRecord) error {
	if len(records) == 0 {
		return nil
	}
	var byClass [2][]AuditRecord
	for _, record := range records {
		record = withAuditDefaults(ctx, record)
		byClass[record.Class.rank()] = append(byClass[record.Class.rank()], record)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for rank, class := range []AuditClass{AuditClassAsync, AuditClassSync} {
		if len(byClass[rank]) == 0 {
			continue
		}
		if err := insertAuditUnnest(ctx, tx, class, byClass[rank]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func insertAuditUnnest(ctx context.Context, execer auditExecer, class AuditClass, records []AuditRecord) error {
	n := len(records)
	var (
		group, repo, member, outcome, actor                        = make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
		occurred                                                   = make([]time.Time, n)
		format, resource, representation, memberType, upstream, op = make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
		status                                                     = make([]int32, n)
		cache, source, reason, requestID, traceID, evidence        = make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
		size                                                       = make([]int64, n)
	)
	for i, r := range records {
		group[i], repo[i], member[i], outcome[i], actor[i] = r.GroupName, r.Repository, r.MemberName, string(r.Outcome), r.Actor
		occurred[i] = r.OccurredAt
		format[i], resource[i], representation[i], memberType[i], upstream[i], op[i] = r.Format, r.Resource, r.Representation, r.MemberType, r.UpstreamHost, r.Operation
		status[i] = int32(r.Status)
		cache[i], source[i], reason[i], requestID[i], traceID[i] = r.CacheDisposition, r.AuthorizationSource, r.AuthorizationReason, r.RequestID, r.TraceID
		size[i] = r.Bytes
		evidence[i] = "{}"
		if len(r.Evidence) > 0 {
			encoded, err := json.Marshal(r.Evidence)
			if err != nil {
				return err
			}
			evidence[i] = string(encoded)
		}
	}
	_, err := execer.ExecContext(ctx, `INSERT INTO `+AuditTable(class)+` (group_name, repository, member_name, outcome, actor, occurred_at, format, resource, representation, member_type, upstream_host, operation, http_status, cache_disposition, bytes, authorization_source, authorization_reason, request_id, trace_id, evidence)
SELECT group_name, repository, member_name, outcome, actor, occurred_at, format, resource, representation, member_type, upstream_host, operation, http_status, cache_disposition, bytes, authorization_source, authorization_reason, request_id, trace_id, evidence::jsonb
FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::timestamptz[], $7::text[], $8::text[], $9::text[], $10::text[], $11::text[], $12::text[], $13::int4[], $14::text[], $15::int8[], $16::text[], $17::text[], $18::text[], $19::text[], $20::text[])
  AS u(group_name, repository, member_name, outcome, actor, occurred_at, format, resource, representation, member_type, upstream_host, operation, http_status, cache_disposition, bytes, authorization_source, authorization_reason, request_id, trace_id, evidence)`,
		group, repo, member, outcome, actor, occurred, format, resource, representation, memberType, upstream, op, status, cache, size, source, reason, requestID, traceID, evidence)
	return err
}

// FlushUsage adds the aggregated deltas in one transaction. Keys are applied in
// (repository id, format, resource) order, so two nodes flushing the same hot keys
// take their row locks in the same order and cannot deadlock.
func (s *PostgresStore) FlushUsage(ctx context.Context, batch UsageBatch) error {
	if len(batch.Usage) == 0 && len(batch.Rollups) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := flushUsage(ctx, tx, batch); err != nil {
		return err
	}
	return tx.Commit()
}

const usageFlushSQL = `INSERT INTO artifact_usage_stats
	(repository_id, format, resource, download_count, total_bytes, first_downloaded_at, last_downloaded_at, last_actor)
SELECT n.id, u.format, u.resource, u.downloads, u.bytes, u.first_at, u.last_at, u.last_actor
FROM unnest($1::text[], $2::text[], $3::text[], $4::int8[], $5::int8[], $6::timestamptz[], $7::timestamptz[], $8::text[])
  AS u(repository, format, resource, downloads, bytes, first_at, last_at, last_actor)
JOIN (` + repositoryNames + `) n ON n.name = u.repository
ORDER BY n.id, u.format, u.resource
ON CONFLICT (repository_id, format, resource) DO UPDATE SET
	download_count = artifact_usage_stats.download_count + EXCLUDED.download_count,
	total_bytes = artifact_usage_stats.total_bytes + EXCLUDED.total_bytes,
	first_downloaded_at = LEAST(artifact_usage_stats.first_downloaded_at, EXCLUDED.first_downloaded_at),
	last_downloaded_at = GREATEST(artifact_usage_stats.last_downloaded_at, EXCLUDED.last_downloaded_at),
	last_actor = CASE WHEN EXCLUDED.last_downloaded_at >= artifact_usage_stats.last_downloaded_at
		THEN EXCLUDED.last_actor ELSE artifact_usage_stats.last_actor END`

const rollupFlushSQL = `INSERT INTO repository_request_rollup (repository_id, bucket_start, outcome, requests, bytes)
SELECT n.id, r.bucket_start, r.outcome, r.requests, r.bytes
FROM unnest($1::text[], $2::timestamptz[], $3::int2[], $4::int8[], $5::int8[])
  AS r(repository, bucket_start, outcome, requests, bytes)
JOIN (` + repositoryNames + `) n ON n.name = r.repository
ORDER BY n.id, r.bucket_start, r.outcome
ON CONFLICT (repository_id, bucket_start, outcome) DO UPDATE SET
	requests = repository_request_rollup.requests + EXCLUDED.requests,
	bytes = repository_request_rollup.bytes + EXCLUDED.bytes`

func flushUsage(ctx context.Context, execer auditExecer, batch UsageBatch) error {
	if n := len(batch.Usage); n > 0 {
		usage := sanitizedUsage(batch.Usage)
		n = len(usage)
		sort.Slice(usage, func(i, j int) bool {
			if usage[i].Repository != usage[j].Repository {
				return usage[i].Repository < usage[j].Repository
			}
			if usage[i].Format != usage[j].Format {
				return usage[i].Format < usage[j].Format
			}
			return usage[i].Resource < usage[j].Resource
		})
		repo, format, resource, actor := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
		downloads, size := make([]int64, n), make([]int64, n)
		first, last := make([]time.Time, n), make([]time.Time, n)
		for i, u := range usage {
			repo[i], format[i], resource[i], actor[i] = u.Repository, u.Format, u.Resource, u.LastActor
			downloads[i], size[i] = u.Downloads, u.Bytes
			first[i], last[i] = u.First, u.Last
			if first[i].IsZero() {
				first[i] = time.Now().UTC()
			}
			if last[i].IsZero() {
				last[i] = first[i]
			}
		}
		if _, err := execer.ExecContext(ctx, usageFlushSQL, repo, format, resource, downloads, size, first, last, actor); err != nil {
			return err
		}
	}
	if rollups := sanitizedRollups(batch.Rollups); len(rollups) > 0 {
		n := len(rollups)
		repo, bucket, outcome := make([]string, n), make([]time.Time, n), make([]int16, n)
		requests, size := make([]int64, n), make([]int64, n)
		for i, r := range rollups {
			repo[i], bucket[i], outcome[i], requests[i], size[i] = r.Repository, r.BucketStart, int16(r.Outcome), r.Requests, r.Bytes
		}
		if _, err := execer.ExecContext(ctx, rollupFlushSQL, repo, bucket, outcome, requests, size); err != nil {
			return err
		}
	}
	return nil
}

// sanitizedUsage rewrites hostile text in the keys, then merges deltas that became
// equal, so one INSERT ... ON CONFLICT never sees a key twice and the sort that
// fixes the lock order runs on the strings the database receives.
func sanitizedUsage(in []UsageDelta) []UsageDelta {
	merged := make(map[[3]string]*UsageDelta, len(in))
	out := make([]UsageDelta, 0, len(in))
	for _, u := range in {
		u.Repository, u.Format, u.Resource, u.LastActor = SanitizeAuditText(u.Repository), SanitizeAuditText(u.Format), SanitizeAuditText(u.Resource), SanitizeAuditText(u.LastActor)
		key := [3]string{u.Repository, u.Format, u.Resource}
		if existing, ok := merged[key]; ok {
			existing.Downloads += u.Downloads
			existing.Bytes += u.Bytes
			if !u.First.IsZero() && (existing.First.IsZero() || u.First.Before(existing.First)) {
				existing.First = u.First
			}
			if !u.Last.Before(existing.Last) {
				existing.Last, existing.LastActor = u.Last, u.LastActor
			}
			continue
		}
		out = append(out, u)
		merged[key] = &out[len(out)-1]
	}
	return out
}

func sanitizedRollups(in []RollupDelta) []RollupDelta {
	type key struct {
		repository string
		bucket     time.Time
		outcome    int16
	}
	merged := make(map[key]*RollupDelta, len(in))
	out := make([]RollupDelta, 0, len(in))
	for _, r := range in {
		r.Repository = SanitizeAuditText(r.Repository)
		k := key{r.Repository, r.BucketStart, int16(r.Outcome)}
		if existing, ok := merged[k]; ok {
			existing.Requests += r.Requests
			existing.Bytes += r.Bytes
			continue
		}
		out = append(out, r)
		merged[k] = &out[len(out)-1]
	}
	return out
}

// PruneRequestRollups deletes rollup buckets that start before the cutoff.
func (s *PostgresStore) PruneRequestRollups(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM repository_request_rollup WHERE bucket_start < $1`, before.UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
