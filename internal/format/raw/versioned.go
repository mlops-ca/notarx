package raw

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

// writeVersionedProblem answers a refusal of the versioned layout (ADR 0028) in the problem format with the
// documented code, and reports whether err was one. Anything else is left to the caller.
func writeVersionedProblem(w http.ResponseWriter, err error) bool {
	status, code := http.StatusConflict, ""
	switch {
	case errors.Is(err, repository.ErrInvalidGenericPath):
		status, code = http.StatusUnprocessableEntity, "invalid_generic_path"
	case errors.Is(err, repository.ErrVersionFileExists):
		code = "version_file_exists"
	case errors.Is(err, repository.ErrVersionSealed):
		code = "version_sealed"
	case errors.Is(err, repository.ErrVersionTombstoned):
		code = "version_tombstoned"
	case errors.Is(err, repository.ErrVersionFileImmutable):
		code = "version_file_immutable"
	case errors.Is(err, repository.ErrVersionFileLimit):
		status, code = http.StatusUnprocessableEntity, "version_file_limit"
	default:
		return false
	}
	writeRawProblem(w, status, code, err.Error())
	return true
}

func writeRawProblem(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "code": code, "message": message})
}

func versioned(repo repository.HostedRepository) bool {
	return repo.RawLayout == repository.RawLayoutVersioned
}

// rejectInvalidVersionedPath refuses a write to a path that is not name/version/file before any body is read.
func rejectInvalidVersionedPath(w http.ResponseWriter, repo repository.HostedRepository, path string) bool {
	if !versioned(repo) {
		return false
	}
	if _, err := repository.ParseRawVersionPath(path); err != nil {
		writeVersionedProblem(w, err)
		return true
	}
	return false
}

// versionedWriteCondition is the precondition of a write to a versioned repository. Files are create-only, so the
// store enforces the rule itself and If-None-Match is not needed; If-Match can never be satisfied by a create-only
// write and is rejected rather than ignored.
func versionedWriteCondition(w http.ResponseWriter, r *http.Request, repo repository.HostedRepository, path string) (repository.RawWriteCondition, bool) {
	if rejectInvalidVersionedPath(w, repo, path) {
		return repository.RawWriteCondition{}, false
	}
	if len(r.Header.Values("If-Match")) > 0 || len(r.Header.Values("If-Unmodified-Since")) > 0 {
		writeRawProblem(w, http.StatusBadRequest, "invalid_request", "If-Match is not supported in a versioned repository: files are create-only")
		return repository.RawWriteCondition{}, false
	}
	return repository.RawWriteCondition{}, true
}

// auditRefusedOverwrite records an attempt to replace a file of a versioned repository, with actor and path
// (ADR 0028, Security considerations). The refusal itself never depends on the audit write.
func (h *Module) auditRefusedOverwrite(r *http.Request, repo repository.HostedRepository, path, actor string, err error) {
	if h.audit == nil || !errors.Is(err, repository.ErrVersionFileExists) {
		return
	}
	_ = h.audit.RecordAudit(r.Context(), repository.AuditRecord{Class: repository.AuditClassSync, Repository: repo.Name, Outcome: repository.AuditAccessDenied, Actor: actor, OccurredAt: time.Now().UTC(), Format: string(repository.FormatRaw), Resource: path, Operation: "raw.version.file.overwrite", Status: http.StatusConflict, CacheDisposition: "bypass"})
}

// rawWriteStatus is the success status of a write: a replay of identical content is 200, a new file 201.
func rawWriteStatus(asset repository.RawAsset) int {
	if asset.Replayed {
		return http.StatusOK
	}
	return http.StatusCreated
}
