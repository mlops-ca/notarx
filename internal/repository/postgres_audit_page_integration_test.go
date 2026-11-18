//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/partitions"
)

func openAuditPageStore(t *testing.T) *PostgresStore {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := partitions.New(store.db, partitions.DefaultRegistry(), partitions.Options{Owner: "audit-page-" + uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestPostgresAuditPageMergesSecurityAndResolverTables(t *testing.T) {
	store := openAuditPageStore(t)
	ctx := context.Background()
	repo := "audit-page-" + uuid.NewString()
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	type seed struct {
		class AuditClass
		at    time.Duration
	}
	// Two records share the same occurred_at across the two tables.
	seeds := []seed{{AuditClassAsync, 0}, {AuditClassSync, 0}, {AuditClassAsync, time.Second}, {AuditClassSync, 2 * time.Second}, {"", 3 * time.Second}, {AuditClassSync, 3 * time.Second}}
	for _, s := range seeds {
		record := AuditRecord{Class: s.class, GroupName: "g", Repository: repo, Outcome: AuditResolved, Actor: "alice", Operation: "get", OccurredAt: base.Add(s.at)}
		if err := store.RecordAudit(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	var security, resolver int
	if err := store.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM security_audit_log WHERE repository=$1),(SELECT count(*) FROM resolver_audit_log WHERE repository=$1)`, repo).Scan(&security, &resolver); err != nil || security != 3 || resolver != 3 {
		t.Fatalf("rows per table = %d security, %d resolver, %v", security, resolver, err)
	}

	// Page through everything two at a time: every record once, newest first.
	var got []AuditRecord
	query := AuditQuery{Repository: repo, Limit: 2}
	for page := 0; page < 10; page++ {
		result, err := store.ListAuditPage(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, result.Items...)
		if result.Next == nil {
			break
		}
		query.Before = *result.Next
	}
	if len(got) != 6 {
		t.Fatalf("paged %d records, want 6: %+v", len(got), got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].OccurredAt.After(got[i-1].OccurredAt) {
			t.Fatalf("not newest first at %d: %v", i, got)
		}
	}
	classes := map[AuditClass]int{}
	ids := map[string]bool{}
	for _, record := range got {
		classes[record.Class]++
		key := string(record.Class) + "/" + record.OccurredAt.String() + "/" + string(rune(record.ID))
		if ids[key] {
			t.Fatalf("record returned twice: %+v", record)
		}
		ids[key] = true
	}
	if classes[AuditClassSync] != 3 || classes[AuditClassAsync] != 3 {
		t.Fatalf("classes = %v", classes)
	}

	// The class filter reads one table.
	for _, class := range []AuditClass{AuditClassSync, AuditClassAsync} {
		page, err := store.ListAuditPage(ctx, AuditQuery{Repository: repo, Class: class, Limit: 100})
		if err != nil || len(page.Items) != 3 {
			t.Fatalf("class %s = %d, %v", class, len(page.Items), err)
		}
		for _, record := range page.Items {
			if record.Class != class {
				t.Fatalf("class filter %s returned %s", class, record.Class)
			}
		}
	}
	// Filters compose and an explicit window is honoured.
	page, err := store.ListAuditPage(ctx, AuditQuery{Repository: repo, Actor: "alice", Outcome: string(AuditResolved), From: base.Add(2 * time.Second), To: base.Add(3 * time.Second), Limit: 100})
	if err != nil || len(page.Items) != 3 {
		t.Fatalf("window = %d, %v", len(page.Items), err)
	}
	if page, err = store.ListAuditPage(ctx, AuditQuery{Repository: repo, Actor: "nobody", Limit: 100}); err != nil || len(page.Items) != 0 {
		t.Fatalf("nobody = %d, %v", len(page.Items), err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM resolver_audit_log WHERE repository=$1`, repo)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM security_audit_log WHERE repository=$1`, repo)
	})
}

type explainNode struct {
	NodeType string        `json:"Node Type"`
	Relation string        `json:"Relation Name"`
	Plans    []explainNode `json:"Plans"`
}

func (n explainNode) walk(visit func(explainNode)) {
	visit(n)
	for _, child := range n.Plans {
		child.walk(visit)
	}
}

// explainAuditPage plans the production statement with sequential scans disabled,
// so what remains shows which partitions and which index kinds the query can use.
func explainAuditPage(t *testing.T, store *PostgresStore, query AuditQuery) (relations map[string]bool, nodeTypes map[string]bool) {
	t.Helper()
	statement, args, _ := buildAuditPageQuery(query, time.Now().UTC())
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = tx.QueryRow(`EXPLAIN (FORMAT JSON) `+statement, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []struct {
		Plan explainNode `json:"Plan"`
	}
	if err = json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("explain output: %s %v", raw, err)
	}
	relations, nodeTypes = map[string]bool{}, map[string]bool{}
	plans[0].Plan.walk(func(n explainNode) {
		nodeTypes[n.NodeType] = true
		if n.Relation != "" {
			relations[n.Relation] = true
		}
	})
	return relations, nodeTypes
}

func TestPostgresAuditPageQueriesPrunePartitionsAndUseIndexes(t *testing.T) {
	store := openAuditPageStore(t)
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	nextMonth := monthStart.AddDate(0, 1, 0)
	suffix := monthStart.Format("200601")
	window := AuditQuery{From: monthStart, To: nextMonth.Add(-time.Nanosecond), Limit: 50}

	// Window only, one table: the primary key serves the keyset order, so there is
	// neither a sequential scan nor a sort, and only the month's partition is read.
	query := window
	query.Class = AuditClassAsync
	relations, nodeTypes := explainAuditPage(t, store, query)
	if len(relations) != 1 || !relations["resolver_audit_log_p"+suffix] {
		t.Fatalf("relations = %v, want only resolver_audit_log_p%s", relations, suffix)
	}
	if nodeTypes["Seq Scan"] || nodeTypes["Sort"] {
		t.Fatalf("window query must be an ordered index scan: %v", nodeTypes)
	}

	// Equality filters and a keyset cursor still prune and stay on indexes.
	query = window
	query.Class = AuditClassSync
	query.Repository, query.Actor = "r", "a"
	query.Before = AuditCursor{OccurredAt: now, ID: 1 << 40, Class: AuditClassSync}
	relations, nodeTypes = explainAuditPage(t, store, query)
	if len(relations) != 1 || !relations["security_audit_log_p"+suffix] || nodeTypes["Seq Scan"] {
		t.Fatalf("filtered keyset query: relations %v node types %v", relations, nodeTypes)
	}

	// The merged page reads exactly the month's partition of each table.
	relations, nodeTypes = explainAuditPage(t, store, window)
	if len(relations) != 2 || !relations["security_audit_log_p"+suffix] || !relations["resolver_audit_log_p"+suffix] || nodeTypes["Seq Scan"] {
		t.Fatalf("merged query: relations %v node types %v", relations, nodeTypes)
	}
}

func TestPartitionManagerLeaseRowIsHiddenFromScheduledTasks(t *testing.T) {
	store := openAuditPageStore(t) // runs the partition manager, which creates its lease row
	ctx := context.Background()
	const leaseID = "00000000-0000-7000-8000-0000000000a1"
	var kind string
	if err := store.db.QueryRowContext(ctx, `SELECT kind FROM scheduled_tasks WHERE id=$1::uuid`, leaseID).Scan(&kind); err != nil || kind != "partition-manager" {
		t.Fatalf("lease row = %q, %v", kind, err)
	}
	tasks, err := store.ListScheduledTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.ID == leaseID || task.Kind == "partition-manager" {
			t.Fatalf("the lease row must not be listed: %+v", task)
		}
	}
	if _, err = store.GetScheduledTask(ctx, leaseID); err != ErrNotFound {
		t.Fatalf("Get = %v, want ErrNotFound", err)
	}
	if err = store.DeleteScheduledTask(ctx, leaseID); err != ErrNotFound {
		t.Fatalf("Delete = %v, want ErrNotFound", err)
	}
	claims, err := store.ClaimDueScheduledTasks(ctx, time.Now().Add(24*time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range claims {
		if claim.Task.ID == leaseID {
			t.Fatal("the scheduler must never claim the lease row")
		}
	}
}

func TestPostgresRepositoryRequestStatisticsCountBothAuditTables(t *testing.T) {
	store := openAuditPageStore(t)
	ctx := context.Background()
	repo := "audit-stats-" + uuid.NewString()
	now := time.Now().UTC().Truncate(time.Second)
	for _, record := range []AuditRecord{
		{Class: AuditClassAsync, Repository: repo, Outcome: AuditResolved, Actor: "a", Format: "raw", OccurredAt: now.Add(-time.Minute)},
		{Class: AuditClassSync, Repository: repo, Outcome: AuditAccessDenied, Actor: "a", Format: "raw", OccurredAt: now.Add(-2 * time.Minute)},
	} {
		if err := store.RecordAudit(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM resolver_audit_log WHERE repository=$1`, repo)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM security_audit_log WHERE repository=$1`, repo)
	})
	statistics, err := store.ListRepositoryRequestStatistics(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range statistics {
		if item.Repository == repo {
			if item.Requests.OneDay != 2 || item.Denied.OneDay != 1 || item.Requests.ThirtyDays != 2 {
				t.Fatalf("statistics must include the security table: %+v", item)
			}
			return
		}
	}
	t.Fatalf("repository %s missing from %+v", repo, statistics)
}
