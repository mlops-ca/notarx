package datagen

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// FormatShare is the share of a profile's version budget that one hosted format receives.
type FormatShare struct {
	Format  string
	Percent int
}

// FormatMix is the documented split of the version budget of a profile across the hosted formats other than npm
// (ADR 0023 section 11: the target database holds its versions across all formats). npm receives the remainder,
// 30 percent with the shares below, so it stays the widest table. The shares follow how a registry of mixed
// ecosystems is used: container images, Maven and Python artifacts dominate, the rest is a tail.
var FormatMix = []FormatShare{
	{"oci", 15}, {"maven", 15}, {"pypi", 10}, {"go", 8}, {"raw", 6}, {"cargo", 4},
	{"nuget", 4}, {"conan", 3}, {"helm", 2}, {"rubygems", 2}, {"apt", 1},
}

// formatRepositories is the number of repositories of each format other than npm.
func formatRepositories(p Profile) int { return max(1, p.Repositories/10) }

// minAllFormatsVersions is the smallest version budget of a profile that spreads versions across all formats: one
// percent of it still gives the smallest share a row.
const minAllFormatsVersions = 100

// otherFormatVersions returns the per-format budgets of FormatMix for a profile of the given size.
func otherFormatVersions(versions int) map[string]int {
	budgets := make(map[string]int, len(FormatMix))
	for _, share := range FormatMix {
		budgets[share.Format] = max(1, versions*share.Percent/100)
	}
	return budgets
}

// coordinate addresses one version: the repository of its format (an index into formatRun.repos), the package
// within the format and the version within the package.
type coordinate struct{ repo, pkg, ver int }

// formatRun is what the row builders of one format share.
type formatRun struct {
	format string
	repos  []uuid.UUID
	rng    *rand.Rand
	now    time.Time
}

func (f *formatRun) repo(c coordinate) pgtype.UUID { return uuidOf(f.repos[c.repo]) }

// key identifies a version across the whole database.
func (f *formatRun) key(c coordinate) string { return fmt.Sprintf("%s/%d/%d", f.format, c.pkg, c.ver) }

// digest is the bare hex digest of a version, with a suffix to tell its objects apart.
func (f *formatRun) digest(c coordinate, suffix string) string { return digestOf(f.key(c) + suffix) }

// id is a deterministic UUID for the object of a version, shared by the tables that reference each other.
func (f *formatRun) id(c coordinate) pgtype.UUID {
	return uuidOf(uuid.NewSHA1(uuid.NameSpaceOID, []byte(f.key(c))))
}

func (f *formatRun) created(c coordinate) time.Time {
	return f.now.Add(-time.Duration(c.pkg*averageVersionsPerPackage+c.ver) * time.Second)
}

// size is derived from the coordinate, so the rows of one version that carry its size agree and the output stays deterministic.
func (f *formatRun) size(c coordinate) int64 {
	var n int64
	for _, b := range f.key(c) {
		n = (n*31 + int64(b)) % (1 << 20)
	}
	return 1024 + n
}

func (f *formatRun) semver(c coordinate) string { return fmt.Sprintf("1.%d.0", c.ver) }

// tableSpec is one table a format writes: its columns, how many rows each version (or package) yields and the
// row builder. Tables are written in order, so a referenced table comes first.
type tableSpec struct {
	table   string
	columns []string
	// perPackage writes one row per package instead of per version.
	perPackage bool
	// perVersion is the number of rows per version; zero means one.
	perVersion int
	row        func(f *formatRun, c coordinate, k int) []any
}

// walk streams the rows of a table over the plan of a format.
func (t tableSpec) walk(f *formatRun, packages []packagePlan) pgx.CopyFromSource {
	pkg, version, k := 0, 0, 0
	per := max(1, t.perVersion)
	return pgx.CopyFromFunc(func() ([]any, error) {
		if pkg == len(packages) {
			return nil, nil
		}
		c := coordinate{repo: packages[pkg].repository, pkg: pkg, ver: version}
		row := t.row(f, c, k)
		switch {
		case t.perPackage:
			pkg++
		default:
			if k++; k == per {
				k, version = 0, version+1
			}
			if version == packages[pkg].versions {
				pkg, version = pkg+1, 0
			}
		}
		return row, nil
	})
}

