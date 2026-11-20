package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/aptpublication"
	formatmodule "github.com/mlops-ca/notarx/internal/format"
	rawformat "github.com/mlops-ca/notarx/internal/format/raw"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

func (h generatedRepositoryAPIAdapter) CreateRepositoryPromotion(w http.ResponseWriter, r *http.Request, repositoryID adminopenapi.RepositoryId, params adminopenapi.CreateRepositoryPromotionParams) {
	h.withRepositoryScope(w, r, repositoryID.String(), RepositoryAdmin, func(principal Principal, source repository.HostedRepository) {
		if !supportsAPTDistributionPreview(source, h.aptSnapshotPublisher, repository.RepositoryOperationPromote) {
			writeHostedProblem(w, http.StatusConflict, "unsupported_operation", "promotion is not supported for this repository format")
			return
		}
		var request adminopenapi.PromotionRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil || !validAPTDistributionScope(source.Format, request.Coordinate, request.AptTargetSuite) || !validRepositoryDigest(request.Digest) || (source.Format == repository.FormatMaven && !validMavenCoordinate(request.Coordinate)) || (source.Format == repository.FormatOCI && (request.Coordinate == "" || strings.Contains(request.Coordinate, "@"))) || !validMigratedCoordinate(source.Format, request.Coordinate) || (source.Format == repository.FormatConan && !validConanReplicationCoordinate(request.Coordinate)) || (source.Format == repository.FormatNPM && !validNPMVersionCoordinate(request.Coordinate)) || (source.Format == repository.FormatPyPI && !validPyPIVersionCoordinate(request.Coordinate)) || (source.Format == repository.FormatGo && !validGoModuleVersionCoordinate(request.Coordinate)) || (source.Format == repository.FormatCargo && !validCargoVersionCoordinate(request.Coordinate)) {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "targetRepositoryId, immutable artifact coordinate, and digest are required")
			return
		}
		h.withRepositoryScopeForPrincipal(w, r, principal, request.TargetRepositoryId.String(), RepositoryAdmin, func(Principal, repository.HostedRepository) {
			job, perr := PromotionService{h: h}.Request(r.Context(), PromotionRequest{Principal: principal, Source: source, TargetID: request.TargetRepositoryId.String(), Coordinate: request.Coordinate, Digest: request.Digest, AptTargetSuite: request.AptTargetSuite, IdempotencyKey: string(params.IdempotencyKey)})
			if perr != nil {
				writeHostedProblem(w, perr.Status, perr.Code, perr.Message)
				return
			}
			writeNativeMavenJSON(w, http.StatusAccepted, lifecycleJobResponse(job))
		})
	})
}

