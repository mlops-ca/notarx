package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

type countingSource struct {
	flaky     map[string]bool
	renewed   []string
	released  []string
	stuck     map[string]bool
	lost      map[string]bool
	mu        sync.Mutex
	jobs      map[repository.LifecycleJobKind][]repository.LifecycleJob
	plans     []repository.ReplicationPlan
	lifecycle int
	replicas  int
	formats   [][]repository.Format
}

func (s *countingSource) ClaimLifecycleJobsByKindAndFormats(_ context.Context, kind repository.LifecycleJobKind, formats []repository.Format, limit int) ([]repository.LifecycleJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lifecycle++
	s.formats = append(s.formats, formats)
	var out, rest []repository.LifecycleJob
	for _, job := range s.jobs[kind] {
		wanted := false
		for _, format := range formats {
			wanted = wanted || fmt.Sprintf(`{"format":%q}`, format) == string(job.Payload)
		}
		if wanted && len(out) < limit {
			out = append(out, job)
		} else {
			rest = append(rest, job)
		}
	}
	s.jobs[kind] = rest
	return out, nil
}

func (s *countingSource) RenewLifecycleJobLease(_ context.Context, id, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewed = append(s.renewed, id)
	if s.lost[id] {
		return repository.ErrNotFound
	}
	if s.flaky[id] {
		return errors.New("connection reset")
	}
	return nil
}

func (s *countingSource) RenewReplicationPlanLease(_ context.Context, id, _ string) error {
	return s.RenewLifecycleJobLease(context.Background(), id, "")
}

func (s *countingSource) ReleaseLifecycleJobLease(_ context.Context, id, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, id)
	if s.stuck[id] {
		return errors.New("connection reset")
	}
	return nil
}

func (s *countingSource) ReleaseReplicationPlanLease(_ context.Context, id, _ string) error {
	return s.ReleaseLifecycleJobLease(context.Background(), id, "")
}

func (s *countingSource) ClaimReplicationPlansByFormats(_ context.Context, formats []repository.Format, limit int) ([]repository.ReplicationPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replicas++
	var out, rest []repository.ReplicationPlan
	for _, plan := range s.plans {
		wanted := false
		for _, format := range formats {
			wanted = wanted || plan.Format == format
		}
		if wanted && len(out) < limit {
			out = append(out, plan)
		} else {
			rest = append(rest, plan)
		}
	}
	s.plans = rest
	return out, nil
}

func job(id string, format repository.Format) repository.LifecycleJob {
	return repository.LifecycleJob{ID: id, Payload: []byte(fmt.Sprintf(`{"format":%q}`, format))}
}

var brokerFormats = []repository.Format{repository.FormatMaven, repository.FormatOCI, repository.FormatNPM, repository.FormatRaw, repository.FormatConan}

// A node with many formats and one enabled kind issues one claim per round, not one per format.
func TestClaimBrokerIssuesOneClaimPerKindAndRoundRegardlessOfFormats(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}}
	for _, format := range brokerFormats {
		source.jobs[repository.LifecycleJobReclaim] = append(source.jobs[repository.LifecycleJobReclaim], job("reclaim-"+string(format), format))
	}
	broker := NewClaimBroker(source)
	ctx := context.Background()
	// The first round registers every format: each first sight claims once.
	got := map[string]bool{}
	for _, format := range brokerFormats {
		jobs, err := broker.ClaimLifecycleJobsByKindAndFormat(ctx, repository.LifecycleJobReclaim, format, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, claimed := range jobs {
			got[claimed.ID] = true
		}
	}
	if len(got) != 2 {
		// the first call claims only its own format; later formats register and claim again
		t.Logf("registration round claimed %d jobs", len(got))
	}
	before := broker.ClaimCalls(repository.LifecycleJobReclaim)
	for round := 0; round < 3; round++ {
		for _, format := range brokerFormats {
			if _, err := broker.ClaimLifecycleJobsByKindAndFormat(ctx, repository.LifecycleJobReclaim, format, 100); err != nil {
				t.Fatal(err)
			}
		}
	}
	if claims := broker.ClaimCalls(repository.LifecycleJobReclaim) - before; claims != 3 {
		t.Fatalf("3 rounds over %d formats issued %d claims, want 3 (one per round)", len(brokerFormats), claims)
	}
}

