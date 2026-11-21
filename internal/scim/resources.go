package scim

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// UserInput is a User as a client writes it (POST and PUT). Attributes the server does not store are dropped.
type UserInput struct {
	UserName string
	// ExternalID is nil when the client sent none.
	ExternalID *string
	// DisplayName is the displayName, else name.formatted, else the given and family names joined.
	DisplayName string
	Email       string
	// Active is nil when the client sent none (a create then means active).
	Active *bool
}

type nameInput struct {
	Formatted  string `json:"formatted"`
	GivenName  string `json:"givenName,omitempty"`
	FamilyName string `json:"familyName,omitempty"`
}

// ParseUser reads the body of a User create or replace.
func ParseUser(body []byte) (UserInput, *Error) {
	var raw struct {
		UserName    string          `json:"userName"`
		ExternalID  json.RawMessage `json:"externalId"`
		DisplayName string          `json:"displayName"`
		Name        *nameInput      `json:"name"`
		Emails      json.RawMessage `json:"emails"`
		Active      json.RawMessage `json:"active"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&raw); err != nil {
		return UserInput{}, invalidSyntax("the request body is not a valid User")
	}
	input := UserInput{UserName: strings.TrimSpace(raw.UserName), DisplayName: strings.TrimSpace(raw.DisplayName)}
	if input.UserName == "" {
		return UserInput{}, invalidValue("userName is required")
	}
	if hasValue(raw.ExternalID) {
		text, ok := parseString(raw.ExternalID)
		if !ok {
			return UserInput{}, invalidValue("externalId must be a string")
		}
		if text = strings.TrimSpace(text); text != "" {
			input.ExternalID = &text
		}
	}
	if input.DisplayName == "" && raw.Name != nil {
		input.DisplayName = strings.TrimSpace(raw.Name.Formatted)
		if input.DisplayName == "" {
			input.DisplayName = strings.TrimSpace(strings.TrimSpace(raw.Name.GivenName) + " " + strings.TrimSpace(raw.Name.FamilyName))
		}
	}
	if hasValue(raw.Emails) {
		email, ok := PrimaryEmail(raw.Emails)
		if !ok {
			return UserInput{}, invalidValue("emails must be an array of email objects")
		}
		input.Email = email
	}
	if hasValue(raw.Active) {
		active, ok := parseBool(raw.Active)
		if !ok {
			return UserInput{}, invalidValue("active must be a boolean")
		}
		input.Active = &active
	}
	return input, nil
}

// GroupInput is a Group as a client writes it.
type GroupInput struct {
	DisplayName string
	// ExternalID is nil when the body carried none, so a replace leaves the stored one alone.
	ExternalID *string
	MemberIDs  []string
	// HasMembers reports that the body names members, even an empty list: an absent attribute leaves the
	// memberships of a group alone on replace, a present one replaces them.
	HasMembers bool
}

// ParseGroup reads the body of a Group create or replace.
func ParseGroup(body []byte) (GroupInput, *Error) {
	var raw struct {
		DisplayName string          `json:"displayName"`
		ExternalID  json.RawMessage `json:"externalId"`
		Members     json.RawMessage `json:"members"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&raw); err != nil {
		return GroupInput{}, invalidSyntax("the request body is not a valid Group")
	}
	input := GroupInput{DisplayName: strings.TrimSpace(raw.DisplayName)}
	if input.DisplayName == "" {
		return GroupInput{}, invalidValue("displayName is required")
	}
	if hasValue(raw.ExternalID) {
		text, ok := parseString(raw.ExternalID)
		if !ok {
			return GroupInput{}, invalidValue("externalId must be a string")
		}
		text = strings.TrimSpace(text)
		input.ExternalID = &text
	}
	if hasValue(raw.Members) {
		members, ok := MemberIDs(raw.Members)
		if !ok {
			return GroupInput{}, invalidValue("members must be an array of {\"value\": id}")
		}
		if len(members) > MaxMembersPerRequest {
			return GroupInput{}, NewError(400, TypeTooMany, "too many members in one request")
		}
		input.MemberIDs, input.HasMembers = members, true
	}
	return input, nil
}

// Meta is the meta attribute of a resource. Version is the weak ETag.
type Meta struct {
	ResourceType string    `json:"resourceType"`
	Created      time.Time `json:"created"`
	LastModified time.Time `json:"lastModified"`
	Location     string    `json:"location"`
	Version      string    `json:"version"`
}

// ETag is the weak entity tag of a resource version.
func ETag(version string) string { return "W/\"" + version + "\"" }

// UserDocument is a User as the server returns it.
type UserDocument struct {
	ID          string
	ExternalID  string
	UserName    string
	DisplayName string
	Email       string
	Active      bool
	Meta        Meta
}

type userEmail struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary"`
	Type    string `json:"type"`
}

// MarshalJSON renders the User resource.
func (u UserDocument) MarshalJSON() ([]byte, error) {
	body := map[string]any{"schemas": []string{SchemaUser}, "id": u.ID, "userName": u.UserName, "active": u.Active, "meta": u.Meta}
	if u.ExternalID != "" {
		body["externalId"] = u.ExternalID
	}
	if u.DisplayName != "" {
		body["displayName"] = u.DisplayName
		body["name"] = nameInput{Formatted: u.DisplayName}
	}
	if u.Email != "" {
		body["emails"] = []userEmail{{Value: u.Email, Primary: true, Type: "work"}}
	}
	return json.Marshal(body)
}

// GroupMember is one member of a Group resource.
type GroupMember struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Ref     string `json:"$ref,omitempty"`
	Type    string `json:"type"`
}

// GroupDocument is a Group as the server returns it. Members is nil when the client excluded them.
type GroupDocument struct {
	ID          string
	ExternalID  string
	DisplayName string
	Members     []GroupMember
	WithMembers bool
	Meta        Meta
}

// MarshalJSON renders the Group resource.
func (g GroupDocument) MarshalJSON() ([]byte, error) {
	body := map[string]any{"schemas": []string{SchemaGroup}, "id": g.ID, "displayName": g.DisplayName, "meta": g.Meta}
	if g.ExternalID != "" {
		body["externalId"] = g.ExternalID
	}
	if g.WithMembers {
		members := g.Members
		if members == nil {
			members = []GroupMember{}
		}
		body["members"] = members
	}
	return json.Marshal(body)
}

// ListResponse is the envelope of a list. TotalResults is exact below the bound and an estimate above it.
type ListResponse struct {
	TotalResults int64
	StartIndex   int
	Resources    []any
	// NextCursor is the RFC 9865 continuation token; empty on the last page.
	NextCursor string
}

// MarshalJSON renders the ListResponse.
func (l ListResponse) MarshalJSON() ([]byte, error) {
	resources := l.Resources
	if resources == nil {
		resources = []any{}
	}
	body := map[string]any{
		"schemas": []string{SchemaListResponse}, "totalResults": l.TotalResults, "startIndex": l.StartIndex,
		"itemsPerPage": len(resources), "Resources": resources,
	}
	if l.NextCursor != "" {
		body["nextCursor"] = l.NextCursor
	}
	return json.Marshal(body)
}
