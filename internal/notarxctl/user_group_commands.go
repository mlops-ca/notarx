package notarxctl

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
)

// User group commands (ADR 0018 Decision 5). A user group is a set of accounts that a repository grant can name as
// the principal group:<id>. The command is "user-group" because "group" already names repository Groups (ADR 0012).
const userGroupsPath = managementPrefix + "/user-groups"

func userGroupPath(id string) string {
	return userGroupsPath + "/" + url.PathEscape(id)
}

func userGroupMembersPath(id string) string {
	return userGroupPath(id) + "/members"
}

func parseUserGroupID(value string) (string, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || id == uuid.Nil {
		return "", &UsageError{Message: "ID must be a user group UUID", Hint: "notarxctl user-group list shows the IDs"}
	}
	return id.String(), nil
}

func userGroupRows(group adminopenapi.UserGroup) [][2]string {
	rows := [][2]string{
		{"id", group.Id.String()}, {"name", group.DisplayName}, {"source", string(group.Source)},
		{"global role", string(group.GlobalRole)}, {"version", group.Version},
	}
	if group.ProviderId != nil {
		rows = append(rows, [2]string{"provider", group.ProviderId.String()})
	}
	if group.ExternalId != nil {
		rows = append(rows, [2]string{"external id", *group.ExternalId})
	}
	return append(rows, [2]string{"created", group.CreatedAt.UTC().Format(time.RFC3339)}, [2]string{"updated", group.UpdatedAt.UTC().Format(time.RFC3339)})
}

func renderUserGroups(w io.Writer, groups []adminopenapi.UserGroup) error {
	if len(groups) == 0 {
		return emptyLine(w, "user groups")
	}
	table := [][]string{{"NAME", "SOURCE", "GLOBAL ROLE", "VERSION", "ID"}}
	for _, group := range groups {
		table = append(table, []string{group.DisplayName, string(group.Source), string(group.GlobalRole), group.Version, group.Id.String()})
	}
	return writeColumns(w, table)
}

func renderUserGroupMembers(w io.Writer, members []adminopenapi.UserGroupMember) error {
	if len(members) == 0 {
		return emptyLine(w, "members")
	}
	table := [][]string{{"USER", "SOURCE", "ADDED", "ID"}}
	for _, member := range members {
		table = append(table, []string{member.UserName, string(member.Source), member.CreatedAt.UTC().Format(time.RFC3339), member.UserId.String()})
	}
	return writeColumns(w, table)
}

func completeUserGroup(group adminopenapi.UserGroup) bool {
	return group.Id != uuid.Nil && group.DisplayName != "" && group.Version != "" && group.GlobalRole != "" && group.Source != ""
}

func userGroupListCommand() command {
	list := &cursorList[adminopenapi.UserGroup]{
		path: userGroupsPath, command: "notarxctl user-group list", noun: "user groups", render: renderUserGroups,
		sizeParam: "limit",
	}
	return command{
		name:     "user-group list",
		synopsis: "list user groups",
		usage:    "notarxctl user-group list [--page-size N] [--cursor TOKEN | --all] [--max-pages N] [--json]",
		setFlags: list.setFlags,
		meta: commandMeta{
			Resource: "user-group", Scenarios: []string{"admin"}, Stage: "m2",
			Endpoints: []string{"GET " + userGroupsPath}, MinimumRole: "platform admin",
			OutputSchema: "UserGroupList", DryRun: "unsupported", Retry: true,
		},
		run: list.run,
	}
}

func userGroupShowCommand() command {
	return command{
		name:        "user-group show",
		synopsis:    "show one user group",
		usage:       "notarxctl user-group show ID [--json]",
		positionals: 1,
		argsUsage:   "ID",
		meta: commandMeta{
			Resource: "user-group", Scenarios: []string{"admin"}, Stage: "m2",
			Endpoints: []string{"GET " + userGroupsPath + "/{groupId}"}, MinimumRole: "platform admin",
			OutputSchema: "UserGroup", DryRun: "unsupported", Retry: true,
		},
		run: documentCommand[adminopenapi.UserGroup]{
			request: func(env *commandContext) (string, url.Values, error) {
				id, err := parseUserGroupID(env.args[0])
				return userGroupPath(id), nil, err
			},
			render: func(w io.Writer, group adminopenapi.UserGroup) error { return keyValues(w, userGroupRows(group)) },
		}.run,
	}
}

