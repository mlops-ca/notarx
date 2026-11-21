package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/config"
	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

// importBodyLimit bounds the JSON that opens a session: the claimed blobs of a session are capped far below it.
const importBodyLimit = 4 << 20

// importDigestPattern is the only digest a session accepts, the same one the session store requires.
var importDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// importHandler owns the receiving side of a push from another notarx instance (ADR 0021, "Remote targets"): it opens
// sessions, stages and verifies blobs and commits through the importer of the target format. The sender is never
// trusted: the claims are recorded as received, the bytes are hashed while they stream, the commit re-verifies them and
// the format's importer recomputes the coordinate from them. Every operation needs ImportWrite on the target repository,
// which implies no read, so nothing here reports what other repositories hold.
type importHandler struct {
	managementScope
	sessions repository.ImportSessionStore
	policies interface {
		GetRepositorySecurityPolicy(ctx context.Context, repositoryID string) (repository.RepositorySecurityPolicy, error)
	}
	instance localInstanceIDReader
	// admission is the store the quarantine admission lock is taken through.
	admission any
	registry  *formatmodule.Registry
	staging   ObjectStore
	limits    repository.ImportLimits
	ttl       time.Duration
	maxHops   int
	now       func() time.Time
}

type importHandlerOptions struct {
	Scope    managementScope
	Store    GatewayStore
	Registry *formatmodule.Registry
	Staging  ObjectStore
	Config   config.ImportConfig
	MaxHops  int
}

func newImportHandler(options importHandlerOptions) importHandler {
	sessions, _ := any(options.Store).(repository.ImportSessionStore)
	ttl := options.Config.SessionTTL
	if ttl <= 0 {
		ttl = config.DefaultImportSessionTTL
	}
	maxHops := options.MaxHops
	if maxHops <= 0 {
		maxHops = config.DefaultPromotionMaxHops
	}
	return importHandler{managementScope: options.Scope, sessions: sessions, policies: options.Store, instance: options.Store, admission: options.Store,
		registry: options.Registry, staging: options.Staging, limits: options.Config.Limits(), ttl: ttl, maxHops: maxHops, now: func() time.Time { return time.Now().UTC() }}
}

// advertisedFormats lists the formats that can take an import, as the instance document advertises them: none while the
// endpoints answer 503, so a sender never picks an instance whose pushes cannot succeed.
func (h importHandler) advertisedFormats() []string {
	formats := []string{}
	if h.sessions == nil || h.staging == nil {
		return formats
	}
	for _, format := range h.registry.Formats() {
		if _, ok := formatmodule.Capability[formatmodule.Importer](h.registry, format); ok {
			formats = append(formats, string(format))
		}
	}
	return formats
}

func (h importHandler) ready(w http.ResponseWriter) bool {
	if h.sessions == nil || h.staging == nil {
		writeHostedProblem(w, http.StatusServiceUnavailable, "import_unavailable", "imports are not available on this instance")
		return false
	}
	return true
}

func importSessionResponse(session repository.ImportSession, missing []repository.ImportBlob) (adminopenapi.ImportSession, error) {
	id, err := uuid.Parse(session.ID)
	if err != nil {
		return adminopenapi.ImportSession{}, err
	}
	repositoryID, err := uuid.Parse(session.TargetRepositoryID)
	if err != nil {
		return adminopenapi.ImportSession{}, err
	}
	response := adminopenapi.ImportSession{Id: id, RepositoryId: repositoryID, Format: string(session.Format), Coordinate: session.Coordinate, Digest: session.Digest,
		State: adminopenapi.ImportSessionState(session.State), Bytes: session.Bytes, Missing: make([]adminopenapi.ImportBlobClaim, 0, len(missing)),
		CreatedAt: session.CreatedAt, ExpiresAt: session.ExpiresAt}
	for _, blob := range missing {
		response.Missing = append(response.Missing, adminopenapi.ImportBlobClaim{Digest: blob.Digest, Size: blob.Size})
	}
	return response, nil
}

func (h importHandler) writeSession(w http.ResponseWriter, r *http.Request, status int, session repository.ImportSession) {
	missing, err := h.sessions.MissingImportBlobs(r.Context(), session.ID)
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list the missing import blobs failed")
		return
	}
	response, err := importSessionResponse(session, missing)
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "stored import session is invalid")
		return
	}
	writeNativeMavenJSON(w, status, response)
}