func (h generatedRepositoryAPIAdapter) CreateRepositoryReplication(w http.ResponseWriter, r *http.Request, repositoryID adminopenapi.RepositoryId, params adminopenapi.CreateRepositoryReplicationParams) {
	h.withRepositoryScope(w, r, repositoryID.String(), RepositoryAdmin, func(principal Principal, source repository.HostedRepository) {
		var request adminopenapi.ReplicationRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || !validAPTDistributionScope(source.Format, request.Coordinate, request.AptTargetSuite) || !supportsAPTDistributionPreview(source, h.aptSnapshotPublisher, repository.RepositoryOperationReplicate) || strings.TrimSpace(request.Coordinate) == "" || !validRepositoryDigest(request.Digest) || (source.Format == repository.FormatMaven && !validMavenCoordinate(request.Coordinate)) || (source.Format == repository.FormatConan && !validConanReplicationCoordinate(request.Coordinate)) || (source.Format == repository.FormatNPM && !validNPMVersionCoordinate(request.Coordinate)) || (source.Format == repository.FormatPyPI && !validPyPIVersionCoordinate(request.Coordinate)) || (source.Format == repository.FormatGo && !validGoModuleVersionCoordinate(request.Coordinate)) || (source.Format == repository.FormatCargo && !validCargoVersionCoordinate(request.Coordinate)) {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "replication requires a visible format-specific coordinate and sha256 digest")
			return
		}
		h.withRepositoryScopeForPrincipal(w, r, principal, request.TargetRepositoryId.String(), RepositoryAdmin, func(Principal, repository.HostedRepository) {
			target, err := h.store.GetHostedRepository(r.Context(), request.TargetRepositoryId.String())
			if err != nil || target.ID == source.ID || target.Format != source.Format || target.Type != repository.RepositoryTypeHosted || target.State != repository.RepositoryActive {
				writeHostedProblem(w, http.StatusConflict, "invalid_target", "target must be an active Hosted repository with the same format")
				return
			}
			if h.rejectQuarantinedDistribution(w, r, principal, source, target, request.Coordinate, request.Digest, "replicate") {
				return
			}
			format := source.Format
			var checkpoints []repository.ReplicationCheckpoint
			if capability, ok := formatmodule.Capability[formatmodule.Replication](rawFormatRegistry(rawformat.Options{Store: h.sessions.store}), format); ok {
				var lookupErr error
				checkpoints, lookupErr = capability.ReplicationCheckpoints(r.Context(), source.ID, request.Coordinate, request.Digest)
				if errors.Is(lookupErr, repository.ErrNotFound) {
					writeHostedProblem(w, http.StatusNotFound, "not_found", "source artifact is unavailable")
					return
				}
				if lookupErr != nil {
					writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "lookup source artifact failed")
					return
				}
			} else if format == repository.FormatAPT {
				asset, lookupErr := h.sessions.store.GetAPTScanAsset(r.Context(), source.ID, request.Coordinate, request.Digest)
				if lookupErr != nil {
					writeAPTSnapshotProblem(w, lookupErr)
					return
				}
				checkpoints = []repository.ReplicationCheckpoint{{SourceObjectKey: asset.ObjectKey, ObjectKey: asset.ObjectKey, Digest: asset.Digest, Size: asset.Size}}
			} else if format == repository.FormatMaven {
				artifact, lookupErr := h.sessions.store.GetMavenArtifactByCoordinate(r.Context(), source.ID, request.Coordinate)
				if errors.Is(lookupErr, repository.ErrNotFound) || artifact.Digest != request.Digest {
					writeHostedProblem(w, http.StatusNotFound, "not_found", "source Maven artifact is unavailable")
					return
				}
				if lookupErr != nil {
					writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "lookup source Maven artifact failed")
					return
				}
				assets, lookupErr := h.sessions.store.ListMavenAssets(r.Context(), source.ID, request.Coordinate)
				if lookupErr != nil || len(assets) == 0 {
					writeHostedProblem(w, http.StatusNotFound, "not_found", "source Maven assets are unavailable")
					return
				}
				checkpoints = make([]repository.ReplicationCheckpoint, 0, len(assets))
				seenDigests := make(map[string]bool, len(assets))
				for _, asset := range assets {
					key := asset.Digest + "\x00" + strconv.FormatInt(asset.Size, 10)
					if seenDigests[key] {
						continue
					}
					seenDigests[key] = true
					checkpoints = append(checkpoints, repository.ReplicationCheckpoint{SourceObjectKey: asset.ObjectKey, ObjectKey: mavenReplicationTargetObjectKey(target.ID, asset.Digest), Digest: asset.Digest, Size: asset.Size})
				}
			} else if format == repository.FormatOCI {
				manifest, lookupErr := h.sessions.store.GetOCIManifest(r.Context(), source.ID, request.Coordinate, request.Digest)
				if errors.Is(lookupErr, repository.ErrNotFound) || manifest.Digest != request.Digest {
					writeHostedProblem(w, http.StatusNotFound, "not_found", "source OCI manifest is unavailable")
					return
				}
				if lookupErr != nil {
					writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "lookup source OCI manifest failed")
					return
				}
				checkpoints = []repository.ReplicationCheckpoint{{SourceObjectKey: manifest.ObjectKey, ObjectKey: ociReplicationTargetObjectKey(target.ID, manifest.Name, manifest.Digest), Digest: manifest.Digest, Size: manifest.Size}}
			} else if format == repository.FormatNPM {
				packageName, version, _ := parseNPMVersionCoordinate(request.Coordinate)
				item, lookupErr := h.sessions.store.GetNPMVersion(r.Context(), source.ID, packageName, version)
				if errors.Is(lookupErr, repository.ErrNotFound) || item.Digest != request.Digest || item.ObjectKey == "" {
					writeHostedProblem(w, http.StatusNotFound, "not_found", "source npm version is unavailable")
					return
				}
				if lookupErr != nil {
					writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "lookup source npm version failed")
					return
				}
				checkpoints = []repository.ReplicationCheckpoint{{SourceObjectKey: item.ObjectKey, ObjectKey: npmReplicationTargetObjectKey(target.ID, item.Digest), Digest: item.Digest, Size: item.Size}}
			} else if format == repository.FormatPyPI {
				project, version, _ := parsePyPIVersionCoordinate(request.Coordinate)
				files, lookupErr := h.sessions.store.ListPyPIProjectFiles(r.Context(), source.ID, project)
				if lookupErr != nil {
					writeHostedProblem(w, http.StatusNotFound, "not_found", "source PyPI version is unavailable")
					return
				}
				digestMatched := false
				for _, file := range files {
					if file.Version != version {
						continue
					}
					if file.ObjectKey == "" {
						writeHostedProblem(w, http.StatusConflict, "not_cached", "source PyPI version is not fully cached")
						return
					}
					digestMatched = digestMatched || file.Digest == request.Digest
					checkpoints = append(checkpoints, repository.ReplicationCheckpoint{SourceObjectKey: file.ObjectKey, ObjectKey: pypiReplicationTargetObjectKey(target.ID, file.Filename, file.Digest), Digest: file.Digest, Size: file.Size})
				}
				if len(checkpoints) == 0 || !digestMatched {
					writeHostedProblem(w, http.StatusNotFound, "not_found", "source PyPI version is unavailable")
					return
				}
			} else if format == repository.FormatCargo {
				name, version, _ := splitVersionCoordinate(request.Coordinate)
				publication, lookupErr := h.cargo.GetCargoPublication(r.Context(), source.ID, name, version)
				if errors.Is(lookupErr, repository.ErrNotFound) || publication.Digest != request.Digest {
					writeHostedProblem(w, http.StatusNotFound, "not_found", "source Cargo version is unavailable")
					return
				}
				if lookupErr != nil {
					writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "lookup source Cargo version failed")
					return
				}
				checkpoints = []repository.ReplicationCheckpoint{{SourceObjectKey: publication.ObjectKey,
					ObjectKey: publication.ObjectKey, Digest: publication.Digest, Size: publication.Size}}
			} else if format == repository.FormatGo {
				modulePath, version, _ := parseGoModuleVersionCoordinate(request.Coordinate)
				publication, lookupErr := loadGoDistributionPublication(r.Context(), h.sessions.store, source.ID, modulePath, version, request.Digest)
				if errors.Is(lookupErr, repository.ErrNotFound) {
					writeHostedProblem(w, http.StatusNotFound, "not_found", "source Go module version is unavailable")
					return
				}
				if lookupErr != nil {
					writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "lookup source Go module version failed")
					return
				}
				checkpoints = make([]repository.ReplicationCheckpoint, 0, len(publication.Assets))
				for _, asset := range publication.Assets {
					checkpoints = append(checkpoints, repository.ReplicationCheckpoint{
						SourceObjectKey: asset.ObjectKey,
						ObjectKey:       goReplicationTargetObjectKey(target.ID, asset.Digest, asset.Kind),
						Digest:          asset.Digest,
						Size:            asset.Size,
					})
				}
			} else {
				reference, revision, _ := strings.Cut(request.Coordinate, "#")
				recipe, lookupErr := h.conan.GetConanRecipeRevision(r.Context(), source.ID, reference, revision)
				if errors.Is(lookupErr, repository.ErrNotFound) || recipe.Digest != request.Digest || recipe.State != "visible" {
					writeHostedProblem(w, http.StatusNotFound, "not_found", "source Conan recipe revision is unavailable")
					return
				}
				if lookupErr != nil {
					writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "lookup source Conan recipe revision failed")
					return
				}
				checkpoints, lookupErr = conanReplicationCheckpoints(r.Context(), h.conan, source.ID, target.ID, reference, revision)
				if lookupErr != nil || len(checkpoints) == 0 {
					writeHostedProblem(w, http.StatusNotFound, "not_found", "source Conan assets are unavailable")
					return
				}
			}
			plan, _, err := h.replication.CreateReplicationPlan(r.Context(), repository.ReplicationPlan{ID: ids.New(), SourceRepositoryID: source.ID, TargetRepositoryID: target.ID, Format: format, APTTargetSuite: aptDistributionSuite(request.AptTargetSuite), Coordinate: request.Coordinate, Digest: request.Digest, IdempotencyKey: string(params.IdempotencyKey)}, checkpoints,
				h.auditFor(repository.AuditRecord{Repository: source.Name, GroupName: source.Name, Actor: principal.Actor, Resource: request.Coordinate, Operation: "replicate", Status: http.StatusAccepted})...)
			if errors.Is(err, repository.ErrIdempotencyConflict) {
				writeHostedProblem(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key conflicts with an existing replication plan")
				return
			}
			if err != nil {
				writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "create replication plan failed")
				return
			}
			writeNativeMavenJSON(w, http.StatusAccepted, toOpenAPIReplicationPlan(plan))
		})
	})
}

