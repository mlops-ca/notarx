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

func (s *scimServer) groups(w http.ResponseWriter, r *http.Request, caller scimCaller, id string) int {
	if id == "" {
		switch r.Method {
		case http.MethodGet:
			return s.listGroups(w, r, caller)
		case http.MethodPost:
			return s.createGroup(w, r, caller)
		}
		return s.methodNotAllowed(w, r, caller, "GET, POST")
	}
	if !ids.Valid(id) {
		return s.respond(w, r, caller, "scim.group.read", scim.NewError(http.StatusNotFound, "", "Group not found"))
	}
	switch r.Method {
	case http.MethodGet:
		return s.getGroup(w, r, caller, id)
	case http.MethodPut:
		return s.replaceGroup(w, r, caller, id)
	case http.MethodPatch:
		return s.patchGroup(w, r, caller, id)
	case http.MethodDelete:
		return s.deleteGroup(w, r, caller, id)
	}
	return s.methodNotAllowed(w, r, caller, "GET, PUT, PATCH, DELETE")
}

// membersExcluded reports excludedAttributes=members (and attributes= without members), which the server honours.
func membersExcluded(r *http.Request) bool {
	query := r.URL.Query()
	for _, name := range strings.Split(query.Get("excludedAttributes"), ",") {
		if strings.EqualFold(strings.TrimSpace(name), "members") {
			return true
		}
	}
	if attributes := query.Get("attributes"); attributes != "" {
		for _, name := range strings.Split(attributes, ",") {
			if strings.EqualFold(strings.TrimSpace(name), "members") {
				return false
			}
		}
		return true
	}
	return false
}

// groupDocument builds the resource from the members the caller read (nil when they are excluded).
func (s *scimServer) groupDocument(r *http.Request, group repository.UserGroup, members []repository.UserGroupMember, withMembers bool) scim.GroupDocument {
	base := s.base(r)
	document := scim.GroupDocument{
		ID: group.ID, ExternalID: group.ExternalID, DisplayName: group.DisplayName, WithMembers: withMembers,
		Meta: scim.Meta{ResourceType: "Group", Created: group.CreatedAt, LastModified: group.UpdatedAt, Location: base + "/Groups/" + group.ID, Version: scim.ETag(group.Version)},
	}
	for _, member := range members {
		document.Members = append(document.Members, scim.GroupMember{Value: member.UserID, Display: member.UserName, Ref: base + "/Users/" + member.UserID, Type: "User"})
	}
	return document
}

// readMembers reads the members of one group, at most scimMaxReturnedMembers, in one query.
func (s *scimServer) readMembers(r *http.Request, caller scimCaller, group repository.UserGroup) ([]repository.UserGroupMember, error) {
	members, err := s.store.ListSCIMGroupMembers(r.Context(), caller.ProviderID, group.ID, scimMaxReturnedMembers+1)
	if err != nil {
		return nil, err
	}
	if len(members) > scimMaxReturnedMembers {
		slog.Warn("a SCIM group has more members than one response returns", "provider", caller.Slug, "group", group.ID, "limit", scimMaxReturnedMembers)
		members = members[:scimMaxReturnedMembers]
	}
	return members, nil
}

func (s *scimServer) writeGroup(w http.ResponseWriter, r *http.Request, caller scimCaller, operation string, status int, group repository.UserGroup, withMembers, audit bool) int {
	var members []repository.UserGroupMember
	if withMembers {
		var err error
		if members, err = s.readMembers(r, caller, group); err != nil {
			return s.respond(w, r, caller, operation, s.storeFailure(err, "Group"))
		}
	}
	headers := map[string]string{"ETag": scim.ETag(group.Version)}
	if status == http.StatusCreated {
		headers["Location"] = s.base(r) + "/Groups/" + group.ID
	}
	return s.writeJSON(w, r, caller, operation, "/Groups/"+group.ID, status, s.groupDocument(r, group, members, withMembers), headers, audit)
}

func (s *scimServer) getGroup(w http.ResponseWriter, r *http.Request, caller scimCaller, id string) int {
	group, err := s.store.GetSCIMGroup(r.Context(), caller.ProviderID, id)
	if err != nil {
		return s.respond(w, r, caller, "scim.group.read", s.storeFailure(err, "Group"))
	}
	return s.writeGroup(w, r, caller, "scim.group.read", http.StatusOK, group, !membersExcluded(r), true)
}

