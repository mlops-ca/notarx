// Package datagen seeds a PostgreSQL database with synthetic catalogue and audit
// rows at the sizes of the ADR 0023 section 11 profiles, so plans, latencies and
// capacity can be measured instead of estimated.
//
// Popularity is Zipfian: a handful of repositories, packages and actors receive most
// of the traffic, so hot artifacts exist. Generation is deterministic for a given
// profile and seed. The seed format is npm because its tables carry the widest
// per-version rows; further formats join as their catalogue slices land.
package datagen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mlops-ca/notarx/internal/ids"
)

// Profile sizes one seeding run.
type Profile struct {
	Name string
	// Versions is the number of native_npm_versions rows.
	Versions int
	// AuditRows is the number of resolver_audit_log rows.
	AuditRows int
	// Repositories is the number of hosted npm repositories.
	Repositories int
	// AuditWindow is how far back audit rows are spread.
	AuditWindow time.Duration
}

// averageVersionsPerPackage is the mean number of versions a package gets; the
// spread between 1 and twice this value is uniform.
const averageVersionsPerPackage = 8

// zipfSkew is the Zipf exponent: above 1, so a few ranks dominate.
const zipfSkew = 1.2

// Profiles are the named sizes of ADR 0023 section 11 (the webhook delivery
// volume of the target profile is not generated yet).
var Profiles = map[string]Profile{
	"tiny":     {Name: "tiny", Versions: 100_000, AuditRows: 1_000_000, Repositories: 20, AuditWindow: 30 * 24 * time.Hour},
	"freetier": {Name: "freetier", Versions: 1_000_000, AuditRows: 20_000_000, Repositories: 50, AuditWindow: 7 * 24 * time.Hour},
	"target":   {Name: "target", Versions: 10_000_000, AuditRows: 100_000_000, Repositories: 200, AuditWindow: 90 * 24 * time.Hour},
}

// ParseProfile returns the named profile.
func ParseProfile(name string) (Profile, error) {
	profile, ok := Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("datagen: unknown profile %q (want tiny, freetier or target)", name)
	}
	return profile, nil
}

// Scaled returns the profile with every count divided by divisor (at least 1 each),
// for runs that need the shape of a profile at a fraction of its cost.
func (p Profile) Scaled(divisor int) Profile {
	if divisor <= 1 {
		return p
	}
	scale := func(n int) int { return max(1, n/divisor) }
	p.Versions = scale(p.Versions)
	p.AuditRows = scale(p.AuditRows)
	p.Repositories = max(1, min(p.Repositories, scale(p.Repositories*10)))
	return p
}

// Popularity draws ranks in [0, n) with a Zipf distribution: rank 0 is the hottest.
type Popularity struct{ zipf *rand.Zipf }

// NewPopularity returns a sampler over n ranks driven by rng.
func NewPopularity(rng *rand.Rand, n int) Popularity {
	if n < 2 {
		n = 2
	}
	return Popularity{zipf: rand.NewZipf(rng, zipfSkew, 1, uint64(n-1))}
}

// Next returns the next rank.
func (p Popularity) Next() int { return int(p.zipf.Uint64()) }

// Copier is the bulk-load surface of *pgx.Conn and pgx.Tx.
type Copier interface {
	CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error)
}

// Counts reports how many rows Seed wrote per table.
type Counts struct {
	Repositories, Packages, Versions, AuditRows int64
}

type packagePlan struct {
	repository int
	versions   int
}

// plan splits the version budget into packages and places each package in a
// repository by popularity, so hot repositories hold most packages.
func plan(p Profile, rng *rand.Rand) []packagePlan {
	repos := NewPopularity(rng, p.Repositories)
	var packages []packagePlan
	for remaining := p.Versions; remaining > 0; {
		n := min(remaining, 1+rng.Intn(2*averageVersionsPerPackage-1))
		packages = append(packages, packagePlan{repository: repos.Next() % p.Repositories, versions: n})
		remaining -= n
	}
	return packages
}

func repositoryName(i int) string { return fmt.Sprintf("perf-npm-%04d", i) }
func packageName(i int) string    { return fmt.Sprintf("perf-pkg-%08d", i) }

