package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

// ClaimSource is the store side of the per-process claimer: one claim for several formats of one kind.
type ClaimSource interface {
	ClaimLifecycleJobsByKindAndFormats(context.Context, repository.LifecycleJobKind, []repository.Format, int) ([]repository.LifecycleJob, error)
	ClaimReplicationPlansByFormats(context.Context, []repository.Format, int) ([]repository.ReplicationPlan, error)
	RenewLifecycleJobLease(ctx context.Context, id, leaseToken string) error
	RenewReplicationPlanLease(ctx context.Context, id, leaseToken string) error
	// ReleaseLifecycleJobLease and ReleaseReplicationPlanLease hand a parked item that no worker will take
	// back to the queue unrun, without spending the attempt its claim counted.
	ReleaseLifecycleJobLease(ctx context.Context, id, leaseToken string) error
	ReleaseReplicationPlanLease(ctx context.Context, id, leaseToken string) error
}

// ClaimBroker is the one claimer per process and queue kind of ADR 0023 section 7. About thirty worker
// runtimes each ask the store for the jobs of one kind and format; the broker answers those calls from
// buckets and refills every bucket of a kind with a single claim, so a node that enables N formats issues
// one claim per kind and round, not N. A worker's call keeps its shape (ClaimLifecycleJobsByKindAndFormat,
// ClaimReplicationPlansByFormat), so the workers hold no claiming logic.
//
// A job parked in a bucket holds its lease although no worker runs it yet. Run renews the lease of a parked
// job every ParkedRenewInterval, so a worker that is busy with a long job cannot let the parked jobs of other
// formats expire and be run twice; a job whose lease is gone is dropped from its bucket. Parking is bounded:
// a claim asks only for the formats whose worker called within FormatIdleAfter and whose bucket holds less
// than a claim, and a job parked for MaxParkedAge, or for a format whose worker stopped calling, is released
// back to the queue, so one node never hoards work that idle nodes could run.
//
// A kind is claimed again when a worker whose bucket is empty has already been answered by an earlier claim
// (the claim ran after its previous call and found nothing for it), when a format is seen for the first time
// and when a NOTIFY for the kind arrived (Wake). With workers that poll on their own timers this settles at
// one claim per kind per poll period.
type ClaimBroker struct {
	source ClaimSource
	now    func() time.Time

	mu         sync.Mutex
	jobs       map[repository.LifecycleJobKind]*claimGroup[repository.LifecycleJob]
	plans      *claimGroup[repository.ReplicationPlan]
	claims     map[repository.LifecycleJobKind]*atomic.Uint64
	planClaims atomic.Uint64
	// stopped is set by the release on stop: a queue created after it is closed from the start.
	stopped bool
}

// NewClaimBroker returns a broker that claims through source.
func NewClaimBroker(source ClaimSource) *ClaimBroker {
	b := &ClaimBroker{source: source, now: time.Now, jobs: map[repository.LifecycleJobKind]*claimGroup[repository.LifecycleJob]{}, claims: map[repository.LifecycleJobKind]*atomic.Uint64{}}
	b.plans = newClaimGroup(claimQueue[repository.ReplicationPlan]{
		claim: func(ctx context.Context, formats []repository.Format, limit int) ([]repository.ReplicationPlan, error) {
			b.planClaims.Add(1)
			return source.ClaimReplicationPlansByFormats(ctx, formats, limit)
		},
		format: func(plan repository.ReplicationPlan) repository.Format { return plan.Format },
		renew: func(ctx context.Context, plan repository.ReplicationPlan) error {
			return source.RenewReplicationPlanLease(ctx, plan.ID, plan.LeaseToken)
		},
		release: func(ctx context.Context, plan repository.ReplicationPlan) error {
			return source.ReleaseReplicationPlanLease(ctx, plan.ID, plan.LeaseToken)
		},
	}, func() time.Time { return b.now() })
	return b
}

func (b *ClaimBroker) counter(kind repository.LifecycleJobKind) *atomic.Uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	counter := b.claims[kind]
	if counter == nil {
		counter = new(atomic.Uint64)
		b.claims[kind] = counter
	}
	return counter
}

