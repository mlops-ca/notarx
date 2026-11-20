//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/mlops-ca/notarx/tools/datagen"
)

// TestDatagenSeedsTheCatalogueTheStoreWouldCompute seeds a scaled datagen profile inside a transaction and
// compares artifact_catalog, for every format the catalogue covers, with the aggregate the store computes from
// artifact_search_projection (catalogExpectedSQL, the statement the backfill and the equality tests use): the same
// rows in both directions, so latest_*, publisher, version_count and size_bytes are exact. Formats outside the
// projection get no rows. Everything rolls back.
func TestDatagenSeedsTheCatalogueTheStoreWouldCompute(t *testing.T) {
	db := integrationDB(t)
	profile := datagen.Profiles["tiny"].Scaled(20)
	ctx := t.Context()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	var counts datagen.Counts
	if err := conn.Raw(func(driverConn any) error {
		var err error
		counts, err = datagen.Seed(ctx, driverConn.(*stdlib.Conn).Conn(), profile, 1, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if counts.CatalogRows == 0 {
		t.Fatal("the seed wrote no catalogue rows")
	}
	for _, entry := range ArtifactCatalogFormats() {
		expected, err := catalogExpectedSQL(entry.Format)
		if !entry.Covered {
			var rows int
			if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM artifact_catalog WHERE format=$1`, string(entry.Format)).Scan(&rows); err != nil || rows != 0 {
				t.Errorf("%s is not covered by the catalogue but has %d rows (%v)", entry.Format, rows, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Run(string(entry.Format), func(t *testing.T) {
			var seeded int
			if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM artifact_catalog WHERE format=$1`, string(entry.Format)).Scan(&seeded); err != nil {
				t.Fatal(err)
			}
			if seeded == 0 {
				t.Fatalf("a seeded database has no catalogue rows for %s", entry.Format)
			}
			stored := fmt.Sprintf(`SELECT repository_id, coordinate, latest_version, latest_digest, latest_published_at, publisher, version_count, size_bytes
				FROM artifact_catalog WHERE format='%s'`, entry.Format)
			computed := `SELECT repository_id, coordinate, latest_version, latest_digest, latest_published_at, publisher, version_count, size_bytes FROM (` + expected + `) e`
			for _, direction := range [][2]string{{stored, computed}, {computed, stored}} {
				var differing int
				if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM ((`+direction[0]+`) EXCEPT (`+direction[1]+`)) d`).Scan(&differing); err != nil {
					t.Fatal(err)
				}
				if differing != 0 {
					t.Fatalf("%d %s catalogue rows differ from the store's aggregate", differing, entry.Format)
				}
			}
		})
	}
}
