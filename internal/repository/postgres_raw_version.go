package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/capacity"
	"github.com/mlops-ca/notarx/internal/ids"
)

var _ NativeRawVersionStore = (*PostgresStore)(nil)

// rawVersionColumns is the projection of one native_raw_versions row.
const rawVersionColumns = `v.id::text,v.repository_id::text,v.name,v.version,v.state,v.file_count,v.size_bytes,COALESCE(v.manifest_digest,''),v.created_by,v.created_at,v.sealed_at,v.updated_at`

// rawVersionFilesQuery reads the files of a version as the byte-ordered path range [name/version/, name/version0)
// on native_raw_assets_repository_path_pattern_idx (text_pattern_ops, so the scan is locale independent). There is
// no LIKE, so an underscore in a name needs no escaping.
const rawVersionFilesQuery = `SELECT a.repository_id::text,a.path,a.digest,o.object_key,o.size,a.content_type,a.updated_at FROM native_raw_assets a JOIN native_raw_objects o ON o.digest=a.digest WHERE a.repository_id=$1::uuid AND a.path ~>=~ $2 AND a.path ~<~ $3 LIMIT $4`

// rawIdleCandidatesQuery selects one page of the open versions the idle sweep may seal, oldest write first
// (native_raw_versions_state_updated_idx), after the (updated_at, id) cursor of the previous page; sealRawVersion
// re-checks each one under its row lock.
const rawIdleCandidatesQuery = `SELECT v.repository_id::text,v.name,v.version,r.name,v.updated_at,v.id::text FROM native_raw_versions v JOIN hosted_repositories r ON r.id=v.repository_id WHERE v.state='open' AND v.file_count>0 AND v.updated_at<$1 AND (v.updated_at,v.id)>($3::timestamptz,$4::uuid) AND r.seal_mode='idle' AND r.state='active' ORDER BY v.updated_at,v.id LIMIT $2`

// rawOpenStatsQuery counts the stale open versions and finds the oldest one (native_raw_versions_state_created_idx).
const rawOpenStatsQuery = `SELECT count(CASE WHEN created_at<$1 THEN 1 END),min(created_at) FROM native_raw_versions WHERE state='open'`

// rawVersionFilesHardLimit bounds the file read of one version. NOTARX_GENERIC_MAX_FILES_PER_VERSION cannot exceed it,
// and a read that finds more files fails instead of answering with a truncated list.
const rawVersionFilesHardLimit = MaxRawFilesPerVersion

// SetRawMaxFilesPerVersion sets the files-per-version cap; zero or less restores the default.
func (s *PostgresStore) SetRawMaxFilesPerVersion(limit int) { s.rawMaxFiles.Store(int64(limit)) }

func (s *PostgresStore) maxFilesPerVersion() int {
	if limit := int(s.rawMaxFiles.Load()); limit > 0 {
		return limit
	}
	return DefaultRawMaxFilesPerVersion
}

// rawLayoutEntry is the layout of a repository, which never changes after its creation.
type rawLayoutEntry struct {
	layout RawLayout
	strict bool
}

// rawRepositoryLayout reads the layout and strict flag of a repository. Both are fixed when the repository is
// created and repository ids are never reused, so a found row is cached for the life of the process and a write pays
// the lookup once. A repository row that does not exist (a store used without one) has the path layout and is not cached.
func (s *PostgresStore) rawRepositoryLayout(ctx context.Context, tx *sql.Tx, repositoryID string) (RawLayout, bool, error) {
	if cached, ok := s.rawLayouts.Load(repositoryID); ok {
		entry := cached.(rawLayoutEntry)
		return entry.layout, entry.strict, nil
	}
	var entry rawLayoutEntry
	err := tx.QueryRowContext(ctx, `SELECT raw_layout,raw_strict FROM hosted_repositories WHERE id=$1::uuid`, repositoryID).Scan(&entry.layout, &entry.strict)
	if errors.Is(err, sql.ErrNoRows) {
		return RawLayoutPath, false, nil
	}
	if err != nil {
		return "", false, err
	}
	s.rawLayouts.Store(repositoryID, entry)
	return entry.layout, entry.strict, nil
}

