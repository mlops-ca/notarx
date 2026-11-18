package app

import (
	"encoding/json"
	"net/http"

	"github.com/mlops-ca/notarx/internal/access"
	"github.com/mlops-ca/notarx/internal/repository"
)

// publicRepositoryCatalogHandler intentionally exposes only repository metadata
// that is needed to discover anonymous browse targets. Configuration and
// management metadata remain authenticated-only.
type publicRepositoryCatalogHandler struct {
	repositories repository.HostedRepositoryStore
	groups       repository.HostedGroupStore
	anonymous    repository.PublicAccessPolicyStore
}

type publicRepositoryCatalogEntry struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Format repository.Format `json:"format"`
	Type   string            `json:"type"`
}

type publicRepositoryCatalogResponse struct {
	Enabled bool                           `json:"enabled"`
	Items   []publicRepositoryCatalogEntry `json:"items"`
}

func (h publicRepositoryCatalogHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !anonymousAccessAllowed(r.Context(), h.anonymous) {
		writeNativeMavenJSON(w, http.StatusOK, publicRepositoryCatalogResponse{Enabled: false, Items: []publicRepositoryCatalogEntry{}})
		return
	}

	// The gate is open here; decide every target against that one reading.
	gate := access.FixedGate(true)
	items := make([]publicRepositoryCatalogEntry, 0)
	after := ""
	for {
		repositories, next, err := h.repositories.ListHostedRepositories(r.Context(), 200, after)
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list public repositories failed")
			return
		}
		for _, repo := range repositories {
			if repository.IsSupportedFormat(repo.Format) && anonymousHostedRepositoryReadAllowed(r.Context(), gate, repo, http.MethodGet) {
				items = append(items, publicRepositoryCatalogEntry{ID: repo.ID, Name: repo.Name, Format: repo.Format, Type: string(repo.Type)})
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	if h.groups != nil {
		after = ""
		for {
			groups, next, err := h.groups.ListHostedGroups(r.Context(), 200, after)
			if err != nil {
				writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list public groups failed")
				return
			}
			for _, group := range groups {
				if repository.IsSupportedFormat(group.Format) && anonymousHostedGroupReadAllowed(r.Context(), gate, h.repositories, group, r.Method) {
					items = append(items, publicRepositoryCatalogEntry{ID: group.ID, Name: group.Name, Format: group.Format, Type: "group"})
				}
			}
			if next == "" {
				break
			}
			after = next
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(publicRepositoryCatalogResponse{Enabled: true, Items: items})
}
