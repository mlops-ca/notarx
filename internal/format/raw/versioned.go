package raw

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

// writeVersionedProblem answers a refusal of the versioned layout (ADR 0028) in the problem format with the
// documented code, and reports whether err was one. Anything else is left to the caller.
func writeVersionedProblem(w http.ResponseWriter, err error) bool {
	if !versionedRefusal(err) {
		return false
	}
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
	}
	writeRawProblem(w, status, code, err.Error())
	return true
}

// versionedRefusal reports whether err is a refusal of a write by the versioned layout. Such a refusal is final for the
// bytes that were sent: the same write is refused again until the version or the path changes.
func versionedRefusal(err error) bool {
	return errors.Is(err, repository.ErrInvalidGenericPath) || errors.Is(err, repository.ErrVersionFileExists) || errors.Is(err, repository.ErrVersionSealed) ||
		errors.Is(err, repository.ErrVersionTombstoned) || errors.Is(err, repository.ErrVersionFileImmutable) || errors.Is(err, repository.ErrVersionFileLimit)
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
func (h *Module) versionedWriteCondition(w http.ResponseWriter, r *http.Request, repo repository.HostedRepository, path, actor string, preflight bool) (repository.RawWriteCondition, bool) {
	if rejectInvalidVersionedPath(w, repo, path) {
		return repository.RawWriteCondition{}, false
	}
	if len(r.Header.Values("If-Match")) > 0 || len(r.Header.Values("If-Unmodified-Since")) > 0 {
		writeRawProblem(w, http.StatusBadRequest, "invalid_request", "If-Match is not supported in a versioned repository: files are create-only")
		return repository.RawWriteCondition{}, false
	}
	// The completion of a resumable upload has transferred its bytes already, so only a plain write has a transfer to save.
	return repository.RawWriteCondition{}, !preflight || h.versionedWritePreflight(w, r, repo, path, actor)
}

// versionedWritePreflight refuses before the body is read what the store would refuse after the bytes were spooled,
// staged and stored: a new file in a sealed version, and a different digest than the one the client declares for a
// path that already holds a file. The store re-evaluates the same rules atomically; this only saves the transfer.
func (h *Module) versionedWritePreflight(w http.ResponseWriter, r *http.Request, repo repository.HostedRepository, path, actor string) bool {
	reader, ok := h.store.(interface {
		GetRawVersion(context.Context, string, string, string) (repository.RawVersion, error)
	})
	parsed, parseErr := repository.ParseRawVersionPath(path)
	if !ok || parseErr != nil {
		return true
	}
	existing, err := h.store.GetRawAsset(r.Context(), repo.ID, path)
	exists := err == nil
	if declared := declaredRawDigest(r.Header.Get("Digest")); exists && declared != "" && declared != existing.Digest {
		h.auditRefusedOverwrite(r, repo, path, actor, repository.ErrVersionFileExists)
		writeVersionedProblem(w, repository.ErrVersionFileExists)
		return false
	}
	if !exists {
		if version, err := reader.GetRawVersion(r.Context(), repo.ID, parsed.Name, parsed.Version); err == nil && version.State == repository.RawVersionSealed {
			writeVersionedProblem(w, repository.ErrVersionSealed)
			return false
		}
	}
	return true
}

// declaredRawDigest reads the digest a client declares in its Digest header ("sha-256=<base64>") as sha256:<hex>; it is
// empty when the header is absent or names another algorithm.
func declaredRawDigest(header string) string {
	encoded, ok := strings.CutPrefix(strings.TrimSpace(header), "sha-256=")
	if !ok {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) != sha256.Size {
		return ""
	}
	return "sha256:" + hex.EncodeToString(raw)
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