func scanRawVersion(scanner interface{ Scan(...any) error }, version *RawVersion) error {
	var sealedAt sql.NullTime
	if err := scanner.Scan(&version.ID, &version.RepositoryID, &version.Name, &version.Version, &version.State, &version.FileCount, &version.SizeBytes, &version.ManifestDigest, &version.CreatedBy, &version.CreatedAt, &sealedAt, &version.UpdatedAt); err != nil {
		return err
	}
	if sealedAt.Valid {
		version.SealedAt = sealedAt.Time
	}
	version.Properties = map[string]string{}
	return nil
}

// lockRawVersion locks the version row; ok is false when there is none.
func lockRawVersion(ctx context.Context, tx *sql.Tx, repositoryID, name, version string) (RawVersion, bool, error) {
	var found RawVersion
	err := scanRawVersion(tx.QueryRowContext(ctx, `SELECT `+rawVersionColumns+` FROM native_raw_versions v WHERE v.repository_id=$1::uuid AND v.name=$2 AND v.version=$3 FOR UPDATE`, repositoryID, name, version), &found)
	if errors.Is(err, sql.ErrNoRows) {
		return RawVersion{}, false, nil
	}
	return found, err == nil, err
}

// lockOrCreateRawVersion locks the version row, creating the open row first when it is absent. The common write finds
// the row and locks it without an insert. Two writers of a new version serialise on the unique index; the loser then
// locks the winner's row.
func lockOrCreateRawVersion(ctx context.Context, tx *sql.Tx, repositoryID, name, version, actor string) (RawVersion, bool, error) {
	found, ok, err := lockRawVersion(ctx, tx, repositoryID, name, version)
	if err != nil || ok {
		return found, false, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO native_raw_versions (id,repository_id,name,version,state,created_by) VALUES ($1::uuid,$2::uuid,$3,$4,'open',$5) ON CONFLICT (repository_id,name,version) DO NOTHING`, ids.New(), repositoryID, name, version, actor)
	if err != nil {
		return RawVersion{}, false, err
	}
	created, _ := result.RowsAffected()
	found, ok, err = lockRawVersion(ctx, tx, repositoryID, name, version)
	if err == nil && !ok {
		err = errors.New("raw version row vanished under its own lock")
	}
	return found, created == 1, err
}

func loadRawVersionProperties(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, versions ...*RawVersion) error {
	if len(versions) == 0 {
		return nil
	}
	byID := make(map[string]*RawVersion, len(versions))
	idList := make([]string, 0, len(versions))
	for _, version := range versions {
		byID[version.ID] = version
		idList = append(idList, version.ID)
	}
	rows, err := q.QueryContext(ctx, `SELECT version_id::text,prop_key,prop_value FROM native_raw_version_properties WHERE version_id = ANY($1::uuid[])`, idList)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, key, value string
		if err = rows.Scan(&id, &key, &value); err != nil {
			return err
		}
		byID[id].Properties[key] = value
	}
	return rows.Err()
}

func replaceRawVersionProperties(ctx context.Context, tx *sql.Tx, version RawVersion, properties map[string]string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM native_raw_version_properties WHERE version_id=$1::uuid`, version.ID); err != nil {
		return err
	}
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx, `INSERT INTO native_raw_version_properties (version_id,repository_id,prop_key,prop_value) VALUES ($1::uuid,$2::uuid,$3,$4)`, version.ID, version.RepositoryID, key, properties[key]); err != nil {
			return err
		}
	}
	return nil
}