func (s *scimServer) listGroups(w http.ResponseWriter, r *http.Request, caller scimCaller) int {
	parameters, failure := s.listParameters(r)
	if failure != nil {
		return s.respond(w, r, caller, "scim.group.list", failure)
	}
	filter, failure := scim.ParseFilter(r.URL.Query().Get("filter"), scim.Groups)
	if failure != nil {
		return s.respond(w, r, caller, "scim.group.list", failure)
	}
	query := repository.SCIMGroupQuery{ProviderID: caller.ProviderID, Limit: max(parameters.count, 1), Cursor: parameters.cursor}
	switch filter.Attribute {
	case "displayName":
		query.DisplayName = filter.Value
	case "externalId":
		query.ExternalID = filter.Value
	}
	startIndex := parameters.startIndex
	if query.Cursor == "" && startIndex > 1 {
		hop := query
		hop.SkipTotal = true
		cursor, ok, failure, err := scimCursorHop(startIndex, func(cursor string, limit int) (int, string, error) {
			hop.Cursor, hop.Limit = cursor, limit
			page, err := s.store.ListSCIMGroups(r.Context(), hop)
			return len(page.Items), page.NextCursor, err
		})
		switch {
		case failure != nil:
			return s.respond(w, r, caller, "scim.group.list", failure)
		case err != nil:
			return s.respond(w, r, caller, "scim.group.list", s.storeFailure(err, "Group"))
		case !ok:
			return s.emptyList(w, r, caller, "scim.group.list", "/Groups", startIndex, func() (int64, error) {
				page, err := s.store.ListSCIMGroups(r.Context(), repository.SCIMGroupQuery{ProviderID: caller.ProviderID, DisplayName: query.DisplayName, ExternalID: query.ExternalID, Limit: 1})
				return page.Total.Value, err
			})
		}
		query.Cursor = cursor
	}
	page, err := s.store.ListSCIMGroups(r.Context(), query)
	if err != nil {
		return s.respond(w, r, caller, "scim.group.list", s.storeFailure(err, "Group"))
	}
	response := scim.ListResponse{TotalResults: page.Total.Value, StartIndex: startIndex, NextCursor: page.NextCursor}
	if parameters.count > 0 {
		withMembers := !membersExcluded(r)
		var members map[string][]repository.UserGroupMember
		perGroup := 0
		if withMembers && len(page.Items) > 0 {
			// One query for the whole page. The members of a page share one budget, divided among its groups, so a
			// page of large groups cannot pull an unbounded response; a client that needs a group's members reads the
			// group, or excludes the members, as Entra ID does.
			perGroup = max(scimMaxListedMembers/len(page.Items), 1)
			groupIDs := make([]string, 0, len(page.Items))
			for _, group := range page.Items {
				groupIDs = append(groupIDs, group.ID)
			}
			var err error
			if members, err = s.store.ListSCIMGroupsMembers(r.Context(), caller.ProviderID, groupIDs, perGroup); err != nil {
				return s.respond(w, r, caller, "scim.group.list", s.storeFailure(err, "Group"))
			}
		}
		for _, group := range page.Items {
			if perGroup > 0 && len(members[group.ID]) >= perGroup {
				slog.Warn("a SCIM group list may have truncated the members of a group", "provider", caller.Slug, "group", group.ID, "limit", perGroup)
			}
			response.Resources = append(response.Resources, s.groupDocument(r, group, members[group.ID], withMembers))
		}
	} else {
		response.NextCursor = ""
	}
	return s.writeJSON(w, r, caller, "scim.group.list", "/Groups", http.StatusOK, response, nil, true)
}

func validateScimGroup(displayName string, externalID *string) *scim.Error {
	switch {
	case externalID != nil && repository.ReservedSCIMExternalID(*externalID):
		return scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, "externalId uses a prefix notarx reserves")
	case len([]rune(displayName)) > 255:
		return scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, "displayName is too long")
	case externalID != nil && !validUserText(*externalID, scimMaxExternalID):
		return scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, "externalId is too long")
	}
	return nil
}

func (s *scimServer) createGroup(w http.ResponseWriter, r *http.Request, caller scimCaller) int {
	body, failure := s.readBody(w, r)
	if failure != nil {
		return s.respond(w, r, caller, "scim.group.create", failure)
	}
	input, failure := scim.ParseGroup(body)
	if failure == nil {
		failure = validateScimGroup(input.DisplayName, input.ExternalID)
	}
	if failure != nil {
		return s.respond(w, r, caller, "scim.group.create", failure)
	}
	// The group is created at global role none; the request has no way to name another role.
	group, err := s.store.CreateSCIMGroup(r.Context(), repository.SCIMGroupCreate{
		ProviderID: caller.ProviderID, ExternalID: stringOrEmpty(input.ExternalID), DisplayName: input.DisplayName, MemberIDs: input.MemberIDs,
	}, s.writeAudit(r, caller, "scim.group.create", "/Groups", http.StatusCreated, nil)...)
	if err != nil {
		return s.respond(w, r, caller, "scim.group.create", s.storeFailure(err, "Group"))
	}
	return s.writeGroup(w, r, caller, "scim.group.create", http.StatusCreated, group, true, false)
}

