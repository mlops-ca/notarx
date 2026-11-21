package scim

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"
	"strings"
)

// MaxPatchOperations bounds one PATCH request.
const MaxPatchOperations = 100

// MaxMembersPerRequest bounds the members one group write names.
const MaxMembersPerRequest = 5000

// UserPatch is the normalized form of a PATCH of a User: the attributes the server stores, as pointers (nil means
// untouched), and the attribute names that were mapped but are not supported, which the server logs and ignores
// with 200 so that Entra ID does not treat the response as a sync failure and quarantine the application.
type UserPatch struct {
	UserName    *string
	DisplayName *string
	Email       *string
	ExternalID  *string
	Active      *bool
	Ignored     []string
}

// GroupPatch is the normalized form of a PATCH of a Group.
type GroupPatch struct {
	DisplayName    *string
	ExternalID     *string
	AddMembers     []string
	RemoveMembers  []string
	ReplaceMembers *[]string
	Ignored        []string
}

type patchRequest struct {
	Schemas    []string         `json:"schemas"`
	Operations []patchOperation `json:"Operations"`
}

type patchOperation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

func invalidSyntax(detail string) *Error { return NewError(400, TypeInvalidSyntax, detail) }
func invalidPath(detail string) *Error   { return NewError(400, TypeInvalidPath, detail) }
func invalidValue(detail string) *Error  { return NewError(400, TypeInvalidValue, detail) }

// selector is a parsed attr[name eq value] filter of a path.
type selector struct {
	name  string
	value string
}

// path is a parsed PATCH path: attr[selector].sub. attr and sub are lower-cased; extension marks a path in a
// schema other than the core one, which the server never stores.
type path struct {
	attr, sub string
	selector  *selector
	extension bool
}

func (p path) display() string {
	name := p.attr
	if p.sub != "" {
		name += "." + p.sub
	}
	return name
}

// parsePath reads a path of the core schema of the resource. ok is false for a path it cannot read; the caller
// decides whether that is an error (core attribute) or an ignorable mapped attribute.
func parsePath(raw string, corePrefix string) (path, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) > len(corePrefix) && strings.EqualFold(raw[:len(corePrefix)], corePrefix) {
		raw = raw[len(corePrefix):]
	} else if strings.HasPrefix(strings.ToLower(raw), "urn:") {
		return path{attr: strings.ToLower(raw), extension: true}, true
	}
	open := strings.IndexByte(raw, '[')
	if open < 0 {
		attr, sub, _ := strings.Cut(raw, ".")
		return path{attr: strings.ToLower(attr), sub: strings.ToLower(sub)}, attr != ""
	}
	closing := strings.LastIndexByte(raw, ']')
	if closing < open {
		return path{attr: strings.ToLower(raw[:open])}, false
	}
	parsed := path{attr: strings.ToLower(strings.TrimSpace(raw[:open]))}
	rest := strings.TrimSpace(raw[closing+1:])
	if rest != "" {
		if !strings.HasPrefix(rest, ".") {
			return parsed, false
		}
		parsed.sub = strings.ToLower(strings.TrimPrefix(rest, "."))
	}
	name, value, found := strings.Cut(strings.TrimSpace(raw[open+1:closing]), " ")
	if !found {
		return parsed, false
	}
	operator, literal, found := strings.Cut(strings.TrimSpace(value), " ")
	if !found || !strings.EqualFold(operator, "eq") {
		return parsed, false
	}
	// Entra ID sometimes sends an unquoted value; the quotes are optional when there is nothing ambiguous.
	literal = strings.Trim(strings.TrimSpace(literal), `"`)
	parsed.selector = &selector{name: strings.ToLower(strings.TrimSpace(name)), value: literal}
	return parsed, parsed.attr != "" && literal != ""
}

