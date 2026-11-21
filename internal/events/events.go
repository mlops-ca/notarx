// Package events is the transactional domain-event log of ADR 0021.
//
// A state change emits its event inside its own transaction with Emit, which calls
// the emit_domain_event database function: it inserts one domain_events row and one
// webhook_deliveries row per matching enabled subscription, and writes nothing when no
// subscription and no internal consumer wants the type. The same function backs the
// database triggers that emit the Cargo distribution events, so there is one emission
// rule. Internal consumers read the log with a Dispatcher.
package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

// Type is a stable consumer-facing event name (ADR 0021, Domain events). Every format
// emits the same set; the format is a payload field.
type Type string

const (
	ArtifactPublished           Type = "artifact.published"
	ArtifactScanCompleted       Type = "artifact.scan.completed"
	ArtifactQuarantined         Type = "artifact.quarantined"
	ArtifactReleased            Type = "artifact.released"
	ArtifactPromoted            Type = "artifact.promoted"
	PromotionPendingApproval    Type = "promotion.pending_approval"
	PromotionApproved           Type = "promotion.approved"
	PromotionRejected           Type = "promotion.rejected"
	PromotionFailed             Type = "promotion.failed"
	ReplicationCompleted        Type = "replication.completed"
	ReplicationFailed           Type = "replication.failed"
	RepositoryVisibilityChanged Type = "repository.visibility_changed"
)

var catalogue = []Type{
	ArtifactPublished, ArtifactScanCompleted, ArtifactQuarantined, ArtifactReleased, ArtifactPromoted,
	PromotionPendingApproval, PromotionApproved, PromotionRejected, PromotionFailed,
	ReplicationCompleted, ReplicationFailed, RepositoryVisibilityChanged,
}

// Types lists every event type.
func Types() []Type { return append([]Type(nil), catalogue...) }

// Valid reports whether t is in the catalogue.
func (t Type) Valid() bool {
	for _, candidate := range catalogue {
		if candidate == t {
			return true
		}
	}
	return false
}

// internalConsumers are the types an in-process consumer reads, so they are written even when no
// webhook subscription asks for them: publicationScanScheduler consumes artifact.published.
var internalConsumers = map[Type]bool{ArtifactPublished: true}

// HasInternalConsumer reports whether an in-process consumer reads type t.
func HasInternalConsumer(t Type) bool { return internalConsumers[t] }

// Event is one immutable domain event.
type Event struct {
	// ID is a UUIDv7; Emit fills it when empty. It is not unique in the table.
	ID   string
	Type Type
	// RepositoryID, Format, Coordinate and Digest identify the artifact; they may be empty.
	RepositoryID, Format, Coordinate, Digest string
	// Version is the type-specific part of the dedupe key: the scan result id for
	// artifact.scan.completed, the lifecycle transition id for quarantine and release, the
	// promotion id for promotion events, empty for artifact.published.
	Version string
	// Payload is a JSON object; Emit writes {} when empty.
	Payload    json.RawMessage
	OccurredAt time.Time
}

// DedupeKey is type|repo|coordinate|digest|version.
func (e Event) DedupeKey() string {
	return strings.Join([]string{string(e.Type), e.RepositoryID, e.Coordinate, e.Digest, e.Version}, "|")
}

// Validate rejects an event Emit must not write.
func (e Event) Validate() error {
	if !e.Type.Valid() {
		return errors.New("events: unknown event type " + string(e.Type))
	}
	if len(e.Payload) > 0 {
		var object map[string]json.RawMessage
		if json.Unmarshal(e.Payload, &object) != nil || object == nil {
			return errors.New("events: payload must be a JSON object")
		}
	}
	return nil
}

// DB is the part of a transaction Emit needs.
type DB interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Prepare returns the event with its id, time and payload filled in.
func (e Event) Prepare() Event {
	if e.ID == "" {
		e.ID = ids.New()
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	e.OccurredAt = e.OccurredAt.UTC()
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage("{}")
	}
	return e
}

const emitStatement = `SELECT emit_domain_event($1::uuid,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7::jsonb,$8,$9,$10)`

// Emit writes the event in the caller's transaction and reports whether a row was written. A failure must
// fail the transaction of the state change: a silently missed event would break the chain guarantee.
func Emit(ctx context.Context, tx DB, event Event) (bool, error) {
	if err := event.Validate(); err != nil {
		return false, err
	}
	event = event.Prepare()
	var written bool
	err := tx.QueryRowContext(ctx, emitStatement, event.ID, string(event.Type), event.RepositoryID, event.Format, event.Coordinate, event.Digest,
		[]byte(event.Payload), event.DedupeKey(), event.OccurredAt, HasInternalConsumer(event.Type)).Scan(&written)
	return written, err
}
