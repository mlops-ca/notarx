package nexussource

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	api "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/authorization"
	source "github.com/mlops-ca/notarx/internal/nexussource"
	"github.com/mlops-ca/notarx/internal/repository"
)

// auditStore is an in-memory source store that fails the audit of a change like a failing
// security_audit_log insert: the change does not take effect.
type auditStore struct {
	mu     sync.Mutex
	values map[string]source.Source
	audits []repository.AuditRecord
	fail   bool
}

func (s *auditStore) commit(audit []repository.AuditRecord) error {
	if s.fail {
		return errors.New("audit store unavailable")
	}
	s.audits = append(s.audits, audit...)
	return nil
}
func (s *auditStore) Create(_ context.Context, v source.Source, audit ...repository.AuditRecord) (source.Source, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.commit(audit); err != nil {
		return source.Source{}, err
	}
	v.Version = "1"
	s.values[v.ID] = v
	return v, nil
}
func (s *auditStore) Get(_ context.Context, id string) (source.Source, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[id]
	if !ok {
		return source.Source{}, source.ErrNotFound
	}
	return v, nil
}
func (s *auditStore) List(context.Context, int, string) ([]source.Source, string, error) {
	return nil, "", nil
}
func (s *auditStore) Update(_ context.Context, v source.Source, expected string, audit ...repository.AuditRecord) (source.Source, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values[v.ID].Version != expected {
		return source.Source{}, source.ErrVersionConflict
	}
	if err := s.commit(audit); err != nil {
		return source.Source{}, err
	}
	v.Version = "2"
	s.values[v.ID] = v
	return v, nil
}
func (s *auditStore) Delete(_ context.Context, id, expected string, audit ...repository.AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values[id].Version != expected {
		return source.ErrVersionConflict
	}
	if err := s.commit(audit); err != nil {
		return err
	}
	delete(s.values, id)
	return nil
}

type adminAuth struct{}

func (adminAuth) AuthenticateContext(context.Context, string) (authorization.Principal, bool) {
	return authorization.Principal{Actor: "alice", Admin: true}, true
}

type countingAuditor struct{ records []repository.AuditRecord }

func (a *countingAuditor) RecordAudit(_ context.Context, record repository.AuditRecord) error {
	a.records = append(a.records, record)
	return nil
}

type noTargets struct{}

func (noTargets) GetHostedGroupByName(context.Context, string) (repository.HostedGroup, error) {
	return repository.HostedGroup{}, repository.ErrNotFound
}
func (noTargets) GetHostedRepositoryByName(context.Context, string) (repository.HostedRepository, error) {
	return repository.HostedRepository{}, repository.ErrNotFound
}

// TestSourceHandlersAuditWithTheChange proves the create, update and delete handlers hand their audit
// to the store, answer with the change's own error when the audit fails, and write no second
// best-effort audit after a change that the store audited.
func TestSourceHandlersAuditWithTheChange(t *testing.T) {
	store := &auditStore{values: map[string]source.Source{}}
	auditor := &countingAuditor{}
	adapter := New(source.New(store, noTargets{}, source.Options{}), adminAuth{}, auditor)
	call := func(handle func(http.ResponseWriter, *http.Request), method, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, "/api/v2/migration/sources", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer admin")
		response := httptest.NewRecorder()
		handle(response, request)
		return response
	}
	body := `{"name":"synthetic-nexus","baseUrl":"https://example.com"}`

	store.fail = true
	if response := call(adapter.CreateMigrationSource, http.MethodPost, body); response.Code < 400 || strings.Contains(response.Body.String(), "audit_failed") || len(store.values) != 0 {
		t.Fatalf("create with a failing audit: %d %s values=%d", response.Code, response.Body.String(), len(store.values))
	}
	store.fail = false
	response := call(adapter.CreateMigrationSource, http.MethodPost, body)
	if response.Code != http.StatusCreated || len(store.values) != 1 || len(store.audits) != 1 || store.audits[0].Operation != "migration.source.create" || store.audits[0].Resource == "" || store.audits[0].Status != http.StatusCreated {
		t.Fatalf("create: %d %s audits=%+v", response.Code, response.Body.String(), store.audits)
	}
	var id uuid.UUID
	for key := range store.values {
		id = uuid.MustParse(key)
	}

	store.fail = true
	if response = call(func(w http.ResponseWriter, r *http.Request) {
		adapter.UpdateMigrationSource(w, r, id, api.UpdateMigrationSourceParams{IfMatch: "1"})
	}, http.MethodPut, `{"name":"renamed","baseUrl":"https://example.com"}`); response.Code < 400 || strings.Contains(response.Body.String(), "audit_failed") || store.values[id.String()].Name == "renamed" {
		t.Fatalf("update with a failing audit: %d %s", response.Code, response.Body.String())
	}
	if response = call(func(w http.ResponseWriter, r *http.Request) {
		adapter.DeleteMigrationSource(w, r, id, api.DeleteMigrationSourceParams{IfMatch: "1"})
	}, http.MethodDelete, ""); response.Code < 400 || strings.Contains(response.Body.String(), "audit_failed") || len(store.values) != 1 {
		t.Fatalf("delete with a failing audit: %d %s", response.Code, response.Body.String())
	}
	store.fail = false
	if response = call(func(w http.ResponseWriter, r *http.Request) {
		adapter.DeleteMigrationSource(w, r, id, api.DeleteMigrationSourceParams{IfMatch: "1"})
	}, http.MethodDelete, ""); response.Code != http.StatusNoContent || len(store.values) != 0 {
		t.Fatalf("delete: %d %s", response.Code, response.Body.String())
	}
	// The audited changes wrote no second record; only the failed attempts did, best effort.
	for _, record := range auditor.records {
		if record.Status < 400 {
			t.Fatalf("a successful change was audited twice: %+v", record)
		}
	}
}
