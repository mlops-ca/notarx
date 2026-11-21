package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mlops-ca/notarx/internal/events"
)

// PublishedEventReconciler is the repair path of ADR 0021 (slice 1c): it emits artifact.published for the visible
// publications of the nine formats that have none inside the retention window, so a publication whose event was lost
// still reaches the consumers. Every format emits in its publish transaction; nothing schedules the reconciler.
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

// conanRecipeCoordinate is the coordinate of a recipe revision: reference#revision.
func conanRecipeCoordinate(reference, revision string) string { return reference + "#" + revision }

// conanPackageCoordinate is the coordinate of a package revision: reference#recipeRevision/packageID#revision.
func conanPackageCoordinate(reference, recipeRevision, packageID, revision string) string {
	return conanRecipeCoordinate(reference, recipeRevision) + "/" + packageID + "#" + revision
}

// goPublicationDigest is the digest a Go module version publishes under: its zip, the artifact a scan and a promotion name.
func goPublicationDigest(assets []GoModuleAsset) string {
	for _, asset := range assets {
		if asset.Kind == "zip" {
			return asset.Digest
		}
	}
	return ""
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
var publishedCandidateQueries = append([]string{
	// The files of a versioned repository are not publications: the sealed version is, below.
	`SELECT a.repository_id::text,'raw',a.path,a.digest FROM native_raw_assets a
WHERE a.updated_at >= $1 AND NOT EXISTS (SELECT 1 FROM hosted_repositories h WHERE h.id = a.repository_id AND h.raw_layout = 'versioned') AND NOT EXISTS (SELECT 1 FROM domain_events e WHERE e.dedupe_key = 'artifact.published|' || a.repository_id::text || '|' || a.path || '|' || a.digest || '|' AND e.occurred_at >= $1)
ORDER BY a.updated_at,a.repository_id,a.path LIMIT $2`,
	`SELECT v.repository_id::text,'raw',v.name || '@' || v.version,v.manifest_digest FROM native_raw_versions v
WHERE v.state = 'sealed' AND v.sealed_at >= $1 AND ` + notAnnounced("v.repository_id", "v.name || '@' || v.version", "v.manifest_digest") + `
ORDER BY v.sealed_at,v.repository_id,v.name,v.version LIMIT $2`,
	`SELECT m.repository_id::text,'oci',m.name,m.digest FROM native_oci_manifests m
WHERE m.created_at >= $1 AND NOT EXISTS (SELECT 1 FROM domain_events e WHERE e.dedupe_key = 'artifact.published|' || m.repository_id::text || '|' || m.name || '|' || m.digest || '|' AND e.occurred_at >= $1)
ORDER BY m.created_at,m.repository_id,m.name,m.digest LIMIT $2`,
	`SELECT a.repository_id::text,'maven',a.coordinate,a.digest FROM native_maven_artifacts a
WHERE a.state = 'visible' AND a.created_at >= $1 AND NOT EXISTS (SELECT 1 FROM domain_events e WHERE e.dedupe_key = 'artifact.published|' || a.repository_id::text || '|' || a.coordinate || '|' || a.digest || '|' AND e.occurred_at >= $1)
ORDER BY a.created_at,a.repository_id,a.coordinate,a.digest LIMIT $2`,
}, hostedPublicationQueries()...)

// notAnnounced is the NOT EXISTS clause shared by the candidate queries of the formats added in slice 1c: no event with the
// dedupe key of (repository, coordinate, digest) exists at or after $1.
func notAnnounced(repository, coordinate, digest string) string {
	return "NOT EXISTS (SELECT 1 FROM domain_events e WHERE e.dedupe_key = 'artifact.published|' || " + repository + "::text || '|' || " + coordinate + " || '|' || " + digest + " || '|' AND e.occurred_at >= $1)"
}

func hostedPublicationQueries() []string {
	hosted := func(alias, column string) string {
		return "JOIN hosted_repositories h ON h.id = " + alias + "." + column + " AND h.repo_type = 'hosted'"
	}
	return []string{
		// npm, PyPI, Go and Cargo select only hosted repositories: a proxy cache write is not a publication.
		`SELECT v.repository_id::text,'npm',v.package_name || '@' || v.version,v.digest FROM native_npm_versions v ` + hosted("v", "repository_id") + `
WHERE v.state = 'visible' AND v.digest <> '' AND v.created_at >= $1 AND ` + notAnnounced("v.repository_id", "v.package_name || '@' || v.version", "v.digest") + `
ORDER BY v.created_at,v.repository_id,v.package_name,v.version LIMIT $2`,
		`SELECT f.repository_id::text,'pypi',f.project || '@' || f.version,f.digest FROM native_pypi_files f ` + hosted("f", "repository_id") + `
WHERE f.state = 'visible' AND f.object_key <> '' AND f.created_at >= $1 AND ` + notAnnounced("f.repository_id", "f.project || '@' || f.version", "f.digest") + `
ORDER BY f.created_at,f.repository_id,f.project,f.version,f.filename LIMIT $2`,
		`SELECT a.repository_id::text,'go',a.module_path || '@' || a.version,a.digest FROM native_go_assets a ` + hosted("a", "repository_id") + `
WHERE a.kind = 'zip' AND a.collected_at IS NULL AND a.created_at >= $1
AND NOT EXISTS (SELECT 1 FROM artifact_tombstones t WHERE t.repository_id = a.repository_id AND t.format = 'go' AND t.coordinate = a.module_path || '@' || a.version)
AND ` + notAnnounced("a.repository_id", "a.module_path || '@' || a.version", "a.digest") + `
ORDER BY a.created_at,a.repository_id,a.module_path,a.version LIMIT $2`,
		`SELECT p.repository_id::text,'cargo',n.name || '@' || r.version,r.digest FROM native_cargo_publications p
JOIN native_cargo_identity_reservations r USING (repository_id,collision_key,version_key) JOIN native_cargo_names n USING (repository_id,collision_key)
WHERE p.collected_at IS NULL AND p.created_at >= $1
AND NOT EXISTS (SELECT 1 FROM artifact_tombstones t WHERE t.repository_id = p.repository_id AND t.format = 'cargo' AND t.coordinate = n.name || '@' || r.version)
AND ` + notAnnounced("p.repository_id", "n.name || '@' || r.version", "r.digest") + `
ORDER BY p.created_at,p.repository_id,n.name,r.version LIMIT $2`,
		`SELECT r.repository_id::text,'conan',r.reference || '#' || r.revision,r.digest FROM native_conan_recipe_revisions r ` + hosted("r", "repository_id") + `
WHERE r.state = 'visible' AND r.created_at >= $1 AND ` + notAnnounced("r.repository_id", "r.reference || '#' || r.revision", "r.digest") + `
ORDER BY r.created_at,r.repository_id,r.reference,r.revision LIMIT $2`,
		`SELECT p.repository_id::text,'conan',p.reference || '#' || p.recipe_revision || '/' || p.package_id || '#' || p.revision,p.digest FROM native_conan_package_revisions p ` + hosted("p", "repository_id") + `
WHERE p.state = 'visible' AND p.created_at >= $1 AND ` + notAnnounced("p.repository_id", "p.reference || '#' || p.recipe_revision || '/' || p.package_id || '#' || p.revision", "p.digest") + `
ORDER BY p.created_at,p.repository_id,p.reference,p.recipe_revision,p.package_id,p.revision LIMIT $2`,
		// APT: a pool path published by the earliest snapshot of the repository that carries it with this digest.
		`SELECT a.repository_id::text,'apt',a.path,a.digest FROM native_apt_snapshot_assets a JOIN native_apt_repository_snapshots s ON s.id = a.snapshot_id
WHERE a.path LIKE 'pool/%' AND s.state IN ('visible','retired') AND s.published_at >= $1
AND NOT EXISTS (SELECT 1 FROM native_apt_snapshot_assets a2 JOIN native_apt_repository_snapshots s2 ON s2.id = a2.snapshot_id
  WHERE a2.repository_id = a.repository_id AND a2.path = a.path AND a2.digest = a.digest AND s2.state IN ('visible','retired') AND s2.published_at < s.published_at)
AND ` + notAnnounced("a.repository_id", "a.path", "a.digest") + `
ORDER BY s.published_at,a.repository_id,a.path LIMIT $2`,
	}
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
		queryWritten, queryFailures := drainPublishedCandidates(ctx, limit,
			func(pageSize int) ([]publishedCandidate, error) {
				return s.publishedCandidates(ctx, query, floor, pageSize)
			},
			func(candidate publishedCandidate) (bool, error) { return s.emitPublishedOnce(ctx, candidate, floor) })
		written += queryWritten
		failures = append(failures, queryFailures...)
		if ctx.Err() != nil {
			break
		}
	}
	return written, errors.Join(failures...)
}

