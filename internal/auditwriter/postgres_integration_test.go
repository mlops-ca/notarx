//go:build integration

package auditwriter_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/auditwriter"
	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/partitions"
	"github.com/mlops-ca/notarx/internal/repository"
)

type fixture struct {
	store  *repository.PostgresStore
	db     *sql.DB
	writer *auditwriter.Writer
	repo   string
	repoID string
}

func openFixture(t *testing.T, cfg auditwriter.Config) *fixture {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := repository.NewPostgresStore(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db, err := database.OpenPostgres(url, database.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	manager, err := partitions.New(db, partitions.DefaultRegistry(), partitions.Options{Owner: "auditwriter-" + ids.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	f := &fixture{store: store, db: db, repo: "aw-" + ids.New(), repoID: ids.New()}
	if _, err = db.Exec(`INSERT INTO hosted_repositories (id, name, format) VALUES ($1::uuid, $2, 'raw')`, f.repoID, f.repo); err != nil {
		t.Fatal(err)
	}
	cfg.FlushInterval, cfg.UsageFlushInterval = time.Hour, time.Hour
	f.writer = auditwriter.New(store, cfg)
	t.Cleanup(func() {
		_ = f.writer.Shutdown(context.Background())
		ctx := context.Background()
		for _, statement := range []string{
			`DELETE FROM artifact_usage_stats WHERE repository_id=$1::uuid`,
			`DELETE FROM repository_request_rollup WHERE repository_id=$1::uuid`,
			`DELETE FROM hosted_repositories WHERE id=$1::uuid`,
		} {
			_, _ = db.ExecContext(ctx, statement, f.repoID)
		}
		_, _ = db.ExecContext(ctx, `DELETE FROM resolver_audit_log WHERE repository=$1`, f.repo)
		_, _ = db.ExecContext(ctx, `DELETE FROM security_audit_log WHERE repository=$1`, f.repo)
	})
	return f
}

func (f *fixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(fmt.Sprintf(`SELECT count(*) FROM %s WHERE repository=$1`, table), f.repo).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *fixture) download(resource string, at time.Time) repository.AuditRecord {
	return repository.AuditRecord{Repository: f.repo, GroupName: f.repo, Format: "raw", Resource: resource, Operation: "get", Status: 200,
		Outcome: repository.AuditResolved, Actor: "alice", Bytes: 100, OccurredAt: at}
}

// Each durability class reaches its own table: sync records are committed to
// security_audit_log before RecordAudit returns, async records reach resolver_audit_log
// with the next flush.
func TestWriterRoutesEachClassToItsTable(t *testing.T) {
	f := openFixture(t, auditwriter.Config{})
	ctx := context.Background()
	now := time.Now().UTC()
	mutation := repository.AuditRecord{Repository: f.repo, GroupName: f.repo, Format: "management", Operation: "repo update", Outcome: repository.AuditResolved, Actor: "admin", Status: 200, OccurredAt: now}
	denial := repository.AuditRecord{Repository: f.repo, GroupName: f.repo, Format: "raw", Operation: "get", Outcome: repository.AuditAccessDenied, Actor: "alice", Status: 403, OccurredAt: now}
	anonymous := repository.AuditRecord{Repository: f.repo, GroupName: f.repo, Format: "raw", Operation: "get", Outcome: repository.AuditAccessDenied, Actor: repository.AnonymousActor, Status: 401, OccurredAt: now}
	for _, record := range []repository.AuditRecord{mutation, denial, anonymous, f.download("a.bin", now), f.download("b.bin", now)} {
		if err := f.writer.RecordAudit(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if sec, res := f.count(t, "security_audit_log"), f.count(t, "resolver_audit_log"); sec != 2 || res != 0 {
		t.Fatalf("sync class is committed immediately, async is still buffered: security=%d resolver=%d", sec, res)
	}
	if err := f.writer.FlushNow(ctx); err != nil {
		t.Fatal(err)
	}
	if sec, res := f.count(t, "security_audit_log"), f.count(t, "resolver_audit_log"); sec != 2 || res != 3 {
		t.Fatalf("after the flush: security=%d resolver=%d, want 2 and 3", sec, res)
	}
	page, err := f.store.ListAuditPage(ctx, repository.AuditQuery{Repository: f.repo, Limit: 100})
	if err != nil || len(page.Items) != 5 {
		t.Fatalf("the audit API merges both tables: %d %v", len(page.Items), err)
	}
}

func TestWriterAggregatesUsageAndRollups(t *testing.T) {
	f := openFixture(t, auditwriter.Config{})
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < 1000; i++ {
		if err := f.writer.RecordAudit(ctx, f.download("hot.bin", now)); err != nil {
			t.Fatal(err)
		}
	}
	denied := f.download("hot.bin", now)
	denied.Outcome, denied.Status, denied.Actor = repository.AuditAccessDenied, 401, repository.AnonymousActor
	if err := f.writer.RecordAudit(ctx, denied); err != nil {
		t.Fatal(err)
	}
	if err := f.writer.FlushNow(ctx); err != nil {
		t.Fatal(err)
	}
	stats, err := f.store.ListArtifactUsage(ctx, f.repo, 10)
	if err != nil || len(stats) != 1 || stats[0].DownloadCount != 1000 || stats[0].TotalBytes != 100000 || stats[0].LastActor != "alice" {
		t.Fatalf("usage=%+v err=%v", stats, err)
	}
	// A second flush adds to the same row (additive upsert).
	for i := 0; i < 5; i++ {
		_ = f.writer.RecordAudit(ctx, f.download("hot.bin", now.Add(time.Minute)))
	}
	if err := f.writer.FlushNow(ctx); err != nil {
		t.Fatal(err)
	}
	if stats, _ = f.store.ListArtifactUsage(ctx, f.repo, 10); stats[0].DownloadCount != 1005 {
		t.Fatalf("usage must be additive: %+v", stats)
	}
	rows, err := f.db.Query(`SELECT outcome, requests FROM repository_request_rollup WHERE repository_id=$1::uuid ORDER BY outcome`, f.repoID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	got := map[repository.RequestOutcome]int64{}
	for rows.Next() {
		var outcome repository.RequestOutcome
		var requests int64
		if err = rows.Scan(&outcome, &requests); err != nil {
			t.Fatal(err)
		}
		// One row per hour bucket: the second flush can fall into the next hour.
		got[outcome] += requests
	}
	if got[repository.OutcomeAllowed] != 1005 || got[repository.OutcomeUnauthenticated] != 1 {
		t.Fatalf("rollup by outcome: %+v", got)
	}
	statistics, err := f.store.ListRepositoryRequestStatistics(ctx, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range statistics {
		if item.Repository == f.repo {
			if item.Requests.OneDay != 1006 || item.Denied.OneDay != 1 || item.Requests.ThirtyDays != 1006 {
				t.Fatalf("statistics read the rollups: %+v", item)
			}
			return
		}
	}
	t.Fatalf("repository missing from %+v", statistics)
}

// A request path with a NUL byte or invalid UTF-8 is rejected by PostgreSQL as is. The
// writer stores it sanitised (U+FFFD) in the audit row, the evidence and the usage key,
// so a client cannot make its own audit record disappear.
func TestHostileTextCannotStallTheFlush(t *testing.T) {
	f := openFixture(t, auditwriter.Config{})
	ctx := context.Background()
	for _, resource := range []string{"a\x00b", "bad\xff\xfeutf8", "fine"} {
		record := f.download(resource, time.Now().UTC())
		record.Actor, record.Evidence = "al\x00ice\xff", map[string]string{"k\x00ey": "v\xffal\x00ue"}
		if err := f.writer.RecordAudit(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.writer.FlushNow(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, "resolver_audit_log"); n != 3 {
		t.Fatalf("flushed %d records, want all three", n)
	}
	if stats := f.writer.Stats(); stats.DroppedInvalid != 0 || stats.DroppedUsage != 0 {
		t.Fatalf("no record may be dropped: %+v", stats)
	}
	var stored string
	if err := f.db.QueryRow(`SELECT resource FROM resolver_audit_log WHERE repository=$1 AND resource LIKE 'a%b'`, f.repo).Scan(&stored); err != nil || stored != "a\uFFFDb" {
		t.Fatalf("resource stored as %q (%v), want the sanitised form", stored, err)
	}
	var evidence string
	if err := f.db.QueryRow(`SELECT evidence->>'k\uFFFDey' FROM resolver_audit_log WHERE repository=$1 AND resource='fine'`, f.repo).Scan(&evidence); err != nil || evidence != "v\uFFFDal\uFFFDue" {
		t.Fatalf("evidence stored as %q (%v)", evidence, err)
	}
}

func TestStatisticsDoNotScanTheAuditTables(t *testing.T) {
	f := openFixture(t, auditwriter.Config{})
	ctx := context.Background()
	// A row only in the audit table is not counted: statistics come from the rollups.
	if err := f.store.InsertAudit(ctx, f.download("x", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	statistics, err := f.store.ListRepositoryRequestStatistics(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range statistics {
		if item.Repository == f.repo {
			t.Fatalf("statistics must not read the audit tables: %+v", item)
		}
	}
}

func TestRequestRollupsExpireAfterRetention(t *testing.T) {
	f := openFixture(t, auditwriter.Config{})
	ctx := context.Background()
	old := time.Now().UTC().Add(-repository.RequestRollupRetention - 48*time.Hour).Truncate(time.Hour)
	recent := time.Now().UTC().Truncate(time.Hour)
	if err := f.store.FlushUsage(ctx, repository.UsageBatch{Rollups: []repository.RollupDelta{
		{Repository: f.repo, BucketStart: old, Outcome: repository.OutcomeAllowed, Requests: 3},
		{Repository: f.repo, BucketStart: recent, Outcome: repository.OutcomeAllowed, Requests: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.PruneRequestRollups(ctx, time.Now().UTC().Add(-repository.RequestRollupRetention)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.db.QueryRow(`SELECT count(*) FROM repository_request_rollup WHERE repository_id=$1::uuid`, f.repoID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("only the recent bucket survives: %d %v", n, err)
	}
}

func TestShutdownFlushPersistsBufferedRecords(t *testing.T) {
	f := openFixture(t, auditwriter.Config{})
	f.writer.Start()
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if err := f.writer.RecordAudit(ctx, f.download(fmt.Sprintf("f%d", i), time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
	}
	flushCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := f.writer.Shutdown(flushCtx); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, "resolver_audit_log"); n != 50 {
		t.Fatalf("shutdown flushed %d of 50 records", n)
	}
	if stats, err := f.store.ListArtifactUsage(ctx, f.repo, 100); err != nil || len(stats) != 50 {
		t.Fatalf("shutdown flushed usage: %d %v", len(stats), err)
	}
}

func TestStoreRoutesRecordAuditThroughTheInstalledRecorder(t *testing.T) {
	f := openFixture(t, auditwriter.Config{})
	f.store.SetAuditRecorder(f.writer)
	t.Cleanup(func() { f.store.SetAuditRecorder(nil) })
	if err := f.store.RecordAudit(context.Background(), f.download("via-store", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if f.count(t, "resolver_audit_log") != 0 {
		t.Fatal("an async record must be buffered, not written inline")
	}
	if err := f.writer.FlushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.count(t, "resolver_audit_log") != 1 {
		t.Fatal("the buffered record must be flushed")
	}
}