// chainPathOf validates the chain the sender reports and appends this instance's own hop (ADR 0021, Chains): a path longer
// than the cap or one that already contains this instance and the target repository is refused; a missing path is depth 0.
func (h importHandler) chainPathOf(ctx context.Context, reported *[]adminopenapi.PromotionChainHop, target repository.HostedRepository, now time.Time) ([]repository.PromotionChainHop, string, string, error) {
	var hops []adminopenapi.PromotionChainHop
	if reported != nil {
		hops = *reported
	}
	if len(hops) > h.maxHops {
		return nil, "chain_too_long", "chainPath is longer than the hops this instance accepts", nil
	}
	local, err := h.instance.LocalInstanceID(ctx)
	if err != nil {
		return nil, "", "", err
	}
	path := make([]repository.PromotionChainHop, 0, len(hops)+1)
	for _, hop := range hops {
		if hop.Instance == "" || len(hop.Instance) > 64 || hop.Repository == "" || len(hop.Repository) > 255 || len(hop.RuleId) > 64 {
			return nil, "invalid_chain_path", "every hop of chainPath needs an instance (at most 64 characters), a repository (at most 255) and a ruleId of at most 64", nil
		}
		if strings.EqualFold(hop.Instance, local) && hop.Repository == target.Name {
			return nil, "chain_loop", "chainPath already contains this instance and the target repository", nil
		}
		path = append(path, repository.PromotionChainHop{Instance: hop.Instance, Repository: hop.Repository, RuleID: hop.RuleId, At: hop.At})
	}
	return append(path, repository.PromotionChainHop{Instance: local, Repository: target.Name, At: now}), "", "", nil
}

// importLimitProblems maps the refusals of the session store to their own status and code.
var importLimitProblems = []struct {
	err    error
	status int
	code   string
}{
	{repository.ErrImportBlobTooLarge, http.StatusRequestEntityTooLarge, "import_blob_too_large"},
	{repository.ErrImportSessionTooLarge, http.StatusRequestEntityTooLarge, "import_session_too_large"},
	{repository.ErrImportTooManyBlobs, http.StatusUnprocessableEntity, "import_too_many_blobs"},
	{repository.ErrImportTooManySessions, http.StatusTooManyRequests, "import_too_many_sessions"},
	{repository.ErrImportStagingQuota, http.StatusInsufficientStorage, "import_staging_quota_exceeded"},
	{repository.ErrImportInvalidBlobClaims, http.StatusUnprocessableEntity, "import_invalid_blob_claims"},
	{repository.ErrImportBlobSizeMismatch, http.StatusUnprocessableEntity, "size_mismatch"},
	{repository.ErrImportSessionClosed, http.StatusConflict, "import_session_closed"},
	{repository.ErrImportBlobsMissing, http.StatusConflict, "import_blobs_missing"},
	{repository.ErrIdempotencyConflict, http.StatusConflict, "idempotency_conflict"},
}

// writeImportStoreError answers an error of the session store and reports whether it was one it knows.
func writeImportStoreError(w http.ResponseWriter, err error) bool {
	for _, problem := range importLimitProblems {
		if errors.Is(err, problem.err) {
			writeHostedProblem(w, problem.status, problem.code, problem.err.Error())
			return true
		}
	}
	return false
}

func (h importHandler) CreateRepositoryImport(w http.ResponseWriter, r *http.Request, repositoryID adminopenapi.RepositoryId) {
	if !h.ready(w) {
		return
	}
	h.withRepositoryScope(w, r, repositoryID.String(), RepositoryImportWrite, func(principal Principal, target repository.HostedRepository) {
		var body adminopenapi.CreateImport
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, importBodyLimit))
		if err := decoder.Decode(&body); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "import payload is invalid")
			return
		}
		if body.Coordinate == "" || len(body.Coordinate) > 1024 || !importDigestPattern.MatchString(body.Digest) || body.IdempotencyKey == "" || len(body.IdempotencyKey) > 255 || body.FormatManifest == nil {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "coordinate, digest, formatManifest and idempotencyKey are required and must be well-formed")
			return
		}
		if target.Type != repository.RepositoryTypeHosted || target.State != repository.RepositoryActive {
			writeHostedProblem(w, http.StatusConflict, "repository_not_importable", "the target must be an active hosted repository")
			return
		}
		if _, ok := formatmodule.Capability[formatmodule.Importer](h.registry, target.Format); !ok {
			writeHostedProblem(w, http.StatusUnprocessableEntity, "unsupported_format", "the target format does not accept imports")
			return
		}
		now := h.now()
		chain, code, message, err := h.chainPathOf(r.Context(), body.ChainPath, target, now)
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "read the instance id failed")
			return
		}
		if code != "" {
			writeHostedProblem(w, http.StatusUnprocessableEntity, code, message)
			return
		}
		manifest, err := json.Marshal(body.FormatManifest)
		if err != nil {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "formatManifest is invalid")
			return
		}
		session := repository.ImportSession{ID: ids.New(), TargetRepositoryID: target.ID, Principal: principal.Actor, Format: target.Format, Coordinate: body.Coordinate, Digest: body.Digest,
			FormatManifest: manifest, IdempotencyKey: body.IdempotencyKey, ChainPath: chain, CreatedAt: now, ExpiresAt: now.Add(h.ttl)}
		if body.Evidence != nil {
			if session.Evidence, err = json.Marshal(body.Evidence); err != nil {
				writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "evidence is invalid")
				return
			}
		}
		blobs := make([]repository.ImportBlob, 0, len(body.Blobs))
		for _, blob := range body.Blobs {
			blobs = append(blobs, repository.ImportBlob{Digest: blob.Digest, Size: blob.Size})
		}
		stored, replayed, err := h.sessions.CreateImportSession(r.Context(), session, blobs, h.limits)
		if err != nil {
			if !writeImportStoreError(w, err) {
				writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "create the import session failed")
			}
			return
		}
		status := http.StatusCreated
		if replayed {
			status = http.StatusOK
		}
		h.writeSession(w, r, status, stored)
	})
}

