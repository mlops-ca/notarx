package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

// provenanceFixture is a Raw source with one published asset and an empty Raw target, with a promoter that
// administers both.
type provenanceFixture struct {
	store          *repository.MemoryStore
	source, target repository.HostedRepository
	asset          repository.RawAsset
	handler        http.Handler
	token          string
}

func newProvenanceFixture(t *testing.T) provenanceFixture {
	t.Helper()
	ctx := context.Background()
	store := repository.NewMemoryStore()
	source, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "prov-source", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "prov-target", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("promoted Raw content")
	sum := sha256.Sum256(body)
	digest := "sha256:" + fmt.Sprintf("%x", sum[:])
	objects := objectstore.NewMemoryStore()
	asset := repository.RawAsset{RepositoryID: source.ID, Path: "releases/widget.txt", Digest: digest, ObjectKey: "native/raw/sha256/" + fmt.Sprintf("%x", sum[:]), Size: int64(len(body)), ContentType: "text/plain"}
	if err = objects.Put(ctx, asset.ObjectKey, body); err != nil {
		t.Fatal(err)
	}
	if _, err = store.PutRawAsset(ctx, asset); err != nil {
		t.Fatal(err)
	}
	grants := []repository.RepositoryGrant{{Principal: "promoter", Scopes: []string{"repositories:admin", "repositories:read"}}}
	for _, repo := range []repository.HostedRepository{source, target} {
		if _, err = store.ReplaceRepositoryGrants(ctx, repo.ID, grants, "1"); err != nil {
			t.Fatal(err)
		}
	}
	authenticator := testAuthenticator()
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: authenticator, Storage: ObjectStores{repository.FormatOCI: objects, repository.FormatRaw: objects}})
	return provenanceFixture{store: store, source: source, target: target, asset: asset, handler: handler, token: authenticator.IssueToken("promoter")}
}

func (f provenanceFixture) promote(t *testing.T, key string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v2/repositories/"+f.source.ID+"/promotions", strings.NewReader(`{"targetRepositoryId":"`+f.target.ID+`","coordinate":"`+f.asset.Path+`","digest":"`+f.asset.Digest+`"}`))
	authorize(request, f.token)
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func (f provenanceFixture) list(t *testing.T, query, token string) adminopenapi.ArtifactPromotionPage {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v2/artifact-promotions"+query, nil)
	authorize(request, token)
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list %s = %d %s", query, response.Code, response.Body.String())
	}
	var page adminopenapi.ArtifactPromotionPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestManualPromotionWritesOneProvenanceRowAndReplayReturnsTheSameRow(t *testing.T) {
	f := newProvenanceFixture(t)
	first := f.promote(t, "provenance-key")
	if first.Code != http.StatusAccepted || first.Header().Get("Notarx-Promotion-Id") == "" {
		t.Fatalf("promotion=%d id=%q %s", first.Code, first.Header().Get("Notarx-Promotion-Id"), first.Body.String())
	}
	replay := f.promote(t, "provenance-key")
	if replay.Code != http.StatusAccepted || replay.Header().Get("Notarx-Promotion-Id") != first.Header().Get("Notarx-Promotion-Id") || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay=%d id=%q body=%s want id=%q body=%s", replay.Code, replay.Header().Get("Notarx-Promotion-Id"), replay.Body.String(), first.Header().Get("Notarx-Promotion-Id"), first.Body.String())
	}
	page := f.list(t, "", f.token)
	if len(page.Items) != 1 {
		t.Fatalf("a promotion and its replay wrote %d rows", len(page.Items))
	}
	row := page.Items[0]
	if row.Id.String() != first.Header().Get("Notarx-Promotion-Id") || row.TargetRepositoryId.String() != f.target.ID || row.Format != adminopenapi.Format("raw") ||
		row.Coordinate != f.asset.Path || row.Digest != f.asset.Digest || row.OriginRepository != f.source.Name || row.RequestedBy != "promoter" ||
		row.State != adminopenapi.ArtifactPromotionState("queued") || row.Attempt != 1 || row.RuleId != nil || row.JobId == nil ||
		len(row.ChainPath) != 1 || row.ChainPath[0].Repository != f.source.Name || row.ChainPath[0].RuleId != "" || row.ChainPath[0].At.IsZero() || len(row.Approvals) != 0 {
		t.Fatalf("row=%+v", row)
	}
	if other := f.promote(t, "another-key"); other.Code != http.StatusAccepted || other.Header().Get("Notarx-Promotion-Id") == row.Id.String() {
		t.Fatalf("a new key must write a new row: %d %q", other.Code, other.Header().Get("Notarx-Promotion-Id"))
	}
	if page = f.list(t, "?limit=1", f.token); len(page.Items) != 1 || page.NextPageToken == nil {
		t.Fatalf("paging: %+v", page)
	}
	if page = f.list(t, "?state=completed", f.token); len(page.Items) != 0 {
		t.Fatalf("state filter: %+v", page)
	}
}

