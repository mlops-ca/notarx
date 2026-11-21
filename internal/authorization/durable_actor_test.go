package authorization

import (
	"testing"

	"github.com/mlops-ca/notarx/internal/durableactor"
)

func TestDurablePrincipalWorkloadFederation(t *testing.T) {
	enabled := Authenticator{Workload: &WorkloadAuthenticator{}}
	for _, tt := range []struct {
		name, actor string
		auth        Authenticator
		active      bool
		want        bool
	}{
		{"active", "service-account:11111111-1111-4111-8111-111111111111", enabled, true, true},
		{"policy, issuer or account ended", "service-account:11111111-1111-4111-8111-111111111111", enabled, false, false},
		{"federation disabled", "service-account:11111111-1111-4111-8111-111111111111", Authenticator{}, true, false},
		{"not a service account", "user:operator", enabled, true, false},
		{"service account id is not a UUID", "service-account:account", enabled, true, false},
		{"API key actor", "api-key:11111111-1111-4111-8111-111111111111", enabled, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := tt.auth.DurablePrincipal(tt.actor, AuthenticationWorkloadFederation, "policy-id", "", tt.active, false)
			if ok != tt.want || ok && (p.Admin || p.Role != "" || p.AuthenticationKind != AuthenticationWorkloadFederation || p.CredentialID != "policy-id") {
				t.Fatalf("principal=%+v allowed=%t", p, ok)
			}
		})
	}
}

func TestDurablePrincipalUsesCurrentIdentityAndAuthority(t *testing.T) {
	auth := Authenticator{AdminToken: "current-admin-secret", AdminActor: "current-admin", ResolverToken: "current-reader-secret", ResolverActor: "current-reader"}
	for _, tt := range []struct {
		name, actor, role             string
		kind                          AuthenticationKind
		active, password, want, admin bool
	}{
		{"admin current", "current-admin", "", AuthenticationStaticAdmin, true, false, true, true},
		{"admin replaced", "old-admin", "admin", AuthenticationStaticAdmin, true, false, false, false},
		{"resolver current", "current-reader", "admin", AuthenticationStaticResolver, true, false, true, false},
		{"resolver replaced", "old-reader", "admin", AuthenticationStaticResolver, true, false, false, false},
		{"user demoted", "user:operator", "member", AuthenticationLocalSession, true, false, true, false},
		{"user disabled", "user:operator", "admin", AuthenticationLocalSession, false, false, false, false},
		{"user password reset", "user:operator", "admin", AuthenticationLocalSession, true, true, false, false},
		{"pending user", "user:operator", "none", AuthenticationLocalSession, true, false, false, false},
		{"user missing role", "user:operator", "", AuthenticationLocalSession, true, false, false, false},
		{"OIDC missing role", "user:operator", "", AuthenticationOIDC, true, false, false, false},
		{"OIDC pending", "user:operator", "none", AuthenticationOIDC, true, false, false, false},
		{"OIDC disabled", "user:operator", "member", AuthenticationOIDC, false, false, false, false},
		{"repository-only key", "api-key:11111111-1111-4111-8111-111111111111", "", AuthenticationAPIKey, true, false, true, false},
		{"key member", "api-key:11111111-1111-4111-8111-111111111111", "member", AuthenticationAPIKey, true, false, true, false},
		{"key admin", "api-key:11111111-1111-4111-8111-111111111111", "admin", AuthenticationAPIKey, true, false, true, true},
		{"key unknown role", "api-key:11111111-1111-4111-8111-111111111111", "unknown", AuthenticationAPIKey, true, false, false, false},
		{"key pending", "api-key:11111111-1111-4111-8111-111111111111", "none", AuthenticationAPIKey, true, false, false, false},
		{"repository-only key missing or revoked", "api-key:11111111-1111-4111-8111-111111111111", "", AuthenticationAPIKey, false, false, false, false},
		{"repository-only key password reset", "api-key:11111111-1111-4111-8111-111111111111", "", AuthenticationAPIKey, true, true, false, false},
		{"key revoked", "api-key:11111111-1111-4111-8111-111111111111", "admin", AuthenticationAPIKey, false, false, false, false},
		{"service disabled", "service-account:11111111-1111-4111-8111-111111111111", "", AuthenticationServiceAccountCredential, false, false, false, false},
		{"external unknown", "external-subject", "admin", AuthenticationKind("unknown"), true, false, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := auth.DurablePrincipal(tt.actor, tt.kind, "", tt.role, tt.active, tt.password)
			if ok != tt.want || ok && p.Admin != tt.admin {
				t.Fatalf("principal=%+v allowed=%t", p, ok)
			}
		})
	}
	for _, empty := range []Authenticator{{ResolverToken: "configured"}, {ResolverActor: "current-reader"}, {AdminToken: "configured"}, {AdminActor: "current-admin"}} {
		if _, ok := empty.DurablePrincipal("current-reader", AuthenticationStaticResolver, "", "", true, false); ok {
			t.Fatal("incomplete resolver identity accepted")
		}
		if _, ok := empty.DurablePrincipal("current-admin", AuthenticationStaticAdmin, "", "admin", true, false); ok {
			t.Fatal("incomplete administrator identity accepted")
		}
	}
}

