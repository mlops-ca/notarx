package app

import (
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/scim"
)

const (
	scimMaxUserName    = 128
	scimMaxDisplayName = 128
	scimMaxExternalID  = 255
)

func (s *scimServer) userDocument(base string, user repository.SCIMUser) scim.UserDocument {
	return scim.UserDocument{
		ID: user.User.ID, ExternalID: user.ExternalID(), UserName: user.User.Name, DisplayName: user.User.DisplayName,
		Email: user.User.Email, Active: user.User.State == repository.UserActive,
		Meta: scim.Meta{
			ResourceType: "User", Created: user.Identity.CreatedAt, LastModified: user.User.UpdatedAt,
			Location: base + "/Users/" + user.User.ID, Version: scim.ETag(user.User.Version),
		},
	}
}

func (s *scimServer) users(w http.ResponseWriter, r *http.Request, caller scimCaller, id string) int {
	if id == "" {
		switch r.Method {
		case http.MethodGet:
			return s.listUsers(w, r, caller)
		case http.MethodPost:
			return s.createUser(w, r, caller)
		}
		return s.methodNotAllowed(w, r, caller, "GET, POST")
	}
	if !ids.Valid(id) {
		return s.respond(w, r, caller, "scim.user.read", scim.NewError(http.StatusNotFound, "", "User not found"))
	}
	switch r.Method {
	case http.MethodGet:
		return s.getUser(w, r, caller, id)
	case http.MethodPut:
		return s.replaceUser(w, r, caller, id)
	case http.MethodPatch:
		return s.patchUser(w, r, caller, id)
	case http.MethodDelete:
		return s.deleteUser(w, r, caller, id)
	}
	return s.methodNotAllowed(w, r, caller, "GET, PUT, PATCH, DELETE")
}

func (s *scimServer) getUser(w http.ResponseWriter, r *http.Request, caller scimCaller, id string) int {
	user, err := s.store.GetSCIMUser(r.Context(), caller.ProviderID, id)
	if err != nil {
		return s.respond(w, r, caller, "scim.user.read", s.storeFailure(err, "User"))
	}
	return s.writeUser(w, r, caller, "scim.user.read", http.StatusOK, user, true)
}

// writeUser answers with the User; audit is false when the change already committed its audit record.
func (s *scimServer) writeUser(w http.ResponseWriter, r *http.Request, caller scimCaller, operation string, status int, user repository.SCIMUser, audit bool) int {
	headers := map[string]string{"ETag": scim.ETag(user.User.Version)}
	if status == http.StatusCreated {
		headers["Location"] = s.base(r) + "/Users/" + user.User.ID
	}
	return s.writeJSON(w, r, caller, operation, "/Users/"+user.User.ID, status, s.userDocument(s.base(r), user), headers, audit)
}

func (s *scimServer) listUsers(w http.ResponseWriter, r *http.Request, caller scimCaller) int {
	parameters, failure := s.listParameters(r)
	if failure != nil {
		return s.respond(w, r, caller, "scim.user.list", failure)
	}
	filter, failure := scim.ParseFilter(r.URL.Query().Get("filter"), scim.Users)
	if failure != nil {
		return s.respond(w, r, caller, "scim.user.list", failure)
	}
	query := repository.SCIMUserQuery{ProviderID: caller.ProviderID, Limit: max(parameters.count, 1)}
	switch filter.Attribute {
	case "userName":
		query.UserName = filter.Value
	case "externalId":
		query.ExternalID = filter.Value
	}
	startIndex := parameters.startIndex
	query.Cursor = parameters.cursor
	if query.Cursor == "" && startIndex > 1 {
		hop := query
		hop.SkipTotal = true
		cursor, ok, failure, err := scimCursorHop(startIndex, func(cursor string, limit int) (int, string, error) {
			hop.Cursor, hop.Limit = cursor, limit
			page, err := s.store.ListSCIMUsers(r.Context(), hop)
			return len(page.Items), page.NextCursor, err
		})
		switch {
		case failure != nil:
			return s.respond(w, r, caller, "scim.user.list", failure)
		case err != nil:
			return s.respond(w, r, caller, "scim.user.list", s.storeFailure(err, "User"))
		case !ok:
			return s.emptyList(w, r, caller, "scim.user.list", "/Users", startIndex, func() (int64, error) {
				page, err := s.store.ListSCIMUsers(r.Context(), repository.SCIMUserQuery{ProviderID: caller.ProviderID, UserName: query.UserName, ExternalID: query.ExternalID, Limit: 1})
				return page.Total.Value, err
			})
		}
		query.Cursor = cursor
	}
	page, err := s.store.ListSCIMUsers(r.Context(), query)
	if err != nil {
		return s.respond(w, r, caller, "scim.user.list", s.storeFailure(err, "User"))
	}
	base := s.base(r)
	response := scim.ListResponse{TotalResults: page.Total.Value, StartIndex: startIndex, NextCursor: page.NextCursor}
	if parameters.count > 0 {
		for _, user := range page.Items {
			response.Resources = append(response.Resources, s.userDocument(base, user))
		}
	} else {
		response.NextCursor = ""
	}
	return s.writeJSON(w, r, caller, "scim.user.list", "/Users", http.StatusOK, response, nil, true)
}