// ClaimCalls is the number of claims the broker issued to the store for the lifecycle jobs of kind.
func (b *ClaimBroker) ClaimCalls(kind repository.LifecycleJobKind) uint64 {
	return b.counter(kind).Load()
}

// ReplicationClaimCalls is the number of claims the broker issued to the store for replication plans.
func (b *ClaimBroker) ReplicationClaimCalls() uint64 {
	return b.planClaims.Load()
}

// existingGroup is the queue of kind, or nil when no worker asked for it yet.
func (b *ClaimBroker) existingGroup(kind repository.LifecycleJobKind) *claimGroup[repository.LifecycleJob] {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.jobs[kind]
}

func (b *ClaimBroker) group(kind repository.LifecycleJobKind) *claimGroup[repository.LifecycleJob] {
	b.mu.Lock()
	defer b.mu.Unlock()
	g := b.jobs[kind]
	if g == nil {
		g = newClaimGroup(claimQueue[repository.LifecycleJob]{
			claim: func(ctx context.Context, formats []repository.Format, limit int) ([]repository.LifecycleJob, error) {
				b.counter(kind).Add(1)
				return b.source.ClaimLifecycleJobsByKindAndFormats(ctx, kind, formats, limit)
			},
			format: repository.LifecycleJobFormat,
			renew: func(ctx context.Context, job repository.LifecycleJob) error {
				return b.source.RenewLifecycleJobLease(ctx, job.ID, job.LeaseToken)
			},
			release: func(ctx context.Context, job repository.LifecycleJob) error {
				return b.source.ReleaseLifecycleJobLease(ctx, job.ID, job.LeaseToken)
			},
		}, func() time.Time { return b.now() })
		g.closed = b.stopped
		b.jobs[kind] = g
	}
	return g
}

// ClaimLifecycleJobsByKindAndFormat serves one worker's claim from the kind's bucket for format.
func (b *ClaimBroker) ClaimLifecycleJobsByKindAndFormat(ctx context.Context, kind repository.LifecycleJobKind, format repository.Format, limit int) ([]repository.LifecycleJob, error) {
	return b.group(kind).take(ctx, format, limit)
}

// ClaimReplicationPlansByFormat serves one replication worker's claim from the plan bucket for format.
func (b *ClaimBroker) ClaimReplicationPlansByFormat(ctx context.Context, format repository.Format, limit int) ([]repository.ReplicationPlan, error) {
	return b.plans.take(ctx, format, limit)
}

// RenewParked renews the leases of the jobs and plans that wait in a bucket (ParkedRenewInterval) and releases
// those parked too long or for a format whose worker stopped calling.
func (b *ClaimBroker) RenewParked(ctx context.Context) {
	for _, g := range b.jobGroups() {
		g.renewParked(ctx)
	}
	b.plans.renewParked(ctx)
}

// jobGroups is a snapshot of the lifecycle job queues.
func (b *ClaimBroker) jobGroups() []*claimGroup[repository.LifecycleJob] {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.jobGroupsLocked()
}

// jobGroupsLocked is jobGroups for a caller that holds b.mu.
func (b *ClaimBroker) jobGroupsLocked() []*claimGroup[repository.LifecycleJob] {
	groups := make([]*claimGroup[repository.LifecycleJob], 0, len(b.jobs))
	for _, g := range b.jobs {
		groups = append(groups, g)
	}
	return groups
}

// Run renews and releases parked items every half ParkedRenewInterval: a worker runtime that runs the jobs of
// one format inline before it asks for the next cannot renew what waits for another format. When ctx ends it
// releases every item still parked, so a stopping process leaves no job waiting for its lease to expire, and
// claims nothing more: the workers stop with the same context. The caller waits for Run, at most ShutdownWait,
// before it closes the store.
func (b *ClaimBroker) Run(ctx context.Context) {
	ticker := time.NewTicker(ParkedRenewInterval / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownReleaseTimeout)
			b.releaseAll(drain)
			cancel()
			return
		case <-ticker.C:
			b.RenewParked(ctx)
		}
	}
}

