package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/promotionrules"
	"github.com/mlops-ca/notarx/internal/repository"
)

// fakeRawVersions serves sealed versions in a fixed order and honours the keyset cursor of ListRawVersions.
type fakeRawVersions struct {
	repository.NativeRawVersionStore
	versions []repository.RawVersion
	err      error
}

func (f fakeRawVersions) ListRawPackages(_ context.Context, _, after string, limit int) ([]repository.RawPackage, error) {
	if f.err != nil {
		return nil, f.err
	}
	seen := map[string]bool{}
	var names []string
	for _, version := range f.versions {
		if !seen[version.Name] && version.Name > after {
			seen[version.Name] = true
			names = append(names, version.Name)
		}
	}
	packages := []repository.RawPackage{}
	for _, name := range names {
		if len(packages) < limit {
			packages = append(packages, repository.RawPackage{Name: name})
		}
	}
	return packages, nil
}

func (f fakeRawVersions) ListRawVersions(_ context.Context, query repository.RawVersionQuery) ([]repository.RawVersion, error) {
	out := []repository.RawVersion{}
	for _, version := range f.versions {
		if version.Name != query.Name || version.State != query.State || (query.After != nil && version.ID <= query.After.ID) {
			continue
		}
		if len(out) < query.Limit {
			out = append(out, version)
		}
	}
	return out, nil
}

type fakeTags map[string][]string

func (f fakeTags) TagName(coordinate string) (string, bool) { return coordinate, true }

func (f fakeTags) TagsOfName(context.Context, string, string) (formatmodule.TagLookup, error) {
	return func(coordinate, _ string) []string { return f[coordinate] }, nil
}

func versionedDryRun(t *testing.T, match promotionrules.Match, versions []repository.RawVersion) (*dryRun, *repository.MemoryStore) {
	t.Helper()
	compiled, err := match.Compile()
	if err != nil {
		t.Fatal(err)
	}
	store := repository.NewMemoryStore()
	source := repository.HostedRepository{ID: uuid.NewString(), Name: "versioned", Format: repository.FormatRaw, RawLayout: repository.RawLayoutVersioned}
	return &dryRun{
		source: source, match: compiled, now: time.Now(),
		ports: promotionDryRunPorts{
			rawVersions: fakeRawVersions{versions: versions}, quarantine: store,
			distributionDigests: func(_ context.Context, _ repository.HostedRepository, _, digest string) ([]string, error) {
				return []string{digest}, nil
			},
		},
	}, store
}

func sealed(id int, name, version string, properties map[string]string) repository.RawVersion {
	return repository.RawVersion{ID: fmt.Sprintf("%06d", id), Name: name, Version: version, State: repository.RawVersionSealed, ManifestDigest: fmt.Sprintf("sha256:%064d", id),
		SealedAt: time.Now().Add(-time.Hour), Properties: properties}
}

// A versioned generic source lists its sealed versions; the properties are the ones frozen at seal, the version range
// matches semantic versions only, and the glob applies to the name@version coordinate.
func TestDryRunVersionedSourceMatchesPropertiesAndSemver(t *testing.T) {
	versions := []repository.RawVersion{
		sealed(1, "app", "1.0.0", map[string]string{"channel": "stable"}),
		sealed(2, "app", "1.1.0", map[string]string{"channel": "beta"}),
		sealed(3, "app", "nightly", map[string]string{"channel": "stable"}),
		sealed(4, "app", "2.0.0", nil),
		sealed(5, "lib", "1.5.0", map[string]string{"channel": "stable"}),
	}
	run, _ := versionedDryRun(t, promotionrules.Match{
		Coordinate: promotionrules.Coordinate{Glob: "app@*"}, Version: &promotionrules.VersionRange{Range: ">=1.0.0 <2.0.0"}, Properties: map[string]string{"channel": "stable"},
	}, versions)
	result, err := run.execute(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if result.Examined != 5 || result.Matched != 1 || len(result.Items) != 1 || result.Items[0].Coordinate != "app@1.0.0" || result.Items[0].Digest != versions[0].ManifestDigest ||
		result.Items[0].Version == nil || *result.Items[0].Version != "1.0.0" || result.Items[0].Outcome != "eligible" || result.Truncated {
		t.Fatalf("result = %+v", result)
	}

	// A range alone never matches the version "nightly"; without a range it does.
	run, _ = versionedDryRun(t, promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "app@*"}, Properties: map[string]string{"channel": "stable"}}, versions)
	result, _ = run.execute(context.Background(), 100)
	if result.Matched != 2 {
		t.Fatalf("without a range = %+v", result)
	}
}

