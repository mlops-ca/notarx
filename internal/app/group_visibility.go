package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/mlops-ca/notarx/internal/repository"
)

// writeVisibilityConflict answers 409 visibility_conflict. The body names the
// offending members the caller may administer; the message counts the ones the
// caller cannot administer without naming them, so the refusal does not disclose
// private repositories.
func writeVisibilityConflict(w http.ResponseWriter, message string, members []repository.VisibilityRef) {
	if members == nil {
		members = []repository.VisibilityRef{}
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(http.StatusConflict), "status": http.StatusConflict, "code": "visibility_conflict", "message": message, "requestId": "", "members": members})
}

// administeredBy keeps the repositories a principal may administer.
func (h managementScope) administeredBy(ctx context.Context, principal Principal, repos []repository.HostedRepository) []repository.VisibilityRef {
	kept := make([]repository.VisibilityRef, 0, len(repos))
	for _, repo := range repos {
		if principal.Admin || h.authorizer.Authorize(ctx, principal, repo, RepositoryAdmin).Allowed {
			kept = append(kept, repo.VisibilityRef())
		}
	}
	return kept
}

// writeGroupVisibilityConflict answers a store refusal of a group write under
// strict mode (repository.ErrVisibilityConflict) with 409 visibility_conflict. It
// reports whether err was such a refusal.
func (h managementScope) writeGroupVisibilityConflict(w http.ResponseWriter, r *http.Request, principal Principal, err error) bool {
	var conflict *repository.VisibilityConflictError
	if !errors.As(err, &conflict) || conflict.Lowering {
		return false
	}
	named := h.administeredBy(r.Context(), principal, conflict.Members)
	message := "group visibility must not exceed the visibility of its least visible member"
	if hidden := len(conflict.Members) - len(named); hidden > 0 {
		message += "; " + strconv.Itoa(hidden) + " offending member(s) are repositories you do not administer and are not listed"
	}
	writeVisibilityConflict(w, message, named)
	return true
}

// writeLoweringVisibilityConflict answers a store refusal of a repository
// visibility change that would leave a group more visible than the repository. The
// caller administers the repository (the update requires it), so naming it
// discloses nothing; the groups are not named.
func writeLoweringVisibilityConflict(w http.ResponseWriter, err error, repo repository.HostedRepository, next repository.Visibility) bool {
	var conflict *repository.VisibilityConflictError
	if !errors.As(err, &conflict) || !conflict.Lowering {
		return false
	}
	writeVisibilityConflict(w, "a group that contains this repository is more visible than the requested visibility", []repository.VisibilityRef{repo.VisibilityRefAt(next)})
	return true
}
