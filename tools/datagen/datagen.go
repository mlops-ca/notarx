// Package datagen seeds a PostgreSQL database with synthetic catalogue and audit
// rows at the sizes of the ADR 0023 section 11 profiles, so plans, latencies and
// capacity can be measured instead of estimated.
//
// Popularity is Zipfian: a handful of repositories, packages and actors receive most
// of the traffic, so hot artifacts exist. Generation is deterministic for a given
// profile, seed and start time (repository identifiers are UUIDv7 values built from the
// seeded generator and the start time). The seed format is npm because its tables carry the widest
// per-version rows; the other hosted formats (formats.go) receive the shares of FormatMix of the version budget when
// the profile sets AllFormats. Webhook deliveries are generated
// with the state mix of a long-running instance (mostly succeeded, a tail of pending, retrying, dead and
// leased rows) so the claim, listing and retention queries meet realistic partial-index populations.
package datagen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Profile sizes one seeding run.
type Profile struct {
	Name string
	// Versions is the version budget: npm receives what FormatMix leaves of it when AllFormats is set, all of it otherwise.
	Versions int
	// AllFormats also seeds the other hosted formats, split by FormatMix; it needs at least minAllFormatsVersions versions.
	AllFormats bool
	// AuditRows is the number of resolver_audit_log rows.
	AuditRows int
	// Repositories is the number of hosted npm repositories; every other format gets a tenth of it, at least one.
	Repositories int
	// AuditWindow is how far back audit rows and webhook deliveries are spread.
	AuditWindow time.Duration
	// WebhookSubscriptions is the number of webhook subscriptions; at least 1 when WebhookDeliveries is positive.
	WebhookSubscriptions int
	// WebhookDeliveries is the number of webhook_deliveries rows; zero skips the webhook tables.
	WebhookDeliveries int
}

// averageVersionsPerPackage is the mean number of versions a package gets; the
// spread between 1 and twice this value is uniform.
const averageVersionsPerPackage = 8

// zipfSkew is the Zipf exponent: above 1, so a few ranks dominate.
const zipfSkew = 1.2

// Profiles are the named sizes of ADR 0023 section 11.
var Profiles = map[string]Profile{
	"tiny":     {Name: "tiny", AllFormats: true, Versions: 100_000, AuditRows: 1_000_000, Repositories: 20, AuditWindow: 30 * 24 * time.Hour, WebhookSubscriptions: 5, WebhookDeliveries: 20_000},
	"freetier": {Name: "freetier", AllFormats: true, Versions: 1_000_000, AuditRows: 20_000_000, Repositories: 50, AuditWindow: 7 * 24 * time.Hour, WebhookSubscriptions: 20, WebhookDeliveries: 1_000_000},
	"target":   {Name: "target", AllFormats: true, Versions: 10_000_000, AuditRows: 100_000_000, Repositories: 200, AuditWindow: 90 * 24 * time.Hour, WebhookSubscriptions: 200, WebhookDeliveries: 20_000_000},
}

// ParseProfile returns the named profile.
func ParseProfile(name string) (Profile, error) {
	profile, ok := Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("datagen: unknown profile %q (want tiny, freetier or target)", name)
	}
	return profile, nil
}

