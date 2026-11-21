package datagen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type fakeCopier struct {
	rows map[string][][]any
	fail string
}

func (f *fakeCopier) CopyFrom(_ context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error) {
	name := table[0]
	if name == f.fail {
		return 0, errors.New("boom")
	}
	if f.rows == nil {
		f.rows = map[string][][]any{}
	}
	var copied int64
	for src.Next() {
		values, err := src.Values()
		if err != nil {
			return 0, err
		}
		if len(values) != len(columns) {
			return 0, errors.New("column count mismatch for " + name)
		}
		f.rows[name] = append(f.rows[name], values)
		copied++
	}
	return copied, src.Err()
}

func TestParseProfile(t *testing.T) {
	for _, name := range []string{"tiny", "freetier", "target"} {
		p, err := ParseProfile(name)
		if err != nil || p.Name != name {
			t.Fatalf("ParseProfile(%q) = %v, %v", name, p, err)
		}
	}
	if _, err := ParseProfile("huge"); err == nil {
		t.Fatal("unknown profile accepted")
	}
	if Profiles["tiny"].Versions != 100_000 || Profiles["tiny"].AuditRows != 1_000_000 ||
		Profiles["freetier"].Versions != 1_000_000 || Profiles["target"].AuditRows != 100_000_000 ||
		Profiles["target"].WebhookDeliveries != 20_000_000 {
		t.Fatal("profile sizes drifted from ADR 0023 section 11")
	}
}

func TestScaled(t *testing.T) {
	p := Profiles["tiny"]
	if got := p.Scaled(1); got != p {
		t.Fatal("Scaled(1) changed the profile")
	}
	got := p.Scaled(100)
	if got.Versions != 1000 || got.AuditRows != 10_000 || got.Repositories != 1 || got.WebhookDeliveries != 200 || got.WebhookSubscriptions != 1 {
		t.Fatalf("Scaled(100) = %+v", got)
	}
	if none := (Profile{Versions: 10, AuditRows: 10, Repositories: 2}).Scaled(10); none.WebhookDeliveries != 0 || none.WebhookSubscriptions != 0 {
		t.Fatalf("a profile without webhooks gained some: %+v", none)
	}
}

func TestPopularityIsSkewed(t *testing.T) {
	pop := NewPopularity(rand.New(rand.NewSource(7)), 1000)
	hot, total := 0, 20000
	for range total {
		rank := pop.Next()
		if rank < 0 || rank >= 1000 {
			t.Fatalf("rank %d out of range", rank)
		}
		if rank < 10 {
			hot++
		}
	}
	if hot < total/4 {
		t.Fatalf("top 1%% of ranks drew only %d of %d samples", hot, total)
	}
	if NewPopularity(rand.New(rand.NewSource(1)), 0).Next() > 1 {
		t.Fatal("degenerate sampler out of range")
	}
}

