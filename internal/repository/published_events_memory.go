package repository

import (
	"strings"
	"time"
)

// hostedOrUnknownLocked reports whether repositoryID is not a proxy or group: a cache write of a proxy repository is not a
// publication. A repository the store does not know (a fixture that wrote rows directly) counts as hosted.
func (s *MemoryStore) hostedOrUnknownLocked(repositoryID string) bool {
	repo, ok := s.hostedRepositories[repositoryID]
	return !ok || repo.Type == RepositoryTypeHosted
}

// addNativePublicationCandidatesLocked adds the visible publications of npm, PyPI, Go, Cargo, Conan and APT, the way the
// hostedPublicationQueries select them for PostgreSQL. The caller holds s.mu.
func (s *MemoryStore) addNativePublicationCandidatesLocked(add func(at time.Time, c publishedCandidate)) {
	for _, version := range s.npmVersions {
		if (version.State == "" || version.State == "visible") && version.Digest != "" && s.hostedOrUnknownLocked(version.RepositoryID) {
			add(version.CreatedAt, publishedCandidate{version.RepositoryID, string(FormatNPM), version.PackageName + "@" + version.Version, version.Digest})
		}
	}
	for _, file := range s.pypiFiles {
		if file.State == "visible" && file.ObjectKey != "" && s.hostedOrUnknownLocked(file.RepositoryID) {
			add(file.CreatedAt, publishedCandidate{file.RepositoryID, string(FormatPyPI), file.Project + "@" + file.Version, file.Digest})
		}
	}
	for _, asset := range s.goAssets {
		if asset.Kind == "zip" && asset.CollectedAt.IsZero() && s.hostedOrUnknownLocked(asset.RepositoryID) && !s.goModuleVersionTombstonedLocked(asset.RepositoryID, asset.Module, asset.Version) {
			add(asset.CreatedAt, publishedCandidate{asset.RepositoryID, string(FormatGo), goModuleCoordinate(asset.Module, asset.Version), asset.Digest})
		}
	}
	for _, publication := range s.cargoPublications {
		if publication.CollectedAt.IsZero() && !s.cargoPublicationTombstonedLocked(publication) {
			add(publication.CreatedAt, publishedCandidate{publication.RepositoryID, string(FormatCargo), cargoPublicationCoordinate(publication.Name, publication.Version), publication.Digest})
		}
	}
	for _, recipe := range s.conanRecipes {
		if recipe.State == "visible" && s.hostedOrUnknownLocked(recipe.RepositoryID) {
			add(recipe.CreatedAt, publishedCandidate{recipe.RepositoryID, string(FormatConan), conanRecipeCoordinate(recipe.Reference, recipe.Revision), recipe.Digest})
		}
	}
	for _, pkg := range s.conanPackages {
		if pkg.State == "visible" && s.hostedOrUnknownLocked(pkg.RepositoryID) {
			add(pkg.CreatedAt, publishedCandidate{pkg.RepositoryID, string(FormatConan), conanPackageCoordinate(pkg.Reference, pkg.RecipeRevision, pkg.PackageID, pkg.Revision), pkg.Digest})
		}
	}
	// APT: a pool path is published by the earliest published snapshot of its repository that carries it with this digest.
	type poolKey struct{ repositoryID, path, digest string }
	first := map[poolKey]time.Time{}
	for id, snapshot := range s.aptSnapshots {
		if snapshot.State != APTRepositorySnapshotVisible && snapshot.State != APTRepositorySnapshotRetired {
			continue
		}
		for _, asset := range s.aptSnapshotAssets[id] {
			if !strings.HasPrefix(asset.Path, "pool/") {
				continue
			}
			key := poolKey{snapshot.RepositoryID, asset.Path, asset.Digest}
			if at, seen := first[key]; !seen || snapshot.PublishedAt.Before(at) {
				first[key] = snapshot.PublishedAt
			}
		}
	}
	for key, at := range first {
		add(at, publishedCandidate{key.repositoryID, string(FormatAPT), key.path, key.digest})
	}
}
