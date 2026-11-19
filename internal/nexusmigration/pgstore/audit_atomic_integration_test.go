//go:build integration

package pgstore_test

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/app"
	m "github.com/mlops-ca/notarx/internal/nexusmigration"
	"github.com/mlops-ca/notarx/internal/nexussource"
	sourcepg "github.com/mlops-ca/notarx/internal/nexussource/pgstore"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/testsupport"
)

// TestPostgresMigrationChangesRollBackWithTheirAudit makes every management audit insert of the
// administrator fail and drives the batch start and control, schedule create and control, and source
// create, update and delete handlers: none may leave its change behind, none may answer
// audit_failed, and each succeeds once audits work again (ADR 0023 section 1).
func TestPostgresMigrationChangesRollBackWithTheirAudit(t *testing.T) {
	f := newScheduleFixture(t)
	store := repository.NewMemoryStore()
	sources := nexussource.New(sourcepg.New(f.db), store, nexussource.Options{})
	h, err := app.NewGatewayHandler(app.GatewayOptions{Dependencies: app.Dependencies{MigrationService: f.batches, MigrationSchedules: f.plans, SourceService: sources}, Store: store, NPMMetadata: store, NPMRestore: store, Storage: app.SharedObjectStores(objectstore.NewMemoryStore()), Adapter: app.TestAdapter{}, Authenticator: app.Authenticator{AdminToken: "audit-admin", AdminActor: f.actor, ResolverToken: "audit-resolver"}})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any, version string, key ...string) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, "/api/v2"+path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer audit-admin")
		idempotency := uuid.NewString()
		if len(key) > 0 {
			idempotency = key[0]
		}
		r.Header.Set("Idempotency-Key", idempotency)
		if version != "" {
			r.Header.Set("If-Match", version)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := f.db.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	var restore func()
	fail := func() { restore = testsupport.FailSecurityAudits(t, f.db, "", f.actor) }
	rejected := func(name string, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code < 400 || strings.Contains(w.Body.String(), "audit_failed") {
			t.Fatalf("%s with a failing audit: %d %s", name, w.Code, w.Body.String())
		}
	}
	input := m.StartInput{SourceID: f.template.SourceID, SourceVersion: f.template.SourceVersion, Mappings: f.template.Mappings, PolicyVersion: m.PolicyVersion, CapacityEvidence: f.evidence(time.Now().Add(-time.Second))}

	// Batch start.
	batches := func() int {
		return count(`SELECT count(*) FROM nexus_migration_batches WHERE source_id=$1::uuid`, f.template.SourceID)
	}
	before := batches()
	fail()
	rejected("batch start", call("POST", "/migrations/nexus/batches", input, ""))
	if batches() != before {
		t.Fatal("batch created although its audit failed")
	}
	restore()
	startKey := uuid.NewString()
	w := call("POST", "/migrations/nexus/batches", input, "", startKey)
	if w.Code != 202 || batches() != before+1 {
		t.Fatalf("batch start with a working audit: %d %s", w.Code, w.Body.String())
	}
	audits := func() int {
		return count(`SELECT count(*) FROM security_audit_log WHERE actor=$1 AND operation='migration.batch.start' AND http_status=202`, f.actor)
	}
	if audits() != 1 {
		t.Fatalf("batch start audits=%d, want the one written with the change", audits())
	}
	// A replay of the idempotency key changes nothing and writes no second audit.
	if replay := call("POST", "/migrations/nexus/batches", input, "", startKey); replay.Code != 202 || audits() != 1 || batches() != before+1 {
		t.Fatalf("batch start replay: %d %s audits=%d", replay.Code, replay.Body.String(), audits())
	}
	var b m.Batch
	if err = json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}

	// Batch control.
	fail()
	rejected("batch cancel", call("POST", "/migrations/nexus/batches/"+b.ID+"/cancel", nil, b.Version))
	if current, getErr := f.batches.Get(t.Context(), b.ID); getErr != nil || current.State == "cancelled" || current.Version != b.Version {
		t.Fatalf("batch changed although its audit failed: %+v %v", current, getErr)
	}
	restore()
	if w = call("POST", "/migrations/nexus/batches/"+b.ID+"/cancel", nil, b.Version); w.Code != 200 {
		t.Fatalf("batch cancel with a working audit: %d %s", w.Code, w.Body.String())
	}

	// Schedule create and control.
	schedules := func() int {
		return count(`SELECT count(*) FROM nexus_migration_schedules WHERE actor=$1`, f.actor)
	}
	create := m.ScheduleCreate{TemplateBatchID: f.template.ID, IntervalSeconds: 60}
	fail()
	rejected("schedule create", call("POST", "/migrations/nexus/schedules", create, ""))
	if schedules() != 0 {
		t.Fatal("schedule created although its audit failed")
	}
	restore()
	if w = call("POST", "/migrations/nexus/schedules", create, ""); w.Code != 201 || schedules() != 1 {
		t.Fatalf("schedule create with a working audit: %d %s", w.Code, w.Body.String())
	}
	var p m.Schedule
	if err = json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	fail()
	rejected("schedule pause", call("POST", "/migrations/nexus/schedules/"+p.ID+"/pause", nil, p.Version))
	if current := f.get(p.ID); current.Version != p.Version || current.State != p.State {
		t.Fatalf("schedule changed although its audit failed: %+v", current)
	}
	restore()
	if w = call("POST", "/migrations/nexus/schedules/"+p.ID+"/pause", nil, p.Version); w.Code != 200 {
		t.Fatalf("schedule pause with a working audit: %d %s", w.Code, w.Body.String())
	}

	// Source create, update and delete.
	name := "audit-source-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	body := map[string]any{"name": name, "baseUrl": "https://example.com"}
	named := func(n string) int { return count(`SELECT count(*) FROM nexus_migration_sources WHERE name=$1`, n) }
	fail()
	rejected("source create", call("POST", "/migration-sources", body, ""))
	if named(name) != 0 {
		t.Fatal("source created although its audit failed")
	}
	restore()
	if w = call("POST", "/migration-sources", body, ""); w.Code != 201 || named(name) != 1 {
		t.Fatalf("source create with a working audit: %d %s", w.Code, w.Body.String())
	}
	var created struct{ ID, Version string }
	if err = json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.db.Exec(`DELETE FROM nexus_migration_sources WHERE id=$1::uuid`, created.ID) })
	fail()
	rejected("source update", call("PUT", "/migration-sources/"+created.ID, map[string]any{"name": name + "-renamed", "baseUrl": "https://example.com"}, created.Version))
	if named(name+"-renamed") != 0 {
		t.Fatal("source updated although its audit failed")
	}
	rejected("source delete", call("DELETE", "/migration-sources/"+created.ID, nil, created.Version))
	if named(name) != 1 {
		t.Fatal("source deleted although its audit failed")
	}
	restore()
	if w = call("DELETE", "/migration-sources/"+created.ID, nil, created.Version); w.Code != 204 || named(name) != 0 {
		t.Fatalf("source delete with a working audit: %d %s", w.Code, w.Body.String())
	}
}
