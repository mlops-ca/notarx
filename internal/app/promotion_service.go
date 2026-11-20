package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/aptpublication"
	formatmodule "github.com/mlops-ca/notarx/internal/format"
	rawformat "github.com/mlops-ca/notarx/internal/format/raw"
	conanprotocol "github.com/mlops-ca/notarx/internal/protocol/conan"
	mavenprotocol "github.com/mlops-ca/notarx/internal/protocol/maven"
	npmprotocol "github.com/mlops-ca/notarx/internal/protocol/npm"
	ociprotocol "github.com/mlops-ca/notarx/internal/protocol/oci"
	"github.com/mlops-ca/notarx/internal/repository"
)

// PromotionService is the single path every promotion takes (manual API, and later rules and
// batches): same-format active Hosted target check, quarantine recheck, admission policy
// evaluation, and the lifecycle_jobs promotion enqueue (ADR 0021 "Evaluator"). Callers have
// already authenticated the principal and authorised it on both repositories.
type PromotionService struct {
	h generatedRepositoryAPIAdapter
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

// PromotionError is a refusal the HTTP layer renders as an RFC 9457 problem.
type PromotionError struct {
	Status  int
	Code    string
	Message string
}

func (e *PromotionError) Error() string { return e.Code + ": " + e.Message }

func promotionError(status int, code, message string) *PromotionError {
	return &PromotionError{Status: status, Code: code, Message: message}
}

// Request runs the admission checks and enqueues the promotion job.
func (s PromotionService) Request(ctx context.Context, in PromotionRequest) (repository.LifecycleJob, *PromotionError) {
	source := in.Source
	target, err := s.h.sessions.store.GetHostedRepository(ctx, in.TargetID)
	if err != nil || target.ID == source.ID || target.Format != source.Format || target.Type != repository.RepositoryTypeHosted || target.State != repository.RepositoryActive {
		return repository.LifecycleJob{}, promotionError(http.StatusConflict, "invalid_target", "target must be an active Hosted repository with the same format")
	}
	if rejection := s.h.checkQuarantinedDistribution(ctx, in.Principal, source, target, in.Coordinate, in.Digest, "promote"); rejection != nil {
		return repository.LifecycleJob{}, promotionError(rejection.status, rejection.code, rejection.message)
	}
	policy, policyErr := s.h.securityPolicies.GetRepositorySecurityPolicy(ctx, target.ID)
	if policyErr != nil {
		return repository.LifecycleJob{}, promotionError(http.StatusInternalServerError, "internal_error", "get security policy failed")
	}
	var intelligence *repository.ArtifactIntelligence
	if value, intelligenceErr := s.h.intelligence.GetArtifactIntelligence(ctx, source.ID, source.Format, in.Coordinate, in.Digest); intelligenceErr == nil {
		intelligence = &value
	} else if !errors.Is(intelligenceErr, repository.ErrNotFound) {
		return repository.LifecycleJob{}, promotionError(http.StatusInternalServerError, "internal_error", "get artifact intelligence failed")
	}
	evaluation := repository.EvaluateRepositorySecurityPolicy(policy, intelligence)
	if !evaluation.Allowed {
		if s.h.audit != nil {
			_ = s.h.audit.RecordAudit(ctx, repository.AuditRecord{GroupName: target.Name, Repository: target.Name, Actor: in.Principal.Actor, Outcome: repository.AuditAccessDenied, OccurredAt: time.Now().UTC(), Format: "management", Resource: in.Coordinate, Representation: in.Digest, Operation: "promote.security_policy", Status: http.StatusForbidden, AuthorizationReason: strings.Join(evaluation.Reasons, ",")})
		}
		return repository.LifecycleJob{}, promotionError(http.StatusForbidden, "security_policy_denied", "promotion denied by security policy: "+strings.Join(evaluation.Reasons, ","))
	}
	promoteAudit := s.h.auditFor(repository.AuditRecord{Repository: source.Name, GroupName: source.Name, Actor: in.Principal.Actor, Resource: in.Coordinate, Operation: "promote", Status: http.StatusAccepted})
	var job repository.LifecycleJob
	if capability, ok := formatmodule.Capability[formatmodule.Promotion](rawFormatRegistry(rawformat.Options{Store: s.h.sessions.store, Jobs: s.h.sessions.store}), in.Source.Format); ok {
		job, _, err = capability.EnqueuePromotion(ctx, in.Source.ID, target.ID, in.Coordinate, in.Digest, in.IdempotencyKey, promoteAudit...)
	} else {
		switch in.Source.Format {
		case repository.FormatAPT:
			job, _, err = (aptpublication.Distribution{Store: s.h.sessions.store}).EnqueuePromotion(ctx, target.ID, in.IdempotencyKey, aptpublication.PromotionPayload{SourceRepositoryID: in.Source.ID, Coordinate: in.Coordinate, Digest: in.Digest, TargetSuite: *in.AptTargetSuite, Actor: in.Principal.Actor}, promoteAudit...)
		case repository.FormatMaven:
			promotionID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("maven-promotion:"+target.ID+":"+in.IdempotencyKey)).String()
			job, _, err = (mavenprotocol.NativePromotion{Store: s.h.sessions.store}).Enqueue(ctx, target.ID, in.IdempotencyKey, mavenprotocol.PromotionPayload{SourceRepositoryID: in.Source.ID, Coordinate: in.Coordinate, Digest: in.Digest, PromotionID: promotionID}, promoteAudit...)
		case repository.FormatOCI:
			job, _, err = (ociprotocol.NativePromotion{Store: s.h.sessions.store}).Enqueue(ctx, target.ID, in.IdempotencyKey, ociprotocol.PromotionPayload{SourceRepositoryID: in.Source.ID, Name: in.Coordinate, Digest: in.Digest}, promoteAudit...)
		case repository.FormatConan:
			reference, revision, _ := strings.Cut(in.Coordinate, "#")
			job, _, err = (conanprotocol.NativePromotion{Store: s.h.sessions.store}).Enqueue(ctx, target.ID, in.IdempotencyKey, conanprotocol.PromotionPayload{SourceRepositoryID: in.Source.ID, Reference: reference, Revision: revision, Digest: in.Digest}, promoteAudit...)
		case repository.FormatNPM:
			packageName, version, _ := parseNPMVersionCoordinate(in.Coordinate)
			job, _, err = (npmprotocol.NativePromotion{Store: s.h.sessions.store}).Enqueue(ctx, target.ID, in.IdempotencyKey, npmprotocol.PromotionPayload{SourceRepositoryID: in.Source.ID, PackageName: packageName, Version: version, Digest: in.Digest}, promoteAudit...)
		case repository.FormatPyPI:
			project, version, _ := parsePyPIVersionCoordinate(in.Coordinate)
			job, _, err = (NativePyPIPromotion{Store: s.h.sessions.store}).Enqueue(ctx, target.ID, in.IdempotencyKey, PyPIPromotionPayload{SourceRepositoryID: in.Source.ID, Project: project, Version: version, Digest: in.Digest}, promoteAudit...)
		case repository.FormatGo:
			modulePath, version, _ := parseGoModuleVersionCoordinate(in.Coordinate)
			job, _, err = (NativeGoPromotion{Store: s.h.sessions.store}).Enqueue(ctx, target.ID, in.IdempotencyKey, GoPromotionPayload{SourceRepositoryID: in.Source.ID, Module: modulePath, Version: version, Digest: in.Digest}, promoteAudit...)
		case repository.FormatCargo:
			name, version, _ := splitVersionCoordinate(in.Coordinate)
			job, _, err = (NativeCargoPromotion{Store: s.h.sessions.store}).Enqueue(ctx, target.ID, in.IdempotencyKey, CargoPromotionPayload{SourceRepositoryID: in.Source.ID, Name: name, Version: version, Digest: in.Digest}, promoteAudit...)
		}
	}
	if errors.Is(err, repository.ErrIdempotencyConflict) {
		return repository.LifecycleJob{}, promotionError(http.StatusConflict, "idempotency_conflict", "Idempotency-Key conflicts with an existing promotion job")
	}
	if err != nil {
		return repository.LifecycleJob{}, promotionError(http.StatusInternalServerError, "internal_error", "enqueue promotion job failed")
	}
	return job, nil
}