// beginVersionedWrite is the first half of the store-level guard of ADR 0028 for a write to a versioned repository:
// path validation, the preconditions a create-only write can never satisfy, and the version row lock, which is taken
// before the path lock and before any object row so every writer of a version orders its locks alike. It returns nil
// for a path repository. The locked version is passed to replaceVersionedRawAsset.
func (s *PostgresStore) beginVersionedWrite(ctx context.Context, tx *sql.Tx, asset RawAsset, condition RawWriteCondition, actor string) (*RawVersion, error) {
	layout, _, err := s.rawRepositoryLayout(ctx, tx, asset.RepositoryID)
	if err != nil || layout != RawLayoutVersioned {
		return nil, err
	}
	parsed, err := ParseRawVersionPath(asset.Path)
	if err != nil {
		return nil, err
	}
	if condition.RequirePresent || len(condition.MatchDigests) > 0 {
		return nil, ErrPreconditionFailed
	}
	version, _, err := lockOrCreateRawVersion(ctx, tx, asset.RepositoryID, parsed.Name, parsed.Version, actor)
	if err != nil {
		return nil, err
	}
	if version.State == RawVersionTombstoned {
		return nil, ErrVersionTombstoned
	}
	return &version, nil
}

// replaceVersionedRawAsset is the second half of the guard: create-only files, the state of the locked version and the
// files cap. A write that finds the same digest at the path changes nothing and returns it with Replayed set.
func (s *PostgresStore) replaceVersionedRawAsset(ctx context.Context, tx *sql.Tx, asset RawAsset, version RawVersion) (RawAsset, rawUsageDelta, error) {
	var err error
	if _, _, err = lockRawPath(ctx, tx, asset.RepositoryID, asset.Path); err != nil {
		return RawAsset{}, rawUsageDelta{}, err
	}
	current := RawAsset{RepositoryID: asset.RepositoryID, Path: asset.Path}
	err = tx.QueryRowContext(ctx, `SELECT digest,content_type,updated_at FROM native_raw_assets WHERE repository_id=$1::uuid AND path=$2`, asset.RepositoryID, asset.Path).Scan(&current.Digest, &current.ContentType, &current.UpdatedAt)
	switch {
	case err == nil && current.Digest == asset.Digest:
		current.Replayed = true
		return current, rawUsageDelta{}, nil
	case err == nil:
		return current, rawUsageDelta{}, ErrVersionFileExists
	case !errors.Is(err, sql.ErrNoRows):
		return RawAsset{}, rawUsageDelta{}, err
	}
	if version.State == RawVersionSealed {
		return RawAsset{}, rawUsageDelta{}, ErrVersionSealed
	}
	if version.FileCount >= s.maxFilesPerVersion() {
		return RawAsset{}, rawUsageDelta{}, ErrVersionFileLimit
	}
	var size int64
	if err = tx.QueryRowContext(ctx, `SELECT size FROM native_raw_objects WHERE digest=$1`, asset.Digest).Scan(&size); err != nil {
		return RawAsset{}, rawUsageDelta{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO native_raw_assets (repository_id,path,digest,content_type) VALUES ($1,$2,$3,$4)`, asset.RepositoryID, asset.Path, asset.Digest, asset.ContentType); err != nil {
		return RawAsset{}, rawUsageDelta{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE native_raw_versions SET file_count=file_count+1,size_bytes=size_bytes+$2,updated_at=now() WHERE id=$1::uuid`, version.ID, size); err != nil {
		return RawAsset{}, rawUsageDelta{}, err
	}
	if err = refreshArtifactCatalog(ctx, tx, asset.RepositoryID, FormatRaw, asset.Path); err != nil {
		return RawAsset{}, rawUsageDelta{}, err
	}
	return RawAsset{}, rawUsageDelta{bytes: size, objects: 1}, nil
}

// deleteVersionedRawAsset removes one file of an open version of a non-strict repository (the repair of a failed
// upload). It leaves no tombstone: the identifier of the version stays open for the re-upload.
func (s *PostgresStore) deleteVersionedRawAsset(ctx context.Context, tx *sql.Tx, repositoryID, path string, strict bool, audit []AuditRecord) error {
	parsed, err := ParseRawVersionPath(path)
	if err != nil {
		return ErrNotFound
	}
	version, ok, err := lockRawVersion(ctx, tx, repositoryID, parsed.Name, parsed.Version)
	if err != nil {
		return err
	}
	if !ok || version.State == RawVersionTombstoned {
		return ErrNotFound
	}
	if _, _, err = lockRawPath(ctx, tx, repositoryID, path); err != nil {
		return err
	}
	var asset RawAsset
	err = tx.QueryRowContext(ctx, `SELECT a.digest,o.size FROM native_raw_assets a JOIN native_raw_objects o ON o.digest=a.digest WHERE a.repository_id=$1::uuid AND a.path=$2 FOR UPDATE OF a`, repositoryID, path).Scan(&asset.Digest, &asset.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	switch {
	case strict:
		return ErrVersionFileImmutable
	case version.State == RawVersionSealed:
		return ErrVersionSealed
	}
	if _, err = tx.ExecContext(ctx, `UPDATE native_raw_objects SET created_at=now(),collected_at=NULL WHERE digest=$1`, asset.Digest); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM native_raw_assets WHERE repository_id=$1::uuid AND path=$2`, repositoryID, path); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE native_raw_versions SET file_count=file_count-1,size_bytes=size_bytes-$2,updated_at=now() WHERE id=$1::uuid`, version.ID, asset.Size); err != nil {
		return err
	}
	if err = refreshArtifactCatalog(ctx, tx, repositoryID, FormatRaw, path); err != nil {
		return err
	}
	if err = capacity.Reserve(ctx, tx, repositoryID, -asset.Size, -1); err != nil {
		return err
	}
	if err = insertAudits(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) GetRawVersion(ctx context.Context, repositoryID, name, version string) (RawVersion, error) {
	var found RawVersion
	err := scanRawVersion(s.db.QueryRowContext(ctx, `SELECT `+rawVersionColumns+` FROM native_raw_versions v WHERE v.repository_id=$1::uuid AND v.name=$2 AND v.version=$3`, repositoryID, name, version), &found)
	if errors.Is(err, sql.ErrNoRows) || err == nil && found.State == RawVersionTombstoned {
		return RawVersion{}, ErrNotFound
	}
	if err != nil {
		return RawVersion{}, err
	}
	return found, loadRawVersionProperties(ctx, s.db, &found)
}

func (s *PostgresStore) ListRawPackages(ctx context.Context, repositoryID, after string, limit int) ([]RawPackage, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT n.name,n.version_count,COALESCE((SELECT v.version FROM native_raw_versions v WHERE v.repository_id=$1::uuid AND v.name=n.name AND v.state='sealed' ORDER BY v.sealed_at DESC,v.id DESC LIMIT 1),'')
FROM (SELECT name,count(*) AS version_count FROM native_raw_versions WHERE repository_id=$1::uuid AND state<>'tombstoned' AND name>$2 GROUP BY name ORDER BY name LIMIT $3) n ORDER BY n.name`, repositoryID, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	packages := make([]RawPackage, 0)
	for rows.Next() {
		var pkg RawPackage
		if err = rows.Scan(&pkg.Name, &pkg.VersionCount, &pkg.LatestVersion); err != nil {
			return nil, err
		}
		packages = append(packages, pkg)
	}
	return packages, rows.Err()
}

// rawVersionListStatement builds the keyset page of one package's versions in listing order (newest seal first,
// unsealed versions last) and its arguments.
func rawVersionListStatement(query RawVersionQuery) (string, []any) {
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	args := []any{query.RepositoryID, query.Name}
	arg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	var where strings.Builder
	where.WriteString(`v.repository_id=$1::uuid AND v.name=$2 AND v.state<>'tombstoned'`)
	if query.State != "" {
		where.WriteString(" AND v.state=" + arg(string(query.State)))
	}
	if !query.OpenCreatedBefore.IsZero() {
		where.WriteString(" AND v.state='open' AND v.created_at<" + arg(query.OpenCreatedBefore))
	}
	for _, property := range query.Properties {
		key, value := arg(property.Key), arg(property.Value)
		where.WriteString(` AND EXISTS (SELECT 1 FROM native_raw_version_properties p WHERE p.version_id=v.id AND p.repository_id=v.repository_id AND p.prop_key=` + key + ` AND p.prop_value=` + value + `)`)
	}
	if after := query.After; after != nil {
		if after.SealedAt.IsZero() {
			where.WriteString(" AND (v.sealed_at IS NULL AND v.id<" + arg(after.ID) + "::uuid)")
		} else {
			sealed, id := arg(after.SealedAt), arg(after.ID)
			where.WriteString(" AND (v.sealed_at<" + sealed + " OR (v.sealed_at=" + sealed + " AND v.id<" + id + "::uuid) OR v.sealed_at IS NULL)")
		}
	}
	return `SELECT ` + rawVersionColumns + ` FROM native_raw_versions v WHERE ` + where.String() + ` ORDER BY v.sealed_at DESC NULLS LAST,v.id DESC LIMIT ` + arg(limit), args
}

func (s *PostgresStore) ListRawVersions(ctx context.Context, query RawVersionQuery) ([]RawVersion, error) {
	statement, args := rawVersionListStatement(query)
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	versions := make([]RawVersion, 0)
	for rows.Next() {
		var version RawVersion
		if err = scanRawVersion(rows, &version); err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()
	pointers := make([]*RawVersion, len(versions))
	for i := range versions {
		pointers[i] = &versions[i]
	}
	return versions, loadRawVersionProperties(ctx, s.db, pointers...)
}

type rawVersionQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listRawVersionFiles(ctx context.Context, q rawVersionQuerier, repositoryID, name, version string) ([]RawAsset, error) {
	from, to := RawVersionPathRange(name, version)
	rows, err := q.QueryContext(ctx, rawVersionFilesQuery, repositoryID, from, to, rawVersionFilesHardLimit+1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	files := make([]RawAsset, 0)
	for rows.Next() {
		var file RawAsset
		if err = rows.Scan(&file.RepositoryID, &file.Path, &file.Digest, &file.ObjectKey, &file.Size, &file.ContentType, &file.UpdatedAt); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(files) > rawVersionFilesHardLimit {
		// Never answer, seal or digest a truncated listing.
		return nil, ErrVersionTooLarge
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func (s *PostgresStore) ListRawVersionFiles(ctx context.Context, repositoryID, name, version string) ([]RawAsset, error) {
	return listRawVersionFiles(ctx, s.db, repositoryID, name, version)
}

func (s *PostgresStore) CreateRawVersion(ctx context.Context, version RawVersion, audit ...AuditRecord) (RawVersion, bool, error) {
	if !ValidRawVersionName(version.Name) || !ValidRawVersionString(version.Version) {
		return RawVersion{}, false, ErrInvalidGenericPath
	}
	if err := ValidateRawVersionProperties(version.Properties); err != nil {
		return RawVersion{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RawVersion{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	layout, _, err := s.rawRepositoryLayout(ctx, tx, version.RepositoryID)
	if err != nil {
		return RawVersion{}, false, err
	}
	if layout != RawLayoutVersioned {
		return RawVersion{}, false, ErrRawLayoutMismatch
	}
	found, created, err := lockOrCreateRawVersion(ctx, tx, version.RepositoryID, version.Name, version.Version, version.CreatedBy)
	if err != nil {
		return RawVersion{}, false, err
	}
	if found.State == RawVersionTombstoned {
		return RawVersion{}, false, ErrVersionTombstoned
	}
	if err = loadRawVersionProperties(ctx, tx, &found); err != nil {
		return RawVersion{}, false, err
	}
	if found.State == RawVersionSealed {
		// A create that repeats what a sealed version already is changes nothing, so a retried publisher is safe.
		if version.Properties != nil && !equalRawVersionProperties(found.Properties, version.Properties) {
			return RawVersion{}, false, ErrVersionSealed
		}
		return found, false, nil
	}
	if created || version.Properties != nil && !equalRawVersionProperties(found.Properties, version.Properties) {
		if version.Properties != nil {
			if err = replaceRawVersionProperties(ctx, tx, found, version.Properties); err != nil {
				return RawVersion{}, false, err
			}
			found.Properties = cloneRawVersionProperties(version.Properties)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE native_raw_versions SET updated_at=now() WHERE id=$1::uuid`, found.ID); err != nil {
			return RawVersion{}, false, err
		}
		if err = insertAudits(ctx, tx, audit); err != nil {
			return RawVersion{}, false, err
		}
	}
	return found, created, tx.Commit()
}

