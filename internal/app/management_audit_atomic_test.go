package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/scanning"
)

// TestManagementMutationsRollBackWithTheirAudit proves ADR 0023 section 1 for the repository
// policy, access, lifecycle job, quarantine, scan, intelligence and tombstone handlers:
// when the audit of a mutation cannot be written the mutation does not take effect and the
// answer is the mutation's own failure, never a 500 audit_failed for a change that took
// effect.
func TestManagementMutationsRollBackWithTheirAudit(t *testing.T) {
	store := repository.NewMemoryStore()
	runManagementAuditRollback(t, store, func(...string) {
		store.SetAuditFault(func(repository.AuditRecord) error { return errors.New("audit store unavailable") })
	}, func() { store.SetAuditFault(nil) }, func() []repository.AuditRecord { return store.Audits })
}

func rawDigest(seed string) (digest, objectKey string) {
	sum := sha256.Sum256([]byte(seed))
	encoded := hex.EncodeToString(sum[:])
	return "sha256:" + encoded, "native/raw/sha256/" + encoded
}

// runManagementAuditRollback drives each management mutation named in ADR 0023 section 1
// slices 2, 4a and 4b with failAudits active and asserts nothing took effect. failAudits
// receives the repository names whose audits must fail ("" is the audit of a mutation
// that names no repository); audits returns the audits written so far.
func runManagementAuditRollback(t *testing.T, store GatewayStore, failAudits func(repositoryNames ...string), clearFaults func(), audits func() []repository.AuditRecord) {
	t.Helper()
	ctx := context.Background()
	suffix := strings.ToLower(uuid.NewString()[:8])
	repo, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "audit-raw-" + suffix, Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	policy := repository.DefaultRepositorySecurityPolicy()
	policy.AutoScanOnPublish = true
	policy, err = store.ReplaceRepositorySecurityPolicy(ctx, repo.ID, policy, policy.Version)
	if err != nil {
		t.Fatal(err)
	}
	asset := func(name string) repository.RawAsset {
		t.Helper()
		digest, key := rawDigest(suffix + "/" + name)
		if err := store.StageRawObject(ctx, repository.RawObject{Digest: digest, ObjectKey: key, Size: 1}); err != nil {
			t.Fatal(err)
		}
		stored, err := store.PutRawAsset(ctx, repository.RawAsset{RepositoryID: repo.ID, Path: name, Digest: digest, ObjectKey: key, Size: 1, ContentType: "text/plain"})
		if err != nil {
			t.Fatal(err)
		}
		return stored
	}
	tombstoned, quarantined, intelligent, enqueued, reconciled := asset("tombstone.bin"), asset("quarantine.bin"), asset("intelligence.bin"), asset("enqueue.bin"), asset("reconcile.bin")
	quarantinePolicy, err := store.GetRepositoryQuarantineReadPolicy(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := store.GetRepositoryGrants(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.UpsertRepositoryGrant(ctx, repo.ID, repository.RepositoryGrant{Principal: "user:existing-" + suffix, Scopes: []string{"repositories:read"}}); err != nil {
		t.Fatal(err)
	}
	if grants, err = store.GetRepositoryGrants(ctx, repo.ID); err != nil {
		t.Fatal(err)
	}
	role, err := store.CreateAuthorizationRole(ctx, repository.AuthorizationRole{Name: "role-" + suffix, Scopes: []string{"repositories:read"}})
	if err != nil {
		t.Fatal(err)
	}
	template, err := store.CreateAuthorizationTemplate(ctx, repository.AuthorizationTemplate{Name: "template-" + suffix, Grants: []repository.RepositoryGrant{{Principal: "user:templated-" + suffix, Scopes: []string{"repositories:read"}}}})
	if err != nil {
		t.Fatal(err)
	}
	job := func(kind repository.LifecycleJobKind, key string, cancel bool) repository.LifecycleJob {
		t.Helper()
		created, _, err := store.EnqueueLifecycleJob(ctx, repository.LifecycleJob{ID: uuid.NewString(), RepositoryID: repo.ID, Kind: kind, IdempotencyKey: key + "-" + suffix, Payload: []byte("{}")})
		if err != nil {
			t.Fatal(err)
		}
		if cancel {
			if created, err = store.CancelLifecycleJob(ctx, repo.ID, created.ID); err != nil {
				t.Fatal(err)
			}
		}
		return created
	}
	cancelJob, runJob := job(repository.LifecycleJobRetention, "cancel", false), job(repository.LifecycleJobRetention, "run", false)
	retryJob, intelligenceJob := job(repository.LifecycleJobRetention, "retry", true), job(repository.LifecycleJobIntelligence, "intelligence", true)

	auth := testAuthenticator()
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{
		ArtifactScanner:        scanning.ScannerFunc(func(context.Context, scanning.Artifact) (scanning.Report, error) { return scanning.Report{}, nil }),
		ArtifactScannerFormats: []repository.Format{repository.FormatRaw},
	}, Store: store, Adapter: TestAdapter{}, Authenticator: auth})
	call := func(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, "/api/v2"+path, strings.NewReader(body))
		authorize(request, "admin-secret")
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	repoPath := "/repositories/" + repo.ID
	identity := "?coordinate=%s&digest=%s"
	identityOf := func(a repository.RawAsset) string {
		return strings.Replace(strings.Replace(identity, "%s", a.Path, 1), "%s", a.Digest, 1)
	}
	failAudits(repo.Name, "")
	type mutation struct {
		name, method, path, body string
		headers                  map[string]string
		unchanged                func(t *testing.T)
	}
	cases := []mutation{
		{"quarantine read policy", http.MethodPut, repoPath + "/quarantine-read-policy", `{"version":"` + quarantinePolicy.Version + `","enabled":true}`, map[string]string{"If-Match": quarantinePolicy.Version}, func(t *testing.T) {
			if got, _ := store.GetRepositoryQuarantineReadPolicy(ctx, repo.ID); got.Enabled || got.Version != quarantinePolicy.Version {
				t.Fatalf("policy changed: %+v", got)
			}
		}},
		{"security policy", http.MethodPut, repoPath + "/security-policy", `{"version":"` + policy.Version + `","enabled":true}`, map[string]string{"If-Match": policy.Version}, func(t *testing.T) {
			if got, _ := store.GetRepositorySecurityPolicy(ctx, repo.ID); got.Version != policy.Version || got.Enabled {
				t.Fatalf("policy changed: %+v", got)
			}
		}},
		{"capacity", http.MethodPut, repoPath + "/capacity", `{"quotaBytes":4096}`, nil, func(t *testing.T) {
			if got, _ := store.GetRepositoryCapacity(ctx, repo.ID); got.QuotaBytes != 0 {
				t.Fatalf("capacity changed: %+v", got)
			}
		}},
		{"replace grants", http.MethodPut, repoPath + "/grants", `[{"principal":"user:replaced-` + suffix + `","scopes":["repositories:read"]}]`, map[string]string{"If-Match": grants.Version}, func(t *testing.T) {
			if got, _ := store.GetRepositoryGrants(ctx, repo.ID); got.Version != grants.Version || len(got.Grants) != len(grants.Grants) {
				t.Fatalf("grants changed: %+v", got)
			}
		}},
		{"upsert grant", http.MethodPost, repoPath + "/grants", `{"principal":"user:upserted-` + suffix + `","scopes":["repositories:read"]}`, nil, func(t *testing.T) {
			if got, _ := store.GetRepositoryGrants(ctx, repo.ID); got.Version != grants.Version || len(got.Grants) != len(grants.Grants) {
				t.Fatalf("grants changed: %+v", got)
			}
		}},
		{"delete grant", http.MethodDelete, repoPath + "/grants?principal=user:existing-" + suffix, "", nil, func(t *testing.T) {
			if got, _ := store.GetRepositoryGrants(ctx, repo.ID); got.Version != grants.Version || len(got.Grants) != len(grants.Grants) {
				t.Fatalf("grants changed: %+v", got)
			}
		}},
		{"create role", http.MethodPost, "/authorization-roles", `{"name":"created-role-` + suffix + `","scopes":["repositories:read"]}`, nil, func(t *testing.T) {
			items, _ := store.ListAuthorizationRoles(ctx)
			for _, item := range items {
				if item.Name == "created-role-"+suffix {
					t.Fatalf("role created: %+v", item)
				}
			}
		}},
		{"update role", http.MethodPut, "/authorization-roles/" + role.ID, `{"name":"renamed-role-` + suffix + `","scopes":["repositories:read"]}`, map[string]string{"If-Match": role.Version}, func(t *testing.T) {
			if got, _ := store.GetAuthorizationRole(ctx, role.ID); got.Name != role.Name || got.Version != role.Version {
				t.Fatalf("role changed: %+v", got)
			}
		}},
		{"delete role", http.MethodDelete, "/authorization-roles/" + role.ID, "", nil, func(t *testing.T) {
			if _, err := store.GetAuthorizationRole(ctx, role.ID); err != nil {
				t.Fatalf("role deleted: %v", err)
			}
		}},
		{"create template", http.MethodPost, "/authorization-templates", `{"name":"created-template-` + suffix + `","grants":[{"principal":"user:created","scopes":["repositories:read"]}]}`, nil, func(t *testing.T) {
			items, _ := store.ListAuthorizationTemplates(ctx)
			for _, item := range items {
				if item.Name == "created-template-"+suffix {
					t.Fatalf("template created: %+v", item)
				}
			}
		}},
		{"update template", http.MethodPut, "/authorization-templates/" + template.ID, `{"name":"renamed-template-` + suffix + `","grants":[{"principal":"user:renamed","scopes":["repositories:read"]}]}`, map[string]string{"If-Match": template.Version}, func(t *testing.T) {
			if got, _ := store.GetAuthorizationTemplate(ctx, template.ID); got.Name != template.Name || got.Version != template.Version {
				t.Fatalf("template changed: %+v", got)
			}
		}},
		{"apply template", http.MethodPost, "/authorization-templates/" + template.ID + "/apply", `{"repositoryId":"` + repo.ID + `"}`, map[string]string{"If-Match": grants.Version}, func(t *testing.T) {
			if got, _ := store.GetRepositoryGrants(ctx, repo.ID); got.Version != grants.Version || len(got.Grants) != len(grants.Grants) {
				t.Fatalf("grants changed: %+v", got)
			}
		}},
		{"delete template", http.MethodDelete, "/authorization-templates/" + template.ID, "", nil, func(t *testing.T) {
			if _, err := store.GetAuthorizationTemplate(ctx, template.ID); err != nil {
				t.Fatalf("template deleted: %v", err)
			}
		}},
		{"cancel lifecycle job", http.MethodPost, repoPath + "/lifecycle-jobs/" + cancelJob.ID + "/cancel", "", nil, func(t *testing.T) {
			if got, _ := store.GetLifecycleJob(ctx, repo.ID, cancelJob.ID); got.State != cancelJob.State {
				t.Fatalf("job changed: %+v", got)
			}
		}},
		{"run lifecycle job now", http.MethodPost, repoPath + "/lifecycle-jobs/" + runJob.ID + "/run", "", nil, func(t *testing.T) {
			if got, _ := store.GetLifecycleJob(ctx, repo.ID, runJob.ID); !got.NextAttemptAt.Equal(runJob.NextAttemptAt) {
				t.Fatalf("job changed: %+v", got)
			}
		}},
		{"retry lifecycle job", http.MethodPost, repoPath + "/lifecycle-jobs/" + retryJob.ID + "/retry", "", nil, func(t *testing.T) {
			if got, _ := store.GetLifecycleJob(ctx, repo.ID, retryJob.ID); got.State != retryJob.State {
				t.Fatalf("job changed: %+v", got)
			}
		}},
		{"reconcile intelligence jobs", http.MethodPost, repoPath + "/lifecycle-jobs:reconcile-intelligence", "", nil, func(t *testing.T) {
			if got, _ := store.GetLifecycleJob(ctx, repo.ID, intelligenceJob.ID); got.State != intelligenceJob.State {
				t.Fatalf("job requeued: %+v", got)
			}
		}},
		{"artifact quarantine", http.MethodPut, repoPath + "/artifact-quarantine" + identityOf(quarantined), `{"state":"quarantined","reason":"suspect"}`, map[string]string{"If-Match": "0"}, func(t *testing.T) {
			if _, err := store.GetArtifactQuarantine(ctx, repo.ID, repository.FormatRaw, quarantined.Path, quarantined.Digest); !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("artifact quarantined: %v", err)
			}
		}},
		{"artifact intelligence", http.MethodPut, repoPath + "/artifact-intelligence" + identityOf(intelligent), `{"signatures":[],"sboms":[],"licenses":[]}`, map[string]string{"If-Match": "0"}, func(t *testing.T) {
			if _, err := store.GetArtifactIntelligence(ctx, repo.ID, repository.FormatRaw, intelligent.Path, intelligent.Digest); !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("intelligence stored: %v", err)
			}
		}},
		{"enqueue artifact scan", http.MethodPost, repoPath + "/artifact-scans", `{"coordinate":"` + enqueued.Path + `","digest":"` + enqueued.Digest + `"}`, map[string]string{"Idempotency-Key": "scan-" + suffix}, func(t *testing.T) {
			if _, err := store.GetLatestArtifactScanJob(ctx, repo.ID, repository.FormatRaw, enqueued.Path, enqueued.Digest); !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("scan enqueued: %v", err)
			}
		}},
		{"reconcile artifact scans", http.MethodPost, repoPath + "/artifact-scans:reconcile?limit=100", "", nil, func(t *testing.T) {
			if _, err := store.GetLatestArtifactScanJob(ctx, repo.ID, repository.FormatRaw, reconciled.Path, reconciled.Digest); !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("scan enqueued: %v", err)
			}
		}},
		{"tombstone artifact", http.MethodPost, repoPath + "/tombstones", `{"coordinate":"` + tombstoned.Path + `"}`, nil, func(t *testing.T) {
			if _, err := store.GetRawAsset(ctx, repo.ID, tombstoned.Path); err != nil {
				t.Fatalf("asset tombstoned: %v", err)
			}
			if _, err := store.GetArtifactTombstone(ctx, repo.ID, repository.FormatRaw, tombstoned.Path); !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("tombstone written: %v", err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			response := call(c.method, c.path, c.body, c.headers)
			body := response.Body.String()
			if response.Code != http.StatusInternalServerError || !strings.Contains(body, "internal_error") {
				t.Fatalf("status=%d body=%s; want the mutation's own 500 internal_error, not audit_failed, success or an unrelated failure", response.Code, body)
			}
			c.unchanged(t)
		})
	}
	for _, record := range audits() {
		if record.Actor == "alice" && (record.Repository == repo.Name || strings.Contains(record.Resource, role.ID) || strings.Contains(record.Resource, template.ID)) {
			t.Fatalf("a failed audit left a record: %+v", record)
		}
	}
	// Without the fault a mutation lands together with its audit.
	clearFaults()
	if response := call(http.MethodPut, repoPath+"/capacity", `{"quotaBytes":4096}`, nil); response.Code != http.StatusOK {
		t.Fatalf("capacity after the fault cleared=%d %s", response.Code, response.Body.String())
	}
	if response := call(http.MethodPost, repoPath+"/tombstones", `{"coordinate":"`+tombstoned.Path+`"}`, nil); response.Code != http.StatusNoContent {
		t.Fatalf("tombstone after the fault cleared=%d %s", response.Code, response.Body.String())
	}
	// A replay of an idempotency key changes nothing, so the store writes no audit for it and the
	// handler audits the request itself: both requests are audited.
	scanBody := `{"coordinate":"` + enqueued.Path + `","digest":"` + enqueued.Digest + `"}`
	for range 2 {
		if response := call(http.MethodPost, repoPath+"/artifact-scans", scanBody, map[string]string{"Idempotency-Key": "replay-" + suffix}); response.Code != http.StatusAccepted {
			t.Fatalf("scan enqueue after the fault cleared=%d %s", response.Code, response.Body.String())
		}
	}
	enqueueAudits := 0
	for _, record := range audits() {
		if record.Actor == "alice" && record.Repository == repo.Name && record.Operation == "artifact.scan.enqueue" {
			enqueueAudits++
		}
	}
	if enqueueAudits != 2 {
		t.Fatalf("enqueue and its replay must both be audited, got %d", enqueueAudits)
	}
	operations := map[string]bool{}
	for _, record := range audits() {
		if record.Actor == "alice" && record.Repository == repo.Name {
			operations[record.Operation] = true
		}
	}
	for _, operation := range []string{"capacity.configure", "artifact.tombstone"} {
		if !operations[operation] {
			t.Fatalf("missing audit %q in %v", operation, operations)
		}
	}
}