// emptyList answers a startIndex past the last row: no resources, and the total of the filter.
func (s *scimServer) emptyList(w http.ResponseWriter, r *http.Request, caller scimCaller, operation, resource string, startIndex int, total func() (int64, error)) int {
	value, err := total()
	if err != nil {
		return s.respond(w, r, caller, operation, s.storeFailure(err, "resource"))
	}
	return s.writeJSON(w, r, caller, operation, resource, http.StatusOK, scim.ListResponse{TotalResults: value, StartIndex: startIndex}, nil, true)
}

// validateUserInput applies the limits the account fields have.
func validateScimUser(userName, displayName, email string, externalID *string) *scim.Error {
	switch {
	case externalID != nil && repository.ReservedSCIMExternalID(*externalID):
		return scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, "externalId uses a prefix notarx reserves")
	case len([]rune(userName)) > scimMaxUserName || len(userName) > scimMaxUserName*4:
		return scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, "userName is too long")
	case !validUserText(displayName, scimMaxDisplayName):
		return scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, "displayName is too long")
	case !validUserEmail(email):
		return scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, "the email address is not valid")
	case externalID != nil && !validUserText(*externalID, scimMaxExternalID):
		return scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, "externalId is too long")
	}
	return nil
}

func (s *scimServer) createUser(w http.ResponseWriter, r *http.Request, caller scimCaller) int {
	body, failure := s.readBody(w, r)
	if failure != nil {
		return s.respond(w, r, caller, "scim.user.create", failure)
	}
	input, failure := scim.ParseUser(body)
	if failure == nil {
		failure = validateScimUser(input.UserName, input.DisplayName, input.Email, input.ExternalID)
	}
	if failure != nil {
		return s.respond(w, r, caller, "scim.user.create", failure)
	}
	create := repository.SCIMUserCreate{ProviderID: caller.ProviderID, UserName: input.UserName, DisplayName: input.DisplayName, Email: input.Email, Active: input.Active == nil || *input.Active}
	if input.ExternalID != nil {
		create.ExternalID = *input.ExternalID
	}
	user, err := s.store.CreateSCIMUser(r.Context(), create, s.writeAudit(r, caller, "scim.user.create", "/Users", http.StatusCreated, nil)...)
	if err != nil {
		return s.respond(w, r, caller, "scim.user.create", s.storeFailure(err, "User"))
	}
	return s.writeUser(w, r, caller, "scim.user.create", http.StatusCreated, user, false)
}

// userChange converts a replace body to the change the store applies: displayName and the email are replaced
// (an absent one is cleared), externalId and active only when sent.
func userReplaceChange(input scim.UserInput) repository.SCIMUserChange {
	return repository.SCIMUserChange{UserName: &input.UserName, DisplayName: &input.DisplayName, Email: &input.Email, ExternalID: input.ExternalID, Active: input.Active}
}

