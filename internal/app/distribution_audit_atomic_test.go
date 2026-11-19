package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

// TestDistributionMutationsRollBackWithTheirAudit proves ADR 0023 section 1 for the promote,
// replicate and Cargo restore handlers on the memory store: a failing audit fails the change.
func TestDistributionMutationsRollBackWithTheirAudit(t *testing.T) {
	store := repository.NewMemoryStore()
	runDistributionAuditRollback(t, store, func(...string) {
		store.SetAuditFault(func(record repository.AuditRecord) error {
			switch record.Operation {
			case "promote", "replicate", "artifact.restore":
				return errors.New("audit store unavailable")
			}
			return nil
		})
	}, func() { store.SetAuditFault(nil) })
}

// runDistributionAuditRollback drives the promote, replicate and Cargo restore handlers while
// audits fail, asserts the job, plan and tombstone removal did not take effect and the answer is
// never audit_failed, then lets audits through and proves the same requests succeed. fail
// receives the repository names whose audits must fail.
func runDistributionAuditRollback(t *testing.T, store GatewayStore, fail func(names ...string), clear func()) {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	source, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "dist-audit-source-" + suffix, Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "dist-audit-target-" + suffix, Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	cargo, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "dist-audit-cargo-" + suffix, Format: repository.FormatCargo})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("distribute this Raw object " + suffix)
	sum := sha256.Sum256(body)
	digest := "sha256:" + fmt.Sprintf("%x", sum[:])
	asset := repository.RawAsset{RepositoryID: source.ID, Path: "releases/widget.txt", Digest: digest, ObjectKey: "native/raw/sha256/" + fmt.Sprintf("%x", sum[:]), Size: int64(len(body)), ContentType: "text/plain"}
	if _, err = store.PutRawAsset(ctx, asset); err != nil {
		t.Fatal(err)
	}
	handler := newTestGateway(t, GatewayOptions{Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator(), Storage: ObjectStores{repository.FormatCargo: objectstore.NewMemoryStore()}})
	call := func(method, path, body, key string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = "localhost:8080"
		authorize(r, "admin-secret")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	payload, _ := cargoC0PublishFixture(t, "demo", "1.0.0", "demo")
	publish := httptest.NewRequest(http.MethodPut, "/cargo/"+cargo.Name+"/api/v1/crates/new", bytes.NewReader(payload))
	publish.Host = "localhost:8080"
	authorize(publish, "admin-secret")
	published := httptest.NewRecorder()
	handler.ServeHTTP(published, publish)
	if published.Code != http.StatusOK {
		t.Fatalf("publish crate fixture: %d %s", published.Code, published.Body.String())
	}
	if w := call(http.MethodPost, "/api/v2/repositories/"+cargo.ID+"/tombstones", `{"coordinate":"demo@1.0.0"}`, ""); w.Code != http.StatusNoContent {
		t.Fatalf("tombstone crate fixture: %d %s", w.Code, w.Body.String())
	}

	promoteBody := `{"targetRepositoryId":"` + target.ID + `","coordinate":"` + asset.Path + `","digest":"` + digest + `"}`
	cases := []struct {
		name, method, path, body, key string
		want                          int
		unchanged                     func(t *testing.T)
	}{
		{"promote", http.MethodPost, "/api/v2/repositories/" + source.ID + "/promotions", promoteBody, "promote-" + suffix, http.StatusAccepted, func(t *testing.T) {
			if jobs, err := store.ListLifecycleJobs(ctx, target.ID, 10); err != nil || len(jobs) != 0 {
				t.Fatalf("promotion job enqueued: %+v %v", jobs, err)
			}
		}},
		{"replicate", http.MethodPost, "/api/v2/repositories/" + source.ID + "/replications", promoteBody, "replicate-" + suffix, http.StatusAccepted, func(t *testing.T) {
			if plans, err := store.ListReplicationPlans(ctx, target.ID, 10); err != nil || len(plans) != 0 {
				t.Fatalf("replication plan created: %+v %v", plans, err)
			}
		}},
		{"cargo restore", http.MethodPost, "/api/v2/repositories/" + cargo.ID + "/restore", `{"coordinate":"demo@1.0.0"}`, "", http.StatusNoContent, func(t *testing.T) {
			if _, err := store.GetArtifactTombstone(ctx, cargo.ID, repository.FormatCargo, "demo@1.0.0"); err != nil {
				t.Fatalf("tombstone removed: %v", err)
			}
		}},
	}
	fail(source.Name, cargo.Name)
	for _, c := range cases {
		response := call(c.method, c.path, c.body, c.key)
		if response.Code < 400 {
			t.Fatalf("%s: status %d, want a failure: %s", c.name, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "audit_failed") {
			t.Fatalf("%s: answered audit_failed for a change that must not take effect: %s", c.name, response.Body.String())
		}
		c.unchanged(t)
	}
	clear()
	for _, c := range cases {
		if response := call(c.method, c.path, c.body, c.key); response.Code != c.want {
			t.Fatalf("%s after audits work again: %d %s", c.name, response.Code, response.Body.String())
		}
	}
}
