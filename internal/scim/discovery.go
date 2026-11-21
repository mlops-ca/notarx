package scim

import "time"

// Discovery documents (RFC 7643 section 5 to 7). They describe exactly what the server implements: PATCH, filter
// with eq on one attribute, ETags, index and cursor pagination, and the excludedAttributes=members parameter of
// GET /Groups. Bulk, sort and change password are not supported.

// DefaultPageSize is the count of a list request that names none.
const DefaultPageSize = 50

// Capabilities are the limits ServiceProviderConfig advertises.
type Capabilities struct {
	// MaxResults is the cap of count (NOTARX_SCIM_MAX_PAGE).
	MaxResults int
}

// ServiceProviderConfig returns the ServiceProviderConfig document. base is the absolute SCIM base URL.
func ServiceProviderConfig(c Capabilities, base string, created time.Time) map[string]any {
	return map[string]any{
		"schemas":        []string{SchemaServiceConfig},
		"patch":          map[string]any{"supported": true},
		"bulk":           map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":         map[string]any{"supported": true, "maxResults": c.MaxResults},
		"changePassword": map[string]any{"supported": false},
		"sort":           map[string]any{"supported": false},
		"etag":           map[string]any{"supported": true},
		"pagination": map[string]any{
			"cursor": true, "index": true, "defaultPaginationMethod": "index",
			"defaultPageSize": DefaultPageSize, "maxPageSize": c.MaxResults,
		},
		"excludedAttributes": map[string]any{"supported": true, "attributes": []string{"members"}},
		"authenticationSchemes": []map[string]any{{
			"type": "oauthbearertoken", "name": "SCIM bearer token", "primary": true,
			"description": "A per-provider SCIM token created in the notarx console or API, sent as Authorization: Bearer.",
			"specUri":     "https://www.rfc-editor.org/info/rfc6750",
		}},
		"meta": map[string]any{"resourceType": "ServiceProviderConfig", "location": base + "/ServiceProviderConfig", "created": created, "lastModified": created},
	}
}

// ResourceTypes returns the ResourceType documents.
func ResourceTypes(base string) []map[string]any {
	meta := func(name string) map[string]any {
		return map[string]any{"resourceType": "ResourceType", "location": base + "/ResourceTypes/" + name}
	}
	return []map[string]any{
		{"schemas": []string{SchemaResourceType}, "id": "User", "name": "User", "endpoint": "/Users", "description": "User account", "schema": SchemaUser, "meta": meta("User")},
		{"schemas": []string{SchemaResourceType}, "id": "Group", "name": "Group", "endpoint": "/Groups", "description": "Group of user accounts", "schema": SchemaGroup, "meta": meta("Group")},
	}
}

func attribute(name, kind, description string, required bool, uniqueness, mutability string, sub ...map[string]any) map[string]any {
	definition := map[string]any{
		"name": name, "type": kind, "multiValued": false, "description": description, "required": required,
		"caseExact": false, "mutability": mutability, "returned": "default", "uniqueness": uniqueness,
	}
	if len(sub) > 0 {
		definition["subAttributes"] = sub
	}
	return definition
}

// Schemas returns the Schema documents of the attributes the server stores.
func Schemas(base string) []map[string]any {
	meta := func(id string) map[string]any {
		return map[string]any{"resourceType": "Schema", "location": base + "/Schemas/" + id}
	}
	emails := attribute("emails", "complex", "The primary email of the account; the server stores one address.", false, "none", "readWrite",
		attribute("value", "string", "The email address.", false, "none", "readWrite"),
		attribute("type", "string", "The label; only work is stored.", false, "none", "readWrite"),
		attribute("primary", "boolean", "Always true for the stored address.", false, "none", "readWrite"))
	emails["multiValued"] = true
	members := attribute("members", "complex", "The user accounts of the provider in this group.", false, "none", "readWrite",
		attribute("value", "string", "The id of the User.", false, "none", "immutable"),
		attribute("display", "string", "The userName of the member.", false, "none", "immutable"),
		attribute("type", "string", "Always User.", false, "none", "immutable"))
	members["multiValued"] = true
	return []map[string]any{
		{"schemas": []string{SchemaSchemaResource}, "id": SchemaUser, "name": "User", "description": "User account provisioned by an identity provider", "attributes": []map[string]any{
			attribute("userName", "string", "The account name; unique case-insensitively across the instance. It is set at creation and cannot be changed through SCIM.", true, "server", "immutable"),
			attribute("externalId", "string", "The identifier of the user at the identity provider.", false, "none", "readWrite"),
			attribute("displayName", "string", "The name shown for the user.", false, "none", "readWrite"),
			attribute("name", "complex", "Only the formatted name is stored, as the display name.", false, "none", "readWrite",
				attribute("formatted", "string", "The full name.", false, "none", "readWrite")),
			emails,
			attribute("active", "boolean", "False disables the account and revokes its sessions.", false, "none", "readWrite"),
		}, "meta": meta(SchemaUser)},
		{"schemas": []string{SchemaSchemaResource}, "id": SchemaGroup, "name": "Group", "description": "Group of users provisioned by an identity provider", "attributes": []map[string]any{
			attribute("displayName", "string", "The name of the group.", true, "none", "readWrite"),
			attribute("externalId", "string", "The identifier of the group at the identity provider.", false, "none", "readWrite"),
			members,
		}, "meta": meta(SchemaGroup)},
	}
}
