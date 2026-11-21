package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/events"
	"github.com/mlops-ca/notarx/internal/repository"
)

type publicationScanStore interface {
	repository.RepositorySecurityPolicyStore
	repository.LifecycleJobStore
	repository.HostedRepositoryStore
	RecordAudit(context.Context, repository.AuditRecord) error
}

type publicationScanScheduler struct {
	store            publicationScanStore
	scannerAvailable bool
	formats          []repository.Format
	catchUp          time.Duration
	metrics          *Metrics
}

const publicationScanAuditTimeout = 2 * time.Second

// NewPublicationScanScheduler builds the scheduler. catchUp is how far back its consumer reads when it first starts:
// the configured retention of the event log (retention.Config.KeepOrDefault("domain_events")).
func NewPublicationScanScheduler(store publicationScanStore, scannerAvailable bool, formats []repository.Format, catchUp time.Duration, metrics *Metrics) publicationScanScheduler {
	return publicationScanScheduler{
		store:            store,
		scannerAvailable: scannerAvailable,
		formats:          append([]repository.Format(nil), formats...),
		catchUp:          catchUp,
		metrics:          metrics,
	}
}

func (s publicationScanScheduler) Schedule(ctx context.Context, repo repository.HostedRepository, coordinate, digest, actor string) error {
	if s.store == nil || !s.scannerAvailable || !publicationScanSupported(repo) || !scanFormatEnabled(s.formats, repo.Format) {
		return nil
	}
	policy, err := s.store.GetRepositorySecurityPolicy(ctx, repo.ID)
	if err != nil {
		return s.recordFailure(ctx, repo, coordinate, digest, actor, err)
	}
	if !policy.AutoScanOnPublish {
		return nil
	}

	key := publicationScanIdempotencyKey(repo.ID, repo.Format, coordinate, digest)
	_, _, err = repository.EnqueueArtifactScanJob(ctx, s.store, repo.ID, key, repository.ArtifactScanPayload{
		Format: repo.Format, Coordinate: coordinate, Digest: digest,
	})
	if err != nil {
		return s.recordFailure(ctx, repo, coordinate, digest, actor, err)
	}
	_ = s.store.RecordAudit(ctx, publicationScanAudit(repo, coordinate, digest, actor, repository.AuditResolved, http.StatusAccepted, ""))
	return nil
}

// PublicationScanConsumer is the name of the scheduler's cursor in the event log.
const PublicationScanConsumer = "publication-scan"

// Consumer returns the scheduler as an internal consumer of artifact.published events (ADR 0021): a
// publication schedules its scan when the repository's autoScanOnPublish is on. The scan job key is
// idempotent, so the at-least-once delivery of the dispatcher cannot enqueue a scan twice.
//
// A node that starts the consumer for the first time reads back over the configured retention window of the event log
// (NOTARX_EVENTS_RETENTION) instead of starting at the present, so the publications of an auto-scan repository made
// while scanning was disabled or during an upgrade gap are scheduled too.
func (s publicationScanScheduler) Consumer() events.Consumer {
	return events.Consumer{Name: PublicationScanConsumer, Types: []events.Type{events.ArtifactPublished}, Handler: s, CatchUp: s.catchUp}
}

// HandleEvent schedules the scan of a published artifact. An event of a repository that no longer exists is
// skipped; every other failure is retried.
func (s publicationScanScheduler) HandleEvent(ctx context.Context, event events.Event) error {
	if event.Type != events.ArtifactPublished {
		return nil
	}
	var payload struct {
		Actor string `json:"actor"`
	}
	_ = json.Unmarshal(event.Payload, &payload)
	err := s.ScheduleRepository(ctx, event.RepositoryID, repository.Format(event.Format), event.Coordinate, event.Digest, payload.Actor)
	if errors.Is(err, repository.ErrNotFound) {
		return events.Permanent(err)
	}
	return err
}

func publicationScanIdempotencyKey(repositoryID string, format repository.Format, coordinate, digest string) string {
	identity := strings.Join([]string{repositoryID, string(format), coordinate, digest}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return "publish-scan:" + hex.EncodeToString(sum[:])
}

func (s publicationScanScheduler) ScheduleRepository(ctx context.Context, repositoryID string, format repository.Format, coordinate, digest, actor string) error {
	if s.store == nil || !s.scannerAvailable {
		return nil
	}
	repo, err := s.store.GetHostedRepository(ctx, repositoryID)
	if err != nil {
		return s.recordFailure(ctx, repository.HostedRepository{ID: repositoryID, Name: repositoryID, Format: format}, coordinate, digest, actor, err)
	}
	return s.Schedule(ctx, repo, coordinate, digest, actor)
}

func publicationScanSupported(repo repository.HostedRepository) bool {
	return repository.FormatSupportsPublicationScanning(repo.Format, repo.Type)
}

func (s publicationScanScheduler) recordFailure(ctx context.Context, repo repository.HostedRepository, coordinate, digest, actor string, cause error) error {
	if s.metrics != nil {
		s.metrics.RecordBackgroundOperation("scan", repo.Format, "failed")
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publicationScanAuditTimeout)
	defer cancel()
	_ = s.store.RecordAudit(auditCtx, publicationScanAudit(repo, coordinate, digest, actor, repository.AuditStorageError, http.StatusInternalServerError, cause.Error()))
	return cause
}

func publicationScanAudit(repo repository.HostedRepository, coordinate, digest, actor string, outcome repository.AuditOutcome, status int, reason string) repository.AuditRecord {
	return repository.AuditRecord{
		Repository: repo.Name, GroupName: repo.Name, Actor: strings.TrimSpace(actor),
		Outcome: outcome, OccurredAt: time.Now().UTC(), Format: string(repo.Format),
		Resource: coordinate, Representation: digest, Operation: "artifact.scan.auto_enqueue",
		Status: status, CacheDisposition: "bypass", AuthorizationReason: reason,
	}
}