type distributionRejection struct {
	status  int
	code    string
	message string
}

func (h generatedRepositoryAPIAdapter) rejectQuarantinedDistribution(w http.ResponseWriter, r *http.Request, principal Principal, source, target repository.HostedRepository, coordinate, digest, operation string) bool {
	rejection := h.checkQuarantinedDistribution(r.Context(), principal, source, target, coordinate, digest, operation)
	if rejection == nil {
		return false
	}
	writeHostedProblem(w, rejection.status, rejection.code, rejection.message)
	return true
}

// checkQuarantinedDistribution refuses a distribution whose artifact is unavailable or quarantined,
// recording the quarantine denial in the audit log.
func (h generatedRepositoryAPIAdapter) checkQuarantinedDistribution(ctx context.Context, principal Principal, source, target repository.HostedRepository, coordinate, digest, operation string) *distributionRejection {
	digests, err := h.artifactDistributionDigests(ctx, source, coordinate, digest)
	if errors.Is(err, repository.ErrNotFound) {
		return &distributionRejection{http.StatusNotFound, "not_found", "source artifact is unavailable"}
	}
	if err != nil {
		return &distributionRejection{http.StatusInternalServerError, "internal_error", "evaluate distribution artifact identities failed"}
	}
	allowed, err := repository.ArtifactDistributionAllowedForDigests(ctx, h.quarantine, source.ID, source.Format, coordinate, digests)
	if err != nil {
		return &distributionRejection{http.StatusInternalServerError, "internal_error", "evaluate artifact quarantine failed"}
	}
	if allowed {
		return nil
	}
	if h.audit != nil {
		_ = h.audit.RecordAudit(ctx, repository.AuditRecord{
			GroupName: target.Name, Repository: target.Name, Actor: principal.Actor,
			Outcome: repository.AuditAccessDenied, OccurredAt: time.Now().UTC(),
			Format: string(source.Format), Resource: coordinate, Representation: digest,
			Operation: operation + ".quarantine", Status: http.StatusForbidden,
			AuthorizationReason: repository.ArtifactQuarantinedReason,
			CacheDisposition:    "bypass",
		})
	}
	return &distributionRejection{http.StatusForbidden, repository.ArtifactQuarantinedReason, "artifact is quarantined and cannot be distributed"}
}

