package saml

import (
	"slices"
	"strings"

	"github.com/russellhaering/gosaml2/types"

	"github.com/mlops-ca/notarx/internal/identity"
)

// Attribute names the defaults try, in order, per field. They cover Entra ID, Okta and Keycloak
// (ADR 0018 section 3); a mapping on the provider replaces the list for its field.
var (
	defaultUsername    = []string{"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/upn", "username", "login", "uid", "urn:oid:0.9.2342.19200300.100.1.1"}
	defaultEmail       = []string{"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress", "email", "mail", "urn:oid:0.9.2342.19200300.100.1.3"}
	defaultDisplayName = []string{"http://schemas.microsoft.com/identity/claims/displayname", "displayName", "cn", "urn:oid:2.16.840.1.113730.3.1.241"}
	defaultGroups      = []string{"http://schemas.microsoft.com/ws/2008/06/identity/claims/groups", "groups", "memberOf", "Role"}
)

func (v *verifier) nameID(subject *types.Subject) (value, format string, err error) {
	if subject.NameID == nil {
		return "", "", reject(ReasonNameID)
	}
	value = strings.TrimSpace(subject.NameID.Value)
	format = strings.TrimSpace(subject.NameID.Format)
	if format == nameIDURITransient {
		return "", "", reject(ReasonTransientNameID)
	}
	if value == "" || len(value) > maxNameIDLength {
		return "", "", reject(ReasonNameID)
	}
	if format == nameIDURIEmail || (format == "" && v.config.NameIDFormat == NameIDEmailAddress) {
		value = strings.ToLower(value)
	}
	return value, format, nil
}

func collectAttributes(claims *Claims, statement *types.AttributeStatement) {
	if statement == nil {
		return
	}
	for _, attribute := range statement.Attributes {
		var values []string
		for _, value := range attribute.Values {
			if text := strings.TrimSpace(value.Value); text != "" {
				values = append(values, text)
			}
		}
		if len(values) == 0 {
			continue
		}
		for _, name := range []string{attribute.Name, attribute.FriendlyName} {
			if name != "" && claims.Attributes[name] == nil {
				claims.Attributes[name] = values
			}
		}
	}
}

func (c *Claims) first(names []string) string {
	for _, name := range names {
		if values := c.Attributes[name]; len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func (c *Claims) all(names []string) []string {
	for _, name := range names {
		if values := c.Attributes[name]; len(values) > 0 {
			return slices.Clone(values)
		}
	}
	return nil
}

func candidates(configured string, defaults []string) []string {
	if configured != "" {
		return []string{configured}
	}
	return defaults
}

func applyMapping(claims *Claims, mapping identity.AttributeMapping) {
	claims.Email = strings.ToLower(claims.first(candidates(mapping.Email, defaultEmail)))
	claims.Username = claims.first(candidates(mapping.Username, defaultUsername))
	claims.DisplayName = claims.first(candidates(mapping.DisplayName, defaultDisplayName))
	claims.Groups = identity.NormalizeStrings(claims.all(candidates(mapping.Groups, defaultGroups)))
	if mapping.ExternalID != "" {
		claims.ExternalID = claims.first([]string{mapping.ExternalID})
	}
}