// formatTables returns the tables of a format in write order.
func formatTables(format string) []tableSpec {
	switch format {
	case "oci":
		return []tableSpec{{table: "native_oci_manifests",
			columns: []string{"repository_id", "name", "digest", "object_key", "media_type", "size", "created_at", "artifact_type"},
			row: func(f *formatRun, c coordinate, _ int) []any {
				d := f.digest(c, "")
				return []any{f.repo(c), fmt.Sprintf("perf/image-%08d", c.pkg), "sha256:" + d, "perf/oci/" + d,
					"application/vnd.oci.image.manifest.v1+json", f.size(c), f.created(c), ""}
			}}}
	case "maven":
		return []tableSpec{{table: "native_maven_artifacts",
			columns: []string{"id", "repository_id", "coordinate", "digest", "state", "created_at", "build_number", "publisher"},
			row: func(f *formatRun, c coordinate, _ int) []any {
				return []any{f.id(c), f.repo(c), fmt.Sprintf("com.perf:perf-art-%08d:%s", c.pkg, f.semver(c)), "sha256:" + f.digest(c, ""),
					"visible", f.created(c), int32(0), "perf-publisher"}
			}}}
	case "pypi":
		return []tableSpec{{table: "native_pypi_files",
			columns: []string{"repository_id", "project", "version", "filename", "file_type", "digest", "object_key", "size", "publisher", "state", "cached_at", "created_at"},
			row: func(f *formatRun, c coordinate, _ int) []any {
				project, d := fmt.Sprintf("perf-pkg-%08d", c.pkg), f.digest(c, "")
				return []any{f.repo(c), project, f.semver(c), project + "-" + f.semver(c) + ".tar.gz", "sdist", "sha256:" + d, "perf/pypi/" + d,
					f.size(c), "perf-publisher", "visible", f.created(c), f.created(c)}
			}}}
	case "go":
		kinds := []string{"info", "mod", "zip"}
		module := func(c coordinate) string { return fmt.Sprintf("example.test/perf/mod%08d", c.pkg) }
		return []tableSpec{
			{table: "native_go_versions",
				columns: []string{"repository_id", "module_path", "version", "published_at", "publisher", "cached_at", "created_at"},
				row: func(f *formatRun, c coordinate, _ int) []any {
					return []any{f.repo(c), module(c), "v" + f.semver(c), f.created(c), "perf-publisher", f.created(c), f.created(c)}
				}},
			{table: "native_go_assets", perVersion: len(kinds),
				columns: []string{"repository_id", "module_path", "version", "kind", "digest", "object_key", "size", "source_url", "cached_at", "created_at"},
				row: func(f *formatRun, c coordinate, k int) []any {
					d := f.digest(c, kinds[k])
					return []any{f.repo(c), module(c), "v" + f.semver(c), kinds[k], "sha256:" + d, "perf/go/" + d, f.size(c), "", f.created(c), f.created(c)}
				}},
		}
	case "raw":
		return []tableSpec{
			{table: "native_raw_objects", columns: []string{"digest", "object_key", "size", "created_at", "repository_id"},
				row: func(f *formatRun, c coordinate, _ int) []any {
					d := f.digest(c, "")
					return []any{"sha256:" + d, "perf/raw/" + d[:2] + "/" + d, f.size(c), f.created(c), f.repo(c)}
				}},
			{table: "native_raw_assets", columns: []string{"repository_id", "path", "digest", "content_type", "updated_at"},
				row: func(f *formatRun, c coordinate, _ int) []any {
					return []any{f.repo(c), fmt.Sprintf("perf/pkg-%08d/%d.bin", c.pkg, c.ver), "sha256:" + f.digest(c, ""), "application/octet-stream", f.created(c)}
				}},
		}
	case "cargo":
		crate := func(c coordinate) string { return fmt.Sprintf("perf-crate-%08d", c.pkg) }
		return []tableSpec{
			{table: "native_cargo_names", perPackage: true, columns: []string{"repository_id", "collision_key", "name", "normalized_name", "created_at"},
				row: func(f *formatRun, c coordinate, _ int) []any {
					return []any{f.repo(c), crate(c), crate(c), crate(c), f.created(c)}
				}},
			{table: "native_cargo_identity_reservations", columns: []string{"repository_id", "collision_key", "version_key", "version", "digest", "metadata_digest", "created_at"},
				row: func(f *formatRun, c coordinate, _ int) []any {
					return []any{f.repo(c), crate(c), f.semver(c), f.semver(c), "sha256:" + f.digest(c, ""), "sha256:" + f.digest(c, "meta"), f.created(c)}
				}},
			{table: "native_cargo_publications",
				columns: []string{"repository_id", "collision_key", "version_key", "object_key", "size", "index_row", "publisher", "published_at", "created_at", "updated_at", "description"},
				row: func(f *formatRun, c coordinate, _ int) []any {
					d := f.digest(c, "")
					return []any{f.repo(c), crate(c), f.semver(c), "native/cargo/sha256/" + d, f.size(c),
						fmt.Sprintf(`{"name":%q,"vers":%q,"cksum":%q}`, crate(c), f.semver(c), d), "perf-publisher", f.created(c), f.created(c), f.created(c), "synthetic crate"}
				}},
		}
	case "nuget":
		return publicationTables("nuget", "native_nuget_publication_sessions", "native_nuget_publications", "package_id", "nuspec",
			func(c coordinate) string { return fmt.Sprintf("perf.pkg.%08d", c.pkg) },
			func(identity, version string) []byte {
				return []byte(fmt.Sprintf("<package><metadata><id>%s</id><version>%s</version></metadata></package>", identity, version))
			})
	case "conan":
		return []tableSpec{{table: "native_conan_recipe_revisions",
			columns: []string{"repository_id", "reference", "revision", "digest", "state", "created_at", "publisher"},
			row: func(f *formatRun, c coordinate, _ int) []any {
				d := f.digest(c, "")
				return []any{f.repo(c), fmt.Sprintf("perf-pkg-%08d/%s", c.pkg, f.semver(c)), d[:32], "sha256:" + d, "visible", f.created(c), "perf-publisher"}
			}}}
	case "helm":
		return publicationTables("helm", "native_helm_publication_sessions", "native_helm_publications", "name", "chart_yaml",
			func(c coordinate) string { return fmt.Sprintf("perf-chart-%08d", c.pkg) },
			func(identity, version string) []byte {
				return []byte(fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\n", identity, version))
			})
	case "rubygems":
		return []tableSpec{{table: "native_rubygems_sessions",
			columns: []string{"id", "repository_id", "name", "version", "canonical_version", "platform", "full_name", "digest", "size", "actor", "state", "object_key", "metadata_json", "metadata_yaml", "created_at", "expires_at", "committed_at"},
			row: func(f *formatRun, c coordinate, _ int) []any {
				name, d := fmt.Sprintf("perf-gem-%08d", c.pkg), f.digest(c, "")
				return []any{f.id(c), f.repo(c), name, f.semver(c), f.semver(c), "ruby", name + "-" + f.semver(c), "sha256:" + d, f.size(c), "perf-publisher", "committed",
					"perf/rubygems/" + d, fmt.Sprintf(`{"name":%q,"version":%q}`, name, f.semver(c)), []byte("--- !ruby/object:Gem::Specification\nname: " + name + "\n"),
					f.created(c), f.created(c).Add(24 * time.Hour), f.created(c)}
			}}}
	case "apt":
		return []tableSpec{{table: "native_apt_assets",
			columns: []string{"repository_id", "path", "digest", "object_key", "size", "content_type", "source_url", "cached_at", "created_at"},
			row: func(f *formatRun, c coordinate, _ int) []any {
				d := f.digest(c, "")
				return []any{f.repo(c), fmt.Sprintf("pool/main/p/perf-pkg-%08d/perf-pkg-%08d_%s_amd64.deb", c.pkg, c.pkg, f.semver(c)), "sha256:" + d, "perf/apt/" + d,
					f.size(c), "application/vnd.debian.binary-package", "", f.created(c), f.created(c)}
			}}}
	}
	return nil
}

// publicationTables returns the committed-session and publication tables of NuGet and Helm, which share a shape: a
// publication references its session by id and repeats the digest, size, object key, metadata and document of it.
// identityColumn is the name column of both tables and documentColumn the bytea column (nuspec or chart_yaml).
func publicationTables(format, sessionTable, publicationTable, identityColumn, documentColumn string,
	identity func(coordinate) string, document func(identity, version string) []byte) []tableSpec {
	values := func(f *formatRun, c coordinate) (string, string, string, []byte, string) {
		name, version, d := identity(c), f.semver(c), f.digest(c, "")
		return name, "sha256:" + d, fmt.Sprintf(`{"name":%q,"version":%q}`, name, version), document(name, version), "perf/" + format + "/" + d
	}
	return []tableSpec{
		{table: sessionTable,
			columns: []string{"id", "repository_id", identityColumn, "version", "digest", "size", "actor", "state", "object_key", "metadata_json", documentColumn, "created_at", "expires_at", "committed_at"},
			row: func(f *formatRun, c coordinate, _ int) []any {
				name, digest, metadata, doc, key := values(f, c)
				return []any{f.id(c), f.repo(c), name, f.semver(c), digest, f.size(c), "perf-publisher", "committed", key, metadata, doc,
					f.created(c), f.created(c).Add(24 * time.Hour), f.created(c)}
			}},
		{table: publicationTable,
			columns: []string{"repository_id", identityColumn, "version", "session_id", "digest", "object_key", "size", "metadata_json", documentColumn, "publisher", "published_at"},
			row: func(f *formatRun, c coordinate, _ int) []any {
				name, digest, metadata, doc, key := values(f, c)
				return []any{f.repo(c), name, f.semver(c), f.id(c), digest, key, f.size(c), metadata, doc, "perf-publisher", f.created(c)}
			}},
	}
}

// seedFormats writes the versions of every format of FormatMix through bulk COPY, with Zipfian placement of
// packages in the repositories of their format, and returns the number of rows written.
func seedFormats(ctx context.Context, db Copier, p Profile, rng *rand.Rand, now time.Time, repos map[string][]uuid.UUID) (int64, error) {
	var total int64
	budgets := otherFormatVersions(p.Versions)
	for _, share := range FormatMix {
		run := &formatRun{format: share.Format, repos: repos[share.Format], rng: rng, now: now}
		packages := plan(Profile{Versions: budgets[share.Format], Repositories: len(run.repos)}, rng)
		for _, table := range formatTables(share.Format) {
			n, err := db.CopyFrom(ctx, pgx.Identifier{table.table}, table.columns, table.walk(run, packages))
			if err != nil {
				return total, fmt.Errorf("datagen: copy %s: %w", table.table, err)
			}
			total += n
		}
	}
	return total, nil
}
