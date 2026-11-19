//go:build integration

package aptpublication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

type failOnceDeleteStore struct {
	objectstore.Store
	mu        sync.Mutex
	remaining int
}

func (s *failOnceDeleteStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	if s.remaining > 0 {
		s.remaining--
		s.mu.Unlock()
		return errors.New("injected RustFS delete failure")
	}
	s.mu.Unlock()
	return s.Store.Delete(ctx, key)
}

// isolateReclaimQueue cancels every pending or retrying APT reclaim job that does not belong to the test
// repository, with the fields CancelLifecycleJob sets, and requires the test's own job to be queued. The
// lifecycle queue is shared by every test of the database (the integration lane runs packages one at a time,
// -p 1), and Schedule also expires and enqueues the sessions of other tests because its clock is an hour
// ahead. The claim orders by next_attempt_at alone, so without this a limit-1 claim could take a foreign job,
// or miss the test's own when a foreign retrying job fell due mid-test.
func isolateReclaimQueue(ctx context.Context, t *testing.T, db *sql.DB, repositoryID string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `UPDATE lifecycle_jobs SET state='cancelled',completed_at=now(),next_attempt_at=NULL
		WHERE kind='reclaim' AND payload->>'format'='apt' AND state IN ('pending','retrying') AND repository_id<>$1::uuid`, repositoryID); err != nil {
		t.Fatal(err)
	}
	var own int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM lifecycle_jobs
		WHERE kind='reclaim' AND state IN ('pending','retrying') AND repository_id=$1::uuid`, repositoryID).Scan(&own); err != nil {
		t.Fatal(err)
	}
	if own == 0 {
		t.Fatal("Schedule did not enqueue the test's reclaim job: its batch limit was spent on other sessions")
	}
}

func TestPostgresRustFSAPTReclaimSerializesWithPublicationAndRetriesDelete(t *testing.T) {
	databaseURL, endpoint := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_OBJECT_STORE_ENDPOINT")
	accessKey, secretKey := os.Getenv("TEST_OBJECT_STORE_ACCESS_KEY"), os.Getenv("TEST_OBJECT_STORE_SECRET_KEY")
	if databaseURL == "" || endpoint == "" || accessKey == "" || secretKey == "" {
		t.Skip("PostgreSQL and RustFS integration environment is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	storeA, err := repository.NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storeA.Close() }()
	storeB, err := repository.NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storeB.Close() }()
	objects, err := objectstore.NewS3StoreWithCredentials(endpoint, accessKey, secretKey, "apt-reclaim-"+strings.ReplaceAll(uuid.NewString(), "-", "")[:20])
	if err != nil {
		t.Fatal(err)
	}
	if err = objects.EnsureBucket(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	repo, err := storeA.CreateHostedRepository(ctx, repository.HostedRepository{
		ID: uuid.NewString(), Name: "apt-reclaim-" + uuid.NewString(), Format: repository.FormatAPT, Type: repository.RepositoryTypeHosted,
	})
	if err != nil {
		t.Fatal(err)
	}

	deb := testDebianPackage(t, "Package: widget\nVersion: 1.0-1\nArchitecture: amd64\n")
	digestBytes := sha256.Sum256(deb)
	digest := "sha256:" + hex.EncodeToString(digestBytes[:])
	objectKey := "native/apt/sha256/" + strings.TrimPrefix(digest, "sha256:")
	expiresAt := time.Now().UTC().Add(time.Hour)
	abandoned, _, err := storeA.CreateAPTPublicationSessionIdempotently(ctx, repository.APTPublicationSession{
		ID: uuid.NewString(), RepositoryID: repo.ID, Suite: "stable", Component: "main", Publisher: "first-ci",
		ObjectName: "widget_1.0-1_amd64.deb", DeclaredDigest: digest, DeclaredSize: int64(len(deb)),
		State: repository.APTPublicationSessionOpen, ExpiresAt: expiresAt,
	}, "first-ci", "apt-integration", "abandoned", "abandoned")
	if err != nil {
		t.Fatal(err)
	}
	if err = storeA.BeginAPTPackageUpload(ctx, abandoned.ID, objectKey); err != nil {
		t.Fatal(err)
	}
	if err = objects.PutVerifiedReader(ctx, objectKey, bytes.NewReader(deb), int64(len(deb)), digest); err != nil {
		t.Fatal(err)
	}
	maintenanceA := Maintenance{Store: storeA, Objects: objects, Now: func() time.Time { return expiresAt.Add(time.Second) }}
	if err = maintenanceA.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	isolateReclaimQueue(ctx, t, db, repo.ID)

	objectCtx, release, err := repository.LockObjectKeys(ctx, []string{objectKey}, storeA, repository.FormatAPT, storeA.LockAPTObject)
	if err != nil {
		t.Fatal(err)
	}
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- (Maintenance{Store: storeB, Objects: objects}).RunReclaimJobs(ctx, 1)
	}()
	select {
	case workerErr := <-workerDone:
		release()
		t.Fatalf("cross-instance reclaim bypassed the publication object lock: %v", workerErr)
	case <-time.After(200 * time.Millisecond):
	}

	manager := NewManager(storeA, objects)
	publication, _, err := manager.CreateSession(objectCtx, CreateSessionInput{
		RepositoryID: repo.ID, Suite: "stable", Component: "main", Publisher: "release-ci",
		ObjectName: "widget_1.0-1_amd64.deb", DeclaredDigest: digest, DeclaredSize: int64(len(deb)),
		ExpectedIdentity: "widget@1.0-1#amd64", IdempotencyKey: "replacement",
	})
	if err != nil {
		release()
		t.Fatal(err)
	}
	if _, err = manager.UploadPackageAs(objectCtx, publication.ID, publication.ObjectName, bytes.NewReader(deb), int64(len(deb)), "release-operator"); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	select {
	case err = <-workerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("reclaim did not resume after the publication lock was released")
	}
	if _, err = objects.Stat(ctx, objectKey); err != nil {
		t.Fatalf("reclaim deleted an object referenced by a concurrent publication: %v", err)
	}
	audits, err := storeB.ListAudits(ctx, repository.AuditQuery{Repository: repo.Name, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var createdAudit, stagedAudit bool
	for _, audit := range audits {
		createdAudit = createdAudit || audit.Operation == "apt.publication_session.create" && audit.Actor == "release-ci"
		stagedAudit = stagedAudit || audit.Operation == "apt.publication_package.stage" && audit.Actor == "release-operator"
	}
	if !createdAudit || !stagedAudit {
		t.Fatalf("transactional publication audits are missing: %#v", audits)
	}

	orphanBody := []byte("unreferenced APT upload")
	orphanDigestBytes := sha256.Sum256(orphanBody)
	orphanDigest := "sha256:" + hex.EncodeToString(orphanDigestBytes[:])
	orphanKey := "native/apt/sha256/" + strings.TrimPrefix(orphanDigest, "sha256:")
	orphan, _, err := storeA.CreateAPTPublicationSessionIdempotently(ctx, repository.APTPublicationSession{
		ID: uuid.NewString(), RepositoryID: repo.ID, Suite: "stable", Component: "main", Publisher: "failed-ci",
		ObjectName: "orphan_1.0-1_amd64.deb", DeclaredDigest: orphanDigest, DeclaredSize: int64(len(orphanBody)),
		State: repository.APTPublicationSessionOpen, ExpiresAt: expiresAt,
	}, "failed-ci", "apt-integration", "delete-retry", "delete-retry")
	if err != nil {
		t.Fatal(err)
	}
	if err = storeA.BeginAPTPackageUpload(ctx, orphan.ID, orphanKey); err != nil {
		t.Fatal(err)
	}
	if err = objects.PutVerifiedReader(ctx, orphanKey, bytes.NewReader(orphanBody), int64(len(orphanBody)), orphanDigest); err != nil {
		t.Fatal(err)
	}
	if err = maintenanceA.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	isolateReclaimQueue(ctx, t, db, repo.ID)
	failingObjects := &failOnceDeleteStore{Store: objects, remaining: 1}
	maintenanceB := Maintenance{Store: storeB, Objects: failingObjects}
	if err = maintenanceB.RunReclaimJobs(ctx, 1); err == nil {
		t.Fatal("injected RustFS deletion failure was not surfaced")
	}
	jobs, err := storeB.ListLifecycleJobs(ctx, repo.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	var retryJobID string
	for _, job := range jobs {
		if job.State == repository.LifecycleJobRetrying && strings.Contains(string(job.Payload), orphan.ID) {
			retryJobID = job.ID
		}
	}
	if retryJobID == "" {
		t.Fatalf("failed reclaim did not remain retryable: %#v", jobs)
	}
	if _, err = storeB.RunLifecycleJobNow(ctx, repo.ID, retryJobID); err != nil {
		t.Fatal(err)
	}
	if err = maintenanceB.RunReclaimJobs(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = objects.Stat(ctx, orphanKey); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("orphan object remains after retry: %v", err)
	}
}
