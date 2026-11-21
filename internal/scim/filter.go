package scim

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Resource names the resource a filter applies to.
type Resource int

// The resources the server filters.
const (
	Users Resource = iota
	Groups
)

// MaxFilterLength bounds a filter: the supported grammar is one short comparison.
const MaxFilterLength = 1024

// Filter is the one comparison the server supports: attribute eq "value". Attribute is the canonical attribute
// name (userName, externalId or displayName). The absent filter is the zero value.
type Filter struct {
	Attribute string
	Value     string
}

// IsZero reports that no filter was given.
func (f Filter) IsZero() bool { return f.Attribute == "" }

var filterAttributes = map[Resource]map[string]string{
	Users:  {"username": "userName", "externalid": "externalId"},
	Groups: {"displayname": "displayName", "externalid": "externalId"},
}

var coreURNs = map[Resource]string{Users: SchemaUser + ":", Groups: SchemaGroup + ":"}

func invalidFilter(detail string) *Error { return NewError(400, TypeInvalidFilter, detail) }

// ParseFilter parses the filter query parameter. Only "attr eq "value"" on userName and externalId (Users) or
// displayName and externalId (Groups) is accepted, combined with nothing else; the attribute name and the
// operator are case-insensitive and the attribute may carry its core schema URN, as Entra ID sends it. Anything
// else is 400 invalidFilter. An empty filter is no filter.
func ParseFilter(raw string, resource Resource) (Filter, *Error) {
	if strings.TrimSpace(raw) == "" {
		return Filter{}, nil
	}
	if len(raw) > MaxFilterLength || !utf8.ValidString(raw) {
		return Filter{}, invalidFilter("the filter is too long or not valid UTF-8")
	}
	rest := strings.TrimLeft(raw, " ")
	end := strings.IndexByte(rest, ' ')
	if end <= 0 {
		return Filter{}, invalidFilter("only attr eq \"value\" filters are supported")
	}
	name := rest[:end]
	if len(name) >= len(coreURNs[resource]) && strings.EqualFold(name[:len(coreURNs[resource])], coreURNs[resource]) {
		name = name[len(coreURNs[resource]):]
	}
	attribute, supported := filterAttributes[resource][strings.ToLower(name)]
	rest = strings.TrimLeft(rest[end:], " ")
	operator, after, found := strings.Cut(rest, " ")
	if !found || !strings.EqualFold(operator, "eq") {
		return Filter{}, invalidFilter("only the eq operator is supported")
	}
	if !supported {
		return Filter{}, invalidFilter("the filter attribute is not supported")
	}
	literal := strings.TrimSpace(after)
	if len(literal) < 2 || literal[0] != '"' || literal[len(literal)-1] != '"' {
		return Filter{}, invalidFilter("the filter value must be a quoted string")
	}
	var value string
	if err := json.Unmarshal([]byte(literal), &value); err != nil || value == "" {
		return Filter{}, invalidFilter("the filter value is not a valid string")
	}
	return Filter{Attribute: attribute, Value: value}, nil
}
