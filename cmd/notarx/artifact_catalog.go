package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

// artifactCatalogBackfillRetry is how long a node waits before it resumes a backfill that
// another node was running or that failed.
const artifactCatalogBackfillRetry = time.Minute

// runArtifactCatalogBackfill fills artifact_catalog from the per-format tables (ADR 0023
// section 8) and returns when every covered format has completed, or when ctx ends. The
// backfill resumes from its stored cursor, so a restart loses at most one batch, and several
// nodes may run it: a node that finds a format busy retries after the pause.
func runArtifactCatalogBackfill(ctx context.Context, store repository.ArtifactCatalogStore, retry time.Duration) {
	for {
		progress, err := store.BackfillArtifactCatalog(ctx, repository.CatalogBackfillBatchSize)
		completed := err == nil
		var refreshed int64
		for _, format := range progress {
			completed = completed && format.Completed
			refreshed += format.Refreshed
		}
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			slog.Error("artifact catalogue backfill failed; retrying", "error", err, "retry_in", retry.String())
		case completed:
			slog.Info("artifact catalogue backfill complete", "refreshed_coordinates", refreshed)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}
