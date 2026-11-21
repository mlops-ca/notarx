package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

func (s *PostgresStore) PublishNPMImport(ctx context.Context, c NPMImportClosure) error {
	if err := validateNPMImport(c); err != nil {
		return err
	}
	keys := make([]string, 0, len(c.Versions))
	for _, v := range c.Versions {
		keys = append(keys, artifactScanLockKey(c.RepositoryID, FormatNPM, c.PackageName+"@"+v.Version, v.Digest))
	}
	// Large manifests go to the object store before the publication advisory lock is taken, so neither the lock nor its connection waits on the object store.
	manifests := make(map[string]npmManifest, len(c.Versions))
	for _, v := range c.Versions {
		inline, ref, err := s.externalizeJSON(ctx, v.Manifest, "{}")
		if err != nil {
			return err
		}
		manifests[v.Version] = npmManifest{inline: inline, ref: ref}
	}
	// The manifests of the versions already stored are compared with the incoming ones, so they are read before the
	// lock too. A version imported between the read and the lock makes the locked transaction ask for a manifest it
	// does not hold; the import then starts over with a fresh read instead of waiting on the object store under the lock.
	for attempt := 1; ; attempt++ {
		prefetched, err := s.prefetchNPMManifests(ctx, c)
		if err != nil {
			return err
		}
		// The last attempt reads a missing manifest under the lock rather than fail an import that is only racing others.
		err = s.publishNPMImportLocked(ctx, c, keys, manifests, prefetched, attempt == npmImportPrefetchAttempts)
		if !errors.Is(err, errPayloadNotPrefetched) {
			return err
		}
	}
}

// npmImportPrefetchAttempts bounds how often an import starts over because a stored manifest was not prefetched.
const npmImportPrefetchAttempts = 3