func TestDryRunVersionedSourcePagesAndStopsAtTheExaminationCap(t *testing.T) {
	var versions []repository.RawVersion
	for i := 1; i <= promotionDryRunPageSize+5; i++ {
		versions = append(versions, sealed(i, "big", fmt.Sprintf("1.0.%d", i), nil))
	}
	versions = append(versions, sealed(900, "small", "1.0.0", nil))
	run, _ := versionedDryRun(t, promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "**"}}, versions)
	result, err := run.execute(context.Background(), 500)
	if err != nil || result.Examined != int32(promotionDryRunPageSize+6) || result.Matched != int32(promotionDryRunPageSize+6) || result.Truncated || len(result.Items) != promotionDryRunPageSize+6 {
		t.Fatalf("paged = %d/%d/%v/%d, %v", result.Examined, result.Matched, result.Truncated, len(result.Items), err)
	}

	// More artifacts than the cap: the walk stops and says the answer is incomplete.
	many := make([]repository.RawVersion, 0, promotionDryRunMaxExamined+10)
	for pkg := 0; len(many) < promotionDryRunMaxExamined+10; pkg++ {
		for i := 1; i <= 100 && len(many) < promotionDryRunMaxExamined+10; i++ {
			many = append(many, sealed(len(many)+1, fmt.Sprintf("pkg%03d", pkg), fmt.Sprintf("1.0.%d", i), nil))
		}
	}
	run, _ = versionedDryRun(t, promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "nothing"}}, many)
	result, err = run.execute(context.Background(), 100)
	if err != nil || result.Examined != int32(promotionDryRunMaxExamined) || !result.Truncated || result.Matched != 0 {
		t.Fatalf("capped = %d/%d/%v, %v", result.Examined, result.Matched, result.Truncated, err)
	}
}

func TestDryRunReportsPortFailuresAndMissingPorts(t *testing.T) {
	run, _ := versionedDryRun(t, promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "**"}}, nil)
	run.ports.rawVersions = fakeRawVersions{err: errors.New("listing failed")}
	if _, err := run.execute(context.Background(), 10); err == nil {
		t.Fatal("a listing failure must surface")
	}
	run.ports.rawVersions = nil
	if _, err := run.execute(context.Background(), 10); !errors.Is(err, errDryRunUnavailable) {
		t.Fatalf("no version store = %v", err)
	}
	run, _ = versionedDryRun(t, promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "**"}}, nil)
	run.source.RawLayout = ""
	run.ports.search = nil
	if _, err := run.execute(context.Background(), 10); !errors.Is(err, errDryRunUnavailable) {
		t.Fatalf("no search projection = %v", err)
	}

	versions := []repository.RawVersion{sealed(1, "app", "1.0.0", nil)}
	run, _ = versionedDryRun(t, promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "**"}}, versions)
	run.ports.distributionDigests = func(context.Context, repository.HostedRepository, string, string) ([]string, error) {
		return nil, repository.ErrNotFound
	}
	if result, err := run.execute(context.Background(), 10); err != nil || result.Matched != 0 || len(result.Items) != 0 {
		t.Fatalf("an artifact that vanished is not reported: %+v, %v", result, err)
	}
	run.ports.distributionDigests = func(context.Context, repository.HostedRepository, string, string) ([]string, error) {
		return nil, errors.New("lookup failed")
	}
	if _, err := run.execute(context.Background(), 10); err == nil {
		t.Fatal("a digest lookup failure must surface")
	}
}