func userGroupCreateCommand() command {
	var source, providerID, externalID string
	return command{
		name:        "user-group create",
		synopsis:    "create a local user group, or a claims-sourced one that a login matches by external id",
		usage:       "notarxctl user-group create NAME [--source local|claims --provider ID --external-id VALUE] [--dry-run] [--json]",
		positionals: 1,
		argsUsage:   "NAME",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&source, "source", "local", "local, or claims for a group that a login's groups claim fills")
			fs.StringVar(&providerID, "provider", "", "identity provider UUID (claims groups only)")
			fs.StringVar(&externalID, "external-id", "", "the value of the groups claim that selects this group (claims groups only)")
		},
		meta: commandMeta{
			Resource: "user-group", Scenarios: []string{"admin"}, Stage: "m2",
			Endpoints: []string{"POST " + userGroupsPath}, Mutating: true, MinimumRole: "platform admin",
			InputSchema: "CreateUserGroup", OutputSchema: "UserGroup", DryRun: "client-plan",
		},
		run: func(ctx context.Context, env *commandContext) error {
			name := strings.TrimSpace(env.args[0])
			if name == "" || len(name) > 255 {
				return &UsageError{Message: "the group name must be 1-255 bytes after trimming whitespace"}
			}
			body := adminopenapi.CreateUserGroup{DisplayName: name}
			switch source {
			case "local":
				if providerID != "" || externalID != "" {
					return &UsageError{Message: "--provider and --external-id only apply to --source claims"}
				}
			case "claims":
				provider, err := uuid.Parse(providerID)
				if err != nil || provider == uuid.Nil || strings.TrimSpace(externalID) == "" || len(externalID) > 255 {
					return &UsageError{Message: "a claims group needs --provider (a provider UUID) and --external-id (1-255 bytes)", Hint: "notarxctl does not list providers; copy the ID from the console or GET /api/v2/authentication/providers"}
				}
				value := adminopenapi.CreateUserGroupSource("claims")
				body.Source, body.ProviderId, body.ExternalId = &value, &provider, &externalID
			default:
				return &UsageError{Message: "--source must be local or claims"}
			}
			if env.globals.dryRun {
				plan := writePlan{Mode: "client-plan", Method: http.MethodPost, Path: userGroupsPath, Headers: map[string]string{}, Body: body,
					Verified: []string{"local input shape", "a new group starts with global role none"}, NotVerified: []string{"caller permission (platform admin)", "name or external id uniqueness", "that the provider exists"}}
				return env.render(plan, nil, func(w io.Writer) error { return renderPlan(w, plan) })
			}
			client, err := env.client()
			if err != nil {
				return err
			}
			encoded, err := encodeBody(body)
			if err != nil {
				return err
			}
			// A repeated create after a lost response answers 409, so the write is not retried.
			var group adminopenapi.UserGroup
			response, err := client.sendJSON(ctx, http.MethodPost, userGroupsPath, requestOptions{body: encoded}, &group)
			if err == nil && (!completeUserGroup(group) || group.DisplayName != name || response.StatusCode != http.StatusCreated) {
				err = incompleteResponse(response, client, "user group")
			}
			if err != nil {
				if outcomeUnknown(err) {
					return unknownOutcomeError(err, unknownOutcome{Operation: "user-group create", Name: name, ReadBack: "notarxctl user-group list"},
						"the group may have been created; list the user groups before repeating the command")
				}
				return err
			}
			return env.render(group, nil, func(w io.Writer) error { return keyValues(w, userGroupRows(group)) })
		},
	}
}

