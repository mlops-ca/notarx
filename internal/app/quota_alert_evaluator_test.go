package app

import (
	"context"
	"github.com/mlops-ca/notarx/internal/repository"
	"testing"
	"time"
)

type quotaRoutingCapture struct {
	repository.RepositoryQuotaAlertStore
	mail  repository.QuotaAlertMailConfig
	calls int
}

func (s *quotaRoutingCapture) EvaluateNextRepositoryQuotaAlert(_ context.Context, cfg repository.QuotaAlertMailConfig, interval time.Duration) (bool, error) {
	s.mail = cfg
	s.calls++
	return false, nil
}
func TestQuotaEvaluatorMissingEncryptionMarksRoutingUnavailable(t *testing.T) {
	t.Setenv("NOTARX_SETTINGS_ENCRYPTION_KEY", "")
	capture := &quotaRoutingCapture{}
	_, err := (QuotaAlertEvaluator{Store: capture, Mail: repository.QuotaAlertMailConfig{Enabled: true, From: "synthetic@example.test"}}).Run(context.Background())
	if err != nil || capture.calls != 1 || capture.mail.Enabled || capture.mail.UnavailableCode != "encryption_key_unavailable" {
		t.Fatalf("missing encryption permitted new queued mail: %#v %v", capture, err)
	}
}

type quotaPollTimingStore struct {
	repository.RepositoryQuotaAlertStore
	started  chan struct{}
	release  chan struct{}
	finished chan time.Time
	next     chan time.Time
	calls    int
}

func (s *quotaPollTimingStore) EvaluateNextRepositoryQuotaAlert(ctx context.Context, _ repository.QuotaAlertMailConfig, _ time.Duration) (bool, error) {
	s.calls++
	if s.calls == 1 {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		s.finished <- time.Now()
	} else {
		s.next <- time.Now()
	}
	return false, nil
}
func TestQuotaPollingWaitsAfterCompletedEvaluation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &quotaPollTimingStore{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan time.Time, 1), next: make(chan time.Time, 10)}
	const interval = 100 * time.Millisecond
	(QuotaAlertEvaluator{Store: s}).Start(ctx, interval)
	select {
	case <-s.started:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not start")
	}
	// A delayed evaluation must not consume a buffered tick and immediately repoll.
	time.Sleep(150 * time.Millisecond)
	close(s.release)
	finished := <-s.finished
	select {
	case next := <-s.next:
		if next.Sub(finished) < interval {
			t.Fatalf("poll interval measured before evaluation finished: %v", next.Sub(finished))
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not continue")
	}
}

type quotaWakeStore struct {
	repository.RepositoryQuotaAlertStore
	wake  chan struct{}
	calls chan struct{}
}

func (s *quotaWakeStore) Listen(_ context.Context, channel string) <-chan struct{} {
	if channel != repository.QuotaEvaluationChannel {
		return nil
	}
	return s.wake
}

func (s *quotaWakeStore) EvaluateNextRepositoryQuotaAlert(context.Context, repository.QuotaAlertMailConfig, time.Duration) (bool, error) {
	s.calls <- struct{}{}
	return false, nil
}

// A rule that becomes due wakes the evaluator on notarx_evaluations long before the poll interval, and the
// poll stays the fallback when the channel closes.
func TestQuotaEvaluationWakesOnTheEvaluationsChannelAndKeepsPolling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &quotaWakeStore{wake: make(chan struct{}, 1), calls: make(chan struct{}, 10)}
	const interval = time.Hour
	(QuotaAlertEvaluator{Store: s}).start(ctx, interval, time.Millisecond, time.Now)
	select {
	case <-s.calls: // the immediate pass at start
	case <-time.After(time.Second):
		t.Fatal("no initial evaluation")
	}
	s.wake <- struct{}{}
	select {
	case <-s.calls:
	case <-time.After(time.Second):
		t.Fatal("a notification on notarx_evaluations did not wake the evaluator")
	}
	close(s.wake) // a lost subscription must not stop the evaluator or spin it
	select {
	case <-s.calls:
		t.Fatal("a closed wake channel triggered an evaluation without a timer")
	case <-time.After(100 * time.Millisecond):
	}
}

// A burst of notifications right after a pass collapses into one more pass once the wake spacing has passed.
func TestQuotaEvaluationCoalescesABurstOfWakeUps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &quotaWakeStore{wake: make(chan struct{}, 1), calls: make(chan struct{}, 10)}
	const spacing = 200 * time.Millisecond
	(QuotaAlertEvaluator{Store: s}).start(ctx, time.Hour, spacing, time.Now)
	select {
	case <-s.calls: // the immediate pass at start
	case <-time.After(time.Second):
		t.Fatal("no initial evaluation")
	}
	for range 5 {
		s.wake <- struct{}{}
	}
	select {
	case <-s.calls:
	case <-time.After(spacing + time.Second):
		t.Fatal("the burst never triggered a pass")
	}
	select {
	case <-s.calls:
		t.Fatal("a burst of notifications triggered more than one pass")
	case <-time.After(2 * spacing):
	}
}
