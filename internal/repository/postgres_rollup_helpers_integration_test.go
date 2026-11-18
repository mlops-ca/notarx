//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/mlops-ca/notarx/internal/ids"
)

// seedHostedRepository inserts a hosted repository row so usage aggregates and request
// rollups, which are keyed by repository id, resolve the name. The row and the data
// that references it are removed when the test ends.
func seedHostedRepository(t *testing.T, store *PostgresStore, name, format string) string {
	t.Helper()
	id := ids.New()
	if _, err := store.db.ExecContext(context.Background(), `INSERT INTO hosted_repositories (id, name, format) VALUES ($1::uuid, $2, $3)`, id, name, format); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = store.db.ExecContext(ctx, `DELETE FROM artifact_usage_stats WHERE repository_id = $1::uuid`, id)
		_, _ = store.db.ExecContext(ctx, `DELETE FROM repository_request_rollup WHERE repository_id = $1::uuid`, id)
		_, _ = store.db.ExecContext(ctx, `DELETE FROM hosted_repositories WHERE id = $1::uuid`, id)
	})
	return id
}