// userGroupUpdateCommand serves rename and set-role: both are PUTs guarded by If-Match, defaulting to the version
// read just before.
func userGroupUpdateCommand(operation string) command {
	var ifMatch string
	var role string
	schema, usage, synopsis := "RenameUserGroup", "notarxctl user-group rename ID NAME [--if-match VERSION] [--dry-run] [--json]", "rename a user group"
	positionals, args := 2, "ID NAME"
	if operation == "set-role" {
		schema, usage, synopsis = "SetUserGroupGlobalRole", "notarxctl user-group set-role ID --role none|member|admin [--if-match VERSION] [--yes] [--dry-run] [--json]", "set the global role that raises the level of a group's members"
		positionals, args = 1, "ID"
	}
	endpoint := "PUT " + userGroupsPath + "/{groupId}"
	if operation == "set-role" {
		endpoint += "/global-role"
	}
	return command{
		name: "user-group " + operation, synopsis: synopsis, usage: usage, positionals: positionals, argsUsage: args,
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&ifMatch, "if-match", "", "update only this version (default: the version read just before)")
			if operation == "set-role" {
				fs.StringVar(&role, "role", "", "global role: none, member, or admin")
			}
		},
		meta: commandMeta{
			Resource: "user-group", Scenarios: []string{"admin"}, Stage: "m2",
			Endpoints: []string{endpoint, "GET " + userGroupsPath + "/{groupId}"}, Mutating: true, MinimumRole: "platform admin",
			InputSchema: schema, OutputSchema: "UserGroup", DryRun: "client-plan",
		},
		run: func(ctx context.Context, env *commandContext) error {
			id, err := parseUserGroupID(env.args[0])
			if err != nil {
				return err
			}
			var body any
			target := userGroupPath(id)
			if operation == "rename" {
				name := strings.TrimSpace(env.args[1])
				if name == "" || len(name) > 255 {
					return &UsageError{Message: "the group name must be 1-255 bytes after trimming whitespace"}
				}
				body = adminopenapi.RenameUserGroup{DisplayName: name}
			} else {
				if role != "none" && role != "member" && role != "admin" {
					return &UsageError{Message: "--role must be none, member, or admin"}
				}
				if role == "admin" && !env.globals.dryRun && !env.globals.yes {
					return &UsageError{Message: "an admin global role makes every member of the group a platform administrator, now and on later membership changes",
						Hint: "re-run with --yes, or with --dry-run to see the request"}
				}
				target += "/global-role"
				body = adminopenapi.SetUserGroupGlobalRole{GlobalRole: adminopenapi.SetUserGroupGlobalRoleGlobalRole(role)}
			}
			if env.flagSet["if-match"] && strings.TrimSpace(ifMatch) == "" {
				return &UsageError{Message: "--if-match cannot be empty", Hint: "pass the version from notarxctl user-group show ID"}
			}
			client, err := env.client()
			if err != nil {
				return err
			}
			var current adminopenapi.UserGroup
			response, err := client.sendJSON(ctx, http.MethodGet, userGroupPath(id), requestOptions{idempotent: true}, &current)
			if err == nil && (!completeUserGroup(current) || current.Id.String() != id) {
				err = incompleteResponse(response, client, "user group")
			}
			if err != nil {
				return err
			}
			if ifMatch == "" {
				ifMatch = current.Version
			}
			headers := http.Header{"If-Match": {ifMatch}}
			if env.globals.dryRun {
				plan := writePlan{Mode: "client-plan", Method: http.MethodPut, Path: target, Headers: map[string]string{"If-Match": ifMatch}, Body: body, Current: current,
					Verified:    []string{"local input shape", "the group's identity and version were read"},
					NotVerified: []string{"caller permission (platform admin)", "that the version still matches when the update is sent"}}
				return env.render(plan, nil, func(w io.Writer) error { return renderPlan(w, plan) })
			}
			encoded, err := encodeBody(body)
			if err != nil {
				return err
			}
			// A repeat after a lost response would fail its precondition, so the write is not retried.
			var group adminopenapi.UserGroup
			response, err = client.sendJSON(ctx, http.MethodPut, target, requestOptions{body: encoded, header: headers}, &group)
			if err == nil && (!completeUserGroup(group) || group.Id.String() != id || response.StatusCode != http.StatusOK) {
				err = incompleteResponse(response, client, "user group")
			}
			if err != nil {
				var api *APIError
				if errors.As(err, &api) && api.HTTPStatus == http.StatusPreconditionFailed {
					api.Hint = "the group changed and was not overwritten; review it with notarxctl user-group show and repeat"
					return &resultError{err: err, data: map[string]any{"outcome": "conflict", "id": id, "expectedVersion": ifMatch, "readBack": "notarxctl user-group show " + id}}
				}
				if outcomeUnknown(err) {
					return unknownOutcomeError(err, unknownOutcome{Operation: "user-group " + operation, ID: id, Version: ifMatch, ReadBack: "notarxctl user-group show " + id},
						"the write may have applied; read the group back before deciding whether to repeat it")
				}
				return err
			}
			return env.render(group, nil, func(w io.Writer) error { return keyValues(w, userGroupRows(group)) })
		},
	}
}