func validConanReplicationCoordinate(value string) bool {
	reference, revision, found := strings.Cut(value, "#")
	if !found || !validConanSegment(revision) {
		return false
	}
	parts := strings.Split(reference, "/")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		if !validConanSegment(part) {
			return false
		}
	}
	return true
}

func (h generatedRepositoryAPIAdapter) ListRepositoryReplications(w http.ResponseWriter, r *http.Request, repositoryID adminopenapi.RepositoryId) {
	h.withRepositoryScope(w, r, repositoryID.String(), RepositoryAdmin, func(Principal, repository.HostedRepository) {
		plans, err := h.replication.ListReplicationPlans(r.Context(), repositoryID.String(), 100)
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list replication plans failed")
			return
		}
		items := make([]adminopenapi.ReplicationPlan, 0, len(plans))
		for _, plan := range plans {
			items = append(items, toOpenAPIReplicationPlan(plan))
		}
		writeNativeMavenJSON(w, http.StatusOK, items)
	})
}

func (h generatedRepositoryAPIAdapter) GetRepositoryReplication(w http.ResponseWriter, r *http.Request, repositoryID adminopenapi.RepositoryId, replicationPlanID adminopenapi.ReplicationPlanId) {
	h.withRepositoryScope(w, r, repositoryID.String(), RepositoryAdmin, func(_ Principal, _ repository.HostedRepository) {
		plan, err := h.replication.GetReplicationPlan(r.Context(), repositoryID.String(), replicationPlanID.String())
		if errors.Is(err, repository.ErrNotFound) {
			writeHostedProblem(w, http.StatusNotFound, "not_found", "replication plan not found")
			return
		}
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "get replication plan failed")
			return
		}
		checkpoints, err := h.replication.ListReplicationCheckpoints(r.Context(), plan.ID)
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list replication checkpoints failed")
			return
		}
		writeNativeMavenJSON(w, http.StatusOK, toOpenAPIReplicationPlanDetail(plan, checkpoints))
	})
}

