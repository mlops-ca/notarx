package authorization

import "github.com/mlops-ca/notarx/internal/durableactor"

// DurablePrincipal rebuilds authority from a transaction-locked current
// subject snapshot. It never restores the submitter's admin/OIDC claims or
// persists a bearer. Callers must supply a fresh snapshot at every execution.
//
// The snapshot invariant is enforced here, not only by the caller: the kind must be a durable actor kind and the
// actor must have the shape that kind produces (a user, API key or service account prefix followed by the id), so a
// snapshot of one kind can never be replayed as another.
func (a Authenticator) DurablePrincipal(actor string, kind AuthenticationKind, role string, active, mustChangePassword bool) (Principal, bool) {
	if actor == "" || !active || !durableactor.ActorShapeValid(string(kind), actor) {
		return Principal{}, false
	}
	p := Principal{Actor: actor, AuthenticationKind: kind, RepositoryPatterns: a.RepositoryReaders[actor], MustChangePassword: mustChangePassword}
	switch kind {
	case AuthenticationStaticAdmin:
		if a.AdminToken == "" || a.AdminActor != actor {
			return Principal{}, false
		}
		p.Admin = true
		p.Role = RoleAdmin
	case AuthenticationStaticResolver:
		if a.ResolverToken == "" || a.ResolverActor == "" || a.ResolverActor != actor {
			return Principal{}, false
		}
	case AuthenticationLocalSession, AuthenticationOIDC, AuthenticationAPIKey:
		p.Role = Role(role)
		// Standalone keys can rely entirely on repository grants. Their current
		// credential snapshot still has to be active; persisted users require
		// an approved Gateway role as well as their account-state checks.
		if !validGatewayRole(p.Role) && (kind != AuthenticationAPIKey || p.Role != "") {
			return Principal{}, false
		}
		p.Admin = p.Role == RoleAdmin
	case AuthenticationServiceAccountCredential:
	case AuthenticationWorkloadFederation:
		// The actor is the service account and holds no role; the store revalidated the policy, issuer and
		// account in the same snapshot. A deployment without workload federation refuses the kind.
		if a.Workload == nil {
			return Principal{}, false
		}
	default:
		return Principal{}, false
	}
	return p, p.AccountStateReason() == ""
}
