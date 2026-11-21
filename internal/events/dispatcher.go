package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/tasklease"
)

// Cursor is a position in the log: the occurred_at and seq of the last event handled.
type Cursor struct {
	OccurredAt time.Time
	Seq        int64
}

// Stored is an event read from the log with its position.
type Stored struct {
	Event    Event
	Position Cursor
}

// Source reads the log for internal consumers and keeps their cursors.
type Source interface {
	// ReadEvents returns up to limit events of the given types after the consumer's cursor, in
	// (occurred_at, seq) order. It leaves out events younger than settle and events that a transaction
	// still in flight could be about to commit before them, so no event is skipped by a late commit. A
	// consumer without a cursor starts at the present.
	ReadEvents(ctx context.Context, consumer string, types []Type, settle time.Duration, limit int) ([]Stored, error)
	// AdvanceCursor moves the cursor forward; it never moves it back.
	AdvanceCursor(ctx context.Context, consumer string, cursor Cursor) error
}

// Leaser elects the one node that reads the log for a consumer (repository.InternalTaskLeaseStore).
type Leaser interface {
	AcquireInternalTaskLease(ctx context.Context, task tasklease.Task, owner string, ttl, minInterval time.Duration) (bool, error)
	ReleaseInternalTaskLease(ctx context.Context, task tasklease.Task, owner string, completed bool) error
}

// Handler consumes one event. Delivery is at least once, so a handler must be idempotent.
type Handler interface {
	HandleEvent(ctx context.Context, event Event) error
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, event Event) error

// HandleEvent calls f.
func (f HandlerFunc) HandleEvent(ctx context.Context, event Event) error { return f(ctx, event) }

// Consumer is an in-process reader of the log.
type Consumer struct {
	Name    string
	Types   []Type
	Handler Handler
}

type permanentError struct{ error }

func (e permanentError) Unwrap() error { return e.error }

// Permanent marks a handler error that retrying cannot fix; the dispatcher logs it and moves past the event.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// DefaultMaxAttempts is how many times in a row a handler may fail on one event before the dispatcher
// gives the event up, logs it and moves on, so one poisoned event cannot block the consumer for good.
const DefaultMaxAttempts = 5

// dispatchLeaseTTL bounds how long a crashed dispatcher keeps the other nodes out.
const dispatchLeaseTTL = time.Minute

// Dispatcher hands each consumer the events it asked for, in order, advancing its cursor once per batch. One
// node at a time serves a consumer (a lease row of scheduled_tasks). A transient handler error stops that
// consumer for the pass and the event is retried, up to MaxAttempts times in a row.
type Dispatcher struct {
	Source    Source
	Consumers []Consumer
	// Leases elects the serving node; nil serves every consumer on every call (a single node, tests).
	Leases Leaser
	// Owner names this node in the lease; it defaults to a random id.
	Owner string
	// Batch is the events read per consumer and pass (default 100); Settle the age an event must reach
	// before it is read (default 5s); Interval the pause between passes (default 2s); MaxAttempts the
	// failures in a row after which an event is given up (default DefaultMaxAttempts).
	Batch       int
	Settle      time.Duration
	Interval    time.Duration
	MaxAttempts int
	Logger      *slog.Logger

	failures map[string]int
}

func (d *Dispatcher) batch() int {
	if d.Batch > 0 {
		return d.Batch
	}
	return 100
}

func (d *Dispatcher) settle() time.Duration {
	if d.Settle > 0 {
		return d.Settle
	}
	return 5 * time.Second
}

func (d *Dispatcher) maxAttempts() int {
	if d.MaxAttempts > 0 {
		return d.MaxAttempts
	}
	return DefaultMaxAttempts
}

func (d *Dispatcher) owner() string {
	if d.Owner == "" {
		d.Owner = ids.New()
	}
	return d.Owner
}

// leaseTask is the lease row of one consumer; its id is derived from the consumer name.
func leaseTask(consumer string) tasklease.Task {
	return tasklease.Task{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("notarx/event-dispatcher/"+consumer)).String(), Name: "internal:event-dispatcher:" + consumer,
		Kind: "event-dispatcher", Description: "Internal lease row for the event dispatcher of " + consumer}
}

// RunOnce performs one pass and returns the number of events handled.
func (d *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	handled := 0
	var errs []error
	for _, consumer := range d.Consumers {
		n, err := d.serve(ctx, consumer)
		handled += n
		if err != nil {
			errs = append(errs, err)
		}
	}
	return handled, errors.Join(errs...)
}

// serve runs one batch of one consumer under its lease.
func (d *Dispatcher) serve(ctx context.Context, consumer Consumer) (int, error) {
	if d.Leases != nil {
		task := leaseTask(consumer.Name)
		held, err := d.Leases.AcquireInternalTaskLease(ctx, task, d.owner(), dispatchLeaseTTL, 0)
		if err != nil {
			return 0, fmt.Errorf("events: lease of %s: %w", consumer.Name, err)
		}
		if !held {
			return 0, nil
		}
		defer func() {
			release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = d.Leases.ReleaseInternalTaskLease(release, task, d.owner(), false)
		}()
	}
	stored, err := d.Source.ReadEvents(ctx, consumer.Name, consumer.Types, d.settle(), d.batch())
	if err != nil {
		return 0, fmt.Errorf("events: read for %s: %w", consumer.Name, err)
	}
	var last *Cursor
	handled := 0
	var failure error
	for i := range stored {
		item := stored[i]
		if err = consumer.Handler.HandleEvent(ctx, item.Event); err != nil && !d.giveUp(consumer, item, err) {
			failure = fmt.Errorf("events: %s handling %s: %w", consumer.Name, item.Event.Type, err)
			break
		}
		delete(d.failures, consumer.Name+"/"+item.Event.ID)
		last = &stored[i].Position
		handled++
	}
	if last != nil {
		if err = d.Source.AdvanceCursor(ctx, consumer.Name, *last); err != nil {
			return 0, errors.Join(failure, fmt.Errorf("events: advance cursor of %s: %w", consumer.Name, err))
		}
	}
	return handled, failure
}

// giveUp reports whether the event is skipped after a handler error: always for a permanent error, and for a
// transient one once it has failed MaxAttempts times in a row.
func (d *Dispatcher) giveUp(consumer Consumer, item Stored, err error) bool {
	var permanent permanentError
	if !errors.As(err, &permanent) {
		if d.failures == nil {
			d.failures = map[string]int{}
		}
		key := consumer.Name + "/" + item.Event.ID
		d.failures[key]++
		if d.failures[key] < d.maxAttempts() {
			return false
		}
	}
	if d.Logger != nil {
		d.Logger.Error("event skipped after a failed handler", "consumer", consumer.Name, "type", item.Event.Type, "event_id", item.Event.ID, "error", err)
	}
	return true
}

// Run passes until ctx ends, logging failures.
func (d *Dispatcher) Run(ctx context.Context) {
	interval := d.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := d.RunOnce(ctx); err != nil && ctx.Err() == nil && d.Logger != nil {
			d.Logger.Error("event dispatch failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
