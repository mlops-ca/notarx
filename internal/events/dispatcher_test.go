package events

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/tasklease"
)

type fakeLeases struct {
	held     bool
	err      error
	acquired []string
	released []string
}

func (f *fakeLeases) AcquireInternalTaskLease(_ context.Context, task tasklease.Task, owner string, _, _ time.Duration) (bool, error) {
	f.acquired = append(f.acquired, task.Kind+"|"+task.Name+"|"+owner)
	return f.held, f.err
}

func (f *fakeLeases) ReleaseInternalTaskLease(_ context.Context, task tasklease.Task, owner string, completed bool) error {
	if completed {
		return errors.New("a dispatcher run must release uncompleted")
	}
	f.released = append(f.released, task.ID)
	return nil
}

type fakeSource struct {
	advances int
	events   []Stored
	cursors  map[string]Cursor
	readErr  error
	advErr   error
	settle   time.Duration
	consumer string
	starts   []string
	lookback time.Duration
	startErr error
}

func (f *fakeSource) ReadEvents(_ context.Context, consumer string, types []Type, settle time.Duration, limit int) ([]Stored, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	f.settle, f.consumer = settle, consumer
	cursor := f.cursors[consumer]
	var out []Stored
	for _, item := range f.events {
		if item.Position.Seq > cursor.Seq && len(out) < limit {
			out = append(out, item)
		}
	}
	return out, nil
}

func (f *fakeSource) StartCursor(_ context.Context, consumer string, lookback time.Duration) error {
	f.starts = append(f.starts, consumer)
	f.lookback = lookback
	return f.startErr
}

func (f *fakeSource) AdvanceCursor(_ context.Context, consumer string, cursor Cursor) error {
	if f.advErr != nil {
		return f.advErr
	}
	if f.cursors == nil {
		f.cursors = map[string]Cursor{}
	}
	f.advances++
	f.cursors[consumer] = cursor
	return nil
}

func stored(seq int64, id string) Stored {
	return Stored{Event: Event{ID: id, Type: ArtifactPublished}, Position: Cursor{OccurredAt: time.Unix(seq, 0), Seq: seq}}
}

func TestDispatcherDeliversInOrderAndAdvances(t *testing.T) {
	source := &fakeSource{events: []Stored{stored(1, "a"), stored(2, "b"), stored(3, "c")}}
	var seen []string
	d := &Dispatcher{Source: source, Batch: 2, Consumers: []Consumer{{Name: "c1", Types: []Type{ArtifactPublished}, Handler: HandlerFunc(func(_ context.Context, e Event) error {
		seen = append(seen, e.ID)
		return nil
	})}}}
	if n, err := d.RunOnce(context.Background()); err != nil || n != 2 {
		t.Fatalf("pass 1: n=%d err=%v", n, err)
	}
	if n, err := d.RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("pass 2: n=%d err=%v", n, err)
	}
	if n, _ := d.RunOnce(context.Background()); n != 0 {
		t.Fatalf("pass 3 handled %d", n)
	}
	if len(seen) != 3 || seen[0] != "a" || seen[2] != "c" || source.settle != 5*time.Second {
		t.Fatalf("seen=%v settle=%v", seen, source.settle)
	}
	if source.advances != 2 {
		t.Fatalf("the cursor moved %d times, want once per non-empty batch", source.advances)
	}
}

func TestDispatcherRetriesTransientAndSkipsPermanentErrors(t *testing.T) {
	source := &fakeSource{events: []Stored{stored(1, "a"), stored(2, "b")}}
	fail := true
	handler := HandlerFunc(func(_ context.Context, e Event) error {
		if e.ID == "a" && fail {
			return errors.New("transient")
		}
		if e.ID == "b" {
			return Permanent(errors.New("gone"))
		}
		return nil
	})
	clock := time.Unix(1_000, 0)
	d := &Dispatcher{Source: source, Now: func() time.Time { return clock }, Consumers: []Consumer{{Name: "c1", Handler: handler}}}
	if n, err := d.RunOnce(context.Background()); err == nil || n != 0 || source.cursors["c1"].Seq != 0 {
		t.Fatalf("transient: n=%d err=%v cursor=%v", n, err, source.cursors["c1"])
	}
	fail = false
	clock = clock.Add(time.Minute)
	if n, err := d.RunOnce(context.Background()); err != nil || n != 2 || source.cursors["c1"].Seq != 2 {
		t.Fatalf("recovered: n=%d err=%v cursor=%v", n, err, source.cursors["c1"])
	}
	if Permanent(nil) != nil {
		t.Error("Permanent(nil) must be nil")
	}
	if err := Permanent(errors.New("x")); !errors.Is(err, err) || err.Error() != "x" {
		t.Error("Permanent must keep the message")
	}
}

