package app

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/repository"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// The management API of user groups (ADR 0018 Decision 5). Every operation needs platform administration; a
// group is a grant principal (group:<id>), so reading or changing one is as sensitive as editing grants.

func userGroupResponse(group repository.UserGroup) adminopenapi.UserGroup {
	response := adminopenapi.UserGroup{
		Id: uuid.MustParse(group.ID), DisplayName: group.DisplayName, Source: adminopenapi.UserGroupSource(group.Source),
		GlobalRole: adminopenapi.UserGroupGlobalRole(group.GlobalRole), Version: group.Version,
		CreatedAt: group.CreatedAt, UpdatedAt: group.UpdatedAt,
	}
	if group.ProviderID != "" {
		providerID := uuid.MustParse(group.ProviderID)
		response.ProviderId = &providerID
	}
	if group.ExternalID != "" {
		externalID := group.ExternalID
		response.ExternalId = &externalID
	}
	return response
}

func writeUserGroup(w http.ResponseWriter, status int, group repository.UserGroup) {
	w.Header().Set("ETag", group.Version)
	writeNativeMavenJSON(w, status, userGroupResponse(group))
}

// userGroupLimit applies the page size of the contract, the same clamp the stores use, so the limit a response
// reports is the one that was applied.
func userGroupLimit(limit *int) int {
	if limit == nil {
		return repository.ClampUserGroupPageSize(0)
	}
	return repository.ClampUserGroupPageSize(*limit)
}

// writeUserGroupError answers a store error. notFound names what a not-found answer means for the operation: the
// store reports a missing group, a missing user and a missing provider through the same sentinels.
func (h accessManagementHandler) writeUserGroupError(w http.ResponseWriter, err error, action string, notFound ...string) {
	message := "user group not found"
	if len(notFound) > 0 {
		message = notFound[0]
	}
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeHostedProblem(w, http.StatusNotFound, "not_found", message)
	case errors.Is(err, repository.ErrUserGroupProviderNotFound):
		writeHostedProblem(w, http.StatusBadRequest, "provider_not_found", "the identity provider of the group does not exist")
	case errors.Is(err, repository.ErrVersionConflict):
		writeHostedProblem(w, http.StatusPreconditionFailed, "version_conflict", "If-Match does not match current user group version")
	case errors.Is(err, repository.ErrUserGroupNameExists):
		writeHostedProblem(w, http.StatusConflict, "name_exists", "user group name already exists")
	case errors.Is(err, repository.ErrUserGroupExternalID):
		writeHostedProblem(w, http.StatusConflict, "external_id_exists", "a user group with this external id already exists for the provider")
	case errors.Is(err, repository.ErrUserGroupMemberExists):
		writeHostedProblem(w, http.StatusConflict, "member_exists", "the user is already a member of the group")
	case errors.Is(err, repository.ErrUserGroupInvalid):
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "user group is invalid")
	default:
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", action+" failed")
	}
}

func (h accessManagementHandler) ListUserGroups(w http.ResponseWriter, r *http.Request, params adminopenapi.ListUserGroupsParams) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	limit := userGroupLimit(params.Limit)
	cursor := ""
	if params.PageToken != nil {
		cursor = string(*params.PageToken)
	}
	items, next, err := h.userGroups.ListUserGroups(r.Context(), limit, cursor)
	if err != nil {
		h.writeUserGroupError(w, err, "list user groups")
		return
	}
	response := adminopenapi.UserGroupList{Items: make([]adminopenapi.UserGroup, 0, len(items)), Limit: limit}
	for _, item := range items {
		response.Items = append(response.Items, userGroupResponse(item))
	}
	if next != "" {
		response.NextPageToken = &next
	}
	writeNativeMavenJSON(w, http.StatusOK, response)
}

func (h accessManagementHandler) CreateUserGroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	var body adminopenapi.CreateUserGroup
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "invalid user group body")
		return
	}
	group := repository.UserGroup{DisplayName: body.DisplayName, Source: repository.UserGroupSourceLocal}
	if body.Source != nil {
		group.Source = repository.UserGroupSource(*body.Source)
	}
	if body.ProviderId != nil {
		group.ProviderID = body.ProviderId.String()
	}
	if body.ExternalId != nil {
		group.ExternalID = *body.ExternalId
	}
	// Only a local group and a claims-sourced group can be created here; SCIM groups come from the SCIM server.
	if group.Source != repository.UserGroupSourceLocal && group.Source != repository.UserGroupSourceClaims {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "source must be local or claims")
		return
	}
	group.ID = ids.New()
	created, err := h.userGroups.CreateUserGroup(r.Context(), group, h.authorizationAudit(r, "", "user-groups/"+group.ID, "user_group.create", http.StatusCreated)...)
	if err != nil {
		h.writeUserGroupError(w, err, "create user group")
		return
	}
	writeUserGroup(w, http.StatusCreated, created)
}