// maxPassedPublicationsPerPass bounds the pages of one format: a pass that met this many candidates it could not write
// stops and reports it, so the window of a page cannot grow without limit.
const maxPassedPublicationsPerPass = 256

// drainPublishedCandidates emits the missing events of one format, at most limit of them. A candidate that is not written
// (it failed, or another reconciler wrote it between the query and now) may keep matching the query and stay at its head,
// so each page asks for as many extra candidates as were passed over in this pass and skips those: a publication that keeps
// failing never hides the newer ones behind it (keyset paging would need the order key of every format's query; the
// candidates passed over in one pass are few, and the pass stops at limit writes). A page asks for the writes still
// allowed, so the pass ends once limit events were written or the candidates ran out. Only a cancelled context stops it
// early.
func drainPublishedCandidates(ctx context.Context, limit int, fetch func(pageSize int) ([]publishedCandidate, error), emit func(publishedCandidate) (bool, error)) (int, []error) {
	written := 0
	passed := map[publishedCandidate]bool{}
	var failures []error
	for written < limit && ctx.Err() == nil {
		if len(passed) >= maxPassedPublicationsPerPass {
			return written, append(failures, fmt.Errorf("%d publications were passed over in this pass; the rest of the format is left for the next one", len(passed)))
		}
		pageSize := limit - written + len(passed)
		candidates, err := fetch(pageSize)
		if err != nil {
			return written, append(failures, err)
		}
		fresh := 0
		for _, candidate := range candidates {
			if passed[candidate] {
				continue
			}
			fresh++
			emitted, err := emit(candidate)
			if err != nil {
				failures = append(failures, fmt.Errorf("%s publication %q in %s: %w", candidate.Format, candidate.Coordinate, candidate.RepositoryID, err))
				passed[candidate] = true
				continue
			}
			if emitted {
				written++
				continue
			}
			passed[candidate] = true
		}
		if len(candidates) < pageSize || fresh == 0 {
			break
		}
	}
	return written, failures
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
		if _, versioned := s.versionedRepositoryLocked(asset.RepositoryID); !versioned {
			add(asset.UpdatedAt, publishedCandidate{asset.RepositoryID, string(FormatRaw), asset.Path, asset.Digest})
		}
	}
	for _, version := range s.rawVersions {
		if version.State == RawVersionSealed {
			add(version.SealedAt, publishedCandidate{version.RepositoryID, string(FormatRaw), version.Coordinate(), version.ManifestDigest})
		}
	}
	for _, manifest := range s.ociManifests {
		add(manifest.CreatedAt, publishedCandidate{manifest.RepositoryID, string(FormatOCI), manifest.Name, manifest.Digest})
	}
	for _, artifact := range s.mavenArtifacts {
		if artifact.State == "visible" {
			add(artifact.CreatedAt, publishedCandidate{artifact.RepositoryID, string(FormatMaven), artifact.Coordinate, artifact.Digest})
		}
	}
	s.addNativePublicationCandidatesLocked(add)
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