// ShutdownReleaseTimeout bounds the releases of a stopping broker; what it cannot release returns through the
// scheduler's lease recovery. What Run does before them, the renewal pass in flight and the wait for each
// queue's lock that a worker's claim may hold, runs on the ended context and returns at once.
const ShutdownReleaseTimeout = 5 * time.Second

// ShutdownWait is how long a stopping process waits for Run, counted from when it starts waiting, which is
// at or after the end of Run's context. It is a safety net for a store call that ignores the cancellation:
// past it the stores close and lease recovery returns what is still held.
const ShutdownWait = 2 * ShutdownReleaseTimeout

// releaseConcurrency is how many releases run at once, over every queue, when the broker stops.
const releaseConcurrency = 8

// releaseAll releases every parked item of every queue, the queues side by side, and logs what it could not
// release; those items return through the scheduler's lease recovery. It returns how many it could not release.
func (b *ClaimBroker) releaseAll(ctx context.Context) int64 {
	// One critical section: a queue is either in the snapshot or created afterwards, closed.
	b.mu.Lock()
	b.stopped = true
	groups := b.jobGroupsLocked()
	b.mu.Unlock()
	slots := make(chan struct{}, releaseConcurrency)
	var wg sync.WaitGroup
	var failed atomic.Int64
	for _, g := range groups {
		wg.Go(func() { failed.Add(int64(g.releaseAll(ctx, slots))) })
	}
	wg.Go(func() { failed.Add(int64(b.plans.releaseAll(ctx, slots))) })
	wg.Wait()
	if n := failed.Load(); n > 0 {
		slog.Warn("parked lifecycle work not released on stop; lease recovery returns it", "component", "lifecycle_claim_broker", "items", n)
	}
	return failed.Load()
}

// Wake marks a channel's queue as changed: the next worker call claims instead of waiting for a poll. Channels
// that are not a queue of the broker are ignored.
func (b *ClaimBroker) Wake(channel string) {
	if channel == repository.ReplicationPlanChannel {
		b.plans.dirty.Store(true)
		return
	}
	for _, kind := range repository.LifecycleJobKinds() {
		if repository.LifecycleJobChannel(kind) == channel {
			// A queue no worker asked for yet claims on its first call (take's unknown caller); none is
			// created for a wake-up.
			if g := b.existingGroup(kind); g != nil {
				g.dirty.Store(true)
			}
			return
		}
	}
}

// NotificationSource is the store's NOTIFY subscription.
type NotificationSource interface {
	Listen(context.Context, string) <-chan struct{}
}

// Relay subscribes to channel on source, marks the broker's queue changed on every notification before the
// subscriber sees it and forwards the wake-up. The returned channel closes when the subscription does.
func (b *ClaimBroker) Relay(ctx context.Context, source NotificationSource, channel string) <-chan struct{} {
	inner := source.Listen(ctx, channel)
	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		for range inner {
			b.Wake(channel)
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}()
	return out
}

// ClaimRetryAfter is how long the workers of other formats wait for a new claim after a claim failed. The
// worker whose call failed retries on its own next call, at the pace of its error back-off.
const ClaimRetryAfter = time.Second

// claimFailure is the latest failed claim of a queue: when it failed and for whose call.
type claimFailure struct {
	at     time.Time
	caller repository.Format
}

// ParkedRenewInterval is how often the lease of a job that waits in a bucket is renewed. A lease lasts ten
// minutes; renewing at a fifth of that keeps a parked job safe across several missed intervals.
const ParkedRenewInterval = 2 * time.Minute

// FormatIdleAfter is how long a format's worker may go without calling before the broker stops claiming for
// it and releases what it parked for it. Workers call at least every poll back-off (seconds); a worker busy
// with one long job for longer than this lets its format's parked jobs go to other nodes.
const FormatIdleAfter = 5 * time.Minute

// MaxParkedAge is how long a claimed item may wait in a bucket before it is released back to the queue
// instead of renewed again.
const MaxParkedAge = 5 * time.Minute

type parkedItem[T any] struct {
	item T
	// claimed is when the item was parked, at is when its lease was last claimed or renewed.
	claimed time.Time
	at      time.Time
	seq     uint64
}

