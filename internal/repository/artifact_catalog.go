package repository

import (
	"context"
	"fmt"
	"slices"
)

// ArtifactCatalogFormat says whether a hosted format's coordinates are kept in
// artifact_catalog (ADR 0023 section 8).
type ArtifactCatalogFormat struct {
	Format Format
	// Covered is true once every writer of the format refreshes its coordinates in the
	// transaction that changes them and the per-format equality test passes.
	Covered bool
	// Pending says why a format is not covered yet.
	Pending string
}

// artifactCatalogRegistry lists every admitted hosted format. A format that lands
// without an entry fails TestArtifactCatalogRegistryListsEveryHostedFormat, so a new
// format cannot ship without its catalogue coverage decision. Formats join in waves; the
// artifact_search_projection view stays until the last one is covered.
var artifactCatalogRegistry = []ArtifactCatalogFormat{
	{Format: FormatMaven, Covered: true},
	{Format: FormatOCI, Covered: true},
	{Format: FormatConan, Covered: true},
	{Format: FormatRaw, Covered: true},
	{Format: FormatNPM, Covered: true},
	{Format: FormatPyPI, Covered: true},
	{Format: FormatGo, Covered: true},
	{Format: FormatAPT, Covered: true},
	{Format: FormatCargo, Pending: "not part of artifact_search_projection yet; joins with the search rewrite"},
	{Format: FormatHelm, Pending: "not part of artifact_search_projection yet; joins with the search rewrite"},
	{Format: FormatNuGet, Pending: "not part of artifact_search_projection yet; joins with the search rewrite"},
	{Format: FormatRubyGems, Pending: "not part of artifact_search_projection yet; joins with the search rewrite"},
}

// ArtifactCatalogFormats returns the registry.
func ArtifactCatalogFormats() []ArtifactCatalogFormat { return slices.Clone(artifactCatalogRegistry) }

// ArtifactCatalogCovered reports whether the catalogue maintains the format.
func ArtifactCatalogCovered(format Format) bool {
	for _, entry := range artifactCatalogRegistry {
		if entry.Format == format {
			return entry.Covered
		}
	}
	return false
}

// CatalogBackfillBatchSize is the number of coordinates one backfill transaction refreshes.
const CatalogBackfillBatchSize = 500

// ArtifactCatalogBackfillProgress reports one format after a backfill pass.
type ArtifactCatalogBackfillProgress struct {
	Format    Format
	Refreshed int64
	Completed bool
}

// ArtifactCatalogStore is the dialect-neutral port of the catalogue maintenance that is
// not part of a publish transaction (ADR 0023 section 8). MySQL gets its twin with the
// ADR 0022 store slices; the start-up gate of ADR 0022 lists it as missing until then.
type ArtifactCatalogStore interface {
	// BackfillArtifactCatalog fills artifact_catalog from the per-format tables, in
	// batches of batchSize coordinates, resuming from the stored cursor of each covered
	// format. It is idempotent and safe to run on several nodes at once: a node that
	// finds another running a format's batch skips that format for this pass. It
	// returns the progress of every covered format.
	BackfillArtifactCatalog(ctx context.Context, batchSize int) ([]ArtifactCatalogBackfillProgress, error)
}

// catalogFormatLiteral returns the quoted SQL literal of a covered format. The format is
// written into the statement rather than bound, so the planner prunes every other branch
// of the artifact_search_projection union; it is validated against the registry first.
func catalogFormatLiteral(format Format) (string, error) {
	if !ArtifactCatalogCovered(format) {
		return "", fmt.Errorf("artifact catalogue does not cover format %q", format)
	}
	return "'" + string(format) + "'", nil
}

// catalogOrdering is the ranking of the search: newest first, then highest digest.
const catalogOrdering = "ORDER BY p.created_at DESC NULLS LAST, p.digest DESC"