func userGroupDeleteCommand() command {
	return command{
		name:        "user-group delete",
		synopsis:    "delete a user group with its memberships",
		usage:       "notarxctl user-group delete ID --yes [--dry-run] [--json]",
		positionals: 1,
		argsUsage:   "ID",
		meta: commandMeta{
			Resource: "user-group", Scenarios: []string{"admin"}, Stage: "m2",
			Endpoints: []string{"DELETE " + userGroupsPath + "/{groupId}"}, Mutating: true, MinimumRole: "platform admin",
			DryRun: "client-plan",
		},
		run: func(ctx context.Context, env *commandContext) error {
			id, err := parseUserGroupID(env.args[0])
			if err != nil {
				return err
			}
			if !env.globals.dryRun && !env.globals.yes {
				return &UsageError{Message: "deleting a user group needs --yes: its members lose what grants to group:" + id + " gave them, and a recreated group starts at global role none",
					Hint: "re-run with --yes, or with --dry-run to see the request"}
			}
			if env.globals.dryRun {
				plan := writePlan{Mode: "client-plan", Method: http.MethodDelete, Path: userGroupPath(id), Headers: map[string]string{},
					Verified: []string{"--yes is not needed for a plan"}, NotVerified: []string{"caller permission (platform admin)", "that the group exists"}}
				return env.render(plan, nil, func(w io.Writer) error { return renderPlan(w, plan) })
			}
			client, err := env.client()
			if err != nil {
				return err
			}
			// A repeat after a lost response answers 404, so the write is not retried; a 404 now means it is gone.
			response, err := client.sendJSON(ctx, http.MethodDelete, userGroupPath(id), requestOptions{}, nil)
			if err == nil && response.StatusCode != http.StatusNoContent {
				err = incompleteResponse(response, client, "user group deletion")
			}
			if err != nil {
				if outcomeUnknown(err) {
					return unknownOutcomeError(err, unknownOutcome{Operation: "user-group delete", ID: id, ReadBack: "notarxctl user-group show " + id},
						"the group may have been deleted; read it back (a 404 means it is gone)")
				}
				return err
			}
			return env.render(map[string]any{"id": id, "deleted": true}, nil, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "user group %s deleted\n", id)
				return err
			})
		},
	}
}

func userGroupMemberListCommand() command {
	list := &cursorList[adminopenapi.UserGroupMember]{
		command: "", noun: "members", render: renderUserGroupMembers, sizeParam: "limit",
		request: func(env *commandContext) (string, url.Values, error) {
			id, err := parseUserGroupID(env.args[0])
			return userGroupMembersPath(id), nil, err
		},
	}
	return command{
		name:        "user-group member list",
		synopsis:    "list the members of a user group",
		usage:       "notarxctl user-group member list ID [--page-size N] [--cursor TOKEN | --all] [--max-pages N] [--json]",
		positionals: 1,
		argsUsage:   "ID",
		setFlags:    list.setFlags,
		meta: commandMeta{
			Resource: "user-group", Scenarios: []string{"admin"}, Stage: "m2",
			Endpoints: []string{"GET " + userGroupsPath + "/{groupId}/members"}, MinimumRole: "platform admin",
			OutputSchema: "UserGroupMemberList", DryRun: "unsupported", Retry: true,
		},
		run: list.run,
	}
}

