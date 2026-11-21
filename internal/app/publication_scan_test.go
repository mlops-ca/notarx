package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/events"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/scanning"
)

type unavailablePublicationRepositoryStore struct {
	*repository.MemoryStore
}

func (unavailablePublicationRepositoryStore) GetHostedRepository(context.Context, string) (repository.HostedRepository, error) {
	return repository.HostedRepository{}, repository.ErrNotFound
}

type failingPublicationEnqueueStore struct {
	*repository.MemoryStore
	enqueueErr      error
	auditContextErr error
}

func (s *failingPublicationEnqueueStore) EnqueueLifecycleJob(context.Context, repository.LifecycleJob, ...repository.AuditRecord) (repository.LifecycleJob, bool, error) {
	return repository.LifecycleJob{}, false, s.enqueueErr
}

func (s *failingPublicationEnqueueStore) RecordAudit(ctx context.Context, record repository.AuditRecord) error {
	if err := ctx.Err(); err != nil {
		s.auditContextErr = err
		return err
	}
	return s.MemoryStore.RecordAudit(ctx, record)
}

func publicationScanDependencies(dependencies Dependencies, format repository.Format) Dependencies {
	dependencies.ArtifactScanner = scanning.ScannerFunc(func(context.Context, scanning.Artifact) (scanning.Report, error) {
		return scanning.Report{}, nil
	})
	dependencies.ArtifactScannerFormats = []repository.Format{format}
	return dependencies
}

