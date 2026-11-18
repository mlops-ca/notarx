package repository

import (
	"context"
)

// ociReferenceBlob adds the repository's reference to a blob. A blob is shared across
// repositories by digest and counts once per repository that references it, for the
// blob's size (ADR 0023 section 3). It returns the size added to the repository's usage
// and whether the reference is new; a re-referenced blob adds nothing.
func ociReferenceBlob(ctx context.Context, q sqlRunner, repositoryID, digest string) (size int64, added bool, err error) {
	err = q.QueryRowContext(ctx, `WITH inserted AS (
		INSERT INTO native_oci_repository_blobs (repository_id,digest) VALUES ($1,$2) ON CONFLICT DO NOTHING RETURNING digest
	) SELECT COALESCE((SELECT b.size FROM native_oci_blobs b JOIN inserted i ON i.digest=b.digest),-1)::bigint`, repositoryID, digest).Scan(&size)
	if err != nil {
		return 0, false, err
	}
	if size < 0 {
		return 0, false, nil
	}
	return size, true, nil
}