// parseBool reads a boolean that is a JSON bool or the strings "True" and "False" (Entra ID sends strings unless
// its aadOptscim062020 flag is set).
func parseBool(raw json.RawMessage) (bool, bool) {
	// A JSON null unmarshals into a bool without error and leaves it false: it must not be read as "false".
	if !hasValue(raw) {
		return false, false
	}
	var boolean bool
	if json.Unmarshal(raw, &boolean) == nil {
		return boolean, true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

func parseString(raw json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return "", false
	}
	return text, true
}

func hasValue(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func decodePatch(body []byte) ([]patchOperation, *Error) {
	var request patchRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&request); err != nil {
		return nil, invalidSyntax("the request body is not a valid PatchOp")
	}
	if len(request.Operations) == 0 {
		return nil, invalidSyntax("the request carries no Operations")
	}
	if len(request.Operations) > MaxPatchOperations {
		return nil, NewError(400, TypeTooMany, "too many Operations in one request")
	}
	return request.Operations, nil
}

// maxPatchDepth bounds how deep a pathless value object is flattened: {"name":{"givenName":"A"}} is two levels,
// which is as deep as any supported attribute goes.
const maxPatchDepth = 3

// flatten turns a pathless value object into attribute assignments: a nested object becomes dotted names, so
// {"name":{"givenName":"A"}} and {"name.givenName":"A"} are the same assignment, as Okta and Entra ID send them.
func flatten(prefix string, raw json.RawMessage, depth int, assigned *int, emit func(name string, value json.RawMessage) *Error) *Error {
	if depth > maxPatchDepth {
		return invalidValue("the value is nested too deeply")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return invalidValue("a pathless operation needs an object value")
	}
	if *assigned += len(object); *assigned > MaxPatchAttributes {
		return NewError(400, TypeTooMany, "too many attributes in one request")
	}
	names := make([]string, 0, len(object))
	for name := range object {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := object[name]
		full := name
		if prefix != "" {
			full = prefix + "." + name
		}
		if isJSONObject(value) && !strings.HasPrefix(strings.ToLower(name), "urn:") && !strings.Contains(name, "[") {
			if err := flatten(full, value, depth+1, assigned, emit); err != nil {
				return err
			}
			continue
		}
		if err := emit(full, value); err != nil {
			return err
		}
	}
	return nil
}

// isJSONObject reports whether the raw value starts an object, without parsing it.
func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// NormalizeUserPatch normalizes a PATCH body of a User (RFC 7644 section 3.5.2).
func NormalizeUserPatch(body []byte) (UserPatch, *Error) {
	operations, failure := decodePatch(body)
	if failure != nil {
		return UserPatch{}, failure
	}
	var patch UserPatch
	assigned := 0
	for _, operation := range operations {
		op := strings.ToLower(strings.TrimSpace(operation.Op))
		if op != "add" && op != "replace" && op != "remove" {
			return UserPatch{}, invalidSyntax("op must be add, replace or remove")
		}
		if strings.TrimSpace(operation.Path) == "" {
			if op == "remove" {
				return UserPatch{}, NewError(400, TypeNoTarget, "a remove operation needs a path")
			}
			if failure := flatten("", operation.Value, 1, &assigned, func(name string, value json.RawMessage) *Error {
				return patch.apply(op, name, value)
			}); failure != nil {
				return UserPatch{}, failure
			}
			continue
		}
		if failure := patch.apply(op, operation.Path, operation.Value); failure != nil {
			return UserPatch{}, failure
		}
	}
	return patch, nil
}

// MaxIgnoredAttributes and MaxIgnoredNameLength bound what a request can make the server remember about the attributes
// it ignored: the names are logged and written to the audit evidence, so neither their number nor their length may be
// chosen by the client without limit.
const (
	MaxIgnoredAttributes = 50
	MaxIgnoredNameLength = 128
	// MaxPatchAttributes bounds the attribute assignments one request carries, pathless value objects included.
	MaxPatchAttributes = 200
)

func (p *UserPatch) ignore(name string) { p.Ignored = appendIgnored(p.Ignored, name) }

// appendIgnored records an ignored attribute name once, truncated, and stops recording at MaxIgnoredAttributes.
func appendIgnored(values []string, value string) []string {
	if len(value) > MaxIgnoredNameLength {
		value = strings.ToValidUTF8(value[:MaxIgnoredNameLength], "")
	}
	if len(values) >= MaxIgnoredAttributes || slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

// apply applies one attribute assignment. The four core identity attributes (userName, externalId, active and id)
// answer 400 invalidPath when they are removed, read only or addressed with a selector; every other attribute the
// server does not store is recorded in Ignored.
func (p *UserPatch) apply(op, rawPath string, value json.RawMessage) *Error {
	parsed, ok := parsePath(rawPath, SchemaUser+":")
	core := map[string]bool{"username": true, "externalid": true, "active": true, "id": true}
	if !ok {
		if core[parsed.attr] {
			return invalidPath("the path of " + parsed.attr + " is not valid")
		}
		p.ignore(strings.ToLower(strings.TrimSpace(rawPath)))
		return nil
	}
	if parsed.extension {
		p.ignore(parsed.attr)
		return nil
	}
	if core[parsed.attr] {
		if parsed.sub != "" || parsed.selector != nil || parsed.attr == "id" || op == "remove" {
			return invalidPath(parsed.attr + " cannot be addressed this way")
		}
		switch parsed.attr {
		case "active":
			active, ok := parseBool(value)
			if !ok {
				return invalidValue("active must be a boolean")
			}
			p.Active = &active
		case "username", "externalid":
			text, ok := parseString(value)
			if text = strings.TrimSpace(text); !ok || text == "" {
				return invalidValue(parsed.attr + " must be a non-empty string")
			}
			if parsed.attr == "username" {
				p.UserName = &text
			} else {
				p.ExternalID = &text
			}
		}
		return nil
	}
	switch parsed.attr {
	case "displayname":
		text := ""
		if op != "remove" && hasValue(value) {
			var ok bool
			if text, ok = parseString(value); !ok {
				return invalidValue("displayName must be a string")
			}
		}
		text = strings.TrimSpace(text)
		p.DisplayName = &text
	case "name":
		return p.applyName(op, parsed, value)
	case "emails":
		return p.applyEmails(op, parsed, value)
	default:
		p.ignore(parsed.display())
	}
	return nil
}

func (p *UserPatch) applyName(op string, parsed path, value json.RawMessage) *Error {
	switch parsed.sub {
	case "formatted":
		text := ""
		if op != "remove" && hasValue(value) {
			var ok bool
			if text, ok = parseString(value); !ok {
				return invalidValue("name.formatted must be a string")
			}
		}
		text = strings.TrimSpace(text)
		p.DisplayName = &text
	case "":
		var name struct {
			Formatted *string `json:"formatted"`
		}
		if op != "remove" && json.Unmarshal(value, &name) == nil && name.Formatted != nil {
			formatted := strings.TrimSpace(*name.Formatted)
			p.DisplayName = &formatted
		} else {
			p.ignore("name")
		}
	default:
		// The account stores one display name; the name parts are not kept.
		p.ignore(parsed.display())
	}
	return nil
}

// emailSelectorApplies reports whether the selector addresses the one email the server stores: no selector, the
// work type or the primary flag.
func emailSelectorApplies(s *selector) bool {
	switch {
	case s == nil:
		return true
	case s.name == "type":
		return strings.EqualFold(s.value, "work")
	case s.name == "primary":
		return strings.EqualFold(s.value, "true")
	}
	return false
}

func (p *UserPatch) applyEmails(op string, parsed path, value json.RawMessage) *Error {
	if !emailSelectorApplies(parsed.selector) || parsed.sub != "" && parsed.sub != "value" {
		p.ignore(parsed.display())
		return nil
	}
	if op == "remove" {
		empty := ""
		p.Email = &empty
		return nil
	}
	if parsed.sub == "value" {
		text, ok := parseString(value)
		if !ok {
			return invalidValue("emails.value must be a string")
		}
		text = strings.TrimSpace(text)
		p.Email = &text
		return nil
	}
	email, ok := PrimaryEmail(value)
	if !ok {
		return invalidValue("emails must be an array of email objects")
	}
	p.Email = &email
	return nil
}

// PrimaryEmail picks the email an emails value carries: the primary one, else the work one, else the first. An
// empty array is the empty email. ok is false when the value is not an array or object of emails.
func PrimaryEmail(raw json.RawMessage) (string, bool) {
	type email struct {
		Value   string          `json:"value"`
		Type    string          `json:"type"`
		Primary json.RawMessage `json:"primary"`
	}
	var list []email
	if err := json.Unmarshal(raw, &list); err != nil {
		var single email
		if json.Unmarshal(raw, &single) != nil {
			return "", false
		}
		list = []email{single}
	}
	best, rank := "", 4
	for _, candidate := range list {
		primary, _ := parseBool(candidate.Primary)
		switch {
		case primary && rank > 1:
			best, rank = candidate.Value, 1
		case strings.EqualFold(candidate.Type, "work") && rank > 2:
			best, rank = candidate.Value, 2
		case rank > 3:
			best, rank = candidate.Value, 3
		}
	}
	return strings.TrimSpace(best), true
}

// NormalizeGroupPatch normalizes a PATCH body of a Group.
func NormalizeGroupPatch(body []byte) (GroupPatch, *Error) {
	operations, failure := decodePatch(body)
	if failure != nil {
		return GroupPatch{}, failure
	}
	var patch GroupPatch
	assigned := 0
	for _, operation := range operations {
		op := strings.ToLower(strings.TrimSpace(operation.Op))
		if op != "add" && op != "replace" && op != "remove" {
			return GroupPatch{}, invalidSyntax("op must be add, replace or remove")
		}
		if strings.TrimSpace(operation.Path) == "" {
			if op == "remove" {
				return GroupPatch{}, NewError(400, TypeNoTarget, "a remove operation needs a path")
			}
			if failure := flatten("", operation.Value, 1, &assigned, func(name string, value json.RawMessage) *Error {
				return patch.apply(op, name, value)
			}); failure != nil {
				return GroupPatch{}, failure
			}
			continue
		}
		if failure := patch.apply(op, operation.Path, operation.Value); failure != nil {
			return GroupPatch{}, failure
		}
	}
	if len(patch.AddMembers) > MaxMembersPerRequest || len(patch.RemoveMembers) > MaxMembersPerRequest ||
		patch.ReplaceMembers != nil && len(*patch.ReplaceMembers) > MaxMembersPerRequest {
		return GroupPatch{}, NewError(400, TypeTooMany, "too many members in one request")
	}
	return patch, nil
}

func (p *GroupPatch) ignore(name string) { p.Ignored = appendIgnored(p.Ignored, name) }

// MemberIDs reads the user ids of a members value: an array (or one object) of {"value": id}.
func MemberIDs(raw json.RawMessage) ([]string, bool) {
	type member struct {
		Value string `json:"value"`
	}
	var list []member
	if err := json.Unmarshal(raw, &list); err != nil {
		var single member
		if json.Unmarshal(raw, &single) != nil {
			return nil, false
		}
		list = []member{single}
	}
	ids := make([]string, 0, len(list))
	for _, entry := range list {
		if id := strings.TrimSpace(entry.Value); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, true
}

func (p *GroupPatch) apply(op, rawPath string, value json.RawMessage) *Error {
	parsed, ok := parsePath(rawPath, SchemaGroup+":")
	core := map[string]bool{"displayname": true, "id": true}
	if !ok {
		if core[parsed.attr] || parsed.attr == "members" {
			return invalidPath("the path of " + parsed.attr + " is not valid")
		}
		p.ignore(strings.ToLower(strings.TrimSpace(rawPath)))
		return nil
	}
	if parsed.extension {
		p.ignore(parsed.attr)
		return nil
	}
	switch parsed.attr {
	case "id":
		return invalidPath("id is read only")
	case "displayname":
		if op == "remove" || parsed.sub != "" || parsed.selector != nil {
			return invalidPath("displayName cannot be addressed this way")
		}
		text, ok := parseString(value)
		if text = strings.TrimSpace(text); !ok || text == "" {
			return invalidValue("displayName must be a non-empty string")
		}
		p.DisplayName = &text
	case "externalid":
		text := ""
		if op != "remove" && hasValue(value) {
			var ok bool
			if text, ok = parseString(value); !ok {
				return invalidValue("externalId must be a string")
			}
		}
		text = strings.TrimSpace(text)
		p.ExternalID = &text
	case "members":
		return p.applyMembers(op, parsed, value)
	default:
		p.ignore(parsed.display())
	}
	return nil
}

func (p *GroupPatch) applyMembers(op string, parsed path, value json.RawMessage) *Error {
	if parsed.sub != "" {
		p.ignore(parsed.display())
		return nil
	}
	if parsed.selector != nil {
		if parsed.selector.name != "value" {
			p.ignore(parsed.display())
			return nil
		}
		switch op {
		case "remove":
			p.removeMember(parsed.selector.value)
		default:
			p.addMember(parsed.selector.value)
		}
		return nil
	}
	if !hasValue(value) {
		if op == "remove" {
			// Removing members without naming any removes every member.
			p.replaceMembers(nil)
			return nil
		}
		return invalidValue("members needs a value")
	}
	members, ok := MemberIDs(value)
	if !ok {
		return invalidValue("members must be an array of {\"value\": id}")
	}
	switch op {
	case "add":
		for _, id := range members {
			p.addMember(id)
		}
	case "remove":
		for _, id := range members {
			p.removeMember(id)
		}
	default:
		p.replaceMembers(members)
	}
	return nil
}

// The member operations of one request are folded in order into a net effect, so [remove X, add X] ends with X a
// member and [add X, remove X] ends without it, whatever order the store applies the three sets in. After a replace
// every later operation edits the replacement set itself.

func (p *GroupPatch) addMember(id string) {
	if p.ReplaceMembers != nil {
		if !slices.Contains(*p.ReplaceMembers, id) {
			*p.ReplaceMembers = append(*p.ReplaceMembers, id)
		}
		return
	}
	p.RemoveMembers = slices.DeleteFunc(p.RemoveMembers, func(existing string) bool { return existing == id })
	if !slices.Contains(p.AddMembers, id) {
		p.AddMembers = append(p.AddMembers, id)
	}
}

func (p *GroupPatch) removeMember(id string) {
	if p.ReplaceMembers != nil {
		*p.ReplaceMembers = slices.DeleteFunc(*p.ReplaceMembers, func(existing string) bool { return existing == id })
		return
	}
	p.AddMembers = slices.DeleteFunc(p.AddMembers, func(existing string) bool { return existing == id })
	if !slices.Contains(p.RemoveMembers, id) {
		p.RemoveMembers = append(p.RemoveMembers, id)
	}
}

func (p *GroupPatch) replaceMembers(ids []string) {
	replacement := make([]string, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(replacement, id) {
			replacement = append(replacement, id)
		}
	}
	p.ReplaceMembers, p.AddMembers, p.RemoveMembers = &replacement, nil, nil
}