// refs come from format.Tagged and are fetched only for a rule that has refs.
func TestDryRunMatchesRefsThroughTheTaggedCapability(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	source, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "tagged", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	var coordinates []string
	for _, path := range []string{"app/one", "app/two"} {
		digest := fmt.Sprintf("sha256:%064d", len(coordinates)+1)
		if _, err = store.PutRawAsset(ctx, repository.RawAsset{RepositoryID: source.ID, Path: path, Digest: digest, ObjectKey: "native/raw/" + path, Size: 1}); err != nil {
			t.Fatal(err)
		}
		coordinates = append(coordinates, path)
	}
	compiled, err := (promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "**"}, Refs: []string{"latest", "v*"}}).Compile()
	if err != nil {
		t.Fatal(err)
	}
	run := &dryRun{source: source, match: compiled, now: time.Now(), tagged: fakeTags{"app/one": {"beta", "v1"}, "app/two": {"beta"}},
		ports: promotionDryRunPorts{search: store, quarantine: store,
			distributionDigests: func(_ context.Context, _ repository.HostedRepository, _, digest string) ([]string, error) {
				return []string{digest}, nil
			}}}
	result, err := run.execute(ctx, 10)
	if err != nil || result.Examined != 2 || result.Matched != 1 || result.Items[0].Coordinate != "app/one" {
		t.Fatalf("result = %+v, %v", result, err)
	}
}

func TestDryRunConditionVerdicts(t *testing.T) {
	run := &dryRun{now: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC), minAge: 72 * time.Hour}
	for name, tc := range map[string]struct {
		publishedAt time.Time
		verdict     string
		reason      string
	}{
		"old enough":   {run.now.Add(-73 * time.Hour), promotionrules.VerdictPass, ""},
		"exactly":      {run.now.Add(-72 * time.Hour), promotionrules.VerdictPass, ""},
		"too young":    {run.now.Add(-71 * time.Hour), promotionrules.VerdictFail, "younger_than_min_age"},
		"unknown time": {time.Time{}, promotionrules.VerdictNotEvaluated, "publication_time_unknown"},
	} {
		if got := run.minAgeVerdict(tc.publishedAt); got.Verdict != tc.verdict || got.Reason != tc.reason {
			t.Errorf("%s = %+v", name, got)
		}
	}
	scan := &promotionrules.Scan{Completed: true, MaxSeverity: "medium"}
	summary := func(status string, high int) *repository.ArtifactIntelligence {
		return &repository.ArtifactIntelligence{Vulnerability: &repository.ArtifactVulnerabilitySummary{Status: status, High: high, Medium: 2}}
	}
	for name, tc := range map[string]struct {
		intelligence *repository.ArtifactIntelligence
		verdict      string
		reason       string
	}{
		"no intelligence": {nil, promotionrules.VerdictFail, "scan_not_completed"},
		"no scan":         {&repository.ArtifactIntelligence{}, promotionrules.VerdictFail, "scan_not_completed"},
		"not scanned":     {summary("not_scanned", 0), promotionrules.VerdictFail, "scan_not_completed"},
		"scan error":      {summary("error", 0), promotionrules.VerdictFail, "scan_not_completed"},
		"stale database":  {summary("stale_db", 0), promotionrules.VerdictFail, "scan_database_stale"},
		"clean":           {summary("clean", 0), promotionrules.VerdictPass, ""},
		"within severity": {summary("vulnerable", 0), promotionrules.VerdictPass, ""},
		"above severity":  {summary("vulnerable", 1), promotionrules.VerdictFail, "severity_above_medium"},
	} {
		if got := scanVerdict(scan, tc.intelligence); got.Verdict != tc.verdict || got.Reason != tc.reason {
			t.Errorf("%s = %+v", name, got)
		}
	}
	if got := scanVerdict(&promotionrules.Scan{Completed: true}, summary("vulnerable", 5)); got.Verdict != promotionrules.VerdictPass {
		t.Errorf("without maxSeverity any completed scan passes: %+v", got)
	}
}
