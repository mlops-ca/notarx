package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/mlops-ca/notarx/internal/access"
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

// rejectVisibilityConflict enforces strict group mode on a group write: the
// group may not be more visible than its least visible member. It reports
// whether it answered.
func (h repositoryManagementHandler) rejectVisibilityConflict(w http.ResponseWriter, r *http.Request, principal Principal, group repository.HostedGroup) bool {
	if !h.groupMode.Enforced() {
		return false
	}
	members := make([]repository.HostedRepository, 0, len(group.Members))
	for _, member := range group.Members {
		repo, err := h.store.GetHostedRepository(r.Context(), member.RepositoryID)
		if err != nil {
			// validHostedGroup resolved every member, so a failure here is transient.
			// Skipping the member would let a conflicting write through; fail closed.
			writeHostedProblem(w, http.StatusServiceUnavailable, "internal_error", "group member lookup failed; retry the request")
			return true
		}
		members = append(members, repo)
	}
	conflicts := access.BelowCeiling(group, members)
	if len(conflicts) == 0 {
		return false
	}
	named := h.administeredBy(r.Context(), principal, conflicts)
	message := "group visibility must not exceed the visibility of its least visible member"
	if hidden := len(conflicts) - len(named); hidden > 0 {
		message += "; " + strconv.Itoa(hidden) + " offending member(s) are repositories you do not administer and are not listed"
	}
	writeVisibilityConflict(w, message, named)
	return true
}

// rejectLoweringBelowGroups enforces strict group mode when a repository's
// visibility is lowered: no group that contains it may stay more visible than
// it. It reports whether it answered.
func (h hostedRepositoryAPIHandler) rejectLoweringBelowGroups(w http.ResponseWriter, r *http.Request, repo repository.HostedRepository, next repository.Visibility) bool {
	if !h.groupMode.Enforced() || !access.LowersRepository(repo, next) {
		return false
	}
	if h.groups == nil {
		// Strict mode cannot be checked without the group store; refusing is the
		// only answer that keeps the invariant.
		writeHostedProblem(w, http.StatusServiceUnavailable, "internal_error", "group store unavailable; the visibility change cannot be checked against groups")
		return true
	}
	after := ""
	for {
		groups, token, err := h.groups.ListHostedGroups(r.Context(), 200, after)
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list groups failed")
			return true
		}
		if len(access.GroupsAbove(next, repo.ID, groups)) > 0 {
			// The caller administers this repository (the update requires it), so
			// naming it discloses nothing; the groups are not named.
			writeVisibilityConflict(w, "a group that contains this repository is more visible than the requested visibility", []repository.VisibilityRef{repo.VisibilityRefAt(next)})
			return true
		}
		if token == "" {
			return false
		}
		after = token
	}
}