func TestArtifactPromotionListShowsOnlyReadableRepositories(t *testing.T) {
	f := newProvenanceFixture(t)
	if response := f.promote(t, "k"); response.Code != http.StatusAccepted {
		t.Fatalf("promotion=%d %s", response.Code, response.Body.String())
	}
	authenticator := testAuthenticator()
	stranger := authenticator.IssueToken("stranger")
	if page := f.list(t, "", stranger); len(page.Items) != 0 {
		t.Fatalf("a caller without access saw %+v", page)
	}
	if _, err := f.store.ReplaceRepositoryGrants(context.Background(), f.target.ID, []repository.RepositoryGrant{{Principal: "stranger", Scopes: []string{"repositories:read"}}}, "2"); err != nil {
		t.Fatal(err)
	}
	if page := f.list(t, "", stranger); len(page.Items) != 1 {
		t.Fatalf("a reader of the target saw %+v", page)
	}
	if page := f.list(t, "", "admin-secret"); len(page.Items) != 1 {
		t.Fatalf("the administrator saw %+v", page)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v2/artifact-promotions?limit=0", nil)
	authorize(request, f.token)
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("limit=0 -> %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v2/artifact-promotions?pageToken=bogus", nil)
	authorize(request, f.token)
	response = httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("a bad page token -> %d", response.Code)
	}
}

