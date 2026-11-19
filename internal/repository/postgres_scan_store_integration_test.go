//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/partitions"
)

func TestPostgresScanStoreContract(t *testing.T) {
	testScanStoreContract(t, samlStore(t))
}

// TestScanHistoryPartitionsAndRetention checks the ADR 0023 section 5 behaviour of artifact_scan_history:
// the manager creates the monthly partitions, the DEFAULT partition stays empty, and a partition older
// than NOTARX_SCAN_HISTORY_RETENTION is dropped.
func TestScanHistoryPartitionsAndRetention(t *testing.T) {
	store := samlStore(t)
	ctx := context.Background()
	var spec partitions.Spec
	for _, candidate := range partitions.DefaultRegistry() {
		if candidate.Table == "artifact_scan_history" {
			spec = candidate
		}
	}
	if spec.Table == "" || spec.Retention != 180*24*time.Hour || spec.Column != "scanned_at" {
		t.Fatalf("artifact_scan_history is not registered with 180 days retention: %+v", spec)
	}
	now := time.Now().UTC()
	manager, err := partitions.New(store.db, []partitions.Spec{spec}, partitions.Options{Owner: "scan-history-test", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	oldName := spec.Name(old)
	statement := "CREATE TABLE IF NOT EXISTS " + oldName + " PARTITION OF artifact_scan_history FOR VALUES FROM ('2020-01-01 00:00:00+00') TO ('2020-02-01 00:00:00+00')"
	if _, err := store.db.ExecContext(ctx, statement); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO artifact_scan_history (id, repository_id, artifact_digest, status, scanned_at) VALUES ($1::uuid,$2::uuid,'sha256:old','clean',$3)`, ids.New(), ids.New(), old.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	report, err := manager.Maintain(ctx)
	if err != nil || !report.Leader {
		t.Fatalf("maintain = %+v, %v", report, err)
	}
	dropped := false
	for _, name := range report.Dropped {
		dropped = dropped || name == oldName
	}
	if !dropped {
		t.Fatalf("the 2020-01 partition must be dropped after 180 days: %+v", report)
	}
	var current, defaultRows int
	currentName := spec.Name(now)
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM pg_class WHERE relname = $1", currentName).Scan(&current); err != nil || current != 1 {
		t.Fatalf("partition %s exists = %d, %v", currentName, current, err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM "+spec.DefaultName()).Scan(&defaultRows); err != nil || defaultRows != 0 {
		t.Fatalf("DEFAULT partition holds %d rows, %v", defaultRows, err)
	}
}
