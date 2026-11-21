// Package durableactor is the single definition of the actor kinds a durable replication job may carry
// (ADR 0019, "Authentication flow and integration") and of the class that selects the authority check for each.
// The Helm, NuGet and RubyGems job constructors, their actor validators and the stores' authority switches all
// take their accepted kinds from here, so adding a kind is one edit plus the class it maps to. It has no
// dependencies on the stores, so the distribution packages can import it.
package durableactor

import (
	"strings"

	"github.com/google/uuid"
)

// The durable actor kinds, spelled as the actor_kind column and Authentication.Kind carry them.
const (
	StaticAdmin              = "static_admin"
	StaticResolver           = "static_resolver"
	LocalSession             = "local_session"
	OIDC                     = "oidc"
	APIKey                   = "api_key"
	ServiceAccountCredential = "service_account_credential"
	WorkloadFederation       = "workload_federation"
)

// Class selects the authority check a store runs for a durable actor.
type Class int

const (
	// ClassNone is a kind no durable job may carry.
	ClassNone Class = iota
	// ClassStatic is the configured admin or resolver token; its authority is the configuration.
	ClassStatic
	// ClassUser is a persisted user (local session or OIDC login).
	ClassUser
	// ClassAPIKey is a standalone API key.
	ClassAPIKey
	// ClassServiceAccountCredential is a service-account API key.
	ClassServiceAccountCredential
	// ClassWorkload is a workload_federation principal: the service account plus the trust policy id.
	ClassWorkload
)

// Classes lists every class a store authority switch has to handle.
func Classes() []Class {
	return []Class{ClassStatic, ClassUser, ClassAPIKey, ClassServiceAccountCredential, ClassWorkload}
}

var classes = map[string]Class{
	StaticAdmin:              ClassStatic,
	StaticResolver:           ClassStatic,
	LocalSession:             ClassUser,
	OIDC:                     ClassUser,
	APIKey:                   ClassAPIKey,
	ServiceAccountCredential: ClassServiceAccountCredential,
	WorkloadFederation:       ClassWorkload,
}

// Kinds returns every accepted actor kind.
func Kinds() []string {
	return []string{StaticAdmin, StaticResolver, LocalSession, OIDC, APIKey, ServiceAccountCredential, WorkloadFederation}
}

// ClassOf maps an actor kind to its authority class; unknown kinds are ClassNone.
func ClassOf(kind string) Class { return classes[kind] }

// Valid reports whether kind is an accepted durable actor kind.
func Valid(kind string) bool { return ClassOf(kind) != ClassNone }

// CredentialRequired reports whether jobs of this kind carry a credential id: the API key of a service
// account or the trust policy of a workload principal.
func CredentialRequired(kind string) bool {
	c := ClassOf(kind)
	return c == ClassServiceAccountCredential || c == ClassWorkload
}

// ValidCredential reports whether credential is shaped as the kind requires: a UUID when it is required and
// empty otherwise. An unknown kind is invalid.
func ValidCredential(kind, credential string) bool {
	if !Valid(kind) {
		return false
	}
	if CredentialRequired(kind) {
		_, err := uuid.Parse(credential)
		return err == nil
	}
	return credential == ""
}

// ActorShapeValid reports whether actor has the form the kind produces: the user, API key or service account
// prefix, or any text without a NUL for the static kinds.
func ActorShapeValid(kind, actor string) bool {
	switch ClassOf(kind) {
	case ClassStatic:
		return !strings.Contains(actor, "\x00")
	case ClassUser:
		return strings.HasPrefix(actor, "user:")
	case ClassAPIKey:
		return strings.HasPrefix(actor, "api-key:")
	case ClassServiceAccountCredential, ClassWorkload:
		return strings.HasPrefix(actor, "service-account:")
	}
	return false
}
