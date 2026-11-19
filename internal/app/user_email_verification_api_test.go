package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/repository"
)

// TestUserManagementEmailVerificationLifecycle covers the administrator-verified email flag of ADR 0018
// Decision 3: it is set and cleared through PATCH /api/v2/users/{id}, records the acting administrator and
// the time, needs an email to verify, and a changed email drops it.
func TestUserManagementEmailVerificationLifecycle(t *testing.T) {
	store := repository.NewMemoryStore()
	handler := newTestGateway(t, GatewayOptions{Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	call := func(method, target, version, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, target, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer admin-secret")
		if version != "" {
			request.Header.Set("If-Match", version)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	decode := func(response *httptest.ResponseRecorder) adminopenapi.User {
		t.Helper()
		var user adminopenapi.User
		if err := json.Unmarshal(response.Body.Bytes(), &user); err != nil {
			t.Fatalf("decode %d %s: %v", response.Code, response.Body.String(), err)
		}
		return user
	}
	created := call(http.MethodPost, "/api/v2/users", "", `{"name":"verified-owner","password":"initial-password","role":"member","email":"owner@example.test"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	user := decode(created)
	if user.EmailVerifiedByAdmin || user.EmailVerifiedAt != nil || user.EmailVerifiedBy != nil {
		t.Fatalf("a new account is not verified: %+v", user)
	}
	target := "/api/v2/users/" + user.Id.String()

	verified := call(http.MethodPatch, target, user.Version, `{"emailVerifiedByAdmin":true}`)
	if verified.Code != http.StatusOK {
		t.Fatalf("verify = %d %s", verified.Code, verified.Body.String())
	}
	user = decode(verified)
	if !user.EmailVerifiedByAdmin || user.EmailVerifiedAt == nil || user.EmailVerifiedBy == nil || *user.EmailVerifiedBy != "alice" {
		t.Fatalf("verified account = %+v", user)
	}

	// A profile save that leaves the email alone keeps the verification.
	kept := call(http.MethodPatch, target, user.Version, `{"displayName":"Owner","email":"owner@example.test"}`)
	if kept.Code != http.StatusOK || !decode(kept).EmailVerifiedByAdmin {
		t.Fatalf("unchanged email = %d %s", kept.Code, kept.Body.String())
	}
	user = decode(kept)

	changed := call(http.MethodPatch, target, user.Version, `{"email":"moved@example.test"}`)
	if changed.Code != http.StatusOK {
		t.Fatalf("change email = %d %s", changed.Code, changed.Body.String())
	}
	if user = decode(changed); user.EmailVerifiedByAdmin || user.EmailVerifiedAt != nil || user.EmailVerifiedBy != nil {
		t.Fatalf("a changed email must clear the verification: %+v", user)
	}

	both := call(http.MethodPatch, target, user.Version, `{"email":"final@example.test","emailVerifiedByAdmin":true}`)
	if both.Code != http.StatusOK || !decode(both).EmailVerifiedByAdmin {
		t.Fatalf("change and verify together = %d %s", both.Code, both.Body.String())
	}
	user = decode(both)
	cleared := call(http.MethodPatch, target, user.Version, `{"emailVerifiedByAdmin":false}`)
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear = %d %s", cleared.Code, cleared.Body.String())
	}
	if user = decode(cleared); user.EmailVerifiedByAdmin || user.EmailVerifiedAt != nil || user.EmailVerifiedBy != nil || user.Email != "final@example.test" {
		t.Fatalf("cleared account = %+v", user)
	}

	blank := decode(call(http.MethodPost, "/api/v2/users", "", `{"name":"no-email","password":"initial-password","role":"member"}`))
	refused := call(http.MethodPatch, "/api/v2/users/"+blank.Id.String(), blank.Version, `{"emailVerifiedByAdmin":true}`)
	if refused.Code != http.StatusBadRequest {
		t.Fatalf("verifying an account without an email = %d %s", refused.Code, refused.Body.String())
	}
	stale := call(http.MethodPatch, target, "1", `{"emailVerifiedByAdmin":true}`)
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("a stale version = %d %s", stale.Code, stale.Body.String())
	}

	unauthorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, target, strings.NewReader(`{"emailVerifiedByAdmin":true}`))
	request.Header.Set("If-Match", user.Version)
	handler.ServeHTTP(unauthorized, request)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", unauthorized.Code)
	}
}
