package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// catalogExecer is the part of *sql.DB and *sql.Tx the refresh needs.
type catalogExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type catalogStatements struct{ lock, recompute string }

var (
	catalogStatementsMu    sync.Mutex
	catalogStatementsCache = map[Format]catalogStatements{}
)

func catalogStatementsFor(format Format) (catalogStatements, error) {
	catalogStatementsMu.Lock()
	defer catalogStatementsMu.Unlock()
	if statements, ok := catalogStatementsCache[format]; ok {
		return statements, nil
	}
	lock, err := catalogLockSQL(format)
	if err != nil {
		return catalogStatements{}, err
	}
	recompute, err := catalogRecomputeSQL(format)
	if err != nil {
		return catalogStatements{}, err
	}
	catalogStatementsCache[format] = catalogStatements{lock: lock, recompute: recompute}
	return catalogStatementsCache[format], nil
}

// refreshArtifactCatalog recomputes the catalogue rows of the coordinates of one
// repository inside the caller's transaction (ADR 0023 section 8). Call it after the
// statements that change the per-format rows and before the capacity reservation that
// ends the transaction: every writer takes the catalogue row locks before the counter
// row, so the order is the same everywhere.
func refreshArtifactCatalog(ctx context.Context, tx catalogExecer, repositoryID string, format Format, coordinates ...string) error {
	repositoryIDs := make([]string, len(coordinates))
	for i := range repositoryIDs {
		repositoryIDs[i] = repositoryID
	}
	return refreshArtifactCatalogPairs(ctx, tx, format, repositoryIDs, coordinates)
}

// refreshArtifactCatalogPairs is refreshArtifactCatalog for coordinates of several
// repositories; repositoryIDs[i] owns coordinates[i].
func refreshArtifactCatalogPairs(ctx context.Context, tx catalogExecer, format Format, repositoryIDs, coordinates []string) error {
	if len(coordinates) == 0 {
		return nil
	}
	if len(repositoryIDs) != len(coordinates) {
		return fmt.Errorf("artifact catalogue refresh: %d repositories for %d coordinates", len(repositoryIDs), len(coordinates))
	}
	statements, err := catalogStatementsFor(format)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, statements.lock, repositoryIDs, coordinates); err != nil {
		return fmt.Errorf("lock artifact catalogue rows: %w", err)
	}
	if _, err := tx.ExecContext(ctx, statements.recompute, repositoryIDs, coordinates); err != nil {
		return fmt.Errorf("recompute artifact catalogue rows: %w", err)
	}
	return nil
}

// BackfillArtifactCatalog implements ArtifactCatalogStore.
func (s *PostgresStore) BackfillArtifactCatalog(ctx context.Context, batchSize int) ([]ArtifactCatalogBackfillProgress, error) {
	if batchSize <= 0 {
		batchSize = CatalogBackfillBatchSize
	}
	var progress []ArtifactCatalogBackfillProgress
	for _, entry := range artifactCatalogRegistry {
		if !entry.Covered {
			continue
		}
		result := ArtifactCatalogBackfillProgress{Format: entry.Format}
		for !result.Completed {
			refreshed, completed, busy, err := s.backfillArtifactCatalogBatch(ctx, entry.Format, batchSize)
			if err != nil {
				return progress, fmt.Errorf("backfill artifact catalogue (%s): %w", entry.Format, err)
			}
			if busy {
				break
			}
			result.Refreshed += refreshed
			result.Completed = completed
		}
		progress = append(progress, result)
	}
	return progress, nil
}

// backfillArtifactCatalogBatch refreshes the next batch of coordinates of a format after
// the stored cursor and advances the cursor in the same transaction, so an interrupted
// run loses at most the batch in flight. busy is true when another node holds the format.
func (s *PostgresStore) backfillArtifactCatalogBatch(ctx context.Context, format Format, batchSize int) (refreshed int64, completed, busy bool, err error) {
	keyset, err := catalogKeysetSQL(format)
	if err != nil {
		return 0, false, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var locked bool
	if err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "artifact-catalog-backfill:"+string(format)).Scan(&locked); err != nil {
		return 0, false, false, err
	}
	if !locked {
		return 0, false, true, nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO artifact_catalog_backfill (format) VALUES ($1) ON CONFLICT (format) DO NOTHING`, string(format)); err != nil {
		return 0, false, false, err
	}
	var cursorRepository, cursorKey string
	var done sql.NullTime
	if err = tx.QueryRowContext(ctx, `SELECT cursor_repository_id::text, cursor_key, completed_at FROM artifact_catalog_backfill WHERE format=$1`, string(format)).
		Scan(&cursorRepository, &cursorKey, &done); err != nil {
		return 0, false, false, err
	}
	if done.Valid {
		return 0, true, false, nil
	}
	rows, err := tx.QueryContext(ctx, keyset, cursorRepository, cursorKey, batchSize)
	if err != nil {
		return 0, false, false, err
	}
	var lastRepository, lastKey string
	var repositoryIDs, coordinates []string
	seen := map[[2]string]bool{}
	read := 0
	for rows.Next() {
		var repositoryID, key, coordinate string
		if err = rows.Scan(&repositoryID, &key, &coordinate); err != nil {
			_ = rows.Close()
			return 0, false, false, err
		}
		read++
		lastRepository, lastKey = repositoryID, key
		if pair := [2]string{repositoryID, coordinate}; !seen[pair] {
			seen[pair] = true
			repositoryIDs = append(repositoryIDs, repositoryID)
			coordinates = append(coordinates, coordinate)
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return 0, false, false, err
	}
	_ = rows.Close()
	if read == 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE artifact_catalog_backfill SET completed_at=now(), updated_at=now() WHERE format=$1`, string(format)); err != nil {
			return 0, false, false, err
		}
		return 0, true, false, tx.Commit()
	}
	if err = refreshArtifactCatalogPairs(ctx, tx, format, repositoryIDs, coordinates); err != nil {
		return 0, false, false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artifact_catalog_backfill SET cursor_repository_id=$2::uuid, cursor_key=$3, updated_at=now() WHERE format=$1`,
		string(format), lastRepository, lastKey); err != nil {
		return 0, false, false, err
	}
	return int64(len(coordinates)), false, false, tx.Commit()
}

var _ ArtifactCatalogStore = (*PostgresStore)(nil)

// refreshAPTSnapshotCatalog refreshes every path of the assets of the snapshots: the one
// that became visible and, when there is one, the one it replaced. A path both snapshots hold
// still changes its row, because the projection dates a snapshot's assets with the snapshot's
// own creation time; the set-based statements keep the cost at one pass over the paths the
// publish already wrote row by row.
func refreshAPTSnapshotCatalog(ctx context.Context, tx *sql.Tx, repositoryID, snapshotID, previousSnapshotID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT path FROM native_apt_snapshot_assets
		WHERE snapshot_id=$1::uuid OR snapshot_id=NULLIF($2,'')::uuid ORDER BY path`, snapshotID, previousSnapshotID)
	if err != nil {
		return err
	}
	var paths []string
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			_ = rows.Close()
			return err
		}
		paths = append(paths, path)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	return refreshArtifactCatalog(ctx, tx, repositoryID, FormatAPT, paths...)
}
