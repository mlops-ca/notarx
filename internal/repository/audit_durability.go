package repository

import (
	"context"
	"strings"
	"time"
)

// AnonymousActor is the actor recorded for unauthenticated requests.
const AnonymousActor = "anonymous"

// AuditRecorder is the port that every audit write goes through (ADR 0023
// section 1). The PostgreSQL store implements it directly; internal/auditwriter
// implements it on top of AuditBatchStore with the tiered durability model.
type AuditRecorder interface {
	RecordAudit(context.Context, AuditRecord) error
}

// AuditBatchStore is the storage side of the audit writer. It writes audit rows
// only; usage aggregates and request rollups arrive pre-aggregated through
// FlushUsage, so one hot artifact costs one row update per flush instead of one
// per download.
type AuditBatchStore interface {
	// InsertAudit commits one record, routed to its table by AuditRecord.Class.
	InsertAudit(context.Context, AuditRecord) error
	// InsertAuditBatch writes the records with one statement per table.
	InsertAuditBatch(context.Context, []AuditRecord) error
	// FlushUsage adds the aggregated usage and rollup deltas in key order.
	FlushUsage(context.Context, UsageBatch) error
}

// UsageDelta is the additive change to one artifact_usage_stats row.
type UsageDelta struct {
	Repository string
	Format     string
	Resource   string
	Downloads  int64
	Bytes      int64
	First      time.Time
	Last       time.Time
	LastActor  string
}

// RollupDelta is the additive change to one hourly repository_request_rollup row.
type RollupDelta struct {
	Repository  string
	BucketStart time.Time
	Outcome     RequestOutcome
	Requests    int64
	Bytes       int64
}

// UsageBatch is one flush of the aggregator.
type UsageBatch struct {
	Usage   []UsageDelta
	Rollups []RollupDelta
}

// RequestOutcome is the small enum in the primary key of repository_request_rollup:
// allowed, or the reason a request was denied, so denied counts break down by reason.
type RequestOutcome int16

const (
	OutcomeAllowed RequestOutcome = iota
	OutcomeUnauthenticated
	OutcomeForbidden
	OutcomeQuota
	OutcomeQuarantined
	OutcomeRateLimited
	OutcomeOther
)

// RequestOutcomeCount is the number of RequestOutcome values.
const RequestOutcomeCount = int(OutcomeOther) + 1

var requestOutcomeNames = [RequestOutcomeCount]string{"allowed", "unauthenticated", "forbidden", "quota", "quarantined", "rate_limited", "other"}

func (o RequestOutcome) String() string {
	if o < 0 || int(o) >= RequestOutcomeCount {
		return "other"
	}
	return requestOutcomeNames[o]
}

// IsDenial reports whether the audit outcome is a denial.
func (o AuditOutcome) IsDenial() bool {
	return o == AuditAccessDenied || o == AuditProxyDenied || o == "denied"
}

// CountsAsRequest reports whether the record is an audited protocol request for
// the rollups: it names a repository and is not management bookkeeping.
func (a AuditRecord) CountsAsRequest() bool {
	return a.Repository != "" && a.Format != "management"
}

// RequestOutcome classifies the record for the request rollup.
func (a AuditRecord) RequestOutcome() RequestOutcome {
	if !a.Outcome.IsDenial() {
		return OutcomeAllowed
	}
	switch {
	case a.AuthorizationReason == ArtifactQuarantinedReason || a.AuthorizationSource == "quarantine_read_policy":
		return OutcomeQuarantined
	case a.Status == 401:
		return OutcomeUnauthenticated
	case a.Status == 403:
		return OutcomeForbidden
	case a.Status == 429:
		return OutcomeRateLimited
	case a.Status == 402 || a.Status == 413 || a.Status == 507 || strings.Contains(strings.ToLower(a.AuthorizationReason), "quota"):
		return OutcomeQuota
	}
	return OutcomeOther
}

// IsAuthenticatedDenial reports a denial of an authenticated principal: the one
// sync-class record that is high-rate enough to be bounded by the writer's semaphore.
func (a AuditRecord) IsAuthenticatedDenial() bool {
	return a.Outcome.IsDenial() && a.Actor != "" && a.Actor != AnonymousActor
}

// Durability returns the durability class of the record (ADR 0023 section 1).
// Successful reads and downloads, cache hits and misses, group resolution and
// anonymous 401/403 are async; denials of authenticated principals, management
// mutations, publish, delete, quarantine, release, promotion, token mint, settings,
// retention and key operations are sync. A record that names its Class keeps it.
// Anything that is not recognisably a read is sync, so an unknown operation is
// kept durable rather than silently best-effort.
func (a AuditRecord) Durability() AuditClass {
	if a.Class.Valid() {
		return a.Class
	}
	if a.Outcome.IsDenial() {
		if a.IsAuthenticatedDenial() {
			return AuditClassSync
		}
		return AuditClassAsync
	}
	if a.Format != "management" && isReadOperation(a.Operation) {
		return AuditClassAsync
	}
	return AuditClassSync
}

func isReadOperation(operation string) bool {
	switch operation = strings.ToLower(operation); operation {
	case "", "get", "head", "options":
		return true
	}
	return strings.HasSuffix(operation, ".read")
}
