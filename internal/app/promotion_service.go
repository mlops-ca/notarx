package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/repository"
)

// PromotionService is the single path every promotion takes (manual API, and later rules and
// batches): same-format active Hosted target check, quarantine recheck, admission policy
// evaluation, and the lifecycle_jobs promotion enqueue (ADR 0021 "Evaluator"). Callers have
// already authenticated the principal and authorised it on both repositories.
type PromotionService struct {
	store            GatewayStore
	securityPolicies repository.RepositorySecurityPolicyStore
	intelligence     repository.ArtifactIntelligenceStore
	audit            repository.Store
	promotions       repository.ArtifactPromotionStore
	registry         *formatmodule.Registry
	// quarantine rechecks that the source artifact exists and is distributable.
	quarantine func(ctx context.Context, principal Principal, source, target repository.HostedRepository, coordinate, digest string) *PromotionError
}

// promotionStores are the provenance and batch ports of the gateway store. NewGatewayHandler refuses a store that lacks
// them, so a promotion never meets a store without provenance after its job is queued.
type promotionStores interface {
	repository.ArtifactPromotionStore
	repository.PromotionBatchStore
}

// newPromotionService wires the service from the management adapter's stores.
func newPromotionService(h generatedRepositoryAPIAdapter) PromotionService {
	return PromotionService{
		store: h.sessions.store, securityPolicies: h.securityPolicies, intelligence: h.intelligence, audit: h.audit, promotions: h.sessions.store.(promotionStores),
		registry: promotionFormatRegistry(promotionModuleDeps{Store: h.sessions.store}),
		quarantine: func(ctx context.Context, principal Principal, source, target repository.HostedRepository, coordinate, digest string) *PromotionError {
			return h.checkQuarantinedDistribution(ctx, principal, source, target, coordinate, digest, "promote")
		},
	}
}

// PromotionRequest is one validated request to promote an immutable artifact.
type PromotionRequest struct {
	Principal      Principal
	Source         repository.HostedRepository
	TargetID       string
	Coordinate     string
	Digest         string
	AptTargetSuite *string
	IdempotencyKey string
}

// PromotionResult is the accepted promotion: its job and its provenance row. A replay of the same Idempotency-Key
// returns the job and the row of the first request.
type PromotionResult struct {
	Job       repository.LifecycleJob
	Promotion repository.ArtifactPromotion
}

// PromotionError is a refusal the HTTP layer renders as an RFC 9457 problem.
type PromotionError struct {
	Status  int
	Code    string
	Message string
}

func (e *PromotionError) Error() string { return e.Code + ": " + e.Message }

// errUnsupportedPromotionFormat is the refusal for a format whose module does not declare the promotion capability.
var errUnsupportedPromotionFormat = promotionError(http.StatusUnprocessableEntity, "unsupported_format", "promotion is not supported for this repository format")

// SupportsFormat reports whether the format's module declares the promotion capability.
func (s PromotionService) SupportsFormat(format repository.Format) bool {
	_, ok := formatmodule.Capability[formatmodule.PromotionPublisher](s.registry, format)
	return ok
}

func promotionError(status int, code, message string) *PromotionError {
	return &PromotionError{Status: status, Code: code, Message: message}
}

// promotionTarget is the target repository of a promotion with its security policy, resolved once so a batch does not
// re-read them per item. A refusal of the target or its policy is carried in err and applies to every item.
type promotionTarget struct {
	repo   repository.HostedRepository
	policy repository.RepositorySecurityPolicy
	err    *PromotionError
}

// resolveTarget loads the target repository and its security policy and checks that the target is an active Hosted
// repository of the source's format other than the source.
func (s PromotionService) resolveTarget(ctx context.Context, source repository.HostedRepository, targetID string) *promotionTarget {
	target, err := s.store.GetHostedRepository(ctx, targetID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return &promotionTarget{err: promotionError(http.StatusInternalServerError, "internal_error", "get target repository failed")}
	}
	if err != nil || target.ID == source.ID || target.Format != source.Format || target.Type != repository.RepositoryTypeHosted || target.State != repository.RepositoryActive {
		return &promotionTarget{err: promotionError(http.StatusConflict, "invalid_target", "target must be an active Hosted repository with the same format")}
	}
	policy, err := s.securityPolicies.GetRepositorySecurityPolicy(ctx, target.ID)
	if err != nil {
		return &promotionTarget{repo: target, err: promotionError(http.StatusInternalServerError, "internal_error", "get security policy failed")}
	}
	return &promotionTarget{repo: target, policy: policy}
}