func (h generatedRepositoryAPIAdapter) DeleteRepositoryReplication(w http.ResponseWriter, r *http.Request, repositoryID adminopenapi.RepositoryId, replicationPlanID adminopenapi.ReplicationPlanId) {
	h.withRepositoryScope(w, r, repositoryID.String(), RepositoryAdmin, func(_ Principal, _ repository.HostedRepository) {
		plan, err := h.replication.GetReplicationPlan(r.Context(), repositoryID.String(), replicationPlanID.String())
		if errors.Is(err, repository.ErrNotFound) {
			writeHostedProblem(w, http.StatusNotFound, "not_found", "replication plan not found")
			return
		}
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "get replication plan failed")
			return
		}
		// Only pending or failed plans can be cancelled: the worker owns a
		// running plan mid-flight, and completed/cancelled plans are terminal.
		if plan.State != "pending" && plan.State != "failed" {
			writeHostedProblem(w, http.StatusConflict, "invalid_state", "only pending or failed replication plans can be cancelled")
			return
		}
		if err := h.replication.CancelReplicationPlan(r.Context(), repositoryID.String(), replicationPlanID.String()); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				writeHostedProblem(w, http.StatusConflict, "invalid_state", "replication plan was claimed or completed before cancellation")
				return
			}
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "cancel replication plan failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func validRepositoryDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func validOCIRestoreCoordinate(value string) bool {
	_, _, ok := parseOCIRestoreCoordinate(value)
	return ok
}

func parseOCIRestoreCoordinate(value string) (name, digest string, ok bool) {
	if len(value) > 1024 || strings.ContainsAny(value, "\\\x00") {
		return "", "", false
	}
	name, digest, split := strings.Cut(value, "@")
	if !split || name == "" || digest == "" || strings.Contains(digest, "@") || !validOCIImagePrefix(name) || !validRepositoryDigest(digest) {
		return "", "", false
	}
	// OCI image names cannot contain empty path components. The shared prefix
	// validator intentionally accepts a trailing slash for search prefixes.
	for _, component := range strings.Split(name, "/") {
		if component == "" {
			return "", "", false
		}
	}
	return name, digest, true
}

func toOpenAPIReplicationPlan(plan repository.ReplicationPlan) adminopenapi.ReplicationPlan {
	item := adminopenapi.ReplicationPlan{Id: uuid.MustParse(plan.ID), SourceRepositoryId: uuid.MustParse(plan.SourceRepositoryID), TargetRepositoryId: uuid.MustParse(plan.TargetRepositoryID), Format: adminopenapi.Format(plan.Format), State: adminopenapi.ReplicationPlanState(plan.State), CreatedAt: plan.CreatedAt}
	if plan.APTTargetSuite != "" {
		item.AptTargetSuite = &plan.APTTargetSuite
	}
	if plan.Coordinate != "" && plan.Digest != "" {
		item.Coordinate = &plan.Coordinate
		item.Digest = &plan.Digest
	}
	if !plan.StartedAt.IsZero() {
		item.StartedAt = &plan.StartedAt
	}
	if !plan.CompletedAt.IsZero() {
		item.CompletedAt = &plan.CompletedAt
	}
	if plan.LastError != "" {
		item.LastError = &plan.LastError
	}
	return item
}

