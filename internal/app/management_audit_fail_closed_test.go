package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/scanning"
)

// ADR 0023 section 1: when the audit of a management mutation cannot be
// written the request fails instead of reporting success.
func TestManagementMutationFailsWhenAuditCannotBeWritten(t *testing.T) {
	store := failingAuditStore{repository.NewMemoryStore()}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{ArtifactScanner: scanning.ScannerFunc(func(context.Context, scanning.Artifact) (scanning.Report, error) { return scanning.Report{}, nil }), ArtifactScannerFormats: []repository.Format{repository.FormatRaw}}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	repo, err := store.CreateHostedRepository(t.Context(), repository.HostedRepository{ID: uuid.NewString(), Name: "fail-closed", Format: repository.FormatRaw, Type: repository.RepositoryTypeHosted, State: repository.RepositoryActive})
	if err != nil {
		t.Fatal(err)
	}
	// A scan reconcile with no work changes nothing, so it is audited after the request through
	// recordUnchangedAudit, where 500 audit_failed is accurate; the handlers that change state are
	// covered by the rollback tests.
	request := httptest.NewRequest(http.MethodPost, "/api/v2/repositories/"+repo.ID+"/artifact-scans:reconcile", nil)
	authorize(request, "admin-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "audit_failed") {
		t.Fatalf("policy with failing audit = %d %s", response.Code, response.Body.String())
	}
}

// An idempotent scan enqueue replay changes nothing, so its audit is written after the request and a
// failure is accurate: the first request's job and audit were written in one transaction.
func TestScanEnqueueReplayFailsWhenAuditCannotBeWritten(t *testing.T) {
	store := failingAuditStore{repository.NewMemoryStore()}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{ArtifactScanner: scanning.ScannerFunc(func(context.Context, scanning.Artifact) (scanning.Report, error) { return scanning.Report{}, nil }), ArtifactScannerFormats: []repository.Format{repository.FormatRaw}}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	repo, err := store.CreateHostedRepository(t.Context(), repository.HostedRepository{ID: uuid.NewString(), Name: "replay-fail-closed", Format: repository.FormatRaw, Type: repository.RepositoryTypeHosted, State: repository.RepositoryActive})
	if err != nil {
		t.Fatal(err)
	}
	post := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v2/repositories/"+repo.ID+"/artifact-scans", strings.NewReader(`{"coordinate":"a.bin","digest":"sha256:`+strings.Repeat("a", 64)+`"}`))
		request.Header.Set("Idempotency-Key", "replay-key")
		authorize(request, "admin-secret")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if first := post(); first.Code != http.StatusAccepted {
		t.Fatalf("first enqueue = %d %s", first.Code, first.Body.String())
	}
	if replay := post(); replay.Code != http.StatusInternalServerError || !strings.Contains(replay.Body.String(), "audit_failed") {
		t.Fatalf("replay with failing audit = %d %s", replay.Code, replay.Body.String())
	}
}
