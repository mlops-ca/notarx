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
	"fmt"
	"strings"
	"sync/atomic"
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
	Payload json.RawMessage
	// OccurredAt is the time of the state change as the in-memory store records it. Emit ignores it: the
	// database stamps its own clock.
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
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// RuleFanout names the promotion rules that evaluate an event (ADR 0021, Evaluator). It reads the enabled rules of the
// event's source repository from an in-process cache (ADR 0023 section 6), so a publication that no rule can match does not
// query the rules table. On a cache miss it loads the rules through tx, the connection of the publishing transaction: a
// second pool connection taken while the transaction holds one could deadlock a saturated pool. The cache lags a rule
// change on another node by at most NOTARX_AUTHZ_CACHE_TTL; an artifact published in that window is evaluated by a rule
// created in it only through backfill (ADR 0021, Rules).
type RuleFanout interface {
	// MatchingRules returns the ids of the enabled rules whose match can accept the event. The evaluator re-checks the
	// match against the artifact, so an implementation may over-approximate what it cannot know at publication (the
	// tags that point at the artifact), and must never leave out a rule that could match.
	MatchingRules(ctx context.Context, tx DB, event Event) ([]string, error)
}

// fanOutTypes are the event types that start rule evaluations. artifact.scan.completed joins them with the conditions
// slice, which re-pends waiting evaluations.
var fanOutTypes = map[Type]bool{ArtifactPublished: true}

var ruleFanout atomic.Pointer[RuleFanout]

// SetRuleFanout installs the process-wide rule fan-out that Emit and EmitOnce call inside the emitting transaction, or
// removes it when f is nil (promotion disabled). It is set once at startup.
func SetRuleFanout(f RuleFanout) {
	if f == nil {
		ruleFanout.Store(nil)
		return
	}
	ruleFanout.Store(&f)
}

// fanOutStatement inserts one pending evaluation per rule of the event that is still enabled. A parked rule gets its
// evaluations too: they wait for the rule to be restored. The rule set came from a cache that may be a notification behind,
// so the join to promotion_rules drops a rule that was deleted or disabled since, and ON CONFLICT makes a second emission
// of the same event insert nothing.
const fanOutStatement = `INSERT INTO promotion_evaluations (id,event_seq,event_occurred_at,rule_id,repository_id,coordinate,digest,state,next_attempt_at,created_at,updated_at)
SELECT gen_random_uuid(),e.seq,e.occurred_at,pr.id,e.repository_id,COALESCE(e.coordinate,''),COALESCE(e.digest,''),'pending',clock_timestamp(),clock_timestamp(),clock_timestamp()
FROM domain_events e JOIN promotion_rules pr ON pr.source_repository_id=e.repository_id AND pr.enabled AND pr.id::text=ANY($3::text[])
WHERE e.id=$1::uuid AND e.occurred_at=$2
ON CONFLICT (event_seq,rule_id) DO NOTHING`

// fanOut inserts the evaluations of a written event in the emitting transaction. A failure fails the transaction: a
// silently missed evaluation would break the chain guarantee.
func fanOut(ctx context.Context, tx DB, event Event, occurredAt time.Time) error {
	fanout := ruleFanout.Load()
	if fanout == nil || !fanOutTypes[event.Type] || event.RepositoryID == "" {
		return nil
	}
	rules, err := (*fanout).MatchingRules(ctx, tx, event)
	if err != nil {
		return fmt.Errorf("events: promotion rule fan-out: %w", err)
	}
	if len(rules) == 0 {
		return nil
	}
	if _, err = tx.ExecContext(ctx, fanOutStatement, event.ID, occurredAt, rules); err != nil {
		return fmt.Errorf("events: insert promotion evaluations: %w", err)
	}
	return nil
}

// Prepare returns the event with its id and payload filled in.
func (e Event) Prepare() Event {
	if e.ID == "" {
		e.ID = ids.New()
	}
	if !e.OccurredAt.IsZero() {
		e.OccurredAt = e.OccurredAt.UTC()
	}
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage("{}")
	}
	return e
}

const emitStatement = `SELECT emit_domain_event($1::uuid,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7::jsonb,$8,$9)`

// Published builds the artifact.published event of one visible publication of a digest (ADR 0021): one per
// publication, so its Version is empty. The payload names the format and, when the publisher is known, the actor.
func Published(repositoryID, format, coordinate, digest, actor string) Event {
	payload := map[string]string{"format": format}
	if actor != "" {
		payload["actor"] = actor
	}
	data, _ := json.Marshal(payload) // a map of strings always marshals
	return Event{Type: ArtifactPublished, RepositoryID: repositoryID, Format: format, Coordinate: coordinate, Digest: digest, Payload: data}
}

// Result is the outcome of Emit.
type Result struct {
	// Written reports whether a row was written: false when no subscription and no internal consumer wants the type.
	Written bool
	// OccurredAt is the database time stamped on the row; zero when nothing was written.
	OccurredAt time.Time
}

// Emit writes the event in the caller's transaction. A failure must fail the transaction of the state change: a
// silently missed event would break the chain guarantee. The row's occurred_at is the database clock at emission
// whatever Event.OccurredAt says, so the position of the event in the log is never behind a consumer's cursor.
func Emit(ctx context.Context, tx DB, event Event) (Result, error) {
	if err := event.Validate(); err != nil {
		return Result{}, err
	}
	event = event.Prepare()
	var occurredAt sql.NullTime
	err := tx.QueryRowContext(ctx, emitStatement, event.ID, string(event.Type), event.RepositoryID, event.Format, event.Coordinate, event.Digest,
		[]byte(event.Payload), event.DedupeKey(), HasInternalConsumer(event.Type)).Scan(&occurredAt)
	if err != nil {
		return Result{}, err
	}
	return written(ctx, tx, event, occurredAt)
}

// written builds the Result of an emission and starts the rule evaluations of an event that was written.
func written(ctx context.Context, tx DB, event Event, occurredAt sql.NullTime) (Result, error) {
	if !occurredAt.Valid {
		return Result{}, nil
	}
	if err := fanOut(ctx, tx, event, occurredAt.Time); err != nil {
		return Result{}, err
	}
	return Result{Written: true, OccurredAt: occurredAt.Time}, nil
}

const emitOnceStatement = `SELECT emit_domain_event_once($1::uuid,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7::jsonb,$8,$9,$10)`

// EmitOnce is the emission of the catch-up reconciler and repair paths (ADR 0021, ADR 0023 section 2): it writes the
// event only when no row with the same dedupe key exists at or after floor, under an advisory lock on the key, so
// two reconcilers racing for one publication write one event and a row older than the retention floor does not
// count (its partition is dropped or about to be). The in-transaction emission of a state change stays Emit: it is
// exactly-once by construction and never relies on this check. Result.Written is false when the key is present and
// when nothing consumes the type.
func EmitOnce(ctx context.Context, tx DB, event Event, floor time.Time) (Result, error) {
	if err := event.Validate(); err != nil {
		return Result{}, err
	}
	event = event.Prepare()
	var occurredAt sql.NullTime
	err := tx.QueryRowContext(ctx, emitOnceStatement, event.ID, string(event.Type), event.RepositoryID, event.Format, event.Coordinate, event.Digest,
		[]byte(event.Payload), event.DedupeKey(), HasInternalConsumer(event.Type), floor.UTC()).Scan(&occurredAt)
	if err != nil {
		return Result{}, err
	}
	return written(ctx, tx, event, occurredAt)
}
