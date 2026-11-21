package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mlops-ca/notarx/internal/events"
)

// PublishedEventReconciler is the catch-up path of ADR 0021 (slice 1b): it emits artifact.published for the visible
// Raw, OCI and Maven publications that have none inside the retention window, so a publication committed before its
// format emitted natively (or by a path that does not emit) still reaches the consumers. It exists as a worker while
// the formats are converted in waves and as a repair tool afterwards.
type PublishedEventReconciler interface {
	// ReconcilePublishedEvents emits the missing events of up to limit publications per format and returns how many
	// it wrote. Only publications at or after floor are considered: an older event is outside the retention window
	// and history is recovered through a rule's backfill. Several reconcilers may run at once; the dedupe key is
	// checked under an advisory lock, so a publication gets one event.
	ReconcilePublishedEvents(ctx context.Context, floor time.Time, limit int) (int, error)
}

var (
	_ PublishedEventReconciler = (*PostgresStore)(nil)
	_ PublishedEventReconciler = (*MemoryStore)(nil)
)

// publishedCandidate is one visible publication to check.
type publishedCandidate struct {
	RepositoryID, Format, Coordinate, Digest string
}

func (c publishedCandidate) event() events.Event {
	return events.Published(c.RepositoryID, c.Format, c.Coordinate, c.Digest, "")
}

// emitPublishedPostgres emits artifact.published in the transaction that makes the publication visible. A failure
// fails that transaction: a silently missed event would break the chain guarantee.
func emitPublishedPostgres(ctx context.Context, tx sqlRunner, repositoryID string, format Format, coordinate, digest string) error {
	_, err := events.Emit(ctx, tx, events.Published(repositoryID, string(format), coordinate, digest, publishActor(ctx)))
	return err
}

// emitPublishedLocked is emitPublishedPostgres for the in-memory store; the caller holds s.mu.
func (s *MemoryStore) emitPublishedLocked(ctx context.Context, repositoryID string, format Format, coordinate, digest string) {
	event := events.Published(repositoryID, string(format), coordinate, digest, publishActor(ctx))
	s.enqueueWebhookEventLocked(WebhookEvent{Type: event.Type, RepositoryID: repositoryID, Format: string(format), Coordinate: coordinate, Digest: digest, Data: event.Payload})
}

// publishedCandidateQueries select the visible publications of one format at or after $1 that have no
// artifact.published event at or after $1, oldest first. The dedupe key is spelled as events.Event.DedupeKey builds
// it: type|repo|coordinate|digest|version with an empty version.
var publishedCandidateQueries = []string{
	`SELECT a.repository_id::text,'raw',a.path,a.digest FROM native_raw_assets a
WHERE a.updated_at >= $1 AND NOT EXISTS (SELECT 1 FROM domain_events e WHERE e.dedupe_key = 'artifact.published|' || a.repository_id::text || '|' || a.path || '|' || a.digest || '|' AND e.occurred_at >= $1)
ORDER BY a.updated_at,a.repository_id,a.path LIMIT $2`,
	`SELECT m.repository_id::text,'oci',m.name,m.digest FROM native_oci_manifests m
WHERE m.created_at >= $1 AND NOT EXISTS (SELECT 1 FROM domain_events e WHERE e.dedupe_key = 'artifact.published|' || m.repository_id::text || '|' || m.name || '|' || m.digest || '|' AND e.occurred_at >= $1)
ORDER BY m.created_at,m.repository_id,m.name,m.digest LIMIT $2`,
	`SELECT a.repository_id::text,'maven',a.coordinate,a.digest FROM native_maven_artifacts a
WHERE a.state = 'visible' AND a.created_at >= $1 AND NOT EXISTS (SELECT 1 FROM domain_events e WHERE e.dedupe_key = 'artifact.published|' || a.repository_id::text || '|' || a.coordinate || '|' || a.digest || '|' AND e.occurred_at >= $1)
ORDER BY a.created_at,a.repository_id,a.coordinate,a.digest LIMIT $2`,
}

// ReconcilePublishedEvents implements PublishedEventReconciler. Each publication is emitted in its own transaction
// with events.EmitOnce, which rechecks the dedupe key under the advisory lock.
//
// A publication that fails does not hold back the others: its error is collected, the pass goes on and returns the
// joined errors with the count it wrote. Only a cancelled context stops the pass.
func (s *PostgresStore) ReconcilePublishedEvents(ctx context.Context, floor time.Time, limit int) (int, error) {
	written := 0
	var failures []error
	for _, query := range publishedCandidateQueries {
		candidates, err := s.publishedCandidates(ctx, query, floor, limit)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, candidate := range candidates {
			emitted, err := s.emitPublishedOnce(ctx, candidate, floor)
			if err != nil {
				failures = append(failures, fmt.Errorf("%s publication %q in %s: %w", candidate.Format, candidate.Coordinate, candidate.RepositoryID, err))
				continue
			}
			if emitted {
				written++
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	return written, errors.Join(failures...)
}

func (s *PostgresStore) publishedCandidates(ctx context.Context, query string, floor time.Time, limit int) ([]publishedCandidate, error) {
	rows, err := s.db.QueryContext(ctx, query, floor.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []publishedCandidate
	for rows.Next() {
		var c publishedCandidate
		if err = rows.Scan(&c.RepositoryID, &c.Format, &c.Coordinate, &c.Digest); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *PostgresStore) emitPublishedOnce(ctx context.Context, candidate publishedCandidate, floor time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := events.EmitOnce(ctx, tx, candidate.event(), floor)
	if err != nil {
		return false, err
	}
	return result.Written, tx.Commit()
}

// ReconcilePublishedEvents implements PublishedEventReconciler for the in-memory store.
func (s *MemoryStore) ReconcilePublishedEvents(_ context.Context, floor time.Time, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	announced := map[string]bool{}
	for _, event := range s.webhookEvents {
		if event.Type == events.ArtifactPublished && !event.OccurredAt.Before(floor) {
			announced[event.domainEvent().DedupeKey()] = true
		}
	}
	var candidates []publishedCandidate
	add := func(at time.Time, c publishedCandidate) {
		if !at.Before(floor) {
			candidates = append(candidates, c)
		}
	}
	for _, asset := range s.rawAssets {
		add(asset.UpdatedAt, publishedCandidate{asset.RepositoryID, string(FormatRaw), asset.Path, asset.Digest})
	}
	for _, manifest := range s.ociManifests {
		add(manifest.CreatedAt, publishedCandidate{manifest.RepositoryID, string(FormatOCI), manifest.Name, manifest.Digest})
	}
	for _, artifact := range s.mavenArtifacts {
		if artifact.State == "visible" {
			add(artifact.CreatedAt, publishedCandidate{artifact.RepositoryID, string(FormatMaven), artifact.Coordinate, artifact.Digest})
		}
	}
	written := 0
	perFormat := map[string]int{}
	for _, c := range candidates {
		if limit > 0 && perFormat[c.Format] >= limit {
			continue
		}
		event := c.event()
		if announced[event.DedupeKey()] {
			continue
		}
		perFormat[c.Format]++
		announced[event.DedupeKey()] = true
		s.enqueueWebhookEventLocked(WebhookEvent{Type: event.Type, RepositoryID: c.RepositoryID, Format: c.Format, Coordinate: c.Coordinate, Digest: c.Digest, Data: event.Payload})
		written++
	}
	return written, nil
}