func (s *PostgresStore) SetRawVersionProperties(ctx context.Context, repositoryID, name, version string, properties map[string]string, audit ...AuditRecord) (RawVersion, error) {
	if err := ValidateRawVersionProperties(properties); err != nil {
		return RawVersion{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RawVersion{}, err
	}
	defer func() { _ = tx.Rollback() }()
	found, ok, err := lockRawVersion(ctx, tx, repositoryID, name, version)
	if err != nil {
		return RawVersion{}, err
	}
	if !ok {
		return RawVersion{}, ErrNotFound
	}
	switch found.State {
	case RawVersionTombstoned:
		return RawVersion{}, ErrVersionTombstoned
	case RawVersionSealed:
		return RawVersion{}, ErrVersionSealed
	}
	if err = replaceRawVersionProperties(ctx, tx, found, properties); err != nil {
		return RawVersion{}, err
	}
	if err = tx.QueryRowContext(ctx, `UPDATE native_raw_versions SET updated_at=now() WHERE id=$1::uuid RETURNING updated_at`, found.ID).Scan(&found.UpdatedAt); err != nil {
		return RawVersion{}, err
	}
	if err = insertAudits(ctx, tx, audit); err != nil {
		return RawVersion{}, err
	}
	found.Properties = cloneRawVersionProperties(properties)
	return found, tx.Commit()
}

// errRawSealSkipped reports that the idle sweep found the version written again after it selected it.
var errRawSealSkipped = errors.New("version is no longer idle")

func (s *PostgresStore) SealRawVersion(ctx context.Context, input SealRawVersionInput) (RawVersion, error) {
	if input.Properties != nil {
		if err := ValidateRawVersionProperties(input.Properties); err != nil {
			return RawVersion{}, err
		}
	}
	return s.sealRawVersion(ctx, input, time.Time{})
}

// sealRawVersion seals one version in its own transaction. A non-zero idleBefore makes it the idle sweep's seal: the
// version is sealed only if it is still open and unwritten since idleBefore once its row is locked.
func (s *PostgresStore) sealRawVersion(ctx context.Context, input SealRawVersionInput, idleBefore time.Time) (RawVersion, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RawVersion{}, err
	}
	defer func() { _ = tx.Rollback() }()
	found, ok, err := lockRawVersion(ctx, tx, input.RepositoryID, input.Name, input.Version)
	if err != nil {
		return RawVersion{}, err
	}
	if !ok {
		return RawVersion{}, ErrNotFound
	}
	if err = loadRawVersionProperties(ctx, tx, &found); err != nil {
		return RawVersion{}, err
	}
	switch found.State {
	case RawVersionTombstoned:
		return RawVersion{}, ErrVersionTombstoned
	case RawVersionSealed:
		if input.Properties != nil && !equalRawVersionProperties(found.Properties, input.Properties) {
			return RawVersion{}, ErrVersionSealed
		}
		return found, nil
	}
	if !idleBefore.IsZero() && !found.UpdatedAt.Before(idleBefore) {
		return RawVersion{}, errRawSealSkipped
	}
	files, err := listRawVersionFiles(ctx, tx, input.RepositoryID, input.Name, input.Version)
	if err != nil {
		return RawVersion{}, err
	}
	if len(files) == 0 {
		return RawVersion{}, ErrVersionEmpty
	}
	if err = verifyRawVersionSeal(input, files); err != nil {
		return RawVersion{}, err
	}
	if input.Properties != nil {
		if err = replaceRawVersionProperties(ctx, tx, found, input.Properties); err != nil {
			return RawVersion{}, err
		}
		found.Properties = cloneRawVersionProperties(input.Properties)
	}
	_, found.ManifestDigest = RawVersionManifest(found.Name, found.Version, files, found.Properties)
	found.State = RawVersionSealed
	if err = tx.QueryRowContext(ctx, `UPDATE native_raw_versions SET state='sealed',manifest_digest=$2,sealed_at=now(),updated_at=now() WHERE id=$1::uuid RETURNING sealed_at,updated_at`, found.ID, found.ManifestDigest).Scan(&found.SealedAt, &found.UpdatedAt); err != nil {
		return RawVersion{}, err
	}
	if err = insertAudits(ctx, tx, rawVersionAudit(input.Audit, found)); err != nil {
		return RawVersion{}, err
	}
	return found, tx.Commit()
}