// catalogAggregate groups the projection rows of one coordinate into its catalogue row.
// The caller supplies FROM and WHERE.
const catalogAggregate = `
	SELECT p.repository_id, p.coordinate,
	       (array_agg(COALESCE(p.version,'') ` + catalogOrdering + `))[1] AS latest_version,
	       (array_agg(COALESCE(p.digest,'') ` + catalogOrdering + `))[1] AS latest_digest,
	       (array_agg(p.created_at ` + catalogOrdering + `))[1] AS latest_published_at,
	       (array_agg(COALESCE(p.publisher,'') ` + catalogOrdering + `))[1] AS publisher,
	       count(DISTINCT COALESCE(p.version,''))::int AS version_count,
	       (array_agg(p.size ` + catalogOrdering + `))[1] AS size_bytes`

// catalogTombstoneFilter hides Go versions that have a tombstone; the other formats
// delete the row or change its state, which the projection already filters.
func catalogTombstoneFilter(format Format) string {
	if format != FormatGo {
		return ""
	}
	return `
	  AND NOT EXISTS (SELECT 1 FROM artifact_tombstones t
	                  WHERE t.repository_id=p.repository_id AND t.format='go'
	                    AND t.coordinate=p.coordinate || '@' || COALESCE(p.version,''))`
}

// catalogExpectedSQL returns the statement that computes, for every coordinate of the
// format, the row artifact_catalog must hold. The equality tests compare the table with
// it, and the backfill and the refresh compute the same aggregate for a subset.
func catalogExpectedSQL(format Format) (string, error) {
	literal, err := catalogFormatLiteral(format)
	if err != nil {
		return "", err
	}
	return catalogAggregate + `
	FROM artifact_search_projection p
	WHERE p.format=` + literal + catalogTombstoneFilter(format) + `
	GROUP BY p.repository_id, p.coordinate`, nil
}

// catalogLockSQL inserts the rows of the coordinates, or takes the row lock of those that
// exist, in key order. A concurrent writer of the same coordinate therefore waits here
// until the first commits, and the recompute that follows reads that commit.
func catalogLockSQL(format Format) (string, error) {
	literal, err := catalogFormatLiteral(format)
	if err != nil {
		return "", err
	}
	return `
	INSERT INTO artifact_catalog (repository_id, format, coordinate)
	SELECT k.repository_id::uuid, ` + literal + `, k.coordinate
	FROM unnest($1::text[], $2::text[]) AS k(repository_id, coordinate)
	GROUP BY k.repository_id::uuid, k.coordinate
	ORDER BY k.repository_id::uuid, k.coordinate
	ON CONFLICT (repository_id, format, coordinate) DO UPDATE SET version_count = artifact_catalog.version_count`, nil
}

// catalogRecomputeSQL recomputes the rows of the coordinates $1 (repository ids as text) and $2
// (coordinates) from the projection, and deletes the rows of coordinates with no visible
// row left.
func catalogRecomputeSQL(format Format) (string, error) {
	literal, err := catalogFormatLiteral(format)
	if err != nil {
		return "", err
	}
	return `
	WITH keys AS (
		SELECT k.repository_id::uuid AS repository_id, k.coordinate
		FROM unnest($1::text[], $2::text[]) AS k(repository_id, coordinate)
		GROUP BY k.repository_id::uuid, k.coordinate
	), agg AS MATERIALIZED (` + catalogAggregate + `
		FROM keys k
		JOIN artifact_search_projection p ON p.repository_id=k.repository_id AND p.format=` + literal + ` AND p.coordinate=k.coordinate
		WHERE true` + catalogTombstoneFilter(format) + `
		GROUP BY p.repository_id, p.coordinate
	), updated AS (
		UPDATE artifact_catalog c
		SET latest_version=a.latest_version, latest_digest=a.latest_digest, latest_published_at=a.latest_published_at,
		    publisher=a.publisher, version_count=a.version_count, size_bytes=a.size_bytes
		FROM agg a
		WHERE c.repository_id=a.repository_id AND c.format=` + literal + ` AND c.coordinate=a.coordinate
		RETURNING 1
	)
	DELETE FROM artifact_catalog c
	USING keys k
	WHERE c.repository_id=k.repository_id AND c.format=` + literal + ` AND c.coordinate=k.coordinate
	  AND NOT EXISTS (SELECT 1 FROM agg a WHERE a.repository_id=k.repository_id AND a.coordinate=k.coordinate)`, nil
}