func (s *scimServer) replaceGroup(w http.ResponseWriter, r *http.Request, caller scimCaller, id string) int {
	body, failure := s.readBody(w, r)
	if failure != nil {
		return s.respond(w, r, caller, "scim.group.replace", failure)
	}
	input, failure := scim.ParseGroup(body)
	if failure == nil {
		failure = validateScimGroup(input.DisplayName, input.ExternalID)
	}
	if failure != nil {
		return s.respond(w, r, caller, "scim.group.replace", failure)
	}
	// An attribute the body leaves out is left alone, except the display name, which is required.
	change := repository.SCIMGroupChange{DisplayName: &input.DisplayName, ExternalID: input.ExternalID}
	attributes := []string{"displayName"}
	if input.ExternalID != nil {
		attributes = append(attributes, "externalId")
	}
	if input.HasMembers {
		members := input.MemberIDs
		if members == nil {
			members = []string{}
		}
		change.ReplaceMembers = &members
		attributes = append(attributes, "members")
	}
	return s.changeGroup(w, r, caller, id, "scim.group.replace", change, attributes, nil)
}

func (s *scimServer) patchGroup(w http.ResponseWriter, r *http.Request, caller scimCaller, id string) int {
	body, failure := s.readBody(w, r)
	if failure != nil {
		return s.respond(w, r, caller, "scim.group.patch", failure)
	}
	patch, failure := scim.NormalizeGroupPatch(body)
	if failure == nil {
		failure = validateScimGroup(stringOrEmpty(patch.DisplayName), patch.ExternalID)
	}
	if failure != nil {
		return s.respond(w, r, caller, "scim.group.patch", failure)
	}
	change := repository.SCIMGroupChange{DisplayName: patch.DisplayName, ExternalID: patch.ExternalID, AddMembers: patch.AddMembers, RemoveMembers: patch.RemoveMembers, ReplaceMembers: patch.ReplaceMembers}
	var changed []string
	if patch.DisplayName != nil {
		changed = append(changed, "displayName")
	}
	if patch.ExternalID != nil {
		changed = append(changed, "externalId")
	}
	if len(patch.AddMembers) > 0 || len(patch.RemoveMembers) > 0 || patch.ReplaceMembers != nil {
		changed = append(changed, "members")
	}
	sort.Strings(changed)
	return s.changeGroup(w, r, caller, id, "scim.group.patch", change, changed, patch.Ignored)
}

func (s *scimServer) changeGroup(w http.ResponseWriter, r *http.Request, caller scimCaller, id, operation string, change repository.SCIMGroupChange, changed, ignored []string) int {
	current, err := s.store.GetSCIMGroup(r.Context(), caller.ProviderID, id)
	if err != nil {
		return s.respond(w, r, caller, operation, s.storeFailure(err, "Group"))
	}
	versions := scimIfMatchVersions(r)
	if !scimVersionMatches(versions, current.Version) {
		return s.respond(w, r, caller, operation, s.preconditionFailed())
	}
	if len(ignored) > 0 {
		slog.Info("SCIM request named attributes the server does not store; they were ignored", "provider", caller.Slug, "operation", operation, "attributes", ignored)
	}
	if len(changed) == 0 {
		return s.writeGroup(w, r, caller, operation, http.StatusOK, current, !membersExcluded(r), true)
	}
	change.IfVersions = versions
	evidence := map[string]string{"attributes": strings.Join(changed, ",")}
	if len(ignored) > 0 {
		evidence["ignored"] = strings.Join(ignored, ",")
	}
	updated, err := s.store.UpdateSCIMGroup(r.Context(), caller.ProviderID, id, change, s.writeAudit(r, caller, operation, "/Groups/"+id, http.StatusOK, evidence)...)
	if err != nil {
		return s.respond(w, r, caller, operation, s.storeFailure(err, "Group"))
	}
	return s.writeGroup(w, r, caller, operation, http.StatusOK, updated, !membersExcluded(r), false)
}

func (s *scimServer) deleteGroup(w http.ResponseWriter, r *http.Request, caller scimCaller, id string) int {
	current, err := s.store.GetSCIMGroup(r.Context(), caller.ProviderID, id)
	if err != nil {
		return s.respond(w, r, caller, "scim.group.delete", s.storeFailure(err, "Group"))
	}
	versions := scimIfMatchVersions(r)
	if !scimVersionMatches(versions, current.Version) {
		return s.respond(w, r, caller, "scim.group.delete", s.preconditionFailed())
	}
	if err := s.store.DeleteSCIMGroup(r.Context(), caller.ProviderID, id, versions, s.writeAudit(r, caller, "scim.group.delete", "/Groups/"+id, http.StatusNoContent, nil)...); err != nil {
		return s.respond(w, r, caller, "scim.group.delete", s.storeFailure(err, "Group"))
	}
	w.WriteHeader(http.StatusNoContent)
	return http.StatusNoContent
}