// rawIdleSealMaxPages bounds how many candidate pages one call reads, so versions whose seal keeps failing cannot make a
// call read the whole table.
const rawIdleSealMaxPages = 20

func (s *PostgresStore) SealIdleRawVersions(ctx context.Context, idleBefore time.Time, limit int) ([]RawVersion, error) {
	if limit <= 0 {
		limit = 100
	}
	type candidate struct {
		repositoryID, name, version, repositoryName, id string
		updatedAt                                       time.Time
	}
	var sealed []RawVersion
	var failures error
	cursorAt, cursorID := time.Unix(0, 0).UTC(), uuid.Nil.String()
	for page := 0; page < rawIdleSealMaxPages && len(sealed) < limit; page++ {
		rows, err := s.db.QueryContext(ctx, rawIdleCandidatesQuery, idleBefore, limit, cursorAt, cursorID)
		if err != nil {
			return sealed, errors.Join(failures, err)
		}
		var candidates []candidate
		for rows.Next() {
			var c candidate
			if err = rows.Scan(&c.repositoryID, &c.name, &c.version, &c.repositoryName, &c.updatedAt, &c.id); err != nil {
				_ = rows.Close()
				return sealed, errors.Join(failures, err)
			}
			candidates = append(candidates, c)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return sealed, errors.Join(failures, err)
		}
		for _, c := range candidates {
			version, sealErr := s.sealRawVersion(ctx, SealRawVersionInput{RepositoryID: c.repositoryID, Name: c.name, Version: c.version, Actor: rawVersionSweepActor, Audit: rawVersionIdleSealAudit(c.repositoryName)}, idleBefore)
			switch {
			case sealErr == nil:
				sealed = append(sealed, version)
			case errors.Is(sealErr, errRawSealSkipped) || errors.Is(sealErr, ErrVersionEmpty) || errors.Is(sealErr, ErrNotFound):
			case ctx.Err() != nil:
				return sealed, errors.Join(failures, ctx.Err())
			default:
				// One version that cannot be sealed must not keep the younger ones from being sealed: report it and go on.
				failures = errors.Join(failures, fmt.Errorf("seal %s@%s of repository %s: %w", c.name, c.version, c.repositoryName, sealErr))
			}
			cursorAt, cursorID = c.updatedAt, c.id
		}
		if len(candidates) < limit {
			break
		}
	}
	return sealed, failures
}

func (s *PostgresStore) RawOpenVersionStats(ctx context.Context, staleBefore time.Time) (RawOpenVersionStats, error) {
	var stats RawOpenVersionStats
	var oldest sql.NullTime
	err := s.db.QueryRowContext(ctx, rawOpenStatsQuery, staleBefore).Scan(&stats.Stale, &oldest)
	if oldest.Valid {
		stats.OldestCreatedAt = oldest.Time
	}
	return stats, err
}