func TestDispatcherBacksOffAndGivesUpAPoisonedEventAfterMaxAttempts(t *testing.T) {
	source := &fakeSource{events: []Stored{stored(1, "poison"), stored(2, "next")}}
	clock := time.Unix(1_000, 0)
	var seen []string
	d := &Dispatcher{Source: source, MaxAttempts: 3, Interval: time.Second, Now: func() time.Time { return clock }, Consumers: []Consumer{{Name: "c1", Handler: HandlerFunc(func(_ context.Context, e Event) error {
		seen = append(seen, e.ID)
		if e.ID == "poison" {
			return errors.New("always failing")
		}
		return nil
	})}}}
	if n, err := d.RunOnce(context.Background()); err == nil || n != 0 {
		t.Fatalf("attempt 1: n=%d err=%v", n, err)
	}
	// Inside the backoff window (1s after the first failure) nothing is retried and nothing is reported.
	if n, err := d.RunOnce(context.Background()); err != nil || n != 0 || len(seen) != 1 {
		t.Fatalf("backoff: n=%d err=%v seen=%v", n, err, seen)
	}
	clock = clock.Add(time.Second)
	if n, err := d.RunOnce(context.Background()); err == nil || n != 0 || len(seen) != 2 {
		t.Fatalf("attempt 2: n=%d err=%v seen=%v", n, err, seen)
	}
	// The second failure doubles the delay.
	clock = clock.Add(time.Second)
	if n, _ := d.RunOnce(context.Background()); n != 0 || len(seen) != 2 {
		t.Fatalf("a doubled backoff was not honoured: seen=%v", seen)
	}
	clock = clock.Add(time.Second)
	if n, err := d.RunOnce(context.Background()); err != nil || n != 2 || source.cursors["c1"].Seq != 2 {
		t.Fatalf("attempt 3 must skip the poisoned event: n=%d err=%v cursor=%v", n, err, source.cursors["c1"])
	}
	if len(d.failures) != 0 {
		t.Fatalf("failure state leaked: %v", d.failures)
	}
	capped := &Dispatcher{Interval: time.Hour, Now: func() time.Time { return clock }}
	capped.failures = map[string]*failure{}
	capped.giveUp(Consumer{Name: "c"}, stored(1, "x"), errors.New("x"))
	if got := capped.failures["c/x"].next.Sub(clock); got != MaxBackoff {
		t.Fatalf("backoff = %v, want the %v cap", got, MaxBackoff)
	}
}

func TestDispatcherServesOnlyUnderTheLease(t *testing.T) {
	source := &fakeSource{events: []Stored{stored(1, "a")}}
	leases := &fakeLeases{}
	var seen int
	d := &Dispatcher{Source: source, Leases: leases, Owner: "node-1", Consumers: []Consumer{{Name: "scan", Handler: HandlerFunc(func(context.Context, Event) error {
		seen++
		return nil
	})}}}
	if n, err := d.RunOnce(context.Background()); err != nil || n != 0 || seen != 0 {
		t.Fatalf("a node without the lease served: n=%d err=%v", n, err)
	}
	leases.held = true
	if n, err := d.RunOnce(context.Background()); err != nil || n != 1 || seen != 1 {
		t.Fatalf("the lease holder did not serve: n=%d err=%v", n, err)
	}
	if n, err := d.RunOnce(context.Background()); err != nil || n != 0 {
		t.Fatalf("second pass: n=%d err=%v", n, err)
	}
	if len(leases.acquired) != 3 || leases.acquired[0] != "event-dispatcher|internal:event-dispatcher:scan|node-1" || len(leases.released) != 0 {
		t.Fatalf("the lease must be renewed on every pass and kept between passes: acquired=%v released=%v", leases.acquired, leases.released)
	}
	d.Close(context.Background())
	d.Close(context.Background())
	if len(leases.released) != 1 || leases.released[0] != LeaseTask("scan").ID {
		t.Fatalf("Close released %v", leases.released)
	}
	first, again := LeaseTask("scan"), LeaseTask("scan")
	if first.ID != again.ID || first.ID == LeaseTask("other").ID {
		t.Error("the lease id must be stable per consumer")
	}
	leases.err = errors.New("db down")
	if _, err := d.RunOnce(context.Background()); err == nil {
		t.Error("lease failure swallowed")
	}
	(&Dispatcher{}).Close(context.Background())
	if (&Dispatcher{}).owner() == "" {
		t.Error("no default owner")
	}
}