func (s *scimServer) replaceUser(w http.ResponseWriter, r *http.Request, caller scimCaller, id string) int {
	body, failure := s.readBody(w, r)
	if failure != nil {
		return s.respond(w, r, caller, "scim.user.replace", failure)
	}
	input, failure := scim.ParseUser(body)
	if failure == nil {
		failure = validateScimUser(input.UserName, input.DisplayName, input.Email, input.ExternalID)
	}
	if failure != nil {
		return s.respond(w, r, caller, "scim.user.replace", failure)
	}
	return s.changeUser(w, r, caller, id, "scim.user.replace", userReplaceChange(input), []string{"userName", "displayName", "emails", "externalId", "active"}, nil)
}

func (s *scimServer) patchUser(w http.ResponseWriter, r *http.Request, caller scimCaller, id string) int {
	body, failure := s.readBody(w, r)
	if failure != nil {
		return s.respond(w, r, caller, "scim.user.patch", failure)
	}
	patch, failure := scim.NormalizeUserPatch(body)
	if failure == nil {
		failure = validateScimUser(stringOrEmpty(patch.UserName), stringOrEmpty(patch.DisplayName), stringOrEmpty(patch.Email), patch.ExternalID)
	}
	if failure != nil {
		return s.respond(w, r, caller, "scim.user.patch", failure)
	}
	var changed []string
	change := repository.SCIMUserChange{UserName: patch.UserName, DisplayName: patch.DisplayName, Email: patch.Email, ExternalID: patch.ExternalID, Active: patch.Active}
	for name, present := range map[string]bool{"userName": patch.UserName != nil, "displayName": patch.DisplayName != nil, "emails": patch.Email != nil, "externalId": patch.ExternalID != nil, "active": patch.Active != nil} {
		if present {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	return s.changeUser(w, r, caller, id, "scim.user.patch", change, changed, patch.Ignored)
}

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

// changeUser applies a change after the If-Match check. A PATCH that only names attributes the server does not
// store changes nothing and still answers 200 with the resource (ADR 0018 section 4): Entra ID treats 400 as a sync
// failure. The audit carries attribute names only, never values.
func (s *scimServer) changeUser(w http.ResponseWriter, r *http.Request, caller scimCaller, id, operation string, change repository.SCIMUserChange, changed, ignored []string) int {
	current, err := s.store.GetSCIMUser(r.Context(), caller.ProviderID, id)
	if err != nil {
		return s.respond(w, r, caller, operation, s.storeFailure(err, "User"))
	}
	versions := scimIfMatchVersions(r)
	if !scimVersionMatches(versions, current.User.Version) {
		return s.respond(w, r, caller, operation, s.preconditionFailed())
	}
	if len(ignored) > 0 {
		slog.Info("SCIM request named attributes the server does not store; they were ignored", "provider", caller.Slug, "operation", operation, "attributes", ignored)
	}
	if len(changed) == 0 {
		return s.writeUser(w, r, caller, operation, http.StatusOK, current, true)
	}
	change.IfVersions = versions
	evidence := map[string]string{"attributes": strings.Join(changed, ",")}
	if len(ignored) > 0 {
		evidence["ignored"] = strings.Join(ignored, ",")
	}
	updated, err := s.store.UpdateSCIMUser(r.Context(), caller.ProviderID, id, change, s.writeAudit(r, caller, operation, "/Users/"+id, http.StatusOK, evidence)...)
	if err != nil {
		return s.respond(w, r, caller, operation, s.storeFailure(err, "User"))
	}
	return s.writeUser(w, r, caller, operation, http.StatusOK, updated, false)
}

func (s *scimServer) deleteUser(w http.ResponseWriter, r *http.Request, caller scimCaller, id string) int {
	current, err := s.store.GetSCIMUser(r.Context(), caller.ProviderID, id)
	if err != nil {
		return s.respond(w, r, caller, "scim.user.delete", s.storeFailure(err, "User"))
	}
	versions := scimIfMatchVersions(r)
	if !scimVersionMatches(versions, current.User.Version) {
		return s.respond(w, r, caller, "scim.user.delete", s.preconditionFailed())
	}
	if err := s.store.DeleteSCIMUser(r.Context(), caller.ProviderID, id, versions, s.writeAudit(r, caller, "scim.user.delete", "/Users/"+id, http.StatusNoContent, nil)...); err != nil {
		return s.respond(w, r, caller, "scim.user.delete", s.storeFailure(err, "User"))
	}
	w.WriteHeader(http.StatusNoContent)
	return http.StatusNoContent
}