// publishNPMImportLocked runs the import under the publication advisory locks of its versions.
func (s *PostgresStore) publishNPMImportLocked(ctx context.Context, c NPMImportClosure, keys []string, manifests map[string]npmManifest, prefetched payloadPrefetch, mayReadStore bool) error {
	ctx, release, err := s.lockPostgresAdvisoryKeys(ctx, keys)
	if err != nil {
		return err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = checkNativePublicationGuard(ctx, tx); err != nil {
		return err
	}
	if err = lockHostedNPMPackage(ctx, tx, c.RepositoryID, c.PackageName); err != nil {
		return err
	}
	var repoName string
	err = tx.QueryRowContext(ctx, `SELECT name FROM hosted_repositories WHERE id=$1::uuid AND format='npm' AND repo_type='hosted' AND state='active' FOR SHARE`, c.RepositoryID).Scan(&repoName)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDisabled
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO native_npm_packages(repository_id,name,dist_tags)VALUES($1,$2,'{}'::jsonb)ON CONFLICT DO NOTHING`, c.RepositoryID, c.PackageName); err != nil {
		return err
	}
	read, err := s.readNPMPackage(ctx, tx, c.RepositoryID, c.PackageName)
	if err != nil {
		return err
	}
	pkg := read.NPMPackage
	if pkg.SourceEndpoint != "" || pkg.Negative {
		return ErrNameExists
	}
	for tag, want := range c.DistTags {
		if old, exists := pkg.DistTags[tag]; exists && old != want {
			return ErrNameExists
		}
	}
	oldVersions := map[string]NPMVersion{}
	for _, v := range pkg.Versions {
		oldVersions[v.Version] = v
	}
	for _, v := range pkg.TombstonedVersions {
		oldVersions[v.Version] = v
	}
	changed := false
	var delta usageDelta
	for _, v := range c.Versions {
		var blocked bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifact_tombstones WHERE repository_id=$1::uuid AND format='npm' AND coordinate=$2) OR EXISTS(SELECT 1 FROM artifact_quarantines WHERE repository_id=$1::uuid AND format='npm' AND coordinate=$2 AND digest=$3 AND state='quarantined')`, c.RepositoryID, c.PackageName+"@"+v.Version, v.Digest).Scan(&blocked)
		if err != nil {
			return err
		}
		if blocked {
			return ErrArtifactQuarantined
		}
		if old, exists := oldVersions[v.Version]; exists {
			if old.State == "" || old.State == "visible" {
				// A manifest stored since the prefetch restarts the import (errPayloadNotPrefetched) instead of reading the object store here.
				if old.Manifest, err = s.internalizeStored(ctx, old.Manifest, read.refs[v.Version], prefetched, mayReadStore); err != nil {
					return err
				}
			}
			if !NPMImportVersionMatches(old, v) {
				return ErrNameExists
			}
			continue
		}
		manifest := manifests[v.Version]
		bodyDigest, bodySize, bodyKey := manifest.ref.columns()
		_, err = tx.ExecContext(ctx, `INSERT INTO native_npm_versions(repository_id,package_name,version,digest,integrity,shasum,tarball_name,object_key,size,manifest,publisher,cached_at,deprecated,body_digest,body_size,body_key)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,now(),$12,$13,$14,$15)`, c.RepositoryID, c.PackageName, v.Version, v.Digest, v.Integrity, v.Shasum, v.TarballName, v.ObjectKey, v.Size, manifest.inline, v.Publisher, v.Deprecated, bodyDigest, bodySize, bodyKey)
		if isUnique(err) {
			return ErrNameExists
		}
		if err != nil {
			return err
		}
		if err = emitPublishedPostgres(ctx, tx, c.RepositoryID, FormatNPM, c.PackageName+"@"+v.Version, v.Digest); err != nil {
			return err
		}
		if v.ObjectKey != "" {
			delta.add(c.RepositoryID, v.Size, 1)
		}
		changed = true
	}
	for tag, version := range c.DistTags {
		if _, exists := pkg.DistTags[tag]; !exists {
			changed = true
			pkg.DistTags[tag] = version
		}
	}
	if changed {
		tags, _ := json.Marshal(pkg.DistTags)
		if _, err = tx.ExecContext(ctx, `UPDATE native_npm_packages SET dist_tags=$3::jsonb,revision=revision+1,updated_at=now()WHERE repository_id=$1::uuid AND name=$2`, c.RepositoryID, c.PackageName, tags); err != nil {
			return err
		}
		if err = insertAudit(ctx, tx, npmImportAudit(c, repoName)); err != nil {
			return err
		}
	}
	if err = refreshArtifactCatalog(ctx, tx, c.RepositoryID, FormatNPM, c.PackageName); err != nil {
		return err
	}
	if err = delta.reserve(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// prefetchNPMManifests reads the manifests of the visible stored versions the closure imports again, by digest, so
// the import transaction compares them without waiting on the object store.
func (s *PostgresStore) prefetchNPMManifests(ctx context.Context, c NPMImportClosure) (payloadPrefetch, error) {
	if s.payloads.Load() == nil || len(c.Versions) == 0 {
		return nil, nil
	}
	versions := make([]string, len(c.Versions))
	for i, v := range c.Versions {
		versions[i] = v.Version
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT repository_id::text,package_name,version,digest,integrity,shasum,
		       tarball_name,upstream_tarball,object_key,size,manifest,publisher,state,cached_at,deleted_at,collected_at,created_at,deprecated,collecting_at,body_digest,body_size,body_key
		FROM native_npm_versions
		WHERE repository_id=$1::uuid AND package_name=$2 AND version=ANY($3::text[]) AND state='visible' AND body_digest IS NOT NULL`, c.RepositoryID, c.PackageName, versions)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	scanned, err := scanNPMVersionRows(rows)
	if err != nil {
		return nil, err
	}
	_ = rows.Close()
	if err = s.resolveNPMManifests(ctx, scanned); err != nil {
		return nil, err
	}
	prefetched := payloadPrefetch{}
	for _, version := range scanned {
		prefetched.add(version.ref, version.Manifest)
	}
	return prefetched, nil
}
