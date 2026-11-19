package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/mlops-ca/notarx/internal/repository"
)

// ADR 0023 section 1: when the audit of a management mutation cannot be
// written the request fails instead of reporting success.
func TestManagementMutationFailsWhenAuditCannotBeWritten(t *testing.T) {
	store := failingAuditStore{repository.NewMemoryStore()}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	repo, err := store.CreateHostedRepository(t.Context(), repository.HostedRepository{ID: uuid.NewString(), Name: "fail-closed", Format: repository.FormatRaw, Type: repository.RepositoryTypeHosted, State: repository.RepositoryActive})
	if err != nil {
		t.Fatal(err)
	}
	// Quarantine read policy is a legacy handler that still audits after the change; the
	// migrated handlers are covered by the rollback tests.
	request := httptest.NewRequest(http.MethodPut, "/api/v2/repositories/"+repo.ID+"/quarantine-read-policy", strings.NewReader(`{"version":"1","enabled":true}`))
	request.Header.Set("If-Match", "1")
	authorize(request, "admin-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "audit_failed") {
		t.Fatalf("policy with failing audit = %d %s", response.Code, response.Body.String())
	}
}