func toOpenAPIReplicationPlanDetail(plan repository.ReplicationPlan, checkpoints []repository.ReplicationCheckpoint) adminopenapi.ReplicationPlanDetail {
	item := adminopenapi.ReplicationPlanDetail{Id: uuid.MustParse(plan.ID), SourceRepositoryId: uuid.MustParse(plan.SourceRepositoryID), TargetRepositoryId: uuid.MustParse(plan.TargetRepositoryID), Format: adminopenapi.Format(plan.Format), State: adminopenapi.ReplicationPlanDetailState(plan.State), CreatedAt: plan.CreatedAt, Checkpoints: make([]adminopenapi.ReplicationCheckpointProgress, 0, len(checkpoints))}
	if plan.APTTargetSuite != "" {
		item.AptTargetSuite = &plan.APTTargetSuite
	}
	if plan.Coordinate != "" && plan.Digest != "" {
		item.Coordinate = &plan.Coordinate
		item.Digest = &plan.Digest
	}
	if !plan.StartedAt.IsZero() {
		item.StartedAt = &plan.StartedAt
	}
	if !plan.CompletedAt.IsZero() {
		item.CompletedAt = &plan.CompletedAt
	}
	if plan.LastError != "" {
		item.LastError = &plan.LastError
	}
	for _, checkpoint := range checkpoints {
		progress := adminopenapi.ReplicationCheckpointProgress{ObjectKey: checkpoint.ObjectKey, Digest: checkpoint.Digest, Size: checkpoint.Size, ByteOffset: checkpoint.ByteOffset, State: adminopenapi.ReplicationCheckpointProgressState(checkpoint.State), Attempts: checkpoint.Attempts}
		if checkpoint.LastError != "" {
			progress.LastError = &checkpoint.LastError
		}
		if !checkpoint.VerifiedAt.IsZero() {
			progress.VerifiedAt = &checkpoint.VerifiedAt
		}
		item.Checkpoints = append(item.Checkpoints, progress)
	}
	return item
}

func (h generatedRepositoryAPIAdapter) RestoreRepositoryArtifact(w http.ResponseWriter, r *http.Request, repositoryID adminopenapi.RepositoryId) {
	h.withRepositoryScope(w, r, repositoryID.String(), RepositoryAdmin, func(principal Principal, repo repository.HostedRepository) {
		var request adminopenapi.RestoreArtifact
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || (repo.Format == repository.FormatConan && !validConanRestoreCoordinate(request.Coordinate)) || (repo.Format == repository.FormatMaven && !validMavenCoordinate(request.Coordinate)) || (repo.Format == repository.FormatOCI && !validOCIRestoreCoordinate(request.Coordinate)) || !validMigratedCoordinate(repo.Format, request.Coordinate) || (repo.Format == repository.FormatNPM && !validNPMVersionCoordinate(request.Coordinate)) || (repo.Format == repository.FormatPyPI && !validPyPIVersionCoordinate(request.Coordinate)) || (repo.Format == repository.FormatGo && !validGoModuleVersionCoordinate(request.Coordinate)) || (repo.Format == repository.FormatCargo && !validCargoVersionCoordinate(request.Coordinate)) {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "coordinate must identify a supported artifact tombstone")
			return
		}
		if !repository.FormatSupportsOperation(repo.Format, repo.Type, repository.RepositoryOperationRestore) {
			writeHostedProblem(w, http.StatusConflict, "unsupported_operation", "restore is not supported for this repository format")
			return
		}
		if _, err := h.tombstones.GetArtifactTombstone(r.Context(), repo.ID, repo.Format, request.Coordinate); errors.Is(err, repository.ErrNotFound) {
			writeHostedProblem(w, http.StatusNotFound, "not_found", "tombstone not found")
			return
		} else if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "get tombstone failed")
			return
		}
		var err error
		if capability, ok := formatmodule.Capability[formatmodule.Governance](rawFormatRegistry(rawformat.Options{Store: h.sessions.store}), repo.Format); ok {
			err = capability.Restore(r.Context(), repo.ID, request.Coordinate)
		} else {
			switch repo.Format {
			case repository.FormatMaven:
				artifact, getErr := h.sessions.store.GetMavenArtifactByCoordinate(r.Context(), repo.ID, request.Coordinate)
				if getErr != nil {
					err = getErr
				} else {
					_, err = h.sessions.store.RestoreMavenArtifact(r.Context(), repo.ID, artifact.ID)
				}
			case repository.FormatConan:
				err = h.restoreConanCoordinate(r, repo.ID, request.Coordinate)
			case repository.FormatOCI:
				name, digest, _ := parseOCIRestoreCoordinate(request.Coordinate)
				_, err = h.oci.RestoreOCIManifest(r.Context(), repo.ID, name, digest)
			case repository.FormatNPM:
				name, version, _ := parseNPMVersionCoordinate(request.Coordinate)
				err = h.restoreNPMVersion(r, repo, name, version, principal.Actor)
			case repository.FormatPyPI:
				project, version, _ := parsePyPIVersionCoordinate(request.Coordinate)
				_, err = h.sessions.store.RestorePyPIVersion(r.Context(), repo.ID, project, version)
			case repository.FormatGo:
				modulePath, version, _ := parseGoModuleVersionCoordinate(request.Coordinate)
				_, err = h.sessions.store.RestoreGoModuleVersion(r.Context(), repo.ID, modulePath, version)
			case repository.FormatCargo:
				name, version, _ := splitVersionCoordinate(request.Coordinate)
				if h.objects[repository.FormatCargo] == nil {
					err = repository.ErrDisabled
					break
				}
				reservation, lookupErr := h.cargo.GetCargoIdentityReservation(r.Context(), repo.ID, name, version)
				if lookupErr != nil {
					err = lookupErr
					break
				}
				objectKey := "native/cargo/sha256/" + strings.TrimPrefix(reservation.Digest, "sha256:")
				stored, statErr := h.objects[repository.FormatCargo].Stat(r.Context(), objectKey)
				if errors.Is(statErr, objectstore.ErrNotFound) {
					err = repository.ErrDisabled
				} else if statErr != nil {
					err = statErr
				} else if stored.Digest != reservation.Digest {
					err = repository.ErrDisabled
				} else {
					_, err = h.cargo.RestoreCargoPublication(r.Context(), repo.ID, name, version,
						h.auditFor(repository.AuditRecord{GroupName: repo.Name, Repository: repo.Name, Actor: principal.Actor, Resource: request.Coordinate, Operation: "artifact.restore", Status: http.StatusNoContent})...)
				}
			}
		}
		if errors.Is(err, repository.ErrNotFound) || errors.Is(err, repository.ErrDisabled) || errors.Is(err, repository.ErrNameExists) {
			writeHostedProblem(w, http.StatusConflict, "restore_unavailable", "artifact cannot be restored")
			return
		}
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "restore artifact failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func (h generatedRepositoryAPIAdapter) restoreNPMVersion(r *http.Request, repo repository.HostedRepository, name, version, actor string) error {
	objects := h.objects[repository.FormatNPM]
	if objects == nil {
		return repository.ErrDisabled
	}
	pkg, err := h.npmRestore.GetNPMPackage(r.Context(), repo.ID, name)
	if err != nil {
		return err
	}
	for _, item := range pkg.TombstonedVersions {
		if item.Version != version {
			continue
		}
		if !item.CollectingAt.IsZero() || !item.CollectedAt.IsZero() {
			return repository.ErrDisabled
		}
		info, statErr := objects.Stat(r.Context(), item.ObjectKey)
		if errors.Is(statErr, objectstore.ErrNotFound) {
			return repository.ErrDisabled
		}
		if statErr != nil {
			return statErr
		}
		if info.Digest != item.Digest || info.Size != item.Size {
			return repository.ErrDisabled
		}
		_, err = h.npmRestore.RestoreNPMVersion(r.Context(), repository.NPMRestoreRequest{RepositoryID: repo.ID, PackageName: name, Version: version,
			Audit: repository.AuditRecord{GroupName: repo.Name, Repository: repo.Name, Actor: actor, Status: http.StatusNoContent, CacheDisposition: "bypass", Evidence: map[string]string{"reason": "management_restore"}}})
		return err
	}
	return repository.ErrNotFound
}