func TestSeedWritesConsistentRows(t *testing.T) {
	p := Profile{Name: "unit", Versions: 500, AuditRows: 300, Repositories: 5, AuditWindow: time.Hour}
	copier := &fakeCopier{}
	counts, err := Seed(context.Background(), copier, p, 3, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if counts.Repositories != 5 || counts.Versions != 500 || counts.AuditRows != 300 || counts.Packages < 500/(2*averageVersionsPerPackage) {
		t.Fatalf("counts = %+v", counts)
	}
	seen := map[string]bool{}
	for _, row := range copier.rows["native_npm_versions"] {
		id := row[1].(string) + "@" + row[2].(string)
		if seen[id] {
			t.Fatalf("duplicate version %s", id)
		}
		seen[id] = true
		if size := row[6].(int64); size <= 0 {
			t.Fatalf("size %d violates the cached-object check", size)
		}
	}
	again := &fakeCopier{}
	if second, err := Seed(context.Background(), again, p, 3, time.Unix(1_700_000_000, 0)); err != nil || second != counts {
		t.Fatalf("seed is not deterministic: %+v vs %+v (%v)", second, counts, err)
	}
	for i, row := range copier.rows["hosted_repositories"] {
		first, second := row[0].(pgtype.UUID), again.rows["hosted_repositories"][i][0].(pgtype.UUID)
		if first != second || !first.Valid || first.Bytes[6]>>4 != 7 || first.Bytes[8]>>6 != 2 {
			t.Fatalf("repository id %d is not a deterministic UUIDv7: %x vs %x", i, first.Bytes, second.Bytes)
		}
	}
}

// The webhook rows satisfy the table constraints (state against lease, delivery time and next attempt), hold
// distinct subscriptions per event and are deterministic.
func TestSeedWritesConsistentWebhookRows(t *testing.T) {
	p := Profile{Name: "unit", Versions: 10, AuditRows: 0, Repositories: 3, AuditWindow: time.Hour, WebhookSubscriptions: 6, WebhookDeliveries: 2000}
	copier := &fakeCopier{}
	counts, err := Seed(context.Background(), copier, p, 5, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if counts.WebhookSubscriptions != 6 || counts.WebhookEvents != 500 || counts.WebhookDeliveries != 2000 {
		t.Fatalf("counts = %+v", counts)
	}
	states := map[string]int{}
	pairs := map[[2]pgtype.UUID]bool{}
	for _, row := range copier.rows["webhook_deliveries"] {
		state := row[3].(string)
		states[state]++
		next, leaseOwner, leaseToken, leaseExpires, delivered := row[5], row[6], row[7], row[8], row[11]
		if (state == "delivering") != (leaseOwner != nil && leaseToken != nil && leaseExpires != nil) {
			t.Fatalf("%s row breaks the lease constraint", state)
		}
		if (state == "succeeded") != (delivered != nil) {
			t.Fatalf("%s row breaks the delivered_at constraint", state)
		}
		if (state == "pending" || state == "retrying") != (next != nil) {
			t.Fatalf("%s row breaks the next_attempt_at constraint", state)
		}
		if attempts := row[4].(int32); attempts < 0 || attempts > 8 {
			t.Fatalf("attempts %d out of range", attempts)
		}
		pair := [2]pgtype.UUID{row[1].(pgtype.UUID), row[2].(pgtype.UUID)}
		if pairs[pair] {
			t.Fatal("an event was delivered twice to one subscription")
		}
		pairs[pair] = true
	}
	for _, state := range []string{"succeeded", "pending", "retrying", "dead", "delivering"} {
		if states[state] == 0 {
			t.Fatalf("no %s deliveries in %v", state, states)
		}
	}
	if states["succeeded"] < 1700 {
		t.Fatalf("succeeded deliveries should dominate: %v", states)
	}
	again := &fakeCopier{}
	if second, err := Seed(context.Background(), again, p, 5, time.Unix(1_700_000_000, 0)); err != nil || second != counts {
		t.Fatalf("webhook seed is not deterministic: %+v vs %+v (%v)", second, counts, err)
	}
}

func TestSeedErrors(t *testing.T) {
	good := Profile{Name: "unit", Versions: 10, AuditRows: 10, Repositories: 2, AuditWindow: time.Hour, WebhookSubscriptions: 2, WebhookDeliveries: 10}
	for _, table := range []string{"hosted_repositories", "native_npm_packages", "native_npm_versions", "resolver_audit_log", "webhook_subscriptions", "domain_events", "webhook_deliveries"} {
		if _, err := Seed(context.Background(), &fakeCopier{fail: table}, good, 1, time.Now()); err == nil {
			t.Errorf("failure of %s not reported", table)
		}
	}
	if _, err := Seed(context.Background(), &fakeCopier{}, Profile{Name: "bad"}, 1, time.Now()); err == nil {
		t.Error("empty profile accepted")
	}
	noSubscribers := good
	noSubscribers.WebhookSubscriptions = 0
	if _, err := Seed(context.Background(), &fakeCopier{}, noSubscribers, 1, time.Now()); err == nil {
		t.Error("webhook deliveries without subscriptions accepted")
	}
}

var allFormatTables = []string{
	"native_oci_manifests", "native_maven_artifacts", "native_pypi_files", "native_go_versions", "native_go_assets", "native_raw_objects",
	"native_raw_assets", "native_cargo_names", "native_cargo_identity_reservations", "native_cargo_publications", "native_nuget_publication_sessions",
	"native_nuget_publications", "native_conan_recipe_revisions", "native_helm_publication_sessions", "native_helm_publications",
	"native_rubygems_sessions", "native_apt_assets",
}

// Every format of FormatMix has rows, each table keeps its primary key unique and the rows satisfy the check
// constraints and foreign keys of the schema, so the seeded database loads and the explain suite sees every table.
func TestSeedWritesEveryFormat(t *testing.T) {
	p := Profile{Name: "unit", AllFormats: true, Versions: 2000, AuditRows: 10, Repositories: 20, AuditWindow: time.Hour}
	copier := &fakeCopier{}
	counts, err := Seed(context.Background(), copier, p, 9, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if counts.FormatRows == 0 || counts.Versions < 500 || counts.Versions > 700 {
		t.Fatalf("npm should keep about 30 percent of the versions: %+v", counts)
	}
	for _, table := range allFormatTables {
		if len(copier.rows[table]) == 0 {
			t.Errorf("no rows for %s", table)
		}
	}
	formats := map[string]bool{}
	for _, row := range copier.rows["hosted_repositories"] {
		formats[row[2].(string)] = true
	}
	for _, share := range FormatMix {
		if !formats[share.Format] {
			t.Errorf("no %s repository", share.Format)
		}
	}
	digest := regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	unique := func(table string, columns ...int) {
		seen := map[string]bool{}
		for _, row := range copier.rows[table] {
			var key string
			for _, c := range columns {
				key += fmt.Sprintf("%v|", row[c])
			}
			if seen[key] {
				t.Fatalf("%s repeats key %s", table, key)
			}
			seen[key] = true
		}
	}
	unique("native_oci_manifests", 0, 1, 2)
	unique("native_oci_manifests", 3)
	unique("native_maven_artifacts", 1, 2)
	unique("native_pypi_files", 0, 3)
	unique("native_go_versions", 0, 1, 2)
	unique("native_go_assets", 0, 1, 2, 3)
	unique("native_raw_objects", 0)
	unique("native_raw_objects", 1)
	unique("native_raw_assets", 0, 1)
	unique("native_cargo_names", 0, 1)
	unique("native_cargo_identity_reservations", 0, 1, 2)
	unique("native_cargo_publications", 0, 1, 2)
	unique("native_nuget_publication_sessions", 0)
	unique("native_nuget_publication_sessions", 8)
	unique("native_nuget_publications", 0, 1, 2)
	unique("native_conan_recipe_revisions", 0, 1, 2)
	unique("native_helm_publication_sessions", 0)
	unique("native_helm_publications", 0, 1, 2)
	unique("native_rubygems_sessions", 0)
	unique("native_rubygems_sessions", 1, 6)
	unique("native_apt_assets", 0, 1)

	for _, row := range copier.rows["native_pypi_files"] {
		if !digest.MatchString(row[5].(string)) || row[7].(int64) <= 0 || row[6].(string) == "" || !regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,253}[a-z0-9])?$`).MatchString(row[1].(string)) {
			t.Fatalf("pypi row breaks a constraint: %v", row)
		}
	}
	for _, table := range []string{"native_oci_manifests", "native_go_assets", "native_conan_recipe_revisions"} {
		column := map[string]int{"native_oci_manifests": 2, "native_go_assets": 4, "native_conan_recipe_revisions": 3}[table]
		for _, row := range copier.rows[table] {
			if !digest.MatchString(row[column].(string)) {
				t.Fatalf("%s digest %v", table, row[column])
			}
		}
	}
	for _, row := range copier.rows["native_go_assets"] {
		if row[6].(int64) <= 0 || row[5].(string) == "" {
			t.Fatalf("go asset breaks a constraint: %v", row)
		}
	}
	// Foreign keys: raw assets to raw objects, Go assets to versions, Cargo reservations and publications to
	// names and reservations, NuGet and Helm publications to their sessions.
	objects := map[string]bool{}
	for _, row := range copier.rows["native_raw_objects"] {
		objects[row[0].(string)] = true
	}
	for _, row := range copier.rows["native_raw_assets"] {
		if !objects[row[2].(string)] {
			t.Fatalf("raw asset %v has no object", row[1])
		}
	}
	goVersions := map[string]bool{}
	for _, row := range copier.rows["native_go_versions"] {
		goVersions[fmt.Sprintf("%v|%v|%v", row[0], row[1], row[2])] = true
	}
	for _, row := range copier.rows["native_go_assets"] {
		if !goVersions[fmt.Sprintf("%v|%v|%v", row[0], row[1], row[2])] {
			t.Fatalf("go asset %v has no version", row[1])
		}
	}
	names := map[string]bool{}
	crate := regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	for _, row := range copier.rows["native_cargo_names"] {
		if !crate.MatchString(row[2].(string)) || row[1] != strings.ReplaceAll(strings.ToLower(row[2].(string)), "_", "-") || row[3] != strings.ToLower(row[2].(string)) {
			t.Fatalf("cargo name breaks a constraint: %v", row)
		}
		names[fmt.Sprintf("%v|%v", row[0], row[1])] = true
	}
	reservations := map[string]bool{}
	for _, row := range copier.rows["native_cargo_identity_reservations"] {
		if !names[fmt.Sprintf("%v|%v", row[0], row[1])] || row[2] != strings.SplitN(row[3].(string), "+", 2)[0] {
			t.Fatalf("cargo reservation breaks a constraint: %v", row)
		}
		reservations[fmt.Sprintf("%v|%v|%v", row[0], row[1], row[2])] = true
	}
	objectKey := regexp.MustCompile(`^native/cargo/sha256/[0-9a-f]{64}$`)
	for _, row := range copier.rows["native_cargo_publications"] {
		if !reservations[fmt.Sprintf("%v|%v|%v", row[0], row[1], row[2])] || !objectKey.MatchString(row[3].(string)) || row[4].(int64) <= 0 ||
			!json.Valid([]byte(row[5].(string))) || row[6] == "" {
			t.Fatalf("cargo publication breaks a constraint: %v", row)
		}
	}
	packageID := regexp.MustCompile(`^[a-z0-9_]+([.-][a-z0-9_]+)*$`)
	sessions := map[pgtype.UUID]bool{}
	for _, row := range copier.rows["native_nuget_publication_sessions"] {
		id, version := row[2].(string), row[3].(string)
		if len(id) > 100 || !packageID.MatchString(id) || version != strings.ToLower(version) || row[5].(int64) <= 0 || len(row[10].([]byte)) == 0 ||
			!json.Valid([]byte(row[9].(string))) || row[7] != "committed" || row[13] == nil || !digest.MatchString(row[4].(string)) {
			t.Fatalf("nuget session breaks a constraint: %v", row)
		}
		sessions[row[0].(pgtype.UUID)] = true
	}
	for _, row := range copier.rows["native_nuget_publications"] {
		if !sessions[row[3].(pgtype.UUID)] || len(row[8].([]byte)) == 0 || row[6].(int64) <= 0 {
			t.Fatalf("nuget publication breaks a constraint: %v", row)
		}
	}
	for _, pair := range [][2]string{{"native_nuget_publication_sessions", "native_nuget_publications"}, {"native_helm_publication_sessions", "native_helm_publications"}} {
		sizes := map[pgtype.UUID]int64{}
		for _, row := range copier.rows[pair[0]] {
			sizes[row[0].(pgtype.UUID)] = row[5].(int64)
		}
		for _, row := range copier.rows[pair[1]] {
			if sizes[row[3].(pgtype.UUID)] != row[6].(int64) {
				t.Fatalf("%s size differs from its session", pair[1])
			}
		}
	}
	chart := regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	sessions = map[pgtype.UUID]bool{}
	for _, row := range copier.rows["native_helm_publication_sessions"] {
		if !chart.MatchString(row[2].(string)) || len(row[2].(string)) > 63 || row[5].(int64) <= 0 || len(row[10].([]byte)) == 0 || !digest.MatchString(row[4].(string)) {
			t.Fatalf("helm session breaks a constraint: %v", row)
		}
		sessions[row[0].(pgtype.UUID)] = true
	}
	for _, row := range copier.rows["native_helm_publications"] {
		if !sessions[row[3].(pgtype.UUID)] || len(row[8].([]byte)) == 0 || row[6].(int64) <= 0 {
			t.Fatalf("helm publication breaks a constraint: %v", row)
		}
	}
	for _, row := range copier.rows["native_rubygems_sessions"] {
		if row[8].(int64) < 1 || len(row[13].([]byte)) == 0 || row[10] != "committed" || row[16] == nil || !digest.MatchString(row[7].(string)) {
			t.Fatalf("rubygems session breaks a constraint: %v", row)
		}
	}
	again := &fakeCopier{}
	if second, err := Seed(context.Background(), again, p, 9, time.Unix(1_700_000_000, 0)); err != nil || second != counts {
		t.Fatalf("seed is not deterministic: %+v vs %+v (%v)", second, counts, err)
	}
	for _, table := range allFormatTables {
		if len(again.rows[table]) != len(copier.rows[table]) {
			t.Fatalf("%s row count differs between runs", table)
		}
	}
}

func TestSeedFormatErrors(t *testing.T) {
	p := Profile{Name: "unit", AllFormats: true, Versions: 200, Repositories: 10, AuditWindow: time.Hour}
	for _, table := range allFormatTables {
		if _, err := Seed(context.Background(), &fakeCopier{fail: table}, p, 1, time.Now()); err == nil {
			t.Errorf("failure of %s not reported", table)
		}
	}
	p.Versions = minAllFormatsVersions - 1
	if _, err := Seed(context.Background(), &fakeCopier{}, p, 1, time.Now()); err == nil {
		t.Error("a budget too small for the mix was accepted")
	}
	if formatTables("unknown") != nil {
		t.Error("unknown format has tables")
	}
	total := 0
	for _, share := range FormatMix {
		total += share.Percent
	}
	if total >= 100 {
		t.Errorf("FormatMix leaves npm nothing: %d percent", total)
	}
	for _, name := range []string{"tiny", "freetier", "target"} {
		if !Profiles[name].AllFormats {
			t.Errorf("profile %s does not seed every format", name)
		}
	}
}

func TestSeedWritesACatalogueRowPerCoordinateOfTheProjectedFormats(t *testing.T) {
	p := Profile{Name: "unit", AllFormats: true, Versions: 2000, AuditRows: 10, Repositories: 20, AuditWindow: time.Hour}
	copier := &fakeCopier{}
	counts, err := Seed(context.Background(), copier, p, 9, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	rows := copier.rows["artifact_catalog"]
	if int64(len(rows)) != counts.CatalogRows || len(rows) == 0 {
		t.Fatalf("catalogue rows %d, counts %+v", len(rows), counts)
	}
	perFormat := map[string]int{}
	seen := map[string]bool{}
	for _, row := range rows {
		format := row[1].(string)
		perFormat[format]++
		key := fmt.Sprintf("%v|%s|%s", row[0], format, row[2])
		if seen[key] {
			t.Fatalf("the catalogue repeats %s", key)
		}
		seen[key] = true
		if row[7].(int32) < 1 {
			t.Fatalf("%s has version_count %v", key, row[7])
		}
	}
	for _, format := range []string{"npm", "oci", "maven", "pypi", "go", "raw", "conan", "apt"} {
		if perFormat[format] == 0 {
			t.Errorf("no catalogue rows for %s", format)
		}
	}
	for _, format := range []string{"cargo", "nuget", "helm", "rubygems"} {
		if perFormat[format] != 0 {
			t.Errorf("%s is not part of the search projection but has %d catalogue rows", format, perFormat[format])
		}
	}
	// A coordinate that groups versions counts them; npm keeps one row per package.
	if got, want := perFormat["npm"], len(copier.rows["native_npm_packages"]); got != want {
		t.Errorf("npm has %d catalogue rows for %d packages", got, want)
	}
	versions := 0
	for _, row := range rows {
		if row[1] == "npm" {
			versions += int(row[7].(int32))
		}
	}
	if versions != len(copier.rows["native_npm_versions"]) {
		t.Errorf("npm version counts add up to %d, want %d", versions, len(copier.rows["native_npm_versions"]))
	}
}

func TestSeedWithoutFormatsStillSeedsTheNPMCatalogue(t *testing.T) {
	copier := &fakeCopier{}
	if _, err := Seed(context.Background(), copier, Profile{Name: "npm", Versions: 50, Repositories: 3}, 3, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if len(copier.rows["artifact_catalog"]) != len(copier.rows["native_npm_packages"]) {
		t.Fatalf("catalogue rows %d, packages %d", len(copier.rows["artifact_catalog"]), len(copier.rows["native_npm_packages"]))
	}
}
