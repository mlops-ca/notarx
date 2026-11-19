package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/repository"
)

type identityOIDCStub struct{ issuer string }

func (s identityOIDCStub) Settings(context.Context) (OIDCSettingsView, error) {
	return OIDCSettingsView{Issuer: s.issuer}, nil
}
func (identityOIDCStub) Replace(context.Context, OIDCSettingsUpdate, string, ...repository.AuditRecord) (OIDCSettingsView, error) {
	return OIDCSettingsView{}, errors.New("not used")
}
func (identityOIDCStub) Test(context.Context) (OIDCConnectionTest, error) {
	return OIDCConnectionTest{}, errors.New("not used")
}

// TestIdentityMutationsRollBackWithTheirAudit proves ADR 0023 section 1 for the user and
// service account handlers: when the audit of a mutation cannot be written the
// mutation does not take effect and the answer is the mutation's own failure, never
// a 500 audit_failed for a change that took effect.
func TestIdentityMutationsRollBackWithTheirAudit(t *testing.T) {
	store := repository.NewMemoryStore()
	runIdentityAuditRollback(t, store, func() {
		store.SetAuditFault(func(record repository.AuditRecord) error {
			if record.Format == "management" {
				return errors.New("audit store unavailable")
			}
			return nil
		})
	}, func() { store.SetAuditFault(nil) }, func() []repository.AuditRecord { return store.Audits })
}

// identityRollbackStore is what the identity handlers and their assertions need from a
// store; the memory and PostgreSQL stores both satisfy it.
type identityRollbackStore interface {
	identityUserStore
	identityAPIKeyStore
	identityServiceAccountStore
	GetUserByName(context.Context, string) (repository.User, error)
	GetUserSession(context.Context, string, string) (repository.UserSession, error)
	GetServiceAccount(context.Context, string) (repository.ServiceAccount, error)
	CreateUserSession(context.Context, repository.UserSession) (repository.UserSession, error)
}