// claimQueue is the store side of one queue.
type claimQueue[T any] struct {
	claim   func(context.Context, []repository.Format, int) ([]T, error)
	format  func(T) repository.Format
	renew   func(context.Context, T) error
	release func(context.Context, T) error
}

// claimGroup is the bucket set of one queue.
type claimGroup[T any] struct {
	queue claimQueue[T]
	now   func() time.Time
	dirty atomic.Bool

	mu      sync.Mutex
	buckets map[repository.Format][]parkedItem[T]
	// lastCall is the sequence number of each format's latest call, answered the sequence number of the
	// latest claim that asked for the format, and seenAt the time of the format's latest call.
	lastCall  map[repository.Format]uint64
	answered  map[repository.Format]uint64
	seenAt    map[repository.Format]time.Time
	seq       uint64
	maxLimit  int
	parkedSeq uint64
	// failure is the latest failed claim; releasing holds items whose release failed, out of reach of the
	// workers until the next pass releases them.
	failure   claimFailure
	releasing []parkedItem[T]
	// closed is set when the broker stopped: nothing is parked any more and nothing is claimed.
	closed bool
}

func newClaimGroup[T any](queue claimQueue[T], now func() time.Time) *claimGroup[T] {
	return &claimGroup[T]{queue: queue, now: now, buckets: map[repository.Format][]parkedItem[T]{},
		lastCall: map[repository.Format]uint64{}, answered: map[repository.Format]uint64{}, seenAt: map[repository.Format]time.Time{}}
}

// renewParked renews the lease of every job that has waited longer than ParkedRenewInterval and releases the
// jobs parked for MaxParkedAge or for a format idle for FormatIdleAfter. Released jobs leave their bucket
// before the release runs, so no worker can take one meanwhile. The store calls run without the group lock,
// so workers are never held behind database round trips. A job whose lease is gone (repository.ErrNotFound:
// another node recovered it) leaves its bucket; any other renewal error keeps the job and is tried again at
// the next pass, and so is a release that failed for another reason.
func (g *claimGroup[T]) renewParked(ctx context.Context) {
	type due struct {
		seq  uint64
		item T
	}
	now := g.now()
	var dues []due
	g.mu.Lock()
	releases := g.releasing
	g.releasing = nil
	for format, bucket := range g.buckets {
		// A bucket only fills for formats a worker asked for, so seenAt holds every key; a format with no
		// call on record would have no worker to take its jobs and counts as idle.
		idle := now.Sub(g.seenAt[format]) >= FormatIdleAfter
		kept := bucket[:0]
		for _, parked := range bucket {
			switch {
			case idle || now.Sub(parked.claimed) >= MaxParkedAge:
				releases = append(releases, parked)
				// What answered the format's worker went back to the queue: its next call claims again.
				g.answered[format] = 0
				continue
			case now.Sub(parked.at) >= ParkedRenewInterval:
				dues = append(dues, due{seq: parked.seq, item: parked.item})
			}
			kept = append(kept, parked)
		}
		clear(bucket[len(kept):])
		g.buckets[format] = kept
	}
	g.mu.Unlock()
	// A release that fails for another reason than a lost lease is tried again at the next pass, or by the
	// release on stop; no worker receives the item meanwhile.
	var failed []parkedItem[T]
	for _, parked := range releases {
		if releaseFailed(g.queue.release(ctx, parked.item)) {
			failed = append(failed, parked)
		}
	}
	if len(failed) > 0 {
		g.mu.Lock()
		g.releasing = append(g.releasing, failed...)
		g.mu.Unlock()
	}
	renewed, lost := map[uint64]bool{}, map[uint64]bool{}
	for _, d := range dues {
		switch err := g.queue.renew(ctx, d.item); {
		case err == nil:
			renewed[d.seq] = true
		case errors.Is(err, repository.ErrNotFound):
			lost[d.seq] = true
		}
	}
	if len(renewed)+len(lost) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for format, bucket := range g.buckets {
		kept := bucket[:0]
		for _, parked := range bucket {
			if lost[parked.seq] {
				continue
			}
			if renewed[parked.seq] {
				parked.at = now
			}
			kept = append(kept, parked)
		}
		clear(bucket[len(kept):])
		g.buckets[format] = kept
	}
}

