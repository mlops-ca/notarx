package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/repository"
)

// ADR 0023 section 1: when the audit of a management mutation cannot be
// written the request fails instead of reporting success.
func TestManagementMutationFailsWhenAuditCannotBeWritten(t *testing.T) {
	store := failingAuditStore{repository.NewMemoryStore()}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	request := httptest.NewRequest(http.MethodPut, "/api/v2/site-settings", strings.NewReader(`{"siteName":"Acme Packages","logoUrl":"","brandMark":"AC","enabledThemeIds":["notarx-dark","notarx-light"],"defaultThemeId":"notarx-dark"}`))
	request.Header.Set("If-Match", "1")
	authorize(request, "admin-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "audit_failed") {
		t.Fatalf("replace with failing audit = %d %s", response.Code, response.Body.String())
	}
}
