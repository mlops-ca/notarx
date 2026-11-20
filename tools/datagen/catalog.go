package datagen

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// catalogColumns are the columns of artifact_catalog that the seed writes (ADR 0023 section 8).
var catalogColumns = []string{"repository_id", "format", "coordinate", "latest_version", "latest_digest", "latest_published_at", "publisher", "version_count", "size_bytes"}

// catalogEntry is one artifact_catalog row. The values are what the store computes from
// artifact_search_projection (internal/repository/artifact_catalog.go): the row that ranks first by newest
// created_at then highest digest supplies latest_*, publisher and size_bytes (NULL where the projection has no
// size), and version_count counts distinct versions, or distinct digests where the format has no version.
type catalogEntry struct {
	repository                 pgtype.UUID
	format, coordinate         string
	version, digest, publisher string
	publishedAt                time.Time
	versions                   int32
	size                       any
}

func (e catalogEntry) row() []any {
	return []any{e.repository, e.format, e.coordinate, e.version, e.digest, e.publishedAt, e.publisher, e.versions, e.size}
}

// catalogFormats are the formats the catalogue covers: the ones of artifact_search_projection
// (artifactCatalogRegistry in internal/repository). cargo, nuget, helm and rubygems are not projected, so the
// store keeps no catalogue rows for them either.
var catalogFormats = map[string]bool{"oci": true, "maven": true, "pypi": true, "go": true, "raw": true, "conan": true, "apt": true}

// perCoordinate reports the formats whose catalogue coordinate groups several versions (a package, an image
// name, a module, a project): their row summarises the versions, and the newest is version 0 because the seed
// dates versions downwards. The other formats address one version per coordinate (maven: the full coordinate,
// conan: the reference, raw and apt: the path), so each row stands alone with a count of one.
var perCoordinate = map[string]bool{"oci": true, "pypi": true, "go": true}

// catalogEntryOf returns the catalogue row of the coordinate that version c belongs to; versions is the number
// of versions of its package, which perCoordinate formats count.
func catalogEntryOf(f *formatRun, c coordinate, versions int) (catalogEntry, bool) {
	e := catalogEntry{repository: f.repo(c), format: f.format, digest: "sha256:" + f.digest(c, ""), publishedAt: f.created(c), versions: 1}
	switch f.format {
	case "oci":
		e.coordinate, e.size = ociName(c), f.size(c)
	case "maven":
		e.coordinate, e.version, e.publisher = mavenCoordinate(f, c), f.semver(c), "perf-publisher"
	case "pypi":
		e.coordinate, e.version, e.publisher, e.size = pypiProject(c), f.semver(c), "perf-publisher", f.size(c)
	case "go":
		// The projection takes the digest and size of the zip asset, which ranks first among the assets of a version.
		e.coordinate, e.version, e.publisher, e.size = goModule(c), "v"+f.semver(c), "perf-publisher", f.size(c)
		e.digest = "sha256:" + f.digest(c, "zip")
	case "raw":
		e.coordinate, e.size = rawPath(c), f.size(c)
	case "apt":
		e.coordinate, e.size = aptPath(f, c), f.size(c)
	case "conan":
		e.coordinate, e.version, e.publisher = conanReference(f, c), f.semver(c), "perf-publisher"
	default:
		return catalogEntry{}, false
	}
	if perCoordinate[f.format] {
		e.versions = int32(versions)
	}
	return e, true
}

// seedCatalog writes the catalogue rows of one format from the plan its version rows were written with.
func seedCatalog(ctx context.Context, db Copier, f *formatRun, packages []packagePlan) (int64, error) {
	if !catalogFormats[f.format] {
		return 0, nil
	}
	pkg, ver := 0, 0
	n, err := db.CopyFrom(ctx, pgx.Identifier{"artifact_catalog"}, catalogColumns, pgx.CopyFromFunc(func() ([]any, error) {
		if pkg == len(packages) {
			return nil, nil
		}
		entry, _ := catalogEntryOf(f, coordinate{repo: packages[pkg].repository, pkg: pkg, ver: ver}, packages[pkg].versions)
		if perCoordinate[f.format] {
			pkg, ver = pkg+1, 0
		} else if ver++; ver == packages[pkg].versions {
			pkg, ver = pkg+1, 0
		}
		return entry.row(), nil
	}))
	if err != nil {
		return n, fmt.Errorf("datagen: copy artifact_catalog (%s): %w", f.format, err)
	}
	return n, nil
}

// seedNPMCatalog writes one catalogue row per npm package. sizes holds the size of the newest version of each
// package, which the version rows draw from the random generator while they are written.
func seedNPMCatalog(ctx context.Context, db Copier, packages []packagePlan, repositoryIDs []uuid.UUID, sizes []int64, now time.Time) (int64, error) {
	pkg := 0
	n, err := db.CopyFrom(ctx, pgx.Identifier{"artifact_catalog"}, catalogColumns, pgx.CopyFromFunc(func() ([]any, error) {
		if pkg == len(packages) {
			return nil, nil
		}
		name := packageName(pkg)
		entry := catalogEntry{
			repository: uuidOf(repositoryIDs[packages[pkg].repository]), format: "npm", coordinate: name, version: "1.0.0",
			digest: "sha256:" + digestOf(name+"@1.0.0"), publishedAt: now.Add(-time.Duration(pkg*averageVersionsPerPackage) * time.Second),
			publisher: "perf-publisher", versions: int32(packages[pkg].versions), size: sizes[pkg],
		}
		pkg++
		return entry.row(), nil
	}))
	if err != nil {
		return n, fmt.Errorf("datagen: copy artifact_catalog (npm): %w", err)
	}
	return n, nil
}
