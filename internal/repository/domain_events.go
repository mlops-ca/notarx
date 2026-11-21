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
)

// enqueueWebhookEventPostgres emits the event in tx with events.Emit and reports what was written.
func enqueueWebhookEventPostgres(ctx context.Context, tx sqlRunner, event WebhookEvent) (events.Result, error) {
	return events.Emit(ctx, tx, event.domainEvent())
}

const readDomainEventsQuery = `SELECT seq,id::text,type,COALESCE(repository_id::text,''),COALESCE(format,''),COALESCE(coordinate,''),COALESCE(digest,''),payload,occurred_at
FROM domain_events WHERE type = ANY($1::text[]) AND occurred_at >= $2 AND (occurred_at,seq) > ($2,$3)
  AND occurred_at < LEAST(clock_timestamp() - $4::interval, COALESCE((SELECT min(xact_start) FROM pg_stat_activity WHERE datname = current_database() AND usename = current_user AND backend_type = 'client backend' AND pid <> pg_backend_pid()), 'infinity'::timestamptz))
ORDER BY occurred_at,seq LIMIT $5`

// ReadEvents implements events.Source. A consumer without a cursor row starts at the present: its cursor is
// created at the database clock and nothing older is replayed. occurred_at is the database clock (events.Emit
// passes no time of its own), so an event not yet committed has an occurred_at at or after the start of its
// transaction: reading only below the oldest open transaction's start, and below the settle horizon, means an
// event that commits late is never behind the cursor. Only transactions of the connecting role count: they are the
// only ones that emit, and another role's session (or a vacuum worker) must not hold the log back. A long open
// transaction of this role delays the read until it ends, which is what keeps the cursor honest.
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