// Scaled returns the profile with its versions, audit rows, repositories, webhook subscriptions and webhook
// deliveries divided by divisor (at least 1 each, except that a profile without webhooks keeps none), for runs that need the shape of a profile at a
// fraction of its cost.
func (p Profile) Scaled(divisor int) Profile {
	if divisor <= 1 {
		return p
	}
	scale := func(n int) int { return max(1, n/divisor) }
	p.Versions = scale(p.Versions)
	p.AuditRows = scale(p.AuditRows)
	p.Repositories = scale(p.Repositories)
	if p.WebhookDeliveries > 0 {
		p.WebhookDeliveries = scale(p.WebhookDeliveries)
		p.WebhookSubscriptions = scale(p.WebhookSubscriptions)
	}
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
	// FormatRows is the number of rows written for the formats of FormatMix, across their tables.
	FormatRows                                             int64
	WebhookSubscriptions, WebhookEvents, WebhookDeliveries int64
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

// newID returns a UUIDv7 whose timestamp is now and whose remaining bits come from
// rng, so a seeded run produces the same identifiers every time.
func newID(rng *rand.Rand, now time.Time) uuid.UUID {
	var id uuid.UUID
	for i := range id {
		id[i] = byte(rng.Intn(256))
	}
	ms := uint64(now.UnixMilli())
	for i := 0; i < 6; i++ {
		id[i] = byte(ms >> (8 * (5 - i)))
	}
	id[6] = id[6]&0x0f | 0x70
	id[8] = id[8]&0x3f | 0x80
	return id
}

func uuidOf(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }

func repositoryName(i int) string { return fmt.Sprintf("perf-npm-%04d", i) }

// formatRepositories is the number of repositories of each format other than npm.
func formatRepositories(p Profile) int { return max(1, p.Repositories/10) }
func packageName(i int) string         { return fmt.Sprintf("perf-pkg-%08d", i) }

func digestOf(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// Seed writes the profile through bulk COPY. The target database must be migrated
// and must not already hold seeded repositories (the repository names are fixed, so
// a second run fails on the unique name rather than doubling the data).
func Seed(ctx context.Context, db Copier, p Profile, seed int64, now time.Time) (Counts, error) {
	if p.Versions < 1 || p.AuditRows < 0 || p.Repositories < 1 || p.WebhookDeliveries < 0 || p.WebhookSubscriptions < 0 ||
		p.WebhookDeliveries > 0 && p.WebhookSubscriptions < 1 {
		return Counts{}, fmt.Errorf("datagen: profile %q has non-positive sizes", p.Name)
	}
	if p.AllFormats && p.Versions < minAllFormatsVersions {
		return Counts{}, fmt.Errorf("datagen: profile %q has %d versions, fewer than the %d that spreading them across formats needs", p.Name, p.Versions, minAllFormatsVersions)
	}
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // synthetic data, determinism wanted
	var counts Counts

	npmVersions := p.Versions
	formatRepoIDs := map[string][]uuid.UUID{}
	if p.AllFormats {
		for _, budget := range otherFormatVersions(p.Versions) {
			npmVersions -= budget
		}
	}
	repositoryIDs := make([]uuid.UUID, p.Repositories)
	repositoryRows := make([][]any, p.Repositories)
	for i := range repositoryIDs {
		repositoryIDs[i] = newID(rng, now)
		repositoryRows[i] = []any{uuidOf(repositoryIDs[i]), repositoryName(i), "npm"}
	}
	if p.AllFormats {
		for _, share := range FormatMix {
			for i := range formatRepositories(p) {
				id := newID(rng, now)
				formatRepoIDs[share.Format] = append(formatRepoIDs[share.Format], id)
				repositoryRows = append(repositoryRows, []any{uuidOf(id), fmt.Sprintf("perf-%s-%04d", share.Format, i), share.Format})
			}
		}
	}
	var err error
	if counts.Repositories, err = db.CopyFrom(ctx, pgx.Identifier{"hosted_repositories"}, []string{"id", "name", "format"}, pgx.CopyFromRows(repositoryRows)); err != nil {
		return counts, fmt.Errorf("datagen: copy repositories: %w", err)
	}

	packages := plan(Profile{Versions: npmVersions, Repositories: p.Repositories}, rng)
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

	if p.AllFormats {
		if counts.FormatRows, err = seedFormats(ctx, db, p, rng, now, formatRepoIDs); err != nil {
			return counts, err
		}
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
	if p.WebhookDeliveries > 0 {
		if err = seedWebhooks(ctx, db, p, rng, now, &counts); err != nil {
			return counts, err
		}
	}
	return counts, nil
}

// webhookFanout is how many subscriptions receive each webhook event.
const webhookFanout = 4

func webhookName(i int) string { return fmt.Sprintf("perf-hook-%04d", i) }

// seedWebhooks writes subscriptions, events and deliveries. Each event goes to webhookFanout distinct
// subscriptions drawn by popularity, so hot subscriptions hold most deliveries. The delivery states follow the
// mix of a long-running instance: 90 percent succeeded, 4 pending, 2 retrying, 3 dead and 1 leased.
func seedWebhooks(ctx context.Context, db Copier, p Profile, rng *rand.Rand, now time.Time, counts *Counts) error {
	subscriptionIDs := make([]uuid.UUID, p.WebhookSubscriptions)
	subscriptionRows := make([][]any, p.WebhookSubscriptions)
	for i := range subscriptionIDs {
		subscriptionIDs[i] = newID(rng, now)
		subscriptionRows[i] = []any{uuidOf(subscriptionIDs[i]), webhookName(i), fmt.Sprintf("https://hooks.example.test/perf/%04d", i),
			"perf-ciphertext", `["artifact.quarantined","artifact.released"]`, true}
	}
	var err error
	if counts.WebhookSubscriptions, err = db.CopyFrom(ctx, pgx.Identifier{"webhook_subscriptions"},
		[]string{"id", "name", "endpoint_url", "secret_ciphertext", "event_types", "enabled"}, pgx.CopyFromRows(subscriptionRows)); err != nil {
		return fmt.Errorf("datagen: copy webhook subscriptions: %w", err)
	}

	fanout := min(webhookFanout, p.WebhookSubscriptions)
	eventCount := (p.WebhookDeliveries + fanout - 1) / fanout
	eventIDs := make([]uuid.UUID, eventCount)
	eventAt := make([]time.Time, eventCount)
	event := 0
	if counts.WebhookEvents, err = db.CopyFrom(ctx, pgx.Identifier{"webhook_events"}, []string{"id", "event_type", "occurred_at", "payload"},
		pgx.CopyFromFunc(func() ([]any, error) {
			if event == eventCount {
				return nil, nil
			}
			eventIDs[event] = newID(rng, now)
			eventAt[event] = now.Add(-time.Duration(rng.Int63n(int64(p.AuditWindow))))
			eventType := "artifact.released"
			if rng.Intn(10) == 0 {
				eventType = "artifact.quarantined"
			}
			row := []any{uuidOf(eventIDs[event]), eventType, eventAt[event], fmt.Sprintf(`{"repository":%q,"sequence":%d}`, repositoryName(event%p.Repositories), event)}
			event++
			return row, nil
		})); err != nil {
		return fmt.Errorf("datagen: copy webhook events: %w", err)
	}

	subscriptionPop := NewPopularity(rng, p.WebhookSubscriptions)
	emitted := 0
	chosen := make([]int, 0, fanout)
	if counts.WebhookDeliveries, err = db.CopyFrom(ctx, pgx.Identifier{"webhook_deliveries"},
		[]string{"id", "event_id", "subscription_id", "state", "attempts", "next_attempt_at", "lease_owner", "lease_token", "lease_expires_at", "last_status", "last_error", "delivered_at", "created_at", "updated_at"},
		pgx.CopyFromFunc(func() ([]any, error) {
			if emitted == p.WebhookDeliveries {
				return nil, nil
			}
			index := emitted / fanout
			if emitted%fanout == 0 {
				chosen = chosen[:0]
			}
			// Distinct subscriptions per event: the popular draw, moved to the next free index on a repeat.
			subscription := subscriptionPop.Next() % p.WebhookSubscriptions
			for slices.Contains(chosen, subscription) {
				subscription = (subscription + 1) % p.WebhookSubscriptions
			}
			chosen = append(chosen, subscription)
			emitted++
			return webhookDeliveryRow(rng, newID(rng, now), eventIDs[index], subscriptionIDs[subscription], eventAt[index], now), nil
		})); err != nil {
		return fmt.Errorf("datagen: copy webhook deliveries: %w", err)
	}
	return nil
}

// webhookDeliveryRow returns one delivery in a state that satisfies the table's constraints.
func webhookDeliveryRow(rng *rand.Rand, id, eventID, subscriptionID uuid.UUID, created, now time.Time) []any {
	var (
		state                     = "succeeded"
		attempts                  = 1
		nextAttempt, leaseExpires any
		leaseOwner, leaseToken    any
		lastStatus                any = int32(200)
		lastError                     = ""
		deliveredAt               any = created.Add(time.Second)
		updated                       = created.Add(time.Second)
	)
	switch roll := rng.Intn(100); {
	case roll < 4:
		state, attempts, lastStatus, deliveredAt, updated = "pending", 0, nil, nil, created
		nextAttempt = now.Add(time.Duration(rng.Intn(60)) * time.Second)
	case roll < 6:
		state, attempts, lastStatus, deliveredAt = "retrying", 1+rng.Intn(3), int32(503), nil
		lastError = "upstream returned 503"
		nextAttempt, updated = now.Add(time.Duration(rng.Intn(600))*time.Second), now.Add(-time.Minute)
	case roll < 9:
		state, attempts, lastStatus, deliveredAt = "dead", 8, int32(500), nil
		lastError = "delivery abandoned after 8 attempts"
	case roll < 10:
		state, attempts, lastStatus, deliveredAt = "delivering", 1, nil, nil
		leaseOwner, leaseToken, leaseExpires = "perf-node", uuidOf(newID(rng, now)), now.Add(30*time.Second)
	}
	return []any{uuidOf(id), uuidOf(eventID), uuidOf(subscriptionID), state, int32(attempts), nextAttempt, leaseOwner, leaseToken, leaseExpires,
		lastStatus, lastError, deliveredAt, created, updated}
}