// promotionAdmission is what a passed admission check resolved: the target repository and the format publisher.
type promotionAdmission struct {
	target    repository.HostedRepository
	publisher formatmodule.PromotionPublisher
}

// Admit runs the admission checks of a promotion (format capability, target, quarantine recheck, admission policy)
// without queueing anything; a batch admits every item before it queues any.
func (s PromotionService) Admit(ctx context.Context, in PromotionRequest) *PromotionError {
	_, perr := s.admit(ctx, in, s.resolveTarget(ctx, in.Source, in.TargetID))
	return perr
}

// admit runs the per-item checks against a target resolved by resolveTarget. The worker evaluates the target policy
// again when it claims the job, so a policy read earlier in the same batch does not weaken execution.
func (s PromotionService) admit(ctx context.Context, in PromotionRequest, resolved *promotionTarget) (promotionAdmission, *PromotionError) {
	source := in.Source
	publisher, ok := formatmodule.Capability[formatmodule.PromotionPublisher](s.registry, source.Format)
	if !ok {
		return promotionAdmission{}, errUnsupportedPromotionFormat
	}
	if resolved.err != nil && resolved.repo.ID == "" {
		return promotionAdmission{}, resolved.err
	}
	target := resolved.repo
	if source.Format == repository.FormatAPT && in.AptTargetSuite == nil {
		return promotionAdmission{}, promotionError(http.StatusBadRequest, "invalid_request", "aptTargetSuite is required for APT promotion")
	}
	if rejection := s.quarantine(ctx, in.Principal, source, target, in.Coordinate, in.Digest); rejection != nil {
		return promotionAdmission{}, rejection
	}
	if resolved.err != nil {
		return promotionAdmission{}, resolved.err
	}
	var intelligence *repository.ArtifactIntelligence
	if value, intelligenceErr := s.intelligence.GetArtifactIntelligence(ctx, source.ID, source.Format, in.Coordinate, in.Digest); intelligenceErr == nil {
		intelligence = &value
	} else if !errors.Is(intelligenceErr, repository.ErrNotFound) {
		return promotionAdmission{}, promotionError(http.StatusInternalServerError, "internal_error", "get artifact intelligence failed")
	}
	evaluation := repository.EvaluateRepositorySecurityPolicy(resolved.policy, intelligence)
	if !evaluation.Allowed {
		if s.audit != nil {
			_ = s.audit.RecordAudit(ctx, repository.AuditRecord{GroupName: target.Name, Repository: target.Name, Actor: in.Principal.Actor, Outcome: repository.AuditAccessDenied, OccurredAt: time.Now().UTC(), Format: "management", Resource: in.Coordinate, Representation: in.Digest, Operation: "promote.security_policy", Status: http.StatusForbidden, AuthorizationReason: strings.Join(evaluation.Reasons, ",")})
		}
		return promotionAdmission{}, promotionError(http.StatusForbidden, "security_policy_denied", "promotion denied by security policy: "+strings.Join(evaluation.Reasons, ","))
	}
	return promotionAdmission{target: target, publisher: publisher}, nil
}

// Request runs the admission checks and enqueues the promotion job.
func (s PromotionService) Request(ctx context.Context, in PromotionRequest) (PromotionResult, *PromotionError) {
	return s.request(ctx, in, s.resolveTarget(ctx, in.Source, in.TargetID))
}