func userGroupMemberChangeCommand(operation string) command {
	adding := operation == "add"
	method, synopsis, endpoint := http.MethodDelete, "remove an account from a user group", "DELETE "+userGroupsPath+"/{groupId}/members/{userId}"
	inputSchema := ""
	if adding {
		method, synopsis, endpoint, inputSchema = http.MethodPost, "add an account to a user group", "POST "+userGroupsPath+"/{groupId}/members", "AddUserGroupMember"
	}
	usage := "notarxctl user-group member " + operation + " ID USER_ID"
	if !adding {
		usage += " --yes"
	}
	return command{
		name: "user-group member " + operation, synopsis: synopsis, usage: usage + " [--dry-run] [--json]", positionals: 2, argsUsage: "ID USER_ID",
		meta: commandMeta{
			Resource: "user-group", Scenarios: []string{"admin"}, Stage: "m2",
			Endpoints: []string{endpoint}, Mutating: true, MinimumRole: "platform admin",
			InputSchema: inputSchema, DryRun: "client-plan",
		},
		run: func(ctx context.Context, env *commandContext) error {
			id, err := parseUserGroupID(env.args[0])
			if err != nil {
				return err
			}
			user, err := uuid.Parse(strings.TrimSpace(env.args[1]))
			if err != nil || user == uuid.Nil {
				return &UsageError{Message: "USER_ID must be a user UUID", Hint: "notarxctl user list shows the IDs"}
			}
			if !adding && !env.globals.dryRun && !env.globals.yes {
				return &UsageError{Message: "removing a member needs --yes: the account loses what the group granted on its next request", Hint: "re-run with --yes, or with --dry-run to see the request"}
			}
			path, payload := userGroupMembersPath(id), any(nil)
			if adding {
				payload = adminopenapi.AddUserGroupMember{UserId: user}
			} else {
				path += "/" + user.String()
			}
			if env.globals.dryRun {
				plan := writePlan{Mode: "client-plan", Method: method, Path: path, Headers: map[string]string{}, Body: payload,
					Verified: []string{"local input shape"}, NotVerified: []string{"caller permission (platform admin)", "that the group and the user exist"}}
				return env.render(plan, nil, func(w io.Writer) error { return renderPlan(w, plan) })
			}
			client, err := env.client()
			if err != nil {
				return err
			}
			encoded, err := encodeBody(payload)
			if err != nil {
				return err
			}
			// Adding a member again answers 409 and removing one twice answers 404: both mean the account is already
			// in the wanted state, so the command reports that with exit 0 instead of retrying.
			response, err := client.sendJSON(ctx, method, path, requestOptions{body: encoded}, nil)
			changed := true
			if err == nil && response.StatusCode != http.StatusNoContent {
				err = incompleteResponse(response, client, "user group membership")
			}
			var api *APIError
			if errors.As(err, &api) {
				switch {
				case adding && api.HTTPStatus == http.StatusConflict && api.Code == "member_exists":
					err, changed = nil, false
				case !adding && api.HTTPStatus == http.StatusNotFound && strings.Contains(api.Message, "not a member"):
					err, changed = nil, false
				}
			}
			if err != nil {
				if outcomeUnknown(err) {
					return unknownOutcomeError(err, unknownOutcome{Operation: "user-group member " + operation, ID: id, Name: user.String(), ReadBack: "notarxctl user-group member list " + id},
						"the membership may have changed; list the members before repeating the command")
				}
				return err
			}
			result := map[string]any{"groupId": id, "userId": user.String(), "changed": changed, "member": adding}
			return env.render(result, nil, func(w io.Writer) error {
				verb := map[bool]string{true: "added to", false: "removed from"}[adding]
				if !changed {
					verb = map[bool]string{true: "is already in", false: "is not in"}[adding]
				}
				_, err := fmt.Fprintf(w, "user %s %s user group %s\n", user, verb, id)
				return err
			})
		},
	}
}