// Every kind a durable job may carry must rebuild a principal here, and a kind the job layer does not accept must not.
func TestDurablePrincipalAcceptsExactlyTheDurableActorKinds(t *testing.T) {
	auth := Authenticator{AdminToken: "admin-secret", AdminActor: "admin", ResolverToken: "reader-secret", ResolverActor: "reader", Workload: &WorkloadAuthenticator{}}
	actors := map[durableactor.Class]string{
		durableactor.ClassUser:                     "user:operator",
		durableactor.ClassAPIKey:                   "api-key:11111111-1111-4111-8111-111111111111",
		durableactor.ClassServiceAccountCredential: "service-account:11111111-1111-4111-8111-111111111111",
		durableactor.ClassWorkload:                 "service-account:11111111-1111-4111-8111-111111111111",
	}
	for _, kind := range durableactor.Kinds() {
		actor, ok := actors[durableactor.ClassOf(kind)]
		switch kind {
		case durableactor.StaticAdmin:
			actor = "admin"
		case durableactor.StaticResolver:
			actor = "reader"
		default:
			if !ok {
				t.Fatalf("test has no actor for kind %q", kind)
			}
		}
		if _, allowed := auth.DurablePrincipal(actor, AuthenticationKind(kind), "", "member", true, false); !allowed {
			t.Errorf("durable kind %q is refused by DurablePrincipal", kind)
		}
	}
	if _, allowed := auth.DurablePrincipal("user:operator", AuthenticationKind("unknown"), "", "member", true, false); allowed {
		t.Error("an unknown kind must be refused")
	}
}

// A snapshot whose actor does not have the shape of its kind is refused, whatever the store reported as active.
func TestDurablePrincipalRefusesMismatchedSnapshot(t *testing.T) {
	auth := Authenticator{AdminToken: "admin-secret", AdminActor: "admin", Workload: &WorkloadAuthenticator{}}
	for _, tt := range []struct {
		name, actor string
		kind        AuthenticationKind
	}{
		{"user kind with an API key actor", "api-key:11111111-1111-4111-8111-111111111111", AuthenticationLocalSession},
		{"OIDC kind with a service account actor", "service-account:11111111-1111-4111-8111-111111111111", AuthenticationOIDC},
		{"API key kind with a user actor", "user:operator", AuthenticationAPIKey},
		{"API key kind with a malformed id", "api-key:key", AuthenticationAPIKey},
		{"service account credential with a user actor", "user:operator", AuthenticationServiceAccountCredential},
		{"workload kind with a malformed account id", "service-account:", AuthenticationWorkloadFederation},
		{"static admin with a NUL in the actor", "admin\x00", AuthenticationStaticAdmin},
		{"empty kind", "admin", AuthenticationKind("")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if p, ok := auth.DurablePrincipal(tt.actor, tt.kind, "", "member", true, false); ok {
				t.Fatalf("mismatched snapshot accepted: %+v", p)
			}
		})
	}
}
