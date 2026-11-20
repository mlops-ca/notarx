package repository

import (
	"context"
	"database/sql"
)

// conanRevisionUsage measures the assets of one recipe revision of a reference that the
// reconciler counts: an asset counts while its recipe revision is visible and, for a package
// asset, its package revision is visible too (capacity.UsageQuery("conan"), scoped to the
// reference and recipe revision). Every Conan writer changes the rows of one recipe revision,
// so it brackets its transaction with this measurement under the repository's Conan lock
// (lockConanRepositoriesTx), and the usage counter follows the visibility of both tables.
func conanRevisionUsage(repositoryID, reference, recipeRevision string) func(context.Context, *sql.Tx) (measuredUsage, error) {
	return func(ctx context.Context, tx *sql.Tx) (measuredUsage, error) {
		var usage measuredUsage
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(a.size),0)::bigint,COUNT(*)::bigint
FROM native_conan_assets a
JOIN native_conan_recipe_revisions r ON r.repository_id=a.repository_id AND r.reference=a.reference AND r.revision=a.recipe_revision
LEFT JOIN native_conan_package_revisions p ON p.repository_id=a.repository_id AND p.reference=a.reference AND p.recipe_revision=a.recipe_revision AND p.package_id=a.package_id AND p.revision=a.package_revision
WHERE a.repository_id=$1::uuid AND a.reference=$2 AND a.recipe_revision=$3
  AND r.state='visible' AND (a.package_id='' OR p.state='visible')`, repositoryID, reference, recipeRevision).Scan(&usage.bytes, &usage.objects)
		return usage, err
	}
}

// openConanUsageBracket starts the measurement of the recipe revision; call it after
// lockConanRepositoriesTx and before the first change of the transaction.
func openConanUsageBracket(ctx context.Context, tx *sql.Tx, repositoryID, reference, recipeRevision string) (*usageBracket, error) {
	bracket, err := openUsageBracket(ctx, tx, repositoryID, conanRevisionUsage(repositoryID, reference, recipeRevision))
	if err != nil {
		return nil, err
	}
	bracket.catalogFormat, bracket.catalogCoordinate = FormatConan, reference
	return bracket, nil
}
