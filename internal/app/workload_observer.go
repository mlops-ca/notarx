package app

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/repository"
)

// workloadReasons is the bounded label set of notarx_workload_auth_total; a reason outside it is
// counted as "other", so a caller can never grow the series.
var workloadReasons = [...]string{
	authorization.WorkloadAuthenticated, authorization.WorkloadIssuerDisabled, authorization.WorkloadVerifyFailed,
	authorization.WorkloadPolicyNotFound, authorization.WorkloadNoPolicyMatch, authorization.WorkloadAmbiguousPolicy,
	authorization.WorkloadAccountInactive, authorization.WorkloadReplay, authorization.WorkloadMissingJTI,
	authorization.WorkloadStoreUnavailable, authorization.WorkloadKeysUnavailable, "other",
}

// workloadCounters counts workload authentication decisions by reason.
type workloadCounters struct {
	counts     [len(workloadReasons)]atomic.Uint64
	suppressed atomic.Uint64
}

func (c *workloadCounters) record(reason string) {
	for index, known := range workloadReasons {
		if known == reason {
			c.counts[index].Add(1)
			return
		}
	}
	c.counts[len(workloadReasons)-1].Add(1)
}

func (m *Metrics) writeWorkloadMetrics(w http.ResponseWriter) {
	_, _ = w.Write([]byte("# TYPE notarx_workload_auth_total counter\n"))
	for index, reason := range workloadReasons {
		_, _ = w.Write([]byte("notarx_workload_auth_total{outcome=\"" + reason + "\"} " + utoa(m.workload.counts[index].Load()) + "\n"))
	}
	_, _ = w.Write([]byte("# TYPE notarx_workload_audit_suppressed_total counter\nnotarx_workload_audit_suppressed_total " + utoa(m.workload.suppressed.Load()) + "\n"))
}

// AuditRecorder writes one audit record; the PostgreSQL store implements it.
type AuditRecorder interface {
	RecordAudit(context.Context, repository.AuditRecord) error
}

// WorkloadObserver turns every workload authentication decision into a metric and an audit event
// (ADR 0019, "Audit"): issuer, policy id, service account, jti hash, subject and the claims the
// matched conditions refer to; never the raw token. Failures are security-class (sync) records and
// carry the operator-only detail, which never reaches the client.
type WorkloadObserver struct {
	Metrics *Metrics
	Audit   AuditRecorder
	Now     func() time.Time
	// failures bounds the failure audit records per issuer (see NewWorkloadObserver).
	failures *clientLimiter
}

// Failure audit records per issuer: a burst of 50, then 10 per second. A caller without any valid token can make
// the gateway verify as many forged tokens as it likes, so an unthrottled synchronous audit write per failure would
// let it fill the audit table and put a database write on every hostile request. Metrics still count every failure,
// and notarx_workload_audit_suppressed_total counts the records not written.
const (
	workloadFailureAuditBurst    = 50
	workloadFailureAuditInterval = 100 * time.Millisecond
)

// NewWorkloadObserver returns the observer with its failure throttle.
func NewWorkloadObserver(metrics *Metrics, audit AuditRecorder) WorkloadObserver {
	return WorkloadObserver{Metrics: metrics, Audit: audit, failures: newClientLimiter(workloadFailureAuditBurst, workloadFailureAuditInterval, workloadExchangeMaxHosts)}
}

// ObserveWorkload implements authorization.WorkloadObserver.
func (o WorkloadObserver) ObserveWorkload(ctx context.Context, event authorization.WorkloadEvent) {
	if o.Metrics != nil {
		o.Metrics.workload.record(event.Reason)
	}
	if o.Audit == nil {
		return
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	if !event.Success && o.failures != nil && !o.failures.allow(event.Issuer, now()) {
		if o.Metrics != nil {
			o.Metrics.workload.suppressed.Add(1)
		}
		return
	}
	if err := o.Audit.RecordAudit(context.WithoutCancel(ctx), WorkloadAuditRecord(event, now().UTC())); err != nil {
		slog.Error("record workload authentication audit event", "error", err)
	}
}

// WorkloadAuditRecord builds the audit record of one decision.
func WorkloadAuditRecord(event authorization.WorkloadEvent, now time.Time) repository.AuditRecord {
	evidence := map[string]string{"issuer": event.Issuer, "reason": event.Reason}
	set := func(key, value string) {
		if value != "" {
			evidence[key] = value
		}
	}
	set("issuerId", event.IssuerID)
	set("policyId", event.PolicyID)
	set("policy", event.PolicyName)
	set("serviceAccount", event.ServiceAccountID)
	set("jtiHash", event.JTIHash)
	set("subject", event.Subject)
	set("detail", event.Detail)
	if event.Pinned {
		evidence["pinnedPolicy"] = "true"
	}
	for claim, value := range event.Claims {
		evidence["claim."+claim] = value
	}
	record := repository.AuditRecord{
		Actor: "workload:" + event.Issuer, Outcome: repository.AuditResolved, OccurredAt: now, Resource: event.Issuer,
		Operation: "workload.authenticate", Status: http.StatusOK, CacheDisposition: "bypass",
		AuthorizationSource: "workload_federation", AuthorizationReason: event.Reason, Evidence: evidence, Class: repository.AuditClassAsync,
	}
	if event.ServiceAccountID != "" {
		record.Actor = "service-account:" + event.ServiceAccountID
	}
	if !event.Success {
		record.Outcome, record.Status, record.Class = repository.AuditAccessDenied, http.StatusUnauthorized, repository.AuditClassSync
	}
	return record
}