func digestOf(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// Seed writes the profile through bulk COPY. The target database must be migrated
// and must not already hold seeded repositories (the repository names are fixed, so
// a second run fails on the unique name rather than doubling the data).
func Seed(ctx context.Context, db Copier, p Profile, seed int64, now time.Time) (Counts, error) {
	if p.Versions < 1 || p.AuditRows < 0 || p.Repositories < 1 {
		return Counts{}, fmt.Errorf("datagen: profile %q has non-positive sizes", p.Name)
	}
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // synthetic data, determinism wanted
	var counts Counts

	repositoryIDs := make([]uuid.UUID, p.Repositories)
	repositoryRows := make([][]any, p.Repositories)
	for i := range repositoryIDs {
		repositoryIDs[i] = ids.NewUUID()
		repositoryRows[i] = []any{pgtype.UUID{Bytes: repositoryIDs[i], Valid: true}, repositoryName(i), "npm"}
	}
	var err error
	if counts.Repositories, err = db.CopyFrom(ctx, pgx.Identifier{"hosted_repositories"}, []string{"id", "name", "format"}, pgx.CopyFromRows(repositoryRows)); err != nil {
		return counts, fmt.Errorf("datagen: copy repositories: %w", err)
	}

	packages := plan(p, rng)
	packageIndex := 0
	counts.Packages, err = db.CopyFrom(ctx, pgx.Identifier{"native_npm_packages"}, []string{"repository_id", "name"},
		pgx.CopyFromFunc(func() ([]any, error) {
			if packageIndex == len(packages) {
				return nil, nil
			}
			row := []any{pgtype.UUID{Bytes: repositoryIDs[packages[packageIndex].repository], Valid: true}, packageName(packageIndex)}
			packageIndex++
			return row, nil
		}))
	if err != nil {
		return counts, fmt.Errorf("datagen: copy packages: %w", err)
	}

	pkg, version := 0, 0
	counts.Versions, err = db.CopyFrom(ctx, pgx.Identifier{"native_npm_versions"},
		[]string{"repository_id", "package_name", "version", "digest", "tarball_name", "object_key", "size", "manifest", "publisher", "created_at", "cached_at"},
		pgx.CopyFromFunc(func() ([]any, error) {
			for pkg < len(packages) && version == packages[pkg].versions {
				pkg++
				version = 0
			}
			if pkg == len(packages) {
				return nil, nil
			}
			name := packageName(pkg)
			ver := fmt.Sprintf("1.%d.0", version)
			digest := digestOf(name + "@" + ver)
			created := now.Add(-time.Duration(pkg*averageVersionsPerPackage+version) * time.Second)
			row := []any{
				pgtype.UUID{Bytes: repositoryIDs[packages[pkg].repository], Valid: true}, name, ver,
				"sha256:" + digest, name + "-" + ver + ".tgz", "perf/" + digest[:2] + "/" + digest,
				int64(1024 + rng.Intn(1<<20)),
				fmt.Sprintf(`{"name":%q,"version":%q,"description":"synthetic package for performance measurement"}`, name, ver),
				"perf-publisher", created, created,
			}
			version++
			return row, nil
		}))
	if err != nil {
		return counts, fmt.Errorf("datagen: copy versions: %w", err)
	}

	repoPop := NewPopularity(rng, p.Repositories)
	pkgPop := NewPopularity(rng, len(packages))
	actorPop := NewPopularity(rng, 200)
	emitted := 0
	counts.AuditRows, err = db.CopyFrom(ctx, pgx.Identifier{"resolver_audit_log"},
		[]string{"group_name", "repository", "outcome", "actor", "occurred_at", "format", "resource", "operation", "http_status", "bytes", "repository_id"},
		pgx.CopyFromFunc(func() ([]any, error) {
			if emitted == p.AuditRows {
				return nil, nil
			}
			emitted++
			repo := repoPop.Next() % p.Repositories
			outcome, status := "allowed", 200
			if rng.Intn(20) == 0 {
				outcome, status = "denied", 403
			}
			return []any{
				"perf", repositoryName(repo), outcome, fmt.Sprintf("perf-actor-%03d", actorPop.Next()),
				now.Add(-time.Duration(rng.Int63n(int64(p.AuditWindow)))), "npm", packageName(pkgPop.Next()),
				"read", int32(status), int64(rng.Intn(1 << 20)), pgtype.UUID{Bytes: repositoryIDs[repo], Valid: true},
			}, nil
		}))
	if err != nil {
		return counts, fmt.Errorf("datagen: copy audit rows: %w", err)
	}
	return counts, nil
}