func TestClaimBrokerDeliversEveryFormatsJobsOnce(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}}
	broker := NewClaimBroker(source)
	ctx := context.Background()
	for _, format := range brokerFormats { // register every format
		if _, err := broker.ClaimLifecycleJobsByKindAndFormat(ctx, repository.LifecycleJobPromotion, format, 100); err != nil {
			t.Fatal(err)
		}
	}
	for _, format := range brokerFormats {
		source.jobs[repository.LifecycleJobPromotion] = append(source.jobs[repository.LifecycleJobPromotion], job("a-"+string(format), format), job("b-"+string(format), format))
	}
	seen := map[string]int{}
	for round := 0; round < 3; round++ {
		for _, format := range brokerFormats {
			jobs, err := broker.ClaimLifecycleJobsByKindAndFormat(ctx, repository.LifecycleJobPromotion, format, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, claimed := range jobs {
				if string(claimed.Payload) != fmt.Sprintf(`{"format":%q}`, format) {
					t.Fatalf("job %s of another format handed to %s", claimed.ID, format)
				}
				seen[claimed.ID]++
			}
		}
	}
	if len(seen) != 2*len(brokerFormats) {
		t.Fatalf("delivered %d of %d jobs", len(seen), 2*len(brokerFormats))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("job %s delivered %d times", id, n)
		}
	}
}

func TestClaimBrokerHonoursTheWorkersLimitAndKeepsTheRest(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}}
	broker := NewClaimBroker(source)
	ctx := context.Background()
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, repository.LifecycleJobRetention, repository.FormatRaw, 2)
	for i := range 5 {
		source.jobs[repository.LifecycleJobRetention] = append(source.jobs[repository.LifecycleJobRetention], job(fmt.Sprint("j", i), repository.FormatRaw))
	}
	first, _ := broker.ClaimLifecycleJobsByKindAndFormat(ctx, repository.LifecycleJobRetention, repository.FormatRaw, 2)
	second, _ := broker.ClaimLifecycleJobsByKindAndFormat(ctx, repository.LifecycleJobRetention, repository.FormatRaw, 2)
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("batches %d and %d, want 2 and 2", len(first), len(second))
	}
}

// A NOTIFY forces the next call to claim even though a claim already answered the worker.
func TestClaimBrokerWakeForcesAClaim(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}}
	broker := NewClaimBroker(source)
	ctx := context.Background()
	kind := repository.LifecycleJobReclaim
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100) // registers raw
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100) // claims: raw's previous call is the latest event
	other := repository.FormatNPM
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, other, 100) // registers npm
	source.jobs[kind] = append(source.jobs[kind], job("late", repository.FormatRaw))
	before := broker.ClaimCalls(kind)
	if jobs, _ := broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100); len(jobs) != 0 || broker.ClaimCalls(kind) != before {
		t.Fatalf("a call answered by the latest claim must not claim again: %d jobs, %d claims", len(jobs), broker.ClaimCalls(kind)-before)
	}
	broker.Wake(repository.LifecycleJobChannel(kind))
	jobs, _ := broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100)
	if len(jobs) != 1 || jobs[0].ID != "late" {
		t.Fatalf("the woken claim returned %v", jobs)
	}
	broker.Wake("not-a-queue")
}

