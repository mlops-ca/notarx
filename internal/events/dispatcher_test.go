package events

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeSource struct {
	events   []Stored
	cursors  map[string]Cursor
	readErr  error
	advErr   error
	settle   time.Duration
	consumer string
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

func (f *fakeSource) AdvanceCursor(_ context.Context, consumer string, cursor Cursor) error {
	if f.advErr != nil {
		return f.advErr
	}
	if f.cursors == nil {
		f.cursors = map[string]Cursor{}
	}
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
	d := &Dispatcher{Source: source, Consumers: []Consumer{{Name: "c1", Handler: handler}}}
	if n, err := d.RunOnce(context.Background()); err == nil || n != 0 || source.cursors["c1"].Seq != 0 {
		t.Fatalf("transient: n=%d err=%v cursor=%v", n, err, source.cursors["c1"])
	}
	fail = false
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