func (h generatedRepositoryAPIAdapter) restoreConanCoordinate(r *http.Request, repositoryID, coordinate string) error {
	reference, recipeRevision, packageID, packageRevision, packageRestore, ok := parseConanRestoreCoordinate(coordinate)
	if !ok {
		return repository.ErrNotFound
	}
	if packageRestore {
		_, err := h.conan.RestoreConanPackageRevision(r.Context(), repositoryID, reference, recipeRevision, packageID, packageRevision)
		return err
	}
	_, err := h.conan.RestoreConanRecipeRevision(r.Context(), repositoryID, reference, recipeRevision)
	return err
}

func supportsAPTDistributionPreview(repo repository.HostedRepository, publisher *aptpublication.Publisher, operation repository.RepositoryOperation) bool {
	return repository.FormatSupportsOperation(repo.Format, repo.Type, operation) || (repo.Format == repository.FormatAPT && repo.Type == repository.RepositoryTypeHosted && publisher != nil)
}
func validAPTDistributionScope(format repository.Format, coordinate string, suite *string) bool {
	if format != repository.FormatAPT {
		return suite == nil
	}
	return suite != nil && repository.ValidAPTPublicationScope(*suite) && repository.ValidAPTArtifactCoordinate(coordinate)
}
func aptDistributionSuite(suite *string) string {
	if suite == nil {
		return ""
	}
	return *suite
}