func TestClaimBrokerCoalescesReplicationPlansAcrossFormats(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}}
	broker := NewClaimBroker(source)
	ctx := context.Background()
	for _, format := range brokerFormats {
		_, _ = broker.ClaimReplicationPlansByFormat(ctx, format, 100)
	}
	source.plans = []repository.ReplicationPlan{{ID: "p1", Format: repository.FormatOCI}, {ID: "p2", Format: repository.FormatNPM}}
	before := broker.ReplicationClaimCalls()
	delivered := 0
	for range 2 {
		for _, format := range brokerFormats {
			plans, err := broker.ClaimReplicationPlansByFormat(ctx, format, 100)
			if err != nil {
				t.Fatal(err)
			}
			delivered += len(plans)
		}
	}
	if delivered != 2 || broker.ReplicationClaimCalls()-before != 2 {
		t.Fatalf("delivered %d plans with %d claims, want 2 plans with 2 claims over 2 rounds", delivered, broker.ReplicationClaimCalls()-before)
	}
}

// A job parked for another format's worker keeps its lease: it is renewed while it waits, and dropped once
// the lease is gone, so a busy worker cannot make the broker hand out a job another node already recovered.
func TestClaimBrokerRenewsParkedLeasesAndDropsLostOnes(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}, lost: map[string]bool{"gone": true}}
	broker := NewClaimBroker(source)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	broker.now = func() time.Time { return now }
	ctx := context.Background()
	kind := repository.LifecycleJobReclaim
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100)
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100) // both formats known
	source.jobs[kind] = []repository.LifecycleJob{job("kept", repository.FormatNPM), job("gone", repository.FormatNPM)}
	broker.Wake(repository.LifecycleJobChannel(kind))
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100) // parks both for npm
	now = now.Add(ParkedRenewInterval - time.Second)
	broker.RenewParked(ctx)
	if len(source.renewed) != 0 {
		t.Fatalf("a job parked for less than the interval was renewed: %v", source.renewed)
	}
	now = now.Add(2 * time.Second)
	broker.RenewParked(ctx)
	if len(source.renewed) != 2 {
		t.Fatalf("parked jobs renewed %v, want both", source.renewed)
	}
	jobs, _ := broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100)
	if len(jobs) != 1 || jobs[0].ID != "kept" {
		t.Fatalf("npm received %v, want only the job whose lease is still held", jobs)
	}
}

// A renewal that fails for a reason other than a lost lease keeps the job parked and is tried again.
func TestClaimBrokerKeepsAParkedJobWhenRenewalFailsTransiently(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}, flaky: map[string]bool{"j": true}}
	broker := NewClaimBroker(source)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	broker.now = func() time.Time { return now }
	ctx := context.Background()
	kind := repository.LifecycleJobRetention
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100)
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100)
	source.jobs[kind] = []repository.LifecycleJob{job("j", repository.FormatNPM)}
	broker.Wake(repository.LifecycleJobChannel(kind))
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100)
	now = now.Add(ParkedRenewInterval)
	broker.RenewParked(ctx) // transient failure: the job stays parked
	source.mu.Lock()
	source.flaky["j"] = false
	source.mu.Unlock()
	now = now.Add(ParkedRenewInterval)
	broker.RenewParked(ctx)
	jobs, _ := broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100)
	if len(jobs) != 1 || jobs[0].ID != "j" {
		t.Fatalf("a transient renewal failure dropped the parked job: %v", jobs)
	}
}

// Run renews on its own timer, and when its context ends it releases what is still parked and stops.
func TestClaimBrokerRunStopsWithItsContext(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}}
	broker := NewClaimBroker(source)
	setup := context.Background()
	kind := repository.LifecycleJobReclaim
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(setup, kind, repository.FormatRaw, 100)
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(setup, kind, repository.FormatNPM, 100)
	_, _ = broker.ClaimReplicationPlansByFormat(setup, repository.FormatRaw, 100)
	_, _ = broker.ClaimReplicationPlansByFormat(setup, repository.FormatNPM, 100)
	source.jobs[kind] = []repository.LifecycleJob{job("parked", repository.FormatNPM)}
	source.plans = []repository.ReplicationPlan{{ID: "plan", Format: repository.FormatNPM}}
	broker.Wake(repository.LifecycleJobChannel(kind))
	broker.Wake(repository.ReplicationPlanChannel)
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(setup, kind, repository.FormatRaw, 100) // parks "parked" for npm
	_, _ = broker.ClaimReplicationPlansByFormat(setup, repository.FormatRaw, 100)           // parks "plan" for npm
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { broker.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop with its context")
	}
	source.mu.Lock()
	released := fmt.Sprint(slices.Sorted(slices.Values(source.released)))
	source.mu.Unlock()
	if released != "[parked plan]" {
		t.Fatalf("released %s on stop, want the parked job and plan", released)
	}
}