func TestDispatcherReportsSourceFailures(t *testing.T) {
	d := &Dispatcher{Source: &fakeSource{readErr: errors.New("down")}, Consumers: []Consumer{{Name: "c1", Handler: HandlerFunc(func(context.Context, Event) error { return nil })}}}
	if _, err := d.RunOnce(context.Background()); err == nil {
		t.Error("read failure swallowed")
	}
	d = &Dispatcher{Source: &fakeSource{events: []Stored{stored(1, "a")}, advErr: errors.New("cursor")}, Consumers: []Consumer{{Name: "c1", Handler: HandlerFunc(func(context.Context, Event) error { return nil })}}}
	if n, err := d.RunOnce(context.Background()); err == nil || n != 0 {
		t.Errorf("advance failure: n=%d err=%v", n, err)
	}
}

type lagSource struct {
	fakeSource
	lag time.Duration
	err error
}

func (l *lagSource) EventHorizonLag(context.Context) (time.Duration, error) { return l.lag, l.err }

func TestDispatcherObservesAndWarnsAboutHorizonLag(t *testing.T) {
	source := &lagSource{lag: 3 * time.Minute}
	var logs bytes.Buffer
	var observed []time.Duration
	now := time.Unix(1000, 0)
	d := &Dispatcher{Source: source, Logger: slog.New(slog.NewTextHandler(&logs, nil)), ObserveLag: func(lag time.Duration) { observed = append(observed, lag) },
		Now: func() time.Time { return now }, Consumers: []Consumer{{Name: "c1", Handler: HandlerFunc(func(context.Context, Event) error { return nil })}}}
	for range 2 {
		if _, err := d.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(observed) != 2 || observed[0] != 3*time.Minute {
		t.Fatalf("observed = %v", observed)
	}
	if n := strings.Count(logs.String(), "held back"); n != 1 {
		t.Fatalf("warnings within the interval = %d\n%s", n, logs.String())
	}
	now = now.Add(HorizonWarnEvery)
	if _, err := d.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(logs.String(), "held back"); n != 2 {
		t.Fatalf("warnings after the interval = %d", n)
	}
	source.lag = time.Second
	logs.Reset()
	now = now.Add(time.Hour)
	if _, err := d.RunOnce(context.Background()); err != nil || logs.Len() != 0 {
		t.Fatalf("a small lag was logged: %v %s", err, logs.String())
	}
	source.err = errors.New("stats down")
	if _, err := d.RunOnce(context.Background()); err != nil || !strings.Contains(logs.String(), "lag unavailable") {
		t.Fatalf("a lag failure must be logged without failing the pass: %v %s", err, logs.String())
	}
}

func TestDispatcherStartsACatchUpConsumerOnceBeforeReading(t *testing.T) {
	source := &fakeSource{events: []Stored{stored(1, "a")}}
	consumer := Consumer{Name: "late", CatchUp: 72 * time.Hour, Handler: HandlerFunc(func(context.Context, Event) error { return nil })}
	plain := Consumer{Name: "plain", Handler: consumer.Handler}
	d := &Dispatcher{Source: source, Consumers: []Consumer{consumer, plain}}
	for range 2 {
		if _, err := d.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(source.starts) != 1 || source.starts[0] != "late" || source.lookback != 72*time.Hour {
		t.Fatalf("starts = %v lookback = %v; want one start of the catch-up consumer", source.starts, source.lookback)
	}
	source.startErr = errors.New("down")
	d = &Dispatcher{Source: source, Consumers: []Consumer{consumer}}
	if _, err := d.RunOnce(context.Background()); err == nil {
		t.Fatal("a failed cursor start was swallowed")
	}
	source.startErr = nil
	if _, err := d.RunOnce(context.Background()); err != nil || len(source.starts) != 3 {
		t.Fatalf("the start must be retried after a failure: %v %v", err, source.starts)
	}
}

func TestDispatcherRunStopsWithTheContext(t *testing.T) {
	source := &fakeSource{events: []Stored{stored(1, "a")}}
	handled := make(chan struct{}, 1)
	d := &Dispatcher{Source: source, Interval: time.Millisecond, Consumers: []Consumer{{Name: "c1", Handler: HandlerFunc(func(context.Context, Event) error {
		select {
		case handled <- struct{}{}:
		default:
		}
		return nil
	})}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	<-handled
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}
