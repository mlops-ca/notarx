// Package durableactor is the single definition of the actor kinds a durable replication job may carry
// (ADR 0019, "Authentication flow and integration") and of the class that selects the authority check for each.
// The Helm, NuGet and RubyGems job constructors, their actor validators and the stores' authority switches all
// take their accepted kinds from here, so adding a kind is one edit plus the class it maps to. It has no
// dependencies on the stores, so the distribution packages can import it.
package durableactor

import (
	"slices"
	"sort"
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

// The actor prefixes of the kinds that name a user, an API key or a service account.
const (
	UserActorPrefix           = "user:"
	APIKeyActorPrefix         = "api-key:"
	ServiceAccountActorPrefix = "service-account:"
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

var classes = map[string]Class{
	StaticAdmin:              ClassStatic,
	StaticResolver:           ClassStatic,
	LocalSession:             ClassUser,
	OIDC:                     ClassUser,
	APIKey:                   ClassAPIKey,
	ServiceAccountCredential: ClassServiceAccountCredential,
	WorkloadFederation:       ClassWorkload,
}

var (
	kinds        = sortedKinds()
	classesInUse = sortedClasses()
)

func sortedKinds() []string {
	out := make([]string, 0, len(classes))
	for kind := range classes {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

func sortedClasses() []Class {
	var out []Class
	for _, c := range classes {
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return out
}

// Kinds returns every accepted actor kind, sorted.
func Kinds() []string { return slices.Clone(kinds) }

// Classes lists every class a store authority switch has to handle, in ascending order.
func Classes() []Class { return slices.Clone(classesInUse) }

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
		return canonicalUUID(credential)
	}
	return credential == ""
}

// ActorShapeValid reports whether actor has the form the kind produces: the user prefix and a name, the API key or service
// account prefix followed by a UUID, or any text without a NUL for the static kinds.
func ActorShapeValid(kind, actor string) bool {
	switch ClassOf(kind) {
	case ClassStatic:
		return !strings.Contains(actor, "\x00")
	case ClassUser:
		name, ok := strings.CutPrefix(actor, UserActorPrefix)
		return ok && name != ""
	case ClassAPIKey:
		return prefixedUUID(actor, APIKeyActorPrefix)
	case ClassServiceAccountCredential, ClassWorkload:
		return prefixedUUID(actor, ServiceAccountActorPrefix)
	}
	return false
}

// ActorValid reports whether a job's actor and credential id are both shaped as its kind requires.
func ActorValid(kind, actor, credential string) bool {
	return ActorShapeValid(kind, actor) && ValidCredential(kind, credential)
}

// ServiceAccountID returns the service account id of a service-account or workload actor.
func ServiceAccountID(actor string) (string, bool) {
	id, ok := strings.CutPrefix(actor, ServiceAccountActorPrefix)
	if !ok || !canonicalUUID(id) {
		return "", false
	}
	return id, true
}

func prefixedUUID(actor, prefix string) bool {
	id, ok := strings.CutPrefix(actor, prefix)
	return ok && canonicalUUID(id)
}

// canonicalUUID reports whether s is a UUID in its canonical lower-case hyphenated form. uuid.Parse alone also
// accepts braced, urn and hyphenless spellings that PostgreSQL resolves to the same row but the memory store's map
// lookups and the job id derived from the actor string would treat as different subjects.
func canonicalUUID(s string) bool {
	u, err := uuid.Parse(s)
	return err == nil && u.String() == s
}