func (s PromotionService) request(ctx context.Context, in PromotionRequest, resolved *promotionTarget) (PromotionResult, *PromotionError) {
	admission, perr := s.admit(ctx, in, resolved)
	if perr != nil {
		return PromotionResult{}, perr
	}
	source, target, publisher := in.Source, admission.target, admission.publisher
	promoteAudit := s.auditFor(repository.AuditRecord{Repository: source.Name, GroupName: source.Name, Actor: in.Principal.Actor, Resource: in.Coordinate, Operation: "promote", Status: http.StatusAccepted})
	now := time.Now().UTC()
	provenance := repository.ArtifactPromotion{
		ID: ids.New(), Format: source.Format, Coordinate: in.Coordinate, Digest: in.Digest, OriginRepository: source.Name,
		ChainPath:   []repository.PromotionChainHop{{Repository: source.Name, At: now}},
		RequestedBy: in.Principal.Actor, IdempotencyKey: promotionIdempotencyKey(target.ID, in.IdempotencyKey),
	}
	enqueueCtx := formatmodule.WithPromotionOptions(repository.WithArtifactPromotion(ctx, provenance), formatmodule.PromotionOptions{Actor: in.Principal.Actor, APTTargetSuite: aptDistributionSuite(in.AptTargetSuite)})
	job, _, err := publisher.EnqueuePromotion(enqueueCtx, source.ID, target.ID, in.Coordinate, in.Digest, in.IdempotencyKey, promoteAudit...)
	if errors.Is(err, repository.ErrIdempotencyConflict) {
		if result, replayed := s.replayPruned(ctx, in, admission.target); replayed {
			return result, nil
		}
		return PromotionResult{}, promotionError(http.StatusConflict, "idempotency_conflict", "Idempotency-Key conflicts with an existing promotion job")
	}
	if err != nil {
		return PromotionResult{}, promotionError(http.StatusInternalServerError, "internal_error", "enqueue promotion job failed")
	}
	row, err := s.promotions.GetArtifactPromotionByJob(ctx, job.ID)
	if err != nil {
		return PromotionResult{}, promotionError(http.StatusInternalServerError, "internal_error", "get promotion provenance failed")
	}
	return PromotionResult{Job: job, Promotion: row}, nil
}

// replayPruned answers a request whose Idempotency-Key has a provenance row but whose lifecycle job was pruned: the
// enqueue then wrote a fresh job that collided with the row's unique key and was rolled back. The identical request
// (same format, source, coordinate and digest) is accepted with the stored outcome and nothing is enqueued; a key
// reused for anything else, or whose job still exists, stays a conflict. The provenance row outlives its job, so the
// job is rebuilt from the row.
func (s PromotionService) replayPruned(ctx context.Context, in PromotionRequest, target repository.HostedRepository) (PromotionResult, bool) {
	key := promotionIdempotencyKey(target.ID, in.IdempotencyKey)
	rows, err := s.promotions.GetArtifactPromotionsByKeys(ctx, []string{key})
	row, ok := rows[key]
	if err != nil || !ok || row.JobID == "" || row.Format != in.Source.Format || row.Coordinate != in.Coordinate || row.Digest != in.Digest || row.OriginRepository != in.Source.Name {
		return PromotionResult{}, false
	}
	if _, err = s.store.GetLifecycleJob(ctx, row.TargetRepositoryID, row.JobID); !errors.Is(err, repository.ErrNotFound) {
		return PromotionResult{}, false
	}
	job := repository.LifecycleJob{ID: row.JobID, RepositoryID: row.TargetRepositoryID, Kind: repository.LifecycleJobPromotion,
		IdempotencyKey: in.IdempotencyKey, State: lifecycleStateForPromotion(row.State), CreatedAt: row.CreatedAt, CompletedAt: row.CompletedAt}
	return PromotionResult{Job: job, Promotion: row}, true
}

// lifecycleStateForPromotion is the job state a provenance state stands for when the job itself was pruned.
func lifecycleStateForPromotion(state repository.ArtifactPromotionState) repository.LifecycleJobState {
	switch state {
	case repository.ArtifactPromotionRunning:
		return repository.LifecycleJobRunning
	case repository.ArtifactPromotionCompleted:
		return repository.LifecycleJobCompleted
	case repository.ArtifactPromotionFailed, repository.ArtifactPromotionRejected, repository.ArtifactPromotionExpired:
		return repository.LifecycleJobFailed
	case repository.ArtifactPromotionCancelled:
		return repository.LifecycleJobCancelled
	}
	return repository.LifecycleJobPending
}

// promotionIdempotencyKey scopes a caller's Idempotency-Key to the target: provenance keys are unique across the
// registry, a caller's key only within its target repository.
func promotionIdempotencyKey(targetID, key string) string { return "manual:" + targetID + ":" + key }

// auditFor returns the audit records handed to the enqueue, or none when audit is not configured.
func (s PromotionService) auditFor(record repository.AuditRecord) []repository.AuditRecord {
	if s.audit == nil {
		return nil
	}
	return []repository.AuditRecord{managementAudit(record)}
}
