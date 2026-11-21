package repository

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/mlops-ca/notarx/internal/events"
)

// DomainEventStore is the read side of the domain-event log for internal consumers (events.Dispatcher).
type DomainEventStore interface {
	events.Source
}

var (
	_ DomainEventStore = (*PostgresStore)(nil)
	_ DomainEventStore = (*MemoryStore)(nil)
	_ events.LagSource = (*PostgresStore)(nil)
	_ events.LagSource = (*MemoryStore)(nil)
)

// enqueueWebhookEventPostgres emits the event in tx with events.Emit and reports what was written.
func enqueueWebhookEventPostgres(ctx context.Context, tx sqlRunner, event WebhookEvent) (events.Result, error) {
	return events.Emit(ctx, tx, event.domainEvent())
}

// emitHorizonExpr is the start of the oldest open transaction of this role that has begun to emit: emit_domain_event
// stamps the session with application_name 'notarx-emit:<epoch microseconds>' at the transaction's first emission and
// the setting reverts when it ends. A transaction that has not emitted yet is not counted: its events will be stamped
// after the instant it first emits, which the settle delay keeps above the read bound. The stamp is parsed only when
// it matches the pattern, so another session's application_name cannot break the read.
const emitHorizonExpr = `SELECT COALESCE(min(CASE WHEN application_name ~ '^notarx-emit:[0-9]{1,18}$' THEN to_timestamp(substring(application_name from 13)::numeric / 1000000) END), 'infinity'::timestamptz)
FROM pg_stat_activity WHERE datname = current_database() AND usename = current_user AND backend_type = 'client backend' AND pid <> pg_backend_pid()`

const readDomainEventsQuery = `SELECT seq,id::text,type,COALESCE(repository_id::text,''),COALESCE(format,''),COALESCE(coordinate,''),COALESCE(digest,''),payload,occurred_at
FROM domain_events WHERE type = ANY($1::text[]) AND occurred_at >= $2 AND (occurred_at,seq) > ($2,$3)
  AND occurred_at < LEAST(clock_timestamp() - $4::interval, (` + emitHorizonExpr + `))
ORDER BY occurred_at,seq LIMIT $5`

// ReadEvents implements events.Source. A consumer without a cursor row starts at the present: its cursor is
// created at the database clock and nothing older is replayed. occurred_at is the database clock (events.Emit
// passes no time of its own), so an event not yet committed has an occurred_at at or after the first emission of
// its transaction: reading only below the oldest such emission (emitHorizonExpr), and below the settle horizon,
// means an event that commits late is never behind the cursor. Transactions that never emit, however long, do not
// hold the log back; one that emits delays the read from its first emission until it ends, so the bound on the
// delay is the time an emitting transaction stays open after its first emission.
func (s *PostgresStore) ReadEvents(ctx context.Context, consumer string, types []events.Type, settle time.Duration, limit int) ([]events.Stored, error) {
	var cursor events.Cursor
	err := s.db.QueryRowContext(ctx, `SELECT occurred_at,seq FROM domain_event_cursors WHERE consumer=$1`, consumer).Scan(&cursor.OccurredAt, &cursor.Seq)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx, `INSERT INTO domain_event_cursors (consumer,occurred_at,seq) VALUES ($1,clock_timestamp(),0)
ON CONFLICT (consumer) DO NOTHING RETURNING occurred_at,seq`, consumer).Scan(&cursor.OccurredAt, &cursor.Seq)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // another node created it first; read on the next pass
		}
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = string(t)
	}
	rows, err := s.db.QueryContext(ctx, readDomainEventsQuery, names, cursor.OccurredAt, cursor.Seq, intervalOf(settle), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []events.Stored
	for rows.Next() {
		var item events.Stored
		var payload []byte
		var eventType string
		if err = rows.Scan(&item.Position.Seq, &item.Event.ID, &eventType, &item.Event.RepositoryID, &item.Event.Format, &item.Event.Coordinate, &item.Event.Digest, &payload, &item.Event.OccurredAt); err != nil {
			return nil, err
		}
		item.Position.OccurredAt = item.Event.OccurredAt
		item.Event.Type, item.Event.Payload = events.Type(eventType), payload
		out = append(out, item)
	}
	return out, rows.Err()
}

// EventHorizonLag implements events.LagSource: how long the oldest emitting transaction has been open, or zero.
func (s *PostgresStore) EventHorizonLag(ctx context.Context) (time.Duration, error) {
	var seconds float64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(extract(epoch FROM clock_timestamp() - h), 0) FROM (`+emitHorizonExpr+`) AS t(h) WHERE h < 'infinity'`).Scan(&seconds)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return time.Duration(seconds * float64(time.Second)), err
}

// EventHorizonLag implements events.LagSource; the in-memory store has no open transactions.
func (s *MemoryStore) EventHorizonLag(context.Context) (time.Duration, error) { return 0, nil }

// AdvanceCursor implements events.Source; the cursor only moves forward.
func (s *PostgresStore) AdvanceCursor(ctx context.Context, consumer string, cursor events.Cursor) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO domain_event_cursors (consumer,occurred_at,seq) VALUES ($1,$2,$3)
ON CONFLICT (consumer) DO UPDATE SET occurred_at=EXCLUDED.occurred_at,seq=EXCLUDED.seq,updated_at=now()
WHERE (domain_event_cursors.occurred_at,domain_event_cursors.seq) < (EXCLUDED.occurred_at,EXCLUDED.seq)`, consumer, cursor.OccurredAt, cursor.Seq)
	return err
}

// ReadEvents implements events.Source for the in-memory store.
func (s *MemoryStore) ReadEvents(_ context.Context, consumer string, types []events.Type, settle time.Duration, limit int) ([]events.Stored, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventCursors == nil {
		s.eventCursors = map[string]events.Cursor{}
	}
	cursor, ok := s.eventCursors[consumer]
	if !ok {
		cursor = events.Cursor{OccurredAt: time.Now().UTC()}
		s.eventCursors[consumer] = cursor
	}
	wanted := make(map[events.Type]bool, len(types))
	for _, t := range types {
		wanted[t] = true
	}
	horizon := time.Now().UTC().Add(-settle)
	var out []events.Stored
	for id, event := range s.webhookEvents {
		position := events.Cursor{OccurredAt: event.OccurredAt, Seq: s.eventSeqs[id]}
		if !wanted[event.Type] || !cursorAfter(position, cursor) || event.OccurredAt.After(horizon) {
			continue
		}
		out = append(out, events.Stored{Event: cloneWebhookEvent(event).domainEvent(), Position: position})
	}
	sort.Slice(out, func(i, j int) bool { return cursorAfter(out[j].Position, out[i].Position) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// AdvanceCursor implements events.Source for the in-memory store.
func (s *MemoryStore) AdvanceCursor(_ context.Context, consumer string, cursor events.Cursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventCursors == nil {
		s.eventCursors = map[string]events.Cursor{}
	}
	if current, ok := s.eventCursors[consumer]; !ok || cursorAfter(cursor, current) {
		s.eventCursors[consumer] = cursor
	}
	return nil
}

// cursorAfter reports whether a is strictly after b in (occurred_at, seq) order.
func cursorAfter(a, b events.Cursor) bool {
	if !a.OccurredAt.Equal(b.OccurredAt) {
		return a.OccurredAt.After(b.OccurredAt)
	}
	return a.Seq > b.Seq
}