// A release on stop that fails is counted, not retried: the item is left to the lease recovery.
func TestClaimBrokerCountsReleasesThatFailOnStop(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}, stuck: map[string]bool{"npm-1": true}}
	broker := NewClaimBroker(source)
	ctx := context.Background()
	kind := repository.LifecycleJobRetention
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100)
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100)
	source.jobs[kind] = []repository.LifecycleJob{job("npm-1", repository.FormatNPM)}
	broker.Wake(repository.LifecycleJobChannel(kind))
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100) // parks npm-1
	source.plans = []repository.ReplicationPlan{{ID: "plan", Format: repository.FormatNPM}}
	_, _ = broker.ClaimReplicationPlansByFormat(ctx, repository.FormatNPM, 100)
	if failed := broker.releaseAll(ctx); failed != 1 {
		t.Fatalf("failed releases on stop = %d, want 1 (the stuck job; the plan was handed out)", failed)
	}
	if failed := broker.releaseAll(ctx); failed != 0 {
		t.Fatalf("a second release on stop failed %d, want nothing left", failed)
	}
}

// Releases on stop that cannot start before the context ends count as failed.
func TestClaimBrokerCountsReleasesTheStopDeadlineCut(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}}
	broker := NewClaimBroker(source)
	ctx := context.Background()
	kind := repository.LifecycleJobRetention
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100)
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100)
	source.jobs[kind] = []repository.LifecycleJob{job("npm-1", repository.FormatNPM), job("npm-2", repository.FormatNPM)}
	broker.Wake(repository.LifecycleJobChannel(kind))
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100) // parks both
	ended, cancel := context.WithCancel(ctx)
	cancel()
	full := make(chan struct{}) // no free slot: nothing starts before the context's end is seen
	if failed := broker.group(kind).releaseAll(ended, full); failed != 2 {
		t.Fatalf("releases cut by the stop deadline = %d, want 2", failed)
	}
}

// A wake-up for a kind no worker asked for creates no queue, and the first call of a worker claims anyway.
func TestClaimBrokerWakeBeforeAnyWorkerCreatesNoQueue(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}}
	broker := NewClaimBroker(source)
	kind := repository.LifecycleJobRetention
	source.jobs[kind] = []repository.LifecycleJob{job("raw-1", repository.FormatRaw)}
	broker.Wake(repository.LifecycleJobChannel(kind))
	if broker.existingGroup(kind) != nil {
		t.Fatal("a wake-up created a queue no worker asked for")
	}
	jobs, err := broker.ClaimLifecycleJobsByKindAndFormat(context.Background(), kind, repository.FormatRaw, 100)
	if err != nil || len(jobs) != 1 || jobs[0].ID != "raw-1" {
		t.Fatalf("the first call received %v, %v, want raw-1", jobs, err)
	}
}