// publicationScanDispatcher is the consumer side of the publish events: Raw, OCI and Maven emit artifact.published in the
// publish transaction and the scan scheduler schedules the scan when it reads the event, not the publish request. The first
// pass creates the consumer's cursor, so build the dispatcher before the publication it should see.
func publicationScanDispatcher(t *testing.T, store interface {
	publicationScanStore
	events.Source
}, format repository.Format) *events.Dispatcher {
	t.Helper()
	dispatcher := &events.Dispatcher{Source: store, Settle: time.Nanosecond, Consumers: []events.Consumer{NewPublicationScanScheduler(store, true, []repository.Format{format}, nil).Consumer()}}
	if _, err := dispatcher.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func enablePublicationScan(t *testing.T, store *repository.MemoryStore, repositoryID string) {
	t.Helper()
	policy := repository.DefaultRepositorySecurityPolicy()
	policy.AutoScanOnPublish = true
	if _, err := store.ReplaceRepositorySecurityPolicy(context.Background(), repositoryID, policy, policy.Version); err != nil {
		t.Fatal(err)
	}
}

func requirePublicationScan(t *testing.T, store *repository.MemoryStore, repositoryID string, format repository.Format, coordinate, digest string) {
	t.Helper()
	jobs, err := store.ListLifecycleJobs(context.Background(), repositoryID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.Kind != repository.LifecycleJobScan {
			continue
		}
		var payload repository.ArtifactScanPayload
		if err = json.Unmarshal(job.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Format == format && payload.Coordinate == coordinate && payload.Digest == digest {
			return
		}
	}
	t.Fatalf("missing publication scan format=%q coordinate=%q digest=%q in %#v", format, coordinate, digest, jobs)
}

// A node that enables scanning later still scans what was published while it did not: the consumer's first pass reads
// back over the retention window of the event log, and only repositories with autoScanOnPublish are scheduled.
func TestPublicationScanConsumerCatchesUpOnPublicationsMadeBeforeItStarted(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	auto, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: "auto", Name: "auto", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	manual, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: "manual", Name: "manual", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	enablePublicationScan(t, store, auto.ID)
	for _, repo := range []repository.HostedRepository{auto, manual} {
		if _, err = store.PutRawAsset(ctx, repository.RawAsset{RepositoryID: repo.ID, Path: "a.txt", Digest: "sha256:c1", ObjectKey: "k", Size: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if consumer := NewPublicationScanScheduler(store, true, []repository.Format{repository.FormatRaw}, nil).Consumer(); consumer.CatchUp < 24*time.Hour {
		t.Fatalf("catch-up window = %v, want the retention window of the event log", consumer.CatchUp)
	}
	// The scan consumer starts only now, after both publications.
	dispatcher := &events.Dispatcher{Source: store, Settle: time.Nanosecond, Consumers: []events.Consumer{NewPublicationScanScheduler(store, true, []repository.Format{repository.FormatRaw}, nil).Consumer()}}
	if _, err = dispatcher.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	requirePublicationScan(t, store, auto.ID, repository.FormatRaw, "a.txt", "sha256:c1")
	jobs, err := store.ListLifecycleJobs(ctx, manual.ID, 100)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("a repository without auto scan was scheduled: %v %v", jobs, err)
	}
	// A second pass finds nothing new, and a restarted dispatcher keeps its cursor.
	if n, err := dispatcher.RunOnce(ctx); err != nil || n != 0 {
		t.Fatalf("second pass handled %d, %v", n, err)
	}
}

func TestPublicationScanSchedulerEnqueuesEachPublishedDigestOnce(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	repo, err := store.CreateHostedRepository(ctx, repository.HostedRepository{
		ID: "raw-releases", Name: "raw-releases", Format: repository.FormatRaw,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := repository.DefaultRepositorySecurityPolicy()
	policy.AutoScanOnPublish = true
	if _, err = store.ReplaceRepositorySecurityPolicy(ctx, repo.ID, policy, policy.Version); err != nil {
		t.Fatal(err)
	}

	scheduler := NewPublicationScanScheduler(store, true, []repository.Format{repository.FormatRaw}, nil)
	firstDigest := testScanDigest("first publication")
	for range 2 {
		if err = scheduler.Schedule(ctx, repo, "releases/widget.tar.gz", firstDigest, "publisher"); err != nil {
			t.Fatal(err)
		}
	}
	if err = scheduler.Schedule(ctx, repo, "releases/widget.tar.gz", testScanDigest("second publication"), "publisher"); err != nil {
		t.Fatal(err)
	}

	jobs, err := store.ListLifecycleJobs(ctx, repo.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("scan jobs = %d, want 2", len(jobs))
	}
	for _, job := range jobs {
		if job.Kind != repository.LifecycleJobScan {
			t.Fatalf("job kind = %q, want %q", job.Kind, repository.LifecycleJobScan)
		}
		if len(job.IdempotencyKey) != len("publish-scan:")+64 {
			t.Fatalf("idempotency key = %q", job.IdempotencyKey)
		}
	}
}

func TestPublicationScanSchedulerRecordsRepositoryLookupFailures(t *testing.T) {
	store := unavailablePublicationRepositoryStore{MemoryStore: repository.NewMemoryStore()}
	metrics := &Metrics{}
	scheduler := NewPublicationScanScheduler(store, true, []repository.Format{repository.FormatMaven}, metrics)
	err := scheduler.ScheduleRepository(context.Background(), "missing-repository", repository.FormatMaven, "org.example:widget:1.0.0", testScanDigest("widget"), "publisher")
	if err == nil {
		t.Fatal("ScheduleRepository() error = nil, want repository lookup failure")
	}
	if len(store.Audits) != 1 || store.Audits[0].Operation != "artifact.scan.auto_enqueue" || store.Audits[0].Outcome != repository.AuditStorageError || store.Audits[0].Repository != "missing-repository" {
		t.Fatalf("lookup failure audits = %#v", store.Audits)
	}
	response := httptest.NewRecorder()
	metrics.Handler(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), `notarx_background_operations_total{kind="scan",format="maven",outcome="failed"} 1`) {
		t.Fatalf("lookup failure metrics = %s", response.Body.String())
	}
}

func TestRawPublicationSucceedsWhenScanEnqueueFails(t *testing.T) {
	ctx := context.Background()
	enqueueErr := errors.New("scan queue unavailable")
	store := &failingPublicationEnqueueStore{MemoryStore: repository.NewMemoryStore(), enqueueErr: enqueueErr}
	repo, err := store.CreateHostedRepository(ctx, repository.HostedRepository{
		ID: "raw-releases", Name: "raw-releases", Format: repository.FormatRaw,
	})
	if err != nil {
		t.Fatal(err)
	}
	enablePublicationScan(t, store.MemoryStore, repo.ID)
	dispatcher := publicationScanDispatcher(t, store, repository.FormatRaw)
	handler := newTestGateway(t, GatewayOptions{Dependencies: publicationScanDependencies(Dependencies{}, repository.FormatRaw), Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator(), Storage: ObjectStores{repository.FormatOCI: objectstore.NewMemoryStore(), repository.FormatRaw: objectstore.NewMemoryStore()}})

	request := httptest.NewRequest(http.MethodPut, "/raw/raw-releases/releases/widget.tar.gz", bytes.NewBufferString("published artifact"))
	authorize(request, "resolver-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("publish status = %d body=%s", response.Code, response.Body.String())
	}
	// A failing scan enqueue no longer touches the publish: the publication is committed with its event, and the scheduler
	// reports the failure, audits it and retries the event.
	if _, dispatchErr := dispatcher.RunOnce(ctx); !errors.Is(dispatchErr, enqueueErr) {
		t.Fatalf("dispatch error = %v, want %v", dispatchErr, enqueueErr)
	}
	if store.auditContextErr != nil {
		t.Fatalf("failure audit context error = %v", store.auditContextErr)
	}
	if len(store.Audits) != 1 {
		t.Fatalf("publication audits = %#v", store.Audits)
	}
	last := store.Audits[0]
	if last.Operation != "artifact.scan.auto_enqueue" || last.Outcome != repository.AuditStorageError || last.AuthorizationReason != enqueueErr.Error() {
		t.Fatalf("scan enqueue failure audit = %#v", last)
	}
}

func TestPublicationScanFailureAuditSurvivesRequestCancellation(t *testing.T) {
	enqueueErr := errors.New("scan queue unavailable")
	store := &failingPublicationEnqueueStore{MemoryStore: repository.NewMemoryStore(), enqueueErr: enqueueErr}
	repo, err := store.CreateHostedRepository(context.Background(), repository.HostedRepository{
		ID: "raw-releases", Name: "raw-releases", Format: repository.FormatRaw,
	})
	if err != nil {
		t.Fatal(err)
	}
	enablePublicationScan(t, store.MemoryStore, repo.ID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	scheduler := NewPublicationScanScheduler(store, true, []repository.Format{repository.FormatRaw}, nil)
	if err = scheduler.Schedule(ctx, repo, "releases/widget.tar.gz", testScanDigest("widget"), "publisher"); !errors.Is(err, enqueueErr) {
		t.Fatalf("Schedule() error = %v, want %v", err, enqueueErr)
	}
	if store.auditContextErr != nil || len(store.Audits) != 1 || store.Audits[0].Outcome != repository.AuditStorageError {
		t.Fatalf("failure audits = %#v context error = %v", store.Audits, store.auditContextErr)
	}
}

func TestRawPublicationEnqueuesConfiguredArtifactScan(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	repo, err := store.CreateHostedRepository(ctx, repository.HostedRepository{
		ID: "raw-releases", Name: "raw-releases", Format: repository.FormatRaw,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := repository.DefaultRepositorySecurityPolicy()
	policy.AutoScanOnPublish = true
	if _, err = store.ReplaceRepositorySecurityPolicy(ctx, repo.ID, policy, policy.Version); err != nil {
		t.Fatal(err)
	}
	dispatcher := publicationScanDispatcher(t, store, repository.FormatRaw)
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{

		ArtifactScanner: scanning.ScannerFunc(func(context.Context, scanning.Artifact) (scanning.Report, error) {
			return scanning.Report{}, nil
		}),
		ArtifactScannerFormats: []repository.Format{repository.FormatRaw},
	}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator(), Storage: ObjectStores{repository.FormatOCI: objectstore.NewMemoryStore(), repository.FormatRaw: objectstore.NewMemoryStore()}},
	)

	request := httptest.NewRequest(http.MethodPut, "/raw/raw-releases/releases/widget.tar.gz", bytes.NewBufferString("published artifact"))
	authorize(request, "resolver-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("publish status = %d body=%s", response.Code, response.Body.String())
	}
	if jobs, _ := store.ListLifecycleJobs(ctx, repo.ID, 100); len(jobs) != 0 {
		t.Fatalf("the publish request scheduled a scan itself: %#v", jobs)
	}
	if handled, dispatchErr := dispatcher.RunOnce(ctx); dispatchErr != nil || handled != 1 {
		t.Fatalf("dispatched %d events (%v)", handled, dispatchErr)
	}

	jobs, err := store.ListLifecycleJobs(ctx, repo.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Kind != repository.LifecycleJobScan {
		t.Fatalf("scan jobs = %#v", jobs)
	}
	var payload repository.ArtifactScanPayload
	if err = json.Unmarshal(jobs[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Format != repository.FormatRaw || payload.Coordinate != "releases/widget.tar.gz" || payload.Digest == "" {
		t.Fatalf("scan payload = %#v", payload)
	}
}

func TestConanPublicationEnqueuesRecipeAndPackageScans(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	objects := objectstore.NewMemoryStore()
	repo, err := store.CreateHostedRepository(ctx, repository.HostedRepository{
		ID: uuid.NewString(), Name: "conan-releases", Format: repository.FormatConan,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := repository.DefaultRepositorySecurityPolicy()
	policy.AutoScanOnPublish = true
	if _, err = store.ReplaceRepositorySecurityPolicy(ctx, repo.ID, policy, policy.Version); err != nil {
		t.Fatal(err)
	}
	scans := publicationScanDispatcher(t, store, repository.FormatConan)
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{

		ArtifactScanner: scanning.ScannerFunc(func(context.Context, scanning.Artifact) (scanning.Report, error) {
			return scanning.Report{}, nil
		}),
		ArtifactScannerFormats: []repository.Format{repository.FormatConan},
	}, Store: store, ConanPublications: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator(), Storage: ObjectStores{repository.FormatConan: objects}},
	)

	recipe, recipeBodies := conanRecipePublicationFixture("pkg/1.0/user/stable", []byte("recipe"))
	publishConanPublicationFixture(t, handler, repo.ID, "admin-secret", recipe, recipeBodies)
	pkg, packageBodies := conanPackagePublicationFixture(t, recipe.Reference, recipe.RecipeRevision)
	publishConanPublicationFixture(t, handler, repo.ID, "admin-secret", pkg, packageBodies)
	if _, err = scans.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	jobs, err := store.ListLifecycleJobs(ctx, repo.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("scan jobs = %d, want 2", len(jobs))
	}
	coordinates := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		var payload repository.ArtifactScanPayload
		if err = json.Unmarshal(job.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Format != repository.FormatConan || payload.Digest == "" {
			t.Fatalf("scan payload = %#v", payload)
		}
		coordinates[payload.Coordinate] = true
	}
	for _, coordinate := range []string{
		recipe.Reference + "#" + recipe.RecipeRevision,
		pkg.Reference + "#" + pkg.RecipeRevision + "/" + pkg.PackageID + "#" + pkg.PackageRevision,
	} {
		if !coordinates[coordinate] {
			t.Fatalf("missing scan coordinate %q in %#v", coordinate, coordinates)
		}
	}
}

func publishConanPublicationFixture(t *testing.T, handler http.Handler, repositoryID, token string, declaration nativeConanPublishRequest, bodies map[string][]byte) {
	t.Helper()
	requestBody, err := json.Marshal(declaration)
	if err != nil {
		t.Fatal(err)
	}
	create := httptest.NewRequest(http.MethodPost, "/api/v2/repositories/"+repositoryID+"/conan-publish-sessions", bytes.NewReader(requestBody))
	authorize(create, token)
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create Conan publish session = %d body=%s", created.Code, created.Body.String())
	}
	sessionID := requirePublishSessionContractKeys(t, created.Body.Bytes(), len(declaration.Objects))
	for name, body := range bodies {
		upload := httptest.NewRequest(http.MethodPut, "/api/v2/conan-publish-sessions/"+sessionID+"/objects/"+name, bytes.NewReader(body))
		authorize(upload, token)
		uploaded := httptest.NewRecorder()
		handler.ServeHTTP(uploaded, upload)
		if uploaded.Code != http.StatusNoContent {
			t.Fatalf("upload Conan object %s = %d body=%s", name, uploaded.Code, uploaded.Body.String())
		}
	}
	commit := httptest.NewRequest(http.MethodPost, "/api/v2/conan-publish-sessions/"+sessionID+":commit", nil)
	authorize(commit, token)
	committed := httptest.NewRecorder()
	handler.ServeHTTP(committed, commit)
	if committed.Code != http.StatusOK {
		t.Fatalf("commit Conan publication = %d body=%s", committed.Code, committed.Body.String())
	}
}

func TestPublicationScanSchedulerSkipsRepositoriesWithoutEnabledCapability(t *testing.T) {
	tests := []struct {
		name             string
		autoScan         bool
		scannerAvailable bool
		repositoryFormat repository.Format
		repositoryType   repository.RepositoryType
		formats          []repository.Format
	}{
		{name: "policy disabled", scannerAvailable: true, formats: []repository.Format{repository.FormatRaw}},
		{name: "scanner unavailable", autoScan: true, formats: []repository.Format{repository.FormatRaw}},
		{name: "format disabled", autoScan: true, scannerAvailable: true, formats: []repository.Format{repository.FormatMaven}},
		{name: "APT proxy has no publication hook", autoScan: true, scannerAvailable: true, repositoryFormat: repository.FormatAPT, repositoryType: repository.RepositoryTypeProxy, formats: []repository.Format{repository.FormatAPT}},
		{name: "proxy repository", autoScan: true, scannerAvailable: true, repositoryFormat: repository.FormatNPM, repositoryType: repository.RepositoryTypeProxy, formats: []repository.Format{repository.FormatNPM}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store := repository.NewMemoryStore()
			format := tt.repositoryFormat
			if format == "" {
				format = repository.FormatRaw
			}
			candidate := repository.HostedRepository{ID: "scan-releases", Name: "scan-releases", Format: format, Type: tt.repositoryType}
			if tt.repositoryType == repository.RepositoryTypeProxy {
				candidate.Endpoint = "https://upstream.example"
				candidate.AllowedHosts = []string{"upstream.example"}
			}
			repo, err := store.CreateHostedRepository(ctx, candidate)
			if err != nil {
				t.Fatal(err)
			}
			policy := repository.DefaultRepositorySecurityPolicy()
			policy.AutoScanOnPublish = tt.autoScan
			if _, err = store.ReplaceRepositorySecurityPolicy(ctx, repo.ID, policy, policy.Version); err != nil {
				t.Fatal(err)
			}

			scheduler := NewPublicationScanScheduler(store, tt.scannerAvailable, tt.formats, nil)
			if err = scheduler.Schedule(ctx, repo, "releases/widget.tar.gz", testScanDigest(tt.name), "publisher"); err != nil {
				t.Fatal(err)
			}
			jobs, err := store.ListLifecycleJobs(ctx, repo.ID, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) != 0 {
				t.Fatalf("scan jobs = %d, want 0", len(jobs))
			}
		})
	}
}

func TestPublicationScanSchedulerConsumesPublishedEvents(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	repo, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: "raw-events", Name: "raw-events", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewPublicationScanScheduler(store, true, []repository.Format{repository.FormatRaw}, nil)
	consumer := scheduler.Consumer()
	if consumer.Name != PublicationScanConsumer || len(consumer.Types) != 1 || consumer.Types[0] != events.ArtifactPublished {
		t.Fatalf("consumer = %+v", consumer)
	}
	digest := testScanDigest("event publication")
	published := events.Event{Type: events.ArtifactPublished, RepositoryID: repo.ID, Format: "raw", Coordinate: "releases/a.tgz", Digest: digest, Payload: []byte(`{"actor":"publisher"}`)}

	// autoScanOnPublish is off by default: the event is consumed and nothing is scheduled.
	if err = consumer.Handler.HandleEvent(ctx, published); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := store.ListLifecycleJobs(ctx, repo.ID, 10); len(jobs) != 0 {
		t.Fatalf("scan scheduled with autoScanOnPublish off: %v", jobs)
	}
	enablePublicationScan(t, store, repo.ID)
	for range 2 {
		if err = consumer.Handler.HandleEvent(ctx, published); err != nil {
			t.Fatal(err)
		}
	}
	requirePublicationScan(t, store, repo.ID, repository.FormatRaw, "releases/a.tgz", digest)
	if jobs, _ := store.ListLifecycleJobs(ctx, repo.ID, 10); len(jobs) != 1 {
		t.Fatalf("re-delivery of an event enqueued %d scans", len(jobs))
	}
	// Other types are ignored; a missing repository is skipped for good rather than retried.
	if err = consumer.Handler.HandleEvent(ctx, events.Event{Type: events.ArtifactReleased}); err != nil {
		t.Fatal(err)
	}
	missing := published
	missing.RepositoryID = "gone"
	err = consumer.Handler.HandleEvent(ctx, missing)
	if err == nil || !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("missing repository error = %v", err)
	}
	dispatcher := &events.Dispatcher{Source: store, Settle: time.Nanosecond, Consumers: []events.Consumer{consumer}}
	if _, err = dispatcher.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
}