// withImportSession loads a session and runs handler when the caller may use it. The caller must be the principal that
// opened it (anyone else is answered 404, so a session id is never confirmed to a principal that did not open it) and must
// still hold ImportWrite on the session's target.
func (h importHandler) withImportSession(w http.ResponseWriter, r *http.Request, importID adminopenapi.ImportId, handler func(Principal, repository.ImportSession, repository.HostedRepository)) {
	if !h.ready(w) {
		return
	}
	principal, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	session, err := h.sessions.GetImportSession(r.Context(), importID.String())
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "import session not found")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "get the import session failed")
		return
	}
	target, err := h.repositories.GetHostedRepository(r.Context(), session.TargetRepositoryID)
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "import session not found")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "get repository failed")
		return
	}
	// The owner is compared before the grant is checked, so a session id is never confirmed, and no denial naming its
	// repository is recorded, for a principal that did not open it.
	if session.Principal != principal.Actor {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "import session not found")
		return
	}
	if decision := h.authorizer.Authorize(r.Context(), principal, target, RepositoryImportWrite); !decision.Allowed {
		h.writeRepositoryDenial(w, r, principal, target, RepositoryImportWrite, decision)
		return
	}
	handler(principal, session, target)
}

func (h importHandler) UploadImportBlob(w http.ResponseWriter, r *http.Request, importID adminopenapi.ImportId, sha256 string) {
	h.withImportSession(w, r, importID, func(_ Principal, session repository.ImportSession, _ repository.HostedRepository) {
		digest := "sha256:" + sha256
		if !importDigestPattern.MatchString(digest) {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "the blob digest must be 64 lowercase hex characters")
			return
		}
		now := h.now()
		if session.State != repository.ImportSessionOpen || !session.ExpiresAt.After(now) {
			writeHostedProblem(w, http.StatusConflict, "import_session_closed", repository.ErrImportSessionClosed.Error())
			return
		}
		blob, err := h.sessions.GetImportBlob(r.Context(), session.ID, digest)
		if errors.Is(err, repository.ErrNotFound) {
			writeHostedProblem(w, http.StatusNotFound, "import_blob_not_claimed", "the session did not claim this blob")
			return
		}
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "get the import blob failed")
			return
		}
		if blob.Verified {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// The staging object is written only when the bytes hash to the claimed digest and have the claimed size, so a
		// mismatch leaves nothing behind and nothing visible. The reader is bounded one byte past the claim so an overlong
		// body is a size mismatch, not an unbounded read.
		if err = h.staging.PutVerifiedReader(r.Context(), blob.StagingKey, io.LimitReader(r.Body, blob.Size+1), blob.Size, digest); err != nil {
			switch {
			case errors.Is(err, objectstore.ErrDigestMismatch):
				writeHostedProblem(w, http.StatusUnprocessableEntity, "digest_mismatch", "the uploaded bytes do not hash to the claimed digest")
			case errors.Is(err, objectstore.ErrSizeMismatch):
				writeHostedProblem(w, http.StatusUnprocessableEntity, "size_mismatch", "the uploaded bytes differ in size from the claimed size")
			default:
				writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "stage the import blob failed")
			}
			return
		}
		if err = h.sessions.RecordImportBlob(r.Context(), session.ID, digest, blob.Size, now); err != nil {
			if errors.Is(err, repository.ErrImportSessionClosed) {
				// The reaper may already have emptied the staging keys of the session, and nothing else would remove an
				// object written after that. A concurrent upload of the same blob may have recorded it first: the key is
				// shared, so an object the session holds as verified is left alone.
				if held, heldErr := h.sessions.GetImportBlob(context.WithoutCancel(r.Context()), session.ID, digest); heldErr == nil && !held.Verified {
					h.removeStaged(r.Context(), blob.StagingKey)
				}
			}
			if !writeImportStoreError(w, err) {
				writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "record the import blob failed")
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// removeStaged deletes a staged object whose upload the store refused; the request may have been cancelled.
func (h importHandler) removeStaged(ctx context.Context, key string) {
	if err := h.staging.Delete(context.WithoutCancel(ctx), key); err != nil && !errors.Is(err, objectstore.ErrNotFound) {
		slog.Error("delete a staged import object failed", "key", key, "error", err)
	}
}

// verifyStaged re-checks every blob of a session against the staging store before anything is published: the object must
// exist, have the claimed size and carry the digest it was verified under when it was staged.
func (h importHandler) verifyStaged(ctx context.Context, blobs []repository.ImportBlob) (code string, err error) {
	for _, blob := range blobs {
		info, statErr := h.staging.Stat(ctx, blob.StagingKey)
		switch {
		case errors.Is(statErr, objectstore.ErrNotFound):
			return "import_blobs_missing", nil
		case statErr != nil:
			return "", statErr
		case info.Size != blob.Size:
			return "size_mismatch", nil
		case info.Digest != blob.Digest:
			return "digest_mismatch", nil
		}
	}
	return "", nil
}

func (h importHandler) CommitImport(w http.ResponseWriter, r *http.Request, importID adminopenapi.ImportId) {
	h.withImportSession(w, r, importID, func(principal Principal, session repository.ImportSession, target repository.HostedRepository) {
		ctx, now := r.Context(), h.now()
		if session.State == repository.ImportSessionCommitted {
			h.writeSession(w, r, http.StatusOK, session)
			return
		}
		if !session.State.Live() || !session.ExpiresAt.After(now) {
			writeHostedProblem(w, http.StatusConflict, "import_session_closed", repository.ErrImportSessionClosed.Error())
			return
		}
		importer, ok := formatmodule.Capability[formatmodule.Importer](h.registry, target.Format)
		if !ok {
			writeHostedProblem(w, http.StatusUnprocessableEntity, "unsupported_format", "the target format does not accept imports")
			return
		}
		if target.Type != repository.RepositoryTypeHosted || target.State != repository.RepositoryActive {
			writeHostedProblem(w, http.StatusConflict, "repository_not_importable", "the target must be an active hosted repository")
			return
		}
		blobs, err := h.sessions.ListImportBlobs(ctx, session.ID)
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list the import blobs failed")
			return
		}
		for _, blob := range blobs {
			if !blob.Verified {
				writeHostedProblem(w, http.StatusConflict, "import_blobs_missing", repository.ErrImportBlobsMissing.Error())
				return
			}
		}
		if code, verifyErr := h.verifyStaged(ctx, blobs); verifyErr != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "verify the staged import blobs failed")
			return
		} else if code != "" {
			status := http.StatusUnprocessableEntity
			if code == "import_blobs_missing" {
				status = http.StatusConflict
			}
			writeHostedProblem(w, status, code, "the staged bytes no longer match the claims of the session")
			return
		}
		// The receiver's policy has the final word: the sender's evidence is stored as a claim and never counted, so the
		// policy is evaluated as for an artifact without evidence, and the artifact's own quarantine is checked under the
		// distribution admission lock that the publication then holds.
		policy, err := h.policies.GetRepositorySecurityPolicy(ctx, target.ID)
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "read the target admission policy failed")
			return
		}
		if evaluation := repository.EvaluateRepositorySecurityPolicy(policy, nil); !evaluation.Allowed {
			writeHostedProblem(w, http.StatusUnprocessableEntity, "admission_refused", repository.PromotionPolicyRefusalPrefix+strings.Join(evaluation.Reasons, ","))
			return
		}
		release, err := repository.LockArtifactDistributionAdmission(ctx, h.admission, target.ID, target.Format, session.Coordinate, session.Digest)
		if errors.Is(err, repository.ErrArtifactQuarantined) {
			writeHostedProblem(w, http.StatusUnprocessableEntity, "artifact_quarantined", repository.ArtifactQuarantinedReason)
			return
		}
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "evaluate the artifact quarantine failed")
			return
		}
		defer release()
		if err = h.sessions.BeginImportCommit(ctx, session.ID, now); err != nil {
			if !writeImportStoreError(w, err) {
				writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "begin the import commit failed")
			}
			return
		}
		unit := formatmodule.ExportedUnit{Format: session.Format, Coordinate: session.Coordinate, Digest: session.Digest, Manifest: session.FormatManifest, Blobs: make([]formatmodule.BlobDescriptor, 0, len(blobs))}
		keys := make(map[string]string, len(blobs))
		for _, blob := range blobs {
			unit.Blobs = append(unit.Blobs, formatmodule.BlobDescriptor{Digest: blob.Digest, ObjectKey: blob.StagingKey, Size: blob.Size})
			keys[blob.Digest] = blob.StagingKey
		}
		open := func(blob formatmodule.BlobDescriptor) (io.ReadCloser, error) {
			key, known := keys[blob.Digest]
			if !known {
				return nil, objectstore.ErrNotFound
			}
			reader, _, openErr := h.staging.Open(ctx, key)
			return reader, openErr
		}
		if err = importer.ImportUnit(repository.WithWriteActor(ctx, principal.Actor), target.ID, unit, open); err != nil {
			if importRefusal(err) {
				// A refusal that no retry of this session can change ends the session, so it neither holds its staging
				// capacity nor keeps the idempotency key: the sender's next request opens a new session.
				if failErr := h.sessions.FailImportCommit(context.WithoutCancel(ctx), session.ID, h.now()); failErr != nil {
					slog.Error("fail the import commit failed", "importId", session.ID, "error", failErr)
				}
			}
			h.writeImporterError(w, err)
			return
		}
		if err = h.sessions.CompleteImportSession(ctx, session.ID, h.now()); err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "complete the import session failed")
			return
		}
		h.auditCommit(ctx, principal, target, session)
		committed, err := h.sessions.GetImportSession(ctx, session.ID)
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "get the import session failed")
			return
		}
		h.writeSession(w, r, http.StatusOK, committed)
	})
}