// After the broker stopped, it claims nothing: no new lease is taken that nothing would release.
func TestClaimBrokerClaimsNothingAfterStop(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}}
	broker := NewClaimBroker(source)
	ctx := context.Background()
	kind := repository.LifecycleJobRetention
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100)
	broker.releaseAll(ctx)
	source.jobs[kind] = []repository.LifecycleJob{job("raw-1", repository.FormatRaw), job("npm-1", repository.FormatNPM)}
	jobs, err := broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100)
	if err != nil || len(jobs) != 0 || len(source.jobs[kind]) != 2 {
		t.Fatalf("raw after stop received %v, %v; the queue holds %v, want nothing claimed", jobs, err, source.jobs[kind])
	}
	// A kind no worker asked for before the stop is closed as well.
	later := repository.LifecycleJobPromotion
	source.jobs[later] = []repository.LifecycleJob{job("late", repository.FormatRaw)}
	if jobs, err := broker.ClaimLifecycleJobsByKindAndFormat(ctx, later, repository.FormatRaw, 100); err != nil || len(jobs) != 0 || len(source.jobs[later]) != 1 {
		t.Fatalf("a new kind after stop received %v, %v, want nothing claimed", jobs, err)
	}
}

// A wake-up that a failed claim consumed still makes the next claim happen.
func TestClaimBrokerKeepsAWakeUpAcrossAFailedClaim(t *testing.T) {
	source := &flakyClaimSource{countingSource: &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}}}
	broker := NewClaimBroker(source)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	broker.now = func() time.Time { return now }
	ctx := context.Background()
	kind := repository.LifecycleJobPromotion
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100)
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100)
	broker.Wake(repository.LifecycleJobChannel(kind))
	source.fail = true
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100)
	source.fail = false
	source.jobs[kind] = []repository.LifecycleJob{job("late", repository.FormatRaw)}
	now = now.Add(ClaimRetryAfter)
	jobs, _ := broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100)
	if len(jobs) != 1 || jobs[0].ID != "late" {
		t.Fatalf("raw received %v after a failed woken claim, want the notified job", jobs)
	}
}

type wakeSource struct{ wake chan struct{} }

func (s wakeSource) Listen(context.Context, string) <-chan struct{} { return s.wake }

// Relay marks the queue changed before the subscriber sees the wake-up, forwards it, and closes with the
// subscription.
func TestClaimBrokerRelayMarksTheQueueChangedAndForwardsWakeUps(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}}
	broker := NewClaimBroker(source)
	ctx := context.Background()
	kind := repository.LifecycleJobPromotion
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100)
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100)
	inner := make(chan struct{}, 1)
	wake := broker.Relay(ctx, wakeSource{wake: inner}, repository.LifecycleJobChannel(kind))
	source.jobs[kind] = []repository.LifecycleJob{job("late", repository.FormatRaw)}
	inner <- struct{}{}
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("the wake-up was not forwarded")
	}
	jobs, err := broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100)
	if err != nil || len(jobs) != 1 || jobs[0].ID != "late" {
		t.Fatalf("the claim after a relayed wake-up returned %v, %v", jobs, err)
	}
	close(inner)
	select {
	case _, ok := <-wake:
		if ok {
			t.Fatal("unexpected wake-up")
		}
	case <-time.After(time.Second):
		t.Fatal("the relayed channel must close with the subscription")
	}
}

type failingSource struct{ *countingSource }

func (s failingSource) ClaimLifecycleJobsByKindAndFormats(context.Context, repository.LifecycleJobKind, []repository.Format, int) ([]repository.LifecycleJob, error) {
	return nil, errors.New("claim failed")
}

func TestClaimBrokerReportsAFailedClaimAndRetriesOnTheNextCall(t *testing.T) {
	broker := NewClaimBroker(failingSource{&countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}}})
	for range 2 {
		if _, err := broker.ClaimLifecycleJobsByKindAndFormat(context.Background(), repository.LifecycleJobScan, repository.FormatRaw, 0); err == nil {
			t.Fatal("a failed claim must reach the worker")
		}
	}
	if broker.ClaimCalls(repository.LifecycleJobScan) != 2 {
		t.Fatalf("claims = %d, want a new attempt per call", broker.ClaimCalls(repository.LifecycleJobScan))
	}
}

