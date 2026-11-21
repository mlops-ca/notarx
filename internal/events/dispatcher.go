package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
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
	// (occurred_at, seq) order, leaving out events younger than settle so a transaction that commits
	// slightly late is not skipped. A consumer without a cursor starts at the present.
	ReadEvents(ctx context.Context, consumer string, types []Type, settle time.Duration, limit int) ([]Stored, error)
	// AdvanceCursor moves the cursor forward; it never moves it back.
	AdvanceCursor(ctx context.Context, consumer string, cursor Cursor) error
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

// Dispatcher hands each consumer the events it asked for, in order, advancing its cursor after every
// handled event. A transient handler error stops that consumer for the pass and the event is retried.
type Dispatcher struct {
	Source    Source
	Consumers []Consumer
	// Batch is the events read per consumer and pass (default 100); Settle the age an event must reach
	// before it is read (default 5s); Interval the pause between passes (default 2s).
	Batch    int
	Settle   time.Duration
	Interval time.Duration
	Logger   *slog.Logger
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

// RunOnce performs one pass and returns the number of events handled.
func (d *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	handled := 0
	var errs []error
	for _, consumer := range d.Consumers {
		stored, err := d.Source.ReadEvents(ctx, consumer.Name, consumer.Types, d.settle(), d.batch())
		if err != nil {
			errs = append(errs, fmt.Errorf("events: read for %s: %w", consumer.Name, err))
			continue
		}
		for _, item := range stored {
			if err = consumer.Handler.HandleEvent(ctx, item.Event); err != nil {
				var permanent permanentError
				if !errors.As(err, &permanent) {
					errs = append(errs, fmt.Errorf("events: %s handling %s: %w", consumer.Name, item.Event.Type, err))
					break
				}
				if d.Logger != nil {
					d.Logger.Warn("event skipped after a permanent handler error", "consumer", consumer.Name, "type", item.Event.Type, "event_id", item.Event.ID, "error", err)
				}
			}
			if err = d.Source.AdvanceCursor(ctx, consumer.Name, item.Position); err != nil {
				errs = append(errs, fmt.Errorf("events: advance cursor of %s: %w", consumer.Name, err))
				break
			}
			handled++
		}
	}
	return handled, errors.Join(errs...)
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