// importRefusal reports whether err is a verdict of the importer on the unit itself, which a retry of the same session
// cannot change, rather than a failure of storage that a retry may get past.
func importRefusal(err error) bool {
	return errors.Is(err, formatmodule.ErrImportInvalidUnit) || errors.Is(err, formatmodule.ErrImportMismatch) || errors.Is(err, formatmodule.ErrImportConflict) ||
		errors.Is(err, formatmodule.ErrImportUnsupportedLayout)
}

// writeImporterError answers a refusal of the format's importer.
func (h importHandler) writeImporterError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, formatmodule.ErrImportInvalidUnit):
		writeHostedProblem(w, http.StatusUnprocessableEntity, "import_invalid_unit", "the format manifest or the blobs of the import are not valid for the target format")
	case errors.Is(err, formatmodule.ErrImportMismatch):
		writeHostedProblem(w, http.StatusUnprocessableEntity, "import_mismatch", "the verified bytes contradict the coordinate, digest or size claimed")
	case errors.Is(err, formatmodule.ErrImportConflict):
		writeHostedProblem(w, http.StatusConflict, "import_conflict", "a different artifact already exists at the coordinate")
	case errors.Is(err, formatmodule.ErrImportUnsupportedLayout):
		writeHostedProblem(w, http.StatusUnprocessableEntity, "unsupported_layout", "the target repository layout does not accept imports")
	case errors.Is(err, repository.ErrNotFound):
		writeHostedProblem(w, http.StatusNotFound, "not_found", "repository not found")
	case repository.IsQuotaExceeded(err):
		writeHostedProblem(w, http.StatusInsufficientStorage, "quota_exceeded", "repository capacity quota exceeded")
	default:
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "publish the import failed")
	}
}

// auditCommit records the publication. The artifact is already visible when it runs, so a failure to record is logged, not
// answered as a failed import the sender would retry.
func (h importHandler) auditCommit(ctx context.Context, principal Principal, target repository.HostedRepository, session repository.ImportSession) {
	if h.audit == nil {
		return
	}
	record := managementAudit(repository.AuditRecord{Actor: principal.Actor, Repository: target.Name, GroupName: target.Name, Resource: "imports/" + session.ID, Representation: session.Coordinate,
		Operation: "import.commit", Status: http.StatusOK, Evidence: map[string]string{"importId": session.ID, "coordinate": session.Coordinate, "digest": session.Digest, "format": string(session.Format)}})
	if err := h.audit.RecordAudit(ctx, record); err != nil {
		slog.Error("record the import commit audit failed", "importId", session.ID, "error", err)
	}
}