// A parked replication plan is renewed like a job, and a plan whose lease is gone is dropped.
func TestClaimBrokerRenewsParkedReplicationPlansAndDropsLostOnes(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}, lost: map[string]bool{"stuck": true}}
	broker := NewClaimBroker(source)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	broker.now = func() time.Time { return now }
	ctx := context.Background()
	_, _ = broker.ClaimReplicationPlansByFormat(ctx, repository.FormatRaw, 100)
	_, _ = broker.ClaimReplicationPlansByFormat(ctx, repository.FormatNPM, 100)
	source.plans = []repository.ReplicationPlan{{ID: "stuck", Format: repository.FormatNPM}, {ID: "fine", Format: repository.FormatNPM}}
	broker.Wake(repository.ReplicationPlanChannel)
	_, _ = broker.ClaimReplicationPlansByFormat(ctx, repository.FormatRaw, 100) // parks both for npm
	now = now.Add(ParkedRenewInterval)
	broker.RenewParked(ctx)
	plans, _ := broker.ClaimReplicationPlansByFormat(ctx, repository.FormatNPM, 100)
	if len(plans) != 1 || plans[0].ID != "fine" {
		t.Fatalf("a plan whose lease is gone must be dropped: %v", plans)
	}
}

// A format whose worker stopped calling is no longer claimed for, and what was parked for it goes back to the
// queue instead of being renewed forever.
func TestClaimBrokerStopsClaimingForAnIdleFormatAndReleasesItsParkedJobs(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}}
	broker := NewClaimBroker(source)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	broker.now = func() time.Time { return now }
	ctx := context.Background()
	kind := repository.LifecycleJobReclaim
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100) // npm calls once, then stops
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100)
	source.jobs[kind] = []repository.LifecycleJob{job("npm-1", repository.FormatNPM)}
	broker.Wake(repository.LifecycleJobChannel(kind))
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100) // parks npm-1
	now = now.Add(FormatIdleAfter)
	broker.RenewParked(ctx)
	if len(source.released) != 1 || source.released[0] != "npm-1" || len(source.renewed) != 0 {
		t.Fatalf("released %v, renewed %v; want the idle format's job released, not renewed", source.released, source.renewed)
	}
	source.jobs[kind] = []repository.LifecycleJob{job("npm-2", repository.FormatNPM)}
	broker.Wake(repository.LifecycleJobChannel(kind))
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100)
	if last := source.formats[len(source.formats)-1]; len(last) != 1 || last[0] != repository.FormatRaw {
		t.Fatalf("claim asked for %v, want only the format whose worker still calls", last)
	}
	// The idle worker comes back: no claim asked for it since its previous call, so it claims at once.
	jobs, _ := broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100)
	if len(jobs) != 1 || jobs[0].ID != "npm-2" {
		t.Fatalf("the returning worker received %v, want npm-2", jobs)
	}
}

// A job parked for MaxParkedAge is released back to the queue even while its format's worker still calls.
func TestClaimBrokerReleasesJobsParkedTooLong(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}}
	broker := NewClaimBroker(source)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	broker.now = func() time.Time { return now }
	ctx := context.Background()
	kind := repository.LifecycleJobScan
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 1)
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 1)
	source.jobs[kind] = []repository.LifecycleJob{job("a", repository.FormatNPM), job("b", repository.FormatNPM)}
	broker.Wake(repository.LifecycleJobChannel(kind))
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 1) // parks a for npm (limit 1)
	for step := time.Duration(0); step < MaxParkedAge; step += ParkedRenewInterval / 2 {
		now = now.Add(ParkedRenewInterval / 2)
		broker.mu.Lock()
		broker.jobs[kind].mu.Lock()
		broker.jobs[kind].seenAt[repository.FormatNPM] = now // npm keeps calling but is busy
		broker.jobs[kind].mu.Unlock()
		broker.mu.Unlock()
		broker.RenewParked(ctx)
	}
	if len(source.released) != 1 || source.released[0] != "a" {
		t.Fatalf("released %v, want the job parked for MaxParkedAge", source.released)
	}
}