func TestPromotionIntoAFormatWithoutThePromotionCapabilityIsUnprocessable(t *testing.T) {
	ctx := context.Background()
	f := newProvenanceFixture(t)
	source, err := f.store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "nuget-source", Format: repository.FormatNuGet})
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "nuget-target", Format: repository.FormatNuGet})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v2/repositories/"+source.ID+"/promotions", strings.NewReader(`{"targetRepositoryId":"`+target.ID+`","coordinate":"pkg@1.0.0","digest":"sha256:`+strings.Repeat("a", 64)+`"}`))
	authorize(request, "admin-secret")
	request.Header.Set("Idempotency-Key", "nuget")
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"unsupported_format"`) {
		t.Fatalf("nuget promotion=%d %s", response.Code, response.Body.String())
	}
	if page := f.list(t, "", "admin-secret"); len(page.Items) != 0 {
		t.Fatalf("a refused promotion wrote %+v", page)
	}
	service := PromotionService{registry: promotionFormatRegistry(promotionModuleDeps{Store: f.store})}
	if service.SupportsFormat(repository.FormatNuGet) || service.SupportsFormat(repository.FormatHuggingFace) || !service.SupportsFormat(repository.FormatRaw) {
		t.Fatal("the registry reports the wrong promotion support")
	}
}

func TestPromotionJobRefusedWhenTheTargetPolicyTightensBetweenEnqueueAndExecution(t *testing.T) {
	ctx := context.Background()
	f := newProvenanceFixture(t)
	if response := f.promote(t, "tighten"); response.Code != http.StatusAccepted {
		t.Fatalf("promotion=%d %s", response.Code, response.Body.String())
	}
	policy, err := f.store.GetRepositorySecurityPolicy(ctx, f.target.ID)
	if err != nil {
		t.Fatal(err)
	}
	version := policy.Version
	policy.Enabled, policy.RequireSignature = true, true
	if _, err = f.store.ReplaceRepositorySecurityPolicy(ctx, f.target.ID, policy, version); err != nil {
		t.Fatal(err)
	}
	registry := promotionFormatRegistry(promotionModuleDeps{Store: f.store})
	publisher, ok := formatmodule.Capability[formatmodule.PromotionPublisher](registry, repository.FormatRaw)
	if !ok {
		t.Fatal("Raw does not declare PromotionPublisher")
	}
	if err = publisher.RunPromotionJobs(ctx, 10); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err = f.store.GetRawAsset(ctx, f.target.ID, f.asset.Path); err == nil {
		t.Fatal("a promotion that the tightened policy denies published the artifact")
	}
	jobs, err := f.store.ListLifecycleJobs(ctx, f.target.ID, 10)
	if err != nil || len(jobs) != 1 || jobs[0].State != repository.LifecycleJobRetrying || !strings.HasPrefix(jobs[0].LastError, repository.PromotionPolicyRefusalPrefix) || !strings.Contains(jobs[0].LastError, "signature_required") {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	if page := f.list(t, "", f.token); len(page.Items) != 1 || page.Items[0].State != adminopenapi.ArtifactPromotionState("queued") {
		t.Fatalf("a refused attempt that will retry stays queued: %+v", page)
	}
}

func TestRegistryRegistersAllNineFormatsAndDeclaresTheirCapabilities(t *testing.T) {
	registry := promotionFormatRegistry(promotionModuleDeps{Store: repository.NewMemoryStore()})
	core := []repository.Format{repository.FormatOCI, repository.FormatMaven, repository.FormatNPM, repository.FormatPyPI, repository.FormatGo, repository.FormatCargo, repository.FormatRaw, repository.FormatConan, repository.FormatAPT}
	if len(registry.Formats()) != len(core) {
		t.Fatalf("registered formats %v", registry.Formats())
	}
	for _, format := range core {
		capabilities := strings.Join(registry.Capabilities(format), ",")
		if !strings.HasPrefix(capabilities, "promotion,promotion-publisher") {
			t.Errorf("%s declares %q", format, capabilities)
		}
	}
	// The optional capabilities are declared where the format package can honour them.
	for format, want := range map[repository.Format]string{
		repository.FormatRaw:   "promotion,promotion-publisher,importer,release-set",
		repository.FormatMaven: "promotion,promotion-publisher,release-set",
		repository.FormatNPM:   "promotion,promotion-publisher,tagged",
		repository.FormatOCI:   "promotion,promotion-publisher,tagged",
	} {
		if got := strings.Join(registry.Capabilities(format), ","); got != want {
			t.Errorf("%s declares %q, want %q", format, got, want)
		}
	}
	if registry.Capabilities(repository.FormatNuGet) != nil {
		t.Error("an unregistered format declares capabilities")
	}
}

func TestEveryRegisteredFormatEnqueuesAPromotionWithProvenance(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	registry := promotionFormatRegistry(promotionModuleDeps{Store: store})
	digest := "sha256:" + strings.Repeat("a", 64)
	coordinates := map[repository.Format]string{
		repository.FormatRaw: "releases/a.txt", repository.FormatOCI: "team/app", repository.FormatMaven: "com.example:lib:1.0.0",
		repository.FormatNPM: "left-pad@1.0.0", repository.FormatPyPI: "demo@1.0.0", repository.FormatGo: "example.com/mod@v1.0.0",
		repository.FormatCargo: "demo@1.0.0", repository.FormatConan: "pkg/1.0@user/stable#rrev",
		repository.FormatAPT: "pool/main/h/hello/hello_1.0_amd64.deb",
	}
	for _, format := range registry.Formats() {
		publisher, ok := formatmodule.Capability[formatmodule.PromotionPublisher](registry, format)
		if !ok {
			t.Fatalf("%s has no promotion publisher", format)
		}
		target := uuid.NewString()
		ctx := repository.WithArtifactPromotion(formatmodule.WithPromotionOptions(ctx, formatmodule.PromotionOptions{Actor: "alice", APTTargetSuite: "stable"}),
			repository.ArtifactPromotion{ID: uuid.NewString(), Format: format, Coordinate: coordinates[format], Digest: digest, RequestedBy: "alice", IdempotencyKey: "manual:" + target + ":k"})
		job, _, err := publisher.EnqueuePromotion(ctx, uuid.NewString(), target, coordinates[format], digest, "k")
		if err != nil || job.Kind != repository.LifecycleJobPromotion {
			t.Fatalf("%s enqueue job=%+v err=%v", format, job, err)
		}
		row, err := store.GetArtifactPromotionByJob(ctx, job.ID)
		if err != nil || row.Format != format || row.Coordinate != coordinates[format] || row.State != repository.ArtifactPromotionQueued {
			t.Fatalf("%s row=%+v err=%v", format, row, err)
		}
	}
}

func TestNPMAndMavenModulesDeclareTheirOptionalCapabilities(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	registry := promotionFormatRegistry(promotionModuleDeps{Store: store})
	repositoryID := uuid.NewString()
	digest := "sha256:" + strings.Repeat("c", 64)
	if _, err := store.PublishNPMVersion(ctx, repository.NPMVersion{RepositoryID: repositoryID, PackageName: "left-pad", Version: "1.0.0", Digest: digest, ObjectKey: "native/npm/c"}, map[string]string{"latest": "1.0.0", "stable": "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	tagged, ok := formatmodule.Capability[formatmodule.Tagged](registry, repository.FormatNPM)
	if !ok {
		t.Fatal("npm does not declare Tagged")
	}
	tags, err := taggedTags(ctx, tagged, repositoryID, "left-pad@1.0.0", digest)
	sort.Strings(tags)
	if err != nil || strings.Join(tags, ",") != "latest,stable" {
		t.Fatalf("tags=%v err=%v", tags, err)
	}
	if _, err = taggedTags(ctx, tagged, repositoryID, "not a coordinate", digest); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("invalid coordinate err=%v", err)
	}
	releases, ok := formatmodule.Capability[formatmodule.ReleaseSet](registry, repository.FormatMaven)
	if !ok {
		t.Fatal("Maven does not declare ReleaseSet")
	}
	if _, err = releases.ReleaseMembers(ctx, repositoryID, "com.example:lib:1.0.0"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("unknown release err=%v", err)
	}
}

// prunedJobStore is a gateway store whose lifecycle jobs have been pruned while the provenance rows remain: a job is
// never found again, so an enqueue under a used key writes a fresh job that meets the provenance row's unique key.
type prunedJobStore struct{ GatewayStore }

func (p prunedJobStore) EnqueueLifecycleJob(ctx context.Context, job repository.LifecycleJob, audit ...repository.AuditRecord) (repository.LifecycleJob, bool, error) {
	job.IdempotencyKey += "#after-prune"
	return p.GatewayStore.EnqueueLifecycleJob(ctx, job, audit...)
}

func (prunedJobStore) GetLifecycleJob(context.Context, string, string) (repository.LifecycleJob, error) {
	return repository.LifecycleJob{}, repository.ErrNotFound
}

func TestManualPromotionReplayAfterTheJobIsPrunedReturnsTheStoredOutcome(t *testing.T) {
	f := newProvenanceFixture(t)
	first := f.promote(t, "pruned-key")
	if first.Code != http.StatusAccepted {
		t.Fatalf("promotion=%d %s", first.Code, first.Body.String())
	}
	pruned := prunedJobStore{f.store}
	service := PromotionService{store: pruned, securityPolicies: f.store, intelligence: f.store, audit: f.store, promotions: f.store,
		registry: promotionFormatRegistry(promotionModuleDeps{Store: pruned}),
		quarantine: func(context.Context, Principal, repository.HostedRepository, repository.HostedRepository, string, string) *PromotionError {
			return nil
		}}
	request := PromotionRequest{Principal: Principal{Actor: "promoter"}, Source: f.source, TargetID: f.target.ID, Coordinate: f.asset.Path, Digest: f.asset.Digest, IdempotencyKey: "pruned-key"}
	result, perr := service.Request(context.Background(), request)
	if perr != nil || result.Promotion.ID != first.Header().Get("Notarx-Promotion-Id") || result.Job.ID != result.Promotion.JobID || result.Job.ID == "" ||
		result.Job.State != repository.LifecycleJobPending || result.Job.Kind != repository.LifecycleJobPromotion {
		t.Fatalf("replay=%+v err=%v", result, perr)
	}
	request.Coordinate = "releases/other.txt"
	if _, perr = service.Request(context.Background(), request); perr == nil || perr.Status != http.StatusConflict || perr.Code != "idempotency_conflict" {
		t.Fatalf("a key reused for another artifact must conflict: %v", perr)
	}
	if page := f.list(t, "", f.token); len(page.Items) != 1 {
		t.Fatalf("replay wrote %d rows", len(page.Items))
	}
}

func TestManualPromotionKeyReusedForAnotherArtifactIsAConflict(t *testing.T) {
	f := newProvenanceFixture(t)
	if first := f.promote(t, "reused"); first.Code != http.StatusAccepted {
		t.Fatalf("promotion=%d %s", first.Code, first.Body.String())
	}
	f.asset.Path = "releases/other.txt"
	if second := f.promote(t, "reused"); second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "idempotency_conflict") {
		t.Fatalf("reuse=%d %s", second.Code, second.Body.String())
	}
}
