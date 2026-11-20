package app

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mlops-ca/notarx/internal/access"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	formatmodule "github.com/mlops-ca/notarx/internal/format"
	rawformat "github.com/mlops-ca/notarx/internal/format/raw"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/scanning"
)

type repositoryManagementOptions struct {
	Scope               managementScope
	Repositories        repository.HostedRepositoryStore
	Groups              repository.HostedGroupStore
	Capacities          repository.RepositoryCapacityStore
	Maintenance         *CacheMaintenance
	ArtifactScanner     scanning.Scanner
	ArtifactScanFormats []repository.Format
	// GroupVisibilityMode is strict (the zero value) or lenient (ADR 0029).
	GroupVisibilityMode access.GroupMode
}

// repositoryManagementHandler owns repository configuration and Group policy.
// It can be constructed without identity, lifecycle, or protocol stores.
type repositoryManagementHandler struct {
	managementScope
	hosted              hostedRepositoryAPIHandler
	store               repository.HostedRepositoryStore
	groups              repository.HostedGroupStore
	capacities          repository.RepositoryCapacityStore
	maintenance         *CacheMaintenance
	artifactScanner     scanning.Scanner
	artifactScanFormats []repository.Format
	groupMode           access.GroupMode
}

func newRepositoryManagementHandler(options repositoryManagementOptions) repositoryManagementHandler {
	return repositoryManagementHandler{managementScope: options.Scope,
		hosted: hostedRepositoryAPIHandler{store: options.Repositories, groups: options.Groups, authenticator: options.Scope.authenticator, groupMode: options.GroupVisibilityMode},
		store:  options.Repositories, groups: options.Groups, capacities: options.Capacities,
		maintenance: options.Maintenance, artifactScanner: options.ArtifactScanner, groupMode: options.GroupVisibilityMode,
		artifactScanFormats: append([]repository.Format(nil), options.ArtifactScanFormats...),
	}
}

func (h repositoryManagementHandler) ListRepositories(w http.ResponseWriter, r *http.Request, params adminopenapi.ListRepositoriesParams) {
	principal, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if code, message, blocked := accountStateProblem(principal.AccountStateReason()); blocked {
		writeHostedProblem(w, http.StatusForbidden, code, message)
		return
	}
	h.listReadableRepositories(w, r, params, principal)
}

// listReadableRepositories keeps discovery consistent with per-repository reads.
// A page may span multiple store batches when some repositories are not readable.
func (h repositoryManagementHandler) listReadableRepositories(w http.ResponseWriter, r *http.Request, params adminopenapi.ListRepositoriesParams, principal Principal) {
	pageSize := 50
	if params.PageSize != nil {
		pageSize = int(*params.PageSize)
		if pageSize < 1 || pageSize > 200 {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "pageSize must be between 1 and 200")
			return
		}
	}
	pageToken := ""
	if params.PageToken != nil {
		pageToken = string(*params.PageToken)
	}
	after, err := h.hosted.decodeCursor(pageToken)
	if err != nil {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_page_token", "page token is invalid or expired")
		return
	}
	items := make([]repository.HostedRepository, 0, pageSize)
	scanned := after
	hasMore := false
	for len(items) < pageSize {
		batch, next, listErr := h.store.ListHostedRepositories(r.Context(), 200, scanned)
		if errors.Is(listErr, repository.ErrNotFound) {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_page_token", "page token is invalid")
			return
		}
		if listErr != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list repositories failed")
			return
		}
		for index, candidate := range batch {
			scanned = candidate.ID
			if repository.IsSupportedFormat(candidate.Format) && h.authorizer.Authorize(r.Context(), principal, candidate, RepositoryRead).Allowed {
				items = append(items, redactRepositorySecrets(candidate))
			}
			if len(items) == pageSize {
				hasMore = index < len(batch)-1 || next != ""
				break
			}
		}
		if len(items) == pageSize || next == "" {
			break
		}
	}
	nextToken := ""
	if hasMore {
		nextToken = h.hosted.encodeCursor(scanned)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(repositoryPage{Items: items, NextPageToken: nextToken})
}

func (h repositoryManagementHandler) CreateRepository(w http.ResponseWriter, r *http.Request, params adminopenapi.CreateRepositoryParams) {
	if principal, ok := h.authorize(w, r); ok {
		h.hosted.createWithIdempotencyKey(w, r, principal, string(params.IdempotencyKey))
	}
}

func (h repositoryManagementHandler) DeleteRepository(w http.ResponseWriter, r *http.Request, id adminopenapi.RepositoryId) {
	h.withRepositoryScope(w, r, id.String(), RepositoryAdmin, func(_ Principal, repo repository.HostedRepository) {
		if rejectPreviewFormalProjection(w, repo) {
			return
		}
		h.hosted.disable(w, r, id.String())
	})
}

func (h repositoryManagementHandler) GetRepository(w http.ResponseWriter, r *http.Request, id adminopenapi.RepositoryId) {
	h.withRepositoryScope(w, r, id.String(), RepositoryRead, func(Principal, repository.HostedRepository) {
		h.hosted.get(w, r, id.String())
	})
}

func (h repositoryManagementHandler) UpdateRepository(w http.ResponseWriter, r *http.Request, id adminopenapi.RepositoryId, params adminopenapi.UpdateRepositoryParams) {
	h.withRepositoryScope(w, r, id.String(), RepositoryAdmin, func(_ Principal, repo repository.HostedRepository) {
		if rejectPreviewFormalProjection(w, repo) {
			return
		}
		h.hosted.update(w, r, repo, string(params.IfMatch))
	})
}

func (h repositoryManagementHandler) GetRepositoryCapabilities(w http.ResponseWriter, r *http.Request, id adminopenapi.RepositoryId) {
	h.withRepositoryScope(w, r, id.String(), RepositoryRead, func(_ Principal, repo repository.HostedRepository) {
		if rejectPreviewFormalProjection(w, repo) {
			return
		}
		artifactScanning := h.artifactScanner != nil && scanFormatEnabled(h.artifactScanFormats, repo.Format) && scanRepositoryAssetsAvailable(repo)
		publicationScanning := artifactScanning && publicationScanSupported(repo)
		capabilities := repositoryCapabilities(repo.Format, repo.Type, artifactScanning, publicationScanning)
		if repo.Format == repository.FormatRaw && repo.Type == repository.RepositoryTypeHosted && repo.RawLayout != "" {
			layout, versions, strict := adminopenapi.RawLayout(repo.RawLayout), repo.RawLayout == repository.RawLayoutVersioned, repo.RawStrict
			capabilities.Layout, capabilities.Versions, capabilities.Strict = &layout, &versions, &strict
		}
		writeNativeMavenJSON(w, http.StatusOK, capabilities)
	})
}

// ListFormatProfiles serves the static format table. It carries no repository
// state, so it needs an authenticated caller rather than an administrator.
func (h repositoryManagementHandler) ListFormatProfiles(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	profiles := repository.SupportedFormatProfiles()
	items := make([]adminopenapi.FormatProfile, 0, len(profiles))
	modules := rawFormatRegistry(rawformat.Options{})
	for _, profile := range profiles {
		if capability, ok := formatmodule.Capability[formatmodule.Profile](modules, profile.Format); ok {
			profile = capability.Profile()
		}
		items = append(items, formatProfileResponse(profile))
	}
	writeNativeMavenJSON(w, http.StatusOK, adminopenapi.FormatProfileList{Items: items})
}