// runIdentityAuditRollback drives every mutating user and service account handler with
// failAudits active and asserts nothing took effect. audits returns the management
// audits written so far.
func runIdentityAuditRollback(t *testing.T, store identityRollbackStore, failAudits, clearFaults func(), audits func() []repository.AuditRecord) {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	auth := testAuthenticator()
	hash, err := authorization.HashPassword("victim-password")
	if err != nil {
		t.Fatal(err)
	}
	victim, err := store.CreateUser(ctx, repository.User{ID: uuid.NewString(), Name: "victim-" + suffix, SecretHash: hash, Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.CreateUserSession(ctx, repository.UserSession{ID: uuid.NewString(), UserID: victim.ID, Kind: repository.UserSessionLocal, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := store.CreateUserIdentity(ctx, repository.UserIdentity{ID: uuid.NewString(), UserID: victim.ID, Kind: repository.UserIdentityOIDC, Issuer: "https://issuer.example", Subject: "victim-subject-" + suffix})
	if err != nil {
		t.Fatal(err)
	}
	linker, err := store.CreateUser(ctx, repository.User{ID: uuid.NewString(), Name: "linker-" + suffix, SecretHash: hash, Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.CreateServiceAccount(ctx, repository.ServiceAccount{ID: uuid.NewString(), Name: "ci-" + suffix})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.CreateServiceAccountCredential(ctx, repository.APIKey{ID: uuid.NewString(), ServiceAccountID: account.ID, Name: "blue", SecretHash: "hash-" + suffix, Roles: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	audit := &managementAuditFixture{}
	domain := newIdentityManagementHandler(identityManagementOptions{
		Scope: managementScope{authenticator: auth, audit: audit},
		Users: identityUserPort{store}, APIKeys: identityKeyPort{store}, ServiceAccounts: identityAccountPort{store},
		OIDC: identityOIDCStub{issuer: "https://issuer.example"},
	})
	server := identityDomainHTTP(domain)
	call := func(method, path, body, version string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, "/api/v2"+path, strings.NewReader(body))
		authorize(request, "admin-secret")
		if version != "" {
			request.Header.Set("If-Match", version)
		}
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}
	failAudits()
	userPath := "/users/" + victim.ID
	accountPath := "/service-accounts/" + account.ID
	cases := []struct {
		name, method, path, body, version string
		unchanged                         func(t *testing.T)
	}{
		{"create user", http.MethodPost, "/users", `{"name":"newbie-` + suffix + `","password":"newbie-password","role":"member"}`, "", func(t *testing.T) {
			if _, err := store.GetUserByName(ctx, "newbie-"+suffix); !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("user was created: %v", err)
			}
		}},
		{"update user", http.MethodPatch, userPath, `{"displayName":"Changed"}`, victim.Version, func(t *testing.T) {
			if got, _ := store.GetUser(ctx, victim.ID); got.DisplayName != "" || got.Version != victim.Version {
				t.Fatalf("user changed: %+v", got)
			}
		}},
		{"reset password", http.MethodPost, userPath + "/password", `{"password":"another-password"}`, victim.Version, func(t *testing.T) {
			if got, _ := store.GetUser(ctx, victim.ID); got.SecretHash != victim.SecretHash || got.Version != victim.Version {
				t.Fatalf("password changed: %+v", got)
			}
		}},
		{"revoke sessions", http.MethodPost, userPath + "/sessions:revoke", "", victim.Version, func(t *testing.T) {
			if got, _ := store.GetUser(ctx, victim.ID); got.SessionVersion != victim.SessionVersion {
				t.Fatalf("session version changed: %+v", got)
			}
		}},
		{"revoke session", http.MethodDelete, userPath + "/sessions/" + session.ID, "", "", func(t *testing.T) {
			if got, _ := store.GetUserSession(ctx, victim.ID, session.ID); got.RevokedAt != nil {
				t.Fatalf("session revoked: %+v", got)
			}
		}},
		{"unlink identity", http.MethodDelete, userPath + "/identities/" + identity.ID, "", "", func(t *testing.T) {
			if items, _ := store.ListUserIdentities(ctx, victim.ID); len(items) != 1 {
				t.Fatalf("identity unlinked: %+v", items)
			}
		}},
		{"link identity", http.MethodPost, "/users/" + linker.ID + "/identities", `{"issuer":"https://issuer.example","subject":"second-subject"}`, "", func(t *testing.T) {
			if items, _ := store.ListUserIdentities(ctx, linker.ID); len(items) != 0 {
				t.Fatalf("identity linked: %+v", items)
			}
		}},
		{"create service account", http.MethodPost, "/service-accounts", `{"name":"new-account-` + suffix + `"}`, "", func(t *testing.T) {
			items, _ := store.ListServiceAccounts(ctx, 100000, "")
			for _, item := range items {
				if item.Name == "new-account-"+suffix {
					t.Fatalf("service account created: %+v", item)
				}
			}
		}},
		{"update service account", http.MethodPut, accountPath, `{"description":"changed"}`, account.Version, func(t *testing.T) {
			if got, _ := store.GetServiceAccount(ctx, account.ID); got.Description != "" || got.Version != account.Version {
				t.Fatalf("service account changed: %+v", got)
			}
		}},
		{"create credential", http.MethodPost, accountPath + "/credentials", `{"name":"green"}`, "", func(t *testing.T) {
			if items, _ := store.ListServiceAccountCredentials(ctx, account.ID, 10, ""); len(items) != 1 {
				t.Fatalf("credential created: %+v", items)
			}
		}},
		{"revoke credential", http.MethodDelete, accountPath + "/credentials/" + credential.ID, "", "", func(t *testing.T) {
			items, _ := store.ListServiceAccountCredentials(ctx, account.ID, 10, "")
			if len(items) != 1 || items[0].RevokedAt != nil {
				t.Fatalf("credential revoked: %+v", items)
			}
		}},
		{"delete user", http.MethodDelete, userPath, "", "", func(t *testing.T) {
			if _, err := store.GetUser(ctx, victim.ID); err != nil {
				t.Fatalf("user deleted: %v", err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			response := call(c.method, c.path, c.body, c.version)
			body := response.Body.String()
			if strings.Contains(body, "audit_failed") || response.Code < 500 {
				t.Fatalf("status=%d body=%s; want the mutation's own failure, not audit_failed or success", response.Code, body)
			}
			c.unchanged(t)
		})
	}
	if len(audit.records) != 0 {
		t.Fatalf("handlers must not write these audits themselves: %+v", audit.records)
	}
	for _, record := range audits() {
		if record.Format == "management" && (strings.Contains(record.Resource, victim.ID) || strings.Contains(record.Resource, linker.ID) || strings.Contains(record.Resource, account.ID) || record.Operation == "user.create" || record.Operation == "service_account.create" || record.Operation == "service_account.credential.create") {
			t.Fatalf("a failed audit left a record: %+v", record)
		}
	}
	// Without the fault every mutation lands together with its audit.
	clearFaults()
	if response := call(http.MethodPost, "/users", `{"name":"newbie-`+suffix+`","password":"newbie-password","role":"member"}`, ""); response.Code != http.StatusCreated {
		t.Fatalf("create user after the fault cleared=%d %s", response.Code, response.Body.String())
	}
	created, err := store.GetUserByName(ctx, "newbie-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range audits() {
		found = found || (record.Operation == "user.create" && record.Resource == "users/"+created.ID)
	}
	if !found {
		t.Fatal("user.create audit was not written with the change")
	}
}

func TestLocalLoginRollsBackWithItsAudit(t *testing.T) {
	store := repository.NewMemoryStore()
	runLoginAuditRollback(t, store, func(string) {
		store.SetAuditFault(func(repository.AuditRecord) error { return errors.New("audit store unavailable") })
	})
}

// loginRollbackStore is the store a login handler test runs against.
type loginRollbackStore interface {
	GatewayStore
	CreateUser(context.Context, repository.User, ...repository.AuditRecord) (repository.User, error)
	GetUser(context.Context, string) (repository.User, error)
}

// runLoginAuditRollback logs a user in while the audit of the login cannot be written
// (failAudits receives the audit actor) and asserts the login counters did not move.
func runLoginAuditRollback(t *testing.T, store loginRollbackStore, failAudits func(actor string)) {
	t.Helper()
	ctx := context.Background()
	name := "login-" + uuid.NewString()[:8]
	hash, err := authorization.HashPassword("correct-password")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(ctx, repository.User{ID: uuid.NewString(), Name: name, SecretHash: hash, Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	auth := testAuthenticator()
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: auth})
	failAudits("user:" + name)
	request := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"username":"`+name+`","password":"correct-password"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "audit_failed") {
		t.Fatalf("login with a failing audit=%d %s", response.Code, response.Body.String())
	}
	if got, _ := store.GetUser(ctx, user.ID); got.LastLoginAt != nil || got.Version != user.Version {
		t.Fatalf("login took effect without its audit: %+v", got)
	}
}