// A claim does not ask for a format whose bucket already holds a full claim.
func TestClaimBrokerDoesNotClaimForAFullBucket(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}}
	broker := NewClaimBroker(source)
	ctx := context.Background()
	kind := repository.LifecycleJobPromotion
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 2)
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 2)
	source.jobs[kind] = []repository.LifecycleJob{job("n1", repository.FormatNPM), job("n2", repository.FormatNPM)}
	broker.Wake(repository.LifecycleJobChannel(kind))
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 2) // parks n1 and n2
	broker.Wake(repository.LifecycleJobChannel(kind))
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 2)
	if last := source.formats[len(source.formats)-1]; len(last) != 1 || last[0] != repository.FormatRaw {
		t.Fatalf("claim asked for %v, want npm left out while its bucket is full", last)
	}
}

// A release that fails transiently keeps the item parked, and the next pass releases it.
func TestClaimBrokerRetriesAFailedRelease(t *testing.T) {
	source := &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}, stuck: map[string]bool{"npm-1": true}}
	broker := NewClaimBroker(source)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	broker.now = func() time.Time { return now }
	ctx := context.Background()
	kind := repository.LifecycleJobReclaim
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100)
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100)
	source.jobs[kind] = []repository.LifecycleJob{job("npm-1", repository.FormatNPM)}
	broker.Wake(repository.LifecycleJobChannel(kind))
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100)
	now = now.Add(FormatIdleAfter)
	broker.RenewParked(ctx) // the release fails: npm-1 waits for the next pass, out of the workers' reach
	if jobs, _ := broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100); len(jobs) != 0 {
		t.Fatalf("a job whose release failed was handed to a worker: %v", jobs)
	}
	source.mu.Lock()
	source.stuck["npm-1"] = false
	source.mu.Unlock()
	broker.RenewParked(ctx)
	if len(source.released) != 2 || source.released[1] != "npm-1" {
		t.Fatalf("released %v, want npm-1 tried again after the failed release", source.released)
	}
}

// A failed claim answers nobody: the other formats it asked for claim on their next call.
func TestClaimBrokerFailedClaimDoesNotAnswerOtherFormats(t *testing.T) {
	source := &flakyClaimSource{countingSource: &countingSource{jobs: map[repository.LifecycleJobKind][]repository.LifecycleJob{}}}
	broker := NewClaimBroker(source)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	broker.now = func() time.Time { return now }
	ctx := context.Background()
	kind := repository.LifecycleJobRetention
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100)
	_, _ = broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100)
	source.fail = true
	broker.Wake(repository.LifecycleJobChannel(kind))
	if _, err := broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatRaw, 100); err == nil {
		t.Fatal("the failed claim must reach the worker")
	}
	source.fail = false
	source.jobs[kind] = []repository.LifecycleJob{job("npm-1", repository.FormatNPM)}
	before := broker.ClaimCalls(kind)
	if jobs, _ := broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100); len(jobs) != 0 || broker.ClaimCalls(kind) != before {
		t.Fatalf("another format claimed right after a failed claim: %v", jobs)
	}
	now = now.Add(ClaimRetryAfter)
	jobs, _ := broker.ClaimLifecycleJobsByKindAndFormat(ctx, kind, repository.FormatNPM, 100)
	if len(jobs) != 1 || jobs[0].ID != "npm-1" {
		t.Fatalf("npm received %v after another format's failed claim, want npm-1", jobs)
	}
}

type flakyClaimSource struct {
	*countingSource
	fail bool
}

func (s *flakyClaimSource) ClaimLifecycleJobsByKindAndFormats(ctx context.Context, kind repository.LifecycleJobKind, formats []repository.Format, limit int) ([]repository.LifecycleJob, error) {
	if s.fail {
		return nil, errors.New("claim failed")
	}
	return s.countingSource.ClaimLifecycleJobsByKindAndFormats(ctx, kind, formats, limit)
}
