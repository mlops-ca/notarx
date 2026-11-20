package repository

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

var _ NativeRawVersionStore = (*MemoryStore)(nil)

func rawVersionKey(repositoryID, name, version string) string {
	return repositoryID + "\x00" + name + "\x00" + version
}

// SetRawMaxFilesPerVersion sets the files-per-version cap; zero or less restores the default.
func (s *MemoryStore) SetRawMaxFilesPerVersion(limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rawMaxFilesPerVersion = limit
}

func (s *MemoryStore) maxFilesPerVersionLocked() int {
	if s.rawMaxFilesPerVersion > 0 {
		return s.rawMaxFilesPerVersion
	}
	return DefaultRawMaxFilesPerVersion
}

// versionedRepositoryLocked returns the repository when it uses the versioned layout.
func (s *MemoryStore) versionedRepositoryLocked(repositoryID string) (HostedRepository, bool) {
	repo, ok := s.hostedRepositories[repositoryID]
	return repo, ok && repo.RawLayout == RawLayoutVersioned
}

func (s *MemoryStore) rawVersionFilesLocked(repositoryID, name, version string) []RawAsset {
	from, to := RawVersionPathRange(name, version)
	files := make([]RawAsset, 0)
	for _, asset := range s.rawAssets {
		if asset.RepositoryID == repositoryID && asset.Path >= from && asset.Path < to {
			files = append(files, asset)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

// admitVersionedWriteLocked applies the create-only rules of ADR 0028 to a write of asset. It reports a replay (the
// same digest is already stored, so nothing changes) and otherwise the version row the write must update.
func (s *MemoryStore) admitVersionedWriteLocked(asset RawAsset, condition RawWriteCondition) (current RawAsset, replay bool, err error) {
	parsed, err := ParseRawVersionPath(asset.Path)
	if err != nil {
		return RawAsset{}, false, err
	}
	if condition.RequirePresent || len(condition.MatchDigests) > 0 {
		return RawAsset{}, false, ErrPreconditionFailed
	}
	version, exists := s.rawVersions[rawVersionKey(asset.RepositoryID, parsed.Name, parsed.Version)]
	if exists && version.State == RawVersionTombstoned {
		return RawAsset{}, false, ErrVersionTombstoned
	}
	if existing, ok := s.rawAssets[rawAssetKey(asset.RepositoryID, asset.Path)]; ok {
		if existing.Digest == asset.Digest {
			existing.Replayed = true
			return existing, true, nil
		}
		return existing, false, ErrVersionFileExists
	}
	if !exists {
		return RawAsset{}, false, nil
	}
	if version.State == RawVersionSealed {
		return RawAsset{}, false, ErrVersionSealed
	}
	if version.FileCount >= s.maxFilesPerVersionLocked() {
		return RawAsset{}, false, ErrVersionFileLimit
	}
	return RawAsset{}, false, nil
}

// recordVersionedWriteLocked updates (or creates) the open version row after a file was added.
func (s *MemoryStore) recordVersionedWriteLocked(asset RawAsset, actor string) {
	parsed, _ := ParseRawVersionPath(asset.Path)
	if s.rawVersions == nil {
		s.rawVersions = map[string]RawVersion{}
	}
	key := rawVersionKey(asset.RepositoryID, parsed.Name, parsed.Version)
	now := time.Now().UTC()
	version, ok := s.rawVersions[key]
	if !ok {
		version = RawVersion{ID: ids.New(), RepositoryID: asset.RepositoryID, Name: parsed.Name, Version: parsed.Version, State: RawVersionOpen, CreatedBy: actor, CreatedAt: now, Properties: map[string]string{}}
	}
	version.FileCount++
	version.SizeBytes += asset.Size
	version.UpdatedAt = now
	s.rawVersions[key] = version
}

// deleteVersionedRawAssetLocked removes one file of an open version of a non-strict repository.
func (s *MemoryStore) deleteVersionedRawAssetLocked(ctx context.Context, repo HostedRepository, path string, audit []AuditRecord) error {
	parsed, err := ParseRawVersionPath(path)
	if err != nil {
		return ErrNotFound
	}
	key := rawAssetKey(repo.ID, path)
	asset, ok := s.rawAssets[key]
	if !ok {
		return ErrNotFound
	}
	versionKey := rawVersionKey(repo.ID, parsed.Name, parsed.Version)
	version := s.rawVersions[versionKey]
	switch {
	case version.State == RawVersionTombstoned:
		return ErrNotFound
	case repo.RawStrict:
		return ErrVersionFileImmutable
	case version.State == RawVersionSealed:
		return ErrVersionSealed
	}
	if err = s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	now := time.Now().UTC()
	version.FileCount--
	version.SizeBytes -= asset.Size
	version.UpdatedAt = now
	s.rawVersions[versionKey] = version
	if object, exists := s.rawObjects[asset.Digest]; exists {
		object.CreatedAt, object.CollectedAt = now, time.Time{}
		s.rawObjects[asset.Digest] = object
	}
	delete(s.rawAssets, key)
	return nil
}

func (s *MemoryStore) GetRawVersion(_ context.Context, repositoryID, name, version string) (RawVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	found, ok := s.rawVersions[rawVersionKey(repositoryID, name, version)]
	if !ok || found.State == RawVersionTombstoned {
		return RawVersion{}, ErrNotFound
	}
	found.Properties = cloneRawVersionProperties(found.Properties)
	return found, nil
}

func (s *MemoryStore) GetRawVersionWithFiles(_ context.Context, repositoryID, name, version string) (RawVersion, []RawAsset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	found, ok := s.rawVersions[rawVersionKey(repositoryID, name, version)]
	if !ok || found.State == RawVersionTombstoned {
		return RawVersion{}, nil, ErrNotFound
	}
	found.Properties = cloneRawVersionProperties(found.Properties)
	return found, s.rawVersionFilesLocked(repositoryID, name, version), nil
}

func (s *MemoryStore) ListRawPackages(_ context.Context, repositoryID, after string, limit int) ([]RawPackage, error) {
	if limit <= 0 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	byName := map[string]*RawPackage{}
	latest := map[string]RawVersion{}
	for _, version := range s.rawVersions {
		if version.RepositoryID != repositoryID || version.State == RawVersionTombstoned || version.Name <= after {
			continue
		}
		pkg := byName[version.Name]
		if pkg == nil {
			pkg = &RawPackage{Name: version.Name}
			byName[version.Name] = pkg
		}
		pkg.VersionCount++
		if version.State == RawVersionSealed && rawVersionNewer(version, latest[version.Name]) {
			latest[version.Name] = version
		}
	}
	packages := make([]RawPackage, 0, len(byName))
	for name, pkg := range byName {
		pkg.LatestVersion = latest[name].Version
		packages = append(packages, *pkg)
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].Name < packages[j].Name })
	if len(packages) > limit {
		packages = packages[:limit]
	}
	return packages, nil
}

// rawVersionNewer orders versions newest seal first, ties by id descending; a zero version is older than any.
func rawVersionNewer(a, b RawVersion) bool {
	if b.ID == "" {
		return true
	}
	if !a.SealedAt.Equal(b.SealedAt) {
		return a.SealedAt.After(b.SealedAt)
	}
	return a.ID > b.ID
}

func (s *MemoryStore) ListRawVersions(_ context.Context, query RawVersionQuery) ([]RawVersion, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	versions := make([]RawVersion, 0)
	for _, version := range s.rawVersions {
		if version.RepositoryID != query.RepositoryID || version.Name != query.Name || version.State == RawVersionTombstoned {
			continue
		}
		if query.State != "" && version.State != query.State {
			continue
		}
		if !query.OpenCreatedBefore.IsZero() && (version.State != RawVersionOpen || !version.CreatedAt.Before(query.OpenCreatedBefore)) {
			continue
		}
		matches := true
		for _, property := range query.Properties {
			if value, ok := version.Properties[property.Key]; !ok || value != property.Value {
				matches = false
				break
			}
		}
		if matches {
			versions = append(versions, version)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return rawVersionListBefore(versions[i], versions[j]) })
	if query.After != nil {
		kept := versions[:0]
		for _, version := range versions {
			if rawVersionAfterCursor(version, *query.After) {
				kept = append(kept, version)
			}
		}
		versions = kept
	}
	if len(versions) > limit {
		versions = versions[:limit]
	}
	for i := range versions {
		versions[i].Properties = cloneRawVersionProperties(versions[i].Properties)
	}
	return versions, nil
}

// rawVersionListBefore is the listing order: sealed_at descending with unsealed versions last, then id descending.
func rawVersionListBefore(a, b RawVersion) bool {
	switch {
	case a.SealedAt.IsZero() != b.SealedAt.IsZero():
		return !a.SealedAt.IsZero()
	case !a.SealedAt.Equal(b.SealedAt):
		return a.SealedAt.After(b.SealedAt)
	}
	return a.ID > b.ID
}

func rawVersionAfterCursor(version RawVersion, cursor RawVersionCursor) bool {
	return rawVersionListBefore(RawVersion{SealedAt: cursor.SealedAt, ID: cursor.ID}, version)
}

func (s *MemoryStore) ListRawVersionFiles(_ context.Context, repositoryID, name, version string) ([]RawAsset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rawVersionFilesLocked(repositoryID, name, version), nil
}

func (s *MemoryStore) CreateRawVersion(ctx context.Context, version RawVersion, audit ...AuditRecord) (RawVersion, bool, error) {
	if !ValidRawVersionName(version.Name) || !ValidRawVersionString(version.Version) {
		return RawVersion{}, false, ErrInvalidGenericPath
	}
	if err := ValidateRawVersionProperties(version.Properties); err != nil {
		return RawVersion{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.versionedRepositoryLocked(version.RepositoryID); !ok {
		return RawVersion{}, false, ErrRawLayoutMismatch
	}
	if s.rawVersions == nil {
		s.rawVersions = map[string]RawVersion{}
	}
	key := rawVersionKey(version.RepositoryID, version.Name, version.Version)
	if existing, ok := s.rawVersions[key]; ok {
		if existing.State == RawVersionTombstoned {
			return RawVersion{}, false, ErrVersionTombstoned
		}
		if existing.State == RawVersionSealed {
			// A create that repeats what a sealed version already is changes nothing, so a retried publisher is safe.
			if version.Properties != nil && !equalRawVersionProperties(existing.Properties, version.Properties) {
				return RawVersion{}, false, ErrVersionSealed
			}
			existing.Properties = cloneRawVersionProperties(existing.Properties)
			return existing, false, nil
		}
		if version.Properties != nil && !equalRawVersionProperties(existing.Properties, version.Properties) {
			if err := s.commitAuditLocked(ctx, audit); err != nil {
				return RawVersion{}, false, err
			}
			existing.Properties = cloneRawVersionProperties(version.Properties)
			existing.UpdatedAt = time.Now().UTC()
			s.rawVersions[key] = existing
		}
		existing.Properties = cloneRawVersionProperties(existing.Properties)
		return existing, false, nil
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return RawVersion{}, false, err
	}
	now := time.Now().UTC()
	created := RawVersion{ID: ids.New(), RepositoryID: version.RepositoryID, Name: version.Name, Version: version.Version, State: RawVersionOpen, CreatedBy: version.CreatedBy, CreatedAt: now, UpdatedAt: now, Properties: cloneRawVersionProperties(version.Properties)}
	s.rawVersions[key] = created
	created.Properties = cloneRawVersionProperties(created.Properties)
	return created, true, nil
}

func (s *MemoryStore) SetRawVersionProperties(ctx context.Context, repositoryID, name, version string, properties map[string]string, audit ...AuditRecord) (RawVersion, error) {
	if err := ValidateRawVersionProperties(properties); err != nil {
		return RawVersion{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := rawVersionKey(repositoryID, name, version)
	existing, ok := s.rawVersions[key]
	if !ok {
		return RawVersion{}, ErrNotFound
	}
	switch existing.State {
	case RawVersionTombstoned:
		return RawVersion{}, ErrVersionTombstoned
	case RawVersionSealed:
		return RawVersion{}, ErrVersionSealed
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return RawVersion{}, err
	}
	existing.Properties = cloneRawVersionProperties(properties)
	existing.UpdatedAt = time.Now().UTC()
	s.rawVersions[key] = existing
	existing.Properties = cloneRawVersionProperties(existing.Properties)
	return existing, nil
}

func (s *MemoryStore) SealRawVersion(ctx context.Context, input SealRawVersionInput) (RawVersion, error) {
	if input.Properties != nil {
		if err := ValidateRawVersionProperties(input.Properties); err != nil {
			return RawVersion{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := rawVersionKey(input.RepositoryID, input.Name, input.Version)
	version, ok := s.rawVersions[key]
	if !ok {
		return RawVersion{}, ErrNotFound
	}
	sealed, err := s.sealRawVersionLocked(ctx, key, version, input, time.Now().UTC())
	if err != nil {
		return RawVersion{}, err
	}
	sealed.Properties = cloneRawVersionProperties(sealed.Properties)
	return sealed, nil
}

func (s *MemoryStore) sealRawVersionLocked(ctx context.Context, key string, version RawVersion, input SealRawVersionInput, now time.Time) (RawVersion, error) {
	switch version.State {
	case RawVersionTombstoned:
		return RawVersion{}, ErrVersionTombstoned
	case RawVersionSealed:
		if input.Properties != nil && !equalRawVersionProperties(version.Properties, input.Properties) {
			return RawVersion{}, ErrVersionSealed
		}
		// A repeated seal still has to agree with the files that were sealed.
		if input.Expected != nil || input.ChecksumsFile != "" {
			if err := verifyRawVersionSeal(input, s.rawVersionFilesLocked(version.RepositoryID, version.Name, version.Version)); err != nil {
				return RawVersion{}, err
			}
		}
		return version, nil
	}
	files := s.rawVersionFilesLocked(version.RepositoryID, version.Name, version.Version)
	if len(files) == 0 {
		return RawVersion{}, ErrVersionEmpty
	}
	if err := verifyRawVersionSeal(input, files); err != nil {
		return RawVersion{}, err
	}
	if input.Properties != nil {
		version.Properties = cloneRawVersionProperties(input.Properties)
	}
	_, digest := RawVersionManifest(version.Name, version.Version, files, version.Properties)
	version.State, version.ManifestDigest, version.SealedAt, version.UpdatedAt = RawVersionSealed, digest, now, now
	if err := s.commitAuditLocked(ctx, rawVersionAudit(input.Audit, version)); err != nil {
		return RawVersion{}, err
	}
	s.rawVersions[key] = version
	return version, nil
}

func (s *MemoryStore) SealIdleRawVersions(ctx context.Context, idleBefore time.Time, limit int) ([]RawVersion, error) {
	if limit <= 0 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	candidates := make([]string, 0)
	for key, version := range s.rawVersions {
		repo, ok := s.hostedRepositories[version.RepositoryID]
		if ok && repo.State == RepositoryActive && repo.SealMode == RawSealIdle && version.State == RawVersionOpen && version.FileCount > 0 && version.UpdatedAt.Before(idleBefore) && !s.rawVersionUploadingLocked(version) {
			candidates = append(candidates, key)
		}
	}
	// Oldest write first, as the PostgreSQL sweep reads them.
	sort.Slice(candidates, func(i, j int) bool {
		a, b := s.rawVersions[candidates[i]], s.rawVersions[candidates[j]]
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.Before(b.UpdatedAt)
		}
		return a.ID < b.ID
	})
	sealed := make([]RawVersion, 0, limit)
	var failures error
	now := time.Now().UTC()
	for _, key := range candidates {
		if len(sealed) >= limit {
			break
		}
		version := s.rawVersions[key]
		result, err := s.sealRawVersionLocked(ctx, key, version, SealRawVersionInput{Actor: rawVersionSweepActor, Audit: rawVersionIdleSealAudit(s.hostedRepositories[version.RepositoryID].Name)}, now)
		if err != nil {
			// One version that cannot be sealed must not keep the younger ones from being sealed; its idle clock
			// restarts, as in the PostgreSQL sweep.
			if !errors.Is(err, ErrVersionEmpty) {
				failures = errors.Join(failures, err)
			}
			version.UpdatedAt = now
			s.rawVersions[key] = version
			continue
		}
		sealed = append(sealed, result)
	}
	return sealed, failures
}

func (s *MemoryStore) RawOpenVersionStats(_ context.Context, staleBefore time.Time) (RawOpenVersionStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var stats RawOpenVersionStats
	for _, version := range s.rawVersions {
		if version.State != RawVersionOpen {
			continue
		}
		if stats.OldestCreatedAt.IsZero() || version.CreatedAt.Before(stats.OldestCreatedAt) {
			stats.OldestCreatedAt = version.CreatedAt
		}
		if version.CreatedAt.Before(staleBefore) {
			stats.Stale++
		}
	}
	return stats, nil
}

// rawVersionUploadingLocked reports whether a resumable upload of a file of the version is still open; the idle sweep
// leaves such a version open however long the upload takes.
func (s *MemoryStore) rawVersionUploadingLocked(version RawVersion) bool {
	prefix, _ := RawVersionPathRange(version.Name, version.Version)
	now := time.Now()
	for _, upload := range s.rawUploads {
		if upload.RepositoryID == version.RepositoryID && upload.State == "open" && now.Before(upload.ExpiresAt) && strings.HasPrefix(upload.Path, prefix) {
			return true
		}
	}
	return false
}
