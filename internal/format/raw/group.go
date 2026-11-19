package raw

import (
	"net/http"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/access"
	"github.com/mlops-ca/notarx/internal/authorization"
	rawprotocol "github.com/mlops-ca/notarx/internal/protocol/raw"
	"github.com/mlops-ca/notarx/internal/repository"
)

func (h *Module) serveGroup(w http.ResponseWriter, r *http.Request, group repository.HostedGroup) {
	if h.groupRuntime == nil {
		http.Error(w, "group runtime unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, path, ok := rawprotocol.ParsePath(r.URL.EscapedPath())
	if !ok || path == "" || strings.HasSuffix(path, "/") {
		http.NotFound(w, r)
		return
	}
	principal, authenticated := h.auth.AuthenticateContext(r.Context(), r.Header.Get("Authorization"))
	if !authenticated {
		if decision, _, _ := access.Decide(r.Context(), access.ReadRequest{Class: access.Anonymous, Method: r.Method, Gate: h.groupRuntime.Gate(), Group: &group}); decision.Allow {
			principal = anonymousPrincipal()
		} else {
			w.Header().Set("WWW-Authenticate", `Basic realm="notarx Raw"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
	}
	members, err := h.groupRuntime.ResolveMembers(r.Context(), group)
	if err != nil {
		http.Error(w, "unable to resolve group members", http.StatusInternalServerError)
		return
	}
	if len(members) == 0 {
		h.groupRuntime.AuditResolution(r.Context(), group, repository.FormatRaw, path, strings.ToLower(r.Method), principal.Actor, repository.AuditNotFound, http.StatusNotFound)
		http.NotFound(w, r)
		return
	}
	if isAnonymous(principal) {
		_, resolved, _ := access.Decide(r.Context(), access.ReadRequest{Class: access.Anonymous, Method: http.MethodGet, Gate: h.groupRuntime.Gate(), Group: &group, Members: members})
		members = resolved.Members
	}
	var denied bool
	members, denied = h.groupRuntime.AuthorizeMembers(r.Context(), principal, repository.FormatRaw, path, members)
	if denied && len(members) == 0 {
		http.Error(w, "repository read permission required", http.StatusForbidden)
		return
	}
	if len(members) == 0 {
		h.groupRuntime.AuditResolution(r.Context(), group, repository.FormatRaw, path, strings.ToLower(r.Method), principal.Actor, repository.AuditAccessDenied, http.StatusUnauthorized)
		w.Header().Set("WWW-Authenticate", `Basic realm="notarx Raw"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	for _, member := range members {
		if member.Type == repository.MemberHosted {
			repo, err := h.repos.GetHostedRepository(r.Context(), member.RepositoryID)
			if err != nil {
				continue
			}
			if h.serveHostedRead(w, r, repo, path, principal) {
				return
			}
			continue
		}
		repo, err := h.repos.GetHostedRepository(r.Context(), member.RepositoryID)
		if err != nil {
			continue
		}
		if h.tryProxyRead(w, r, repo, path, principal) {
			return
		}
	}
	h.groupRuntime.AuditResolution(r.Context(), group, repository.FormatRaw, path, strings.ToLower(r.Method), principal.Actor, repository.AuditNotFound, http.StatusNotFound)
	http.NotFound(w, r)
}

// serveHostedRead attempts a read against a native hosted Raw repository
// without writing a response when the asset is absent, so the caller can fall
// through to the next group member. It reports whether the request was served.
func (h *Module) serveHostedRead(w http.ResponseWriter, r *http.Request, repo repository.HostedRepository, path string, principal authorization.Principal) bool {
	if rawChecksumExtension(path) != "" {
		source := rawChecksumSourcePath(path)
		if rawChecksumExtension(source) != "" {
			return false
		}
		asset, err := h.store.GetRawAsset(r.Context(), repo.ID, source)
		if err != nil {
			return false
		}
		if h.blockQuarantinedRead(w, r, repo, asset, principal) {
			return true
		}
		body, err := h.rawChecksum(r.Context(), asset, rawChecksumExtension(path))
		if err != nil {
			http.Error(w, "raw object unavailable", http.StatusInternalServerError)
			return true
		}
		h.serveChecksum(w, r, path, body)
		return true
	}
	asset, err := h.store.GetRawAsset(r.Context(), repo.ID, path)
	if err != nil {
		return false
	}
	if h.blockQuarantinedRead(w, r, repo, asset, principal) {
		return true
	}
	h.serveAsset(w, r, path, asset)
	return true
}

func (h *Module) blockQuarantinedRead(w http.ResponseWriter, r *http.Request, repo repository.HostedRepository, asset repository.RawAsset, principal authorization.Principal) bool {
	blocked, err := repository.QuarantinedArtifactReadBlocked(r.Context(), h.readPolicies, h.quarantine, repo.ID, repository.FormatRaw, asset.Path, asset.Digest)
	if err != nil {
		http.Error(w, "evaluate artifact quarantine failed", http.StatusInternalServerError)
		return true
	}
	if !blocked {
		return false
	}
	if h.audit != nil {
		_ = h.audit.RecordAudit(r.Context(), repository.AuditRecord{GroupName: repo.Name, Repository: repo.Name, Actor: principal.Actor, Outcome: repository.AuditAccessDenied, OccurredAt: time.Now().UTC(), Format: string(repository.FormatRaw), Resource: asset.Path, Representation: asset.Digest, Operation: strings.ToLower(r.Method), Status: http.StatusForbidden, CacheDisposition: "bypass", AuthorizationSource: "quarantine_read_policy", AuthorizationReason: repository.ArtifactQuarantinedReason})
	}
	http.Error(w, repository.ArtifactQuarantinedReason, http.StatusForbidden)
	return true
}
