package scim

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Schema URNs of the messages and resources this server speaks.
const (
	SchemaError          = "urn:ietf:params:scim:api:messages:2.0:Error"
	SchemaListResponse   = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	SchemaPatchOp        = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	SchemaUser           = "urn:ietf:params:scim:schemas:core:2.0:User"
	SchemaGroup          = "urn:ietf:params:scim:schemas:core:2.0:Group"
	SchemaServiceConfig  = "urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"
	SchemaResourceType   = "urn:ietf:params:scim:schemas:core:2.0:ResourceType"
	SchemaSchemaResource = "urn:ietf:params:scim:schemas:core:2.0:Schema"
)

// ContentType is the media type of every response; the server also accepts application/json.
const ContentType = "application/scim+json"

// Type is the scimType of an error (RFC 7644 section 3.12).
type Type string

// The scimType values the server uses.
const (
	TypeInvalidFilter Type = "invalidFilter"
	TypeTooMany       Type = "tooMany"
	TypeUniqueness    Type = "uniqueness"
	TypeMutability    Type = "mutability"
	TypeInvalidSyntax Type = "invalidSyntax"
	TypeInvalidPath   Type = "invalidPath"
	TypeNoTarget      Type = "noTarget"
	TypeInvalidValue  Type = "invalidValue"
	TypeInvalidVers   Type = "invalidVers"
)

// Error is an RFC 7644 error: an HTTP status, an optional scimType and a detail that never carries a secret.
type Error struct {
	Status int
	Type   Type
	Detail string
}

func (e *Error) Error() string {
	return strconv.Itoa(e.Status) + " " + string(e.Type) + ": " + e.Detail
}

// NewError builds an error.
func NewError(status int, scimType Type, detail string) *Error {
	return &Error{Status: status, Type: scimType, Detail: detail}
}

// MarshalJSON renders the error envelope; the status is a string, as RFC 7644 requires.
func (e *Error) MarshalJSON() ([]byte, error) {
	body := map[string]any{"schemas": []string{SchemaError}, "status": strconv.Itoa(e.Status), "detail": e.Detail}
	if e.Type != "" {
		body["scimType"] = string(e.Type)
	}
	return json.Marshal(body)
}

// Write sends the error.
func (e *Error) Write(w http.ResponseWriter) {
	body, _ := e.MarshalJSON()
	w.Header().Set("Content-Type", ContentType)
	w.WriteHeader(e.Status)
	_, _ = w.Write(body)
}