func (h accessManagementHandler) GetUserGroup(w http.ResponseWriter, r *http.Request, groupID adminopenapi.UserGroupId) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	group, err := h.userGroups.GetUserGroup(r.Context(), groupID.String())
	if err != nil {
		h.writeUserGroupError(w, err, "get user group")
		return
	}
	writeUserGroup(w, http.StatusOK, group)
}

func (h accessManagementHandler) RenameUserGroup(w http.ResponseWriter, r *http.Request, groupID adminopenapi.UserGroupId, params adminopenapi.RenameUserGroupParams) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	var body adminopenapi.RenameUserGroup
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "invalid user group body")
		return
	}
	group, err := h.userGroups.RenameUserGroup(r.Context(), groupID.String(), body.DisplayName, string(params.IfMatch), h.authorizationAudit(r, "", "user-groups/"+groupID.String(), "user_group.rename", http.StatusOK)...)
	if err != nil {
		h.writeUserGroupError(w, err, "rename user group")
		return
	}
	writeUserGroup(w, http.StatusOK, group)
}

func (h accessManagementHandler) SetUserGroupGlobalRole(w http.ResponseWriter, r *http.Request, groupID adminopenapi.UserGroupId, params adminopenapi.SetUserGroupGlobalRoleParams) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	var body adminopenapi.SetUserGroupGlobalRole
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "invalid global role body")
		return
	}
	role := string(body.GlobalRole)
	// The flag is audited with the role it sets: it is the one way a group raises the level of its members.
	audit := h.authorizationAudit(r, "", "user-groups/"+groupID.String(), "user_group.set_global_role", http.StatusOK)
	for i := range audit {
		audit[i].AuthorizationSource, audit[i].AuthorizationReason = "user_group", "global_role_"+role
	}
	group, err := h.userGroups.SetUserGroupGlobalRole(r.Context(), groupID.String(), role, string(params.IfMatch), audit...)
	if err != nil {
		h.writeUserGroupError(w, err, "set user group global role")
		return
	}
	writeUserGroup(w, http.StatusOK, group)
}

func (h accessManagementHandler) DeleteUserGroup(w http.ResponseWriter, r *http.Request, groupID adminopenapi.UserGroupId) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	if err := h.userGroups.DeleteUserGroup(r.Context(), groupID.String(), h.authorizationAudit(r, "", "user-groups/"+groupID.String(), "user_group.delete", http.StatusNoContent)...); err != nil {
		h.writeUserGroupError(w, err, "delete user group")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h accessManagementHandler) ListUserGroupMembers(w http.ResponseWriter, r *http.Request, groupID adminopenapi.UserGroupId, params adminopenapi.ListUserGroupMembersParams) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	limit := userGroupLimit(params.Limit)
	cursor := ""
	if params.PageToken != nil {
		cursor = string(*params.PageToken)
	}
	items, next, err := h.userGroups.ListUserGroupMembers(r.Context(), groupID.String(), limit, cursor)
	if err != nil {
		h.writeUserGroupError(w, err, "list user group members")
		return
	}
	response := adminopenapi.UserGroupMemberList{Items: make([]adminopenapi.UserGroupMember, 0, len(items)), Limit: limit}
	for _, item := range items {
		response.Items = append(response.Items, adminopenapi.UserGroupMember{
			UserId: uuid.MustParse(item.UserID), UserName: item.UserName, Source: adminopenapi.UserGroupMemberSource(item.Source), CreatedAt: item.CreatedAt,
		})
	}
	if next != "" {
		response.NextPageToken = &next
	}
	writeNativeMavenJSON(w, http.StatusOK, response)
}

func (h accessManagementHandler) AddUserGroupMember(w http.ResponseWriter, r *http.Request, groupID adminopenapi.UserGroupId) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	var body adminopenapi.AddUserGroupMember
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "invalid member body")
		return
	}
	// The store reports a missing group and a missing user alike, so the group is checked first.
	if _, err := h.userGroups.GetUserGroup(r.Context(), groupID.String()); err != nil {
		h.writeUserGroupError(w, err, "add user group member")
		return
	}
	resource := "user-groups/" + groupID.String() + "/members/" + body.UserId.String()
	if err := h.userGroups.AddUserGroupMember(r.Context(), groupID.String(), body.UserId.String(), repository.UserGroupSourceLocal, h.authorizationAudit(r, "", resource, "user_group.member_add", http.StatusNoContent)...); err != nil {
		h.writeUserGroupError(w, err, "add user group member", "user not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h accessManagementHandler) RemoveUserGroupMember(w http.ResponseWriter, r *http.Request, groupID adminopenapi.UserGroupId, userID openapi_types.UUID) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	resource := "user-groups/" + groupID.String() + "/members/" + userID.String()
	if err := h.userGroups.RemoveUserGroupMember(r.Context(), groupID.String(), userID.String(), h.authorizationAudit(r, "", resource, "user_group.member_remove", http.StatusNoContent)...); err != nil {
		h.writeUserGroupError(w, err, "remove user group member", "the user is not a member of the group")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