// releaseAll closes the queue, empties the buckets and releases what they held and what earlier passes failed
// to release, holding a slot of slots for each release. It returns how many it could not release, for another
// reason than a lost lease or because ctx ended first; the process is stopping, so they are left to the lease
// recovery rather than kept.
func (g *claimGroup[T]) releaseAll(ctx context.Context, slots chan struct{}) int {
	g.mu.Lock()
	g.closed = true
	items := g.releasing
	g.releasing = nil
	for format, bucket := range g.buckets {
		items = append(items, bucket...)
		delete(g.buckets, format)
		g.answered[format] = 0
	}
	g.mu.Unlock()
	var failed atomic.Int64
	var wg sync.WaitGroup
	for i, parked := range items {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			failed.Add(int64(len(items) - i))
			wg.Wait()
			return int(failed.Load())
		}
		wg.Go(func() {
			defer func() { <-slots }()
			if releaseFailed(g.queue.release(ctx, parked.item)) {
				failed.Add(1)
			}
		})
	}
	wg.Wait()
	return int(failed.Load())
}

// releaseFailed tells whether a release left the item leased: a lost lease is no failure, nothing is held.
func releaseFailed(err error) bool {
	return err != nil && !errors.Is(err, repository.ErrNotFound)
}

// DefaultClaimLimit is the size of a claim when no worker asked for more.
const DefaultClaimLimit = 100

func (g *claimGroup[T]) take(ctx context.Context, format repository.Format, limit int) ([]T, error) {
	if limit <= 0 {
		limit = DefaultClaimLimit
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	// A stopped broker takes no new lease: the process is stopping and nothing would release it.
	if g.closed {
		return nil, nil
	}
	now := g.now()
	g.maxLimit = max(g.maxLimit, limit)
	previousCall, known := g.lastCall[format]
	g.seq++
	g.lastCall[format] = g.seq
	g.seenAt[format] = now
	// A worker whose bucket is empty claims when no claim asked for its format since its previous call. After a
	// failed claim the other formats' workers wait ClaimRetryAfter, so a database outage costs the failing
	// worker's retries plus one claim per interval, not one per format worker.
	retryDue := g.failure.at.IsZero() || g.failure.caller == format || now.Sub(g.failure.at) >= ClaimRetryAfter
	if len(g.buckets[format]) == 0 && (!known || g.dirty.Load() || g.answered[format] <= previousCall) && retryDue {
		woken := g.dirty.Swap(false)
		formats := g.claimFormats(format, now)
		claimed, err := g.queue.claim(ctx, formats, g.maxLimit)
		g.seq++
		// The claim answers this caller: its next empty call is the one that claims again.
		g.lastCall[format] = g.seq
		if err != nil {
			// A wake-up the failed claim consumed still stands for the next one.
			if woken {
				g.dirty.Store(true)
			}
			g.failure = claimFailure{at: now, caller: format}
			return nil, err
		}
		g.failure = claimFailure{}
		// Only a claim that succeeded answered the other formats it asked for.
		for _, asked := range formats {
			g.answered[asked] = g.seq
		}
		for _, item := range claimed {
			f := g.queue.format(item)
			g.parkedSeq++
			g.buckets[f] = append(g.buckets[f], parkedItem[T]{item: item, claimed: now, at: now, seq: g.parkedSeq})
		}
	}
	bucket := g.buckets[format]
	n := min(limit, len(bucket))
	out := make([]T, 0, n)
	for _, parked := range bucket[:n] {
		out = append(out, parked.item)
	}
	g.buckets[format] = bucket[n:]
	return out, nil
}

// claimFormats is what a claim asks for: the caller's format and every format whose worker called within
// FormatIdleAfter and whose bucket holds less than a claim.
func (g *claimGroup[T]) claimFormats(caller repository.Format, now time.Time) []repository.Format {
	formats := []repository.Format{caller}
	for format, seen := range g.seenAt {
		if format != caller && now.Sub(seen) < FormatIdleAfter && len(g.buckets[format]) < g.maxLimit {
			formats = append(formats, format)
		}
	}
	return formats
}
