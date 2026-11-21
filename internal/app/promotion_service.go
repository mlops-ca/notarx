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

// newPromotionService wires the service from the management adapter's stores.
func newPromotionService(h generatedRepositoryAPIAdapter) PromotionService {
	return PromotionService{
		store: h.sessions.store, securityPolicies: h.securityPolicies, intelligence: h.intelligence, audit: h.audit, promotions: h.sessions.store,
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

// Request runs the admission checks and enqueues the promotion job.
func (s PromotionService) Request(ctx context.Context, in PromotionRequest) (PromotionResult, *PromotionError) {
	source := in.Source
	publisher, ok := formatmodule.Capability[formatmodule.PromotionPublisher](s.registry, source.Format)
	if !ok {
		return PromotionResult{}, errUnsupportedPromotionFormat
	}
	target, err := s.store.GetHostedRepository(ctx, in.TargetID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return PromotionResult{}, promotionError(http.StatusInternalServerError, "internal_error", "get target repository failed")
	}
	if err != nil || target.ID == source.ID || target.Format != source.Format || target.Type != repository.RepositoryTypeHosted || target.State != repository.RepositoryActive {
		return PromotionResult{}, promotionError(http.StatusConflict, "invalid_target", "target must be an active Hosted repository with the same format")
	}
	if source.Format == repository.FormatAPT && in.AptTargetSuite == nil {
		return PromotionResult{}, promotionError(http.StatusBadRequest, "invalid_request", "aptTargetSuite is required for APT promotion")
	}
	if rejection := s.quarantine(ctx, in.Principal, source, target, in.Coordinate, in.Digest); rejection != nil {
		return PromotionResult{}, rejection
	}
	policy, policyErr := s.securityPolicies.GetRepositorySecurityPolicy(ctx, target.ID)
	if policyErr != nil {
		return PromotionResult{}, promotionError(http.StatusInternalServerError, "internal_error", "get security policy failed")
	}
	var intelligence *repository.ArtifactIntelligence
	if value, intelligenceErr := s.intelligence.GetArtifactIntelligence(ctx, source.ID, source.Format, in.Coordinate, in.Digest); intelligenceErr == nil {
		intelligence = &value
	} else if !errors.Is(intelligenceErr, repository.ErrNotFound) {
		return PromotionResult{}, promotionError(http.StatusInternalServerError, "internal_error", "get artifact intelligence failed")
	}
	evaluation := repository.EvaluateRepositorySecurityPolicy(policy, intelligence)
	if !evaluation.Allowed {
		if s.audit != nil {
			_ = s.audit.RecordAudit(ctx, repository.AuditRecord{GroupName: target.Name, Repository: target.Name, Actor: in.Principal.Actor, Outcome: repository.AuditAccessDenied, OccurredAt: time.Now().UTC(), Format: "management", Resource: in.Coordinate, Representation: in.Digest, Operation: "promote.security_policy", Status: http.StatusForbidden, AuthorizationReason: strings.Join(evaluation.Reasons, ",")})
		}
		return PromotionResult{}, promotionError(http.StatusForbidden, "security_policy_denied", "promotion denied by security policy: "+strings.Join(evaluation.Reasons, ","))
	}
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
