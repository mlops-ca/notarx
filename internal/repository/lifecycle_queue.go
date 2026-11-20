package repository

import "sync/atomic"

// Queue claiming (ADR 0023 section 7). Each job kind declares how many of its jobs may run at once per
// repository in this registry and owns one NOTIFY channel, so a node wakes only the runtimes it enables.

// DefaultLifecycleRepoConcurrency preserves the long-standing rule of one running job per repository.
const DefaultLifecycleRepoConcurrency = 1

// WebhookDeliveryChannel wakes the webhook delivery worker.
const WebhookDeliveryChannel = "notarx_deliveries"

// ReplicationPlanChannel wakes the replication workers.
const ReplicationPlanChannel = "notarx_replication_plans"

// QuotaEvaluationChannel wakes the quota alert evaluator when a rule becomes due.
const QuotaEvaluationChannel = "notarx_evaluations"

// QueueClaimScanCap bounds how many candidate rows one claim examines before it gives up, so a hot
// repository whose backlog heads the index cannot make a claim scan an unbounded prefix.
const QueueClaimScanCap = 1000

// LifecycleJobListQuery pages a job listing newest first. PageToken is the NextPageToken of the previous page.
type LifecycleJobListQuery struct {
	Limit     int
	PageToken string
	// RepositoryIDs, when not nil, limits the listing of every repository's jobs to these repositories (the
	// ones a caller administers). It is ignored by the per-repository listing.
	RepositoryIDs []string
}

// LifecycleJobPage is one page of a repository's jobs.
type LifecycleJobPage struct {
	Items         []LifecycleJob
	NextPageToken string
}

// RepositoryLifecycleJobPage is one page of the jobs of every repository.
type RepositoryLifecycleJobPage struct {
	Items         []RepositoryLifecycleJob
	NextPageToken string
}

type lifecycleJobKindSpec struct {
	kind            LifecycleJobKind
	repoConcurrency int
}

// lifecycleJobRegistry is the closed set of kinds in channel and metric order.
var lifecycleJobRegistry = []lifecycleJobKindSpec{
	{LifecycleJobRetention, DefaultLifecycleRepoConcurrency},
	{LifecycleJobPromotion, DefaultLifecycleRepoConcurrency},
	{LifecycleJobReplication, DefaultLifecycleRepoConcurrency},
	{LifecycleJobReclaim, DefaultLifecycleRepoConcurrency},
	{LifecycleJobIntelligence, DefaultLifecycleRepoConcurrency},
	{LifecycleJobScan, 4},
}

// LifecycleJobKinds returns every registered job kind.
func LifecycleJobKinds() []LifecycleJobKind {
	kinds := make([]LifecycleJobKind, len(lifecycleJobRegistry))
	for i, spec := range lifecycleJobRegistry {
		kinds[i] = spec.kind
	}
	return kinds
}

// LifecycleJobRepoConcurrency is the number of jobs of kind that may run at once in one repository.
func LifecycleJobRepoConcurrency(kind LifecycleJobKind) int {
	for _, spec := range lifecycleJobRegistry {
		if spec.kind == kind {
			return spec.repoConcurrency
		}
	}
	return DefaultLifecycleRepoConcurrency
}

// LifecycleJobChannel is the NOTIFY channel of a job kind.
func LifecycleJobChannel(kind LifecycleJobKind) string {
	return "notarx_jobs_" + string(kind)
}

// QueueClaimObserver receives, per claim, the candidate rows it examined (the scanned-rows metric).
type QueueClaimObserver func(kind LifecycleJobKind, scanned int)

type queueClaimObserverHolder struct {
	observe atomic.Pointer[QueueClaimObserver]
}

func (h *queueClaimObserverHolder) set(observer QueueClaimObserver) {
	if observer == nil {
		h.observe.Store(nil)
		return
	}
	h.observe.Store(&observer)
}

func (h *queueClaimObserverHolder) record(kind LifecycleJobKind, scanned int) {
	if observer := h.observe.Load(); observer != nil {
		(*observer)(kind, scanned)
	}
}
