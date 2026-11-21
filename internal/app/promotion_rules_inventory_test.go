package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/promotionrules"
	"github.com/mlops-ca/notarx/internal/repository"
)

// fakeInventory serves package listings in name order; unset methods panic through the nil embedded interface, so a
// test only reaches the ports its format uses.
type fakeInventory struct {
	dryRunInventory
	npm    map[string][]repository.NPMVersion
	goMods map[string][]repository.GoModuleVersion
	cargo  map[string][]repository.CargoPublication
	pypi   map[string][]repository.PyPIFile
}

func sortedKeys[V any](m map[string]V, after string) []string {
	var keys []string
	for key := range m {
		if key > after {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func (f fakeInventory) SearchNPMPackages(_ context.Context, _, _ string, limit int, after string, _ ...repository.ArtifactSearchMatch) ([]repository.NPMPackageSummary, error) {
	var out []repository.NPMPackageSummary
	for _, name := range sortedKeys(f.npm, after) {
		if len(out) < limit {
			out = append(out, repository.NPMPackageSummary{Name: name})
		}
	}
	return out, nil
}

func (f fakeInventory) ListNPMVersions(_ context.Context, _, name string) ([]repository.NPMVersion, error) {
	return f.npm[name], nil
}

func (f fakeInventory) ListPyPIProjects(_ context.Context, _, _ string, limit int, after string, _ ...repository.ArtifactSearchMatch) ([]repository.PyPIProjectSummary, error) {
	var out []repository.PyPIProjectSummary
	for _, name := range sortedKeys(f.pypi, after) {
		if len(out) < limit {
			out = append(out, repository.PyPIProjectSummary{Project: name})
		}
	}
	return out, nil
}

func (f fakeInventory) ListPyPIProjectFiles(_ context.Context, _, project string) ([]repository.PyPIFile, error) {
	return f.pypi[project], nil
}

func (f fakeInventory) ListGoModules(_ context.Context, _, _ string, limit int, after string) ([]string, error) {
	keys := sortedKeys(f.goMods, after)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	return keys, nil
}

func (f fakeInventory) ListGoModuleVersions(_ context.Context, _, module string) ([]repository.GoModuleVersion, error) {
	return f.goMods[module], nil
}

func (f fakeInventory) GetGoModuleAsset(_ context.Context, _, module, version, kind string) (repository.GoModuleAsset, error) {
	if kind != "zip" {
		return repository.GoModuleAsset{}, repository.ErrNotFound
	}
	return repository.GoModuleAsset{Module: module, Version: version, Kind: kind, Digest: seedDigest(module + "@" + version)}, nil
}

func (f fakeInventory) SearchCargoCrates(_ context.Context, _, _ string, limit int, after string, _ bool, _ ...repository.ArtifactSearchMatch) ([]repository.CargoCrateSummary, int, error) {
	var out []repository.CargoCrateSummary
	for _, name := range sortedKeys(f.cargo, after) {
		if len(out) < limit {
			out = append(out, repository.CargoCrateSummary{Name: name})
		}
	}
	return out, len(out), nil
}

func (f fakeInventory) ListCargoPublications(_ context.Context, _, name string) ([]repository.CargoPublication, error) {
	return f.cargo[name], nil
}

func seedDigest(seed string) string {
	var sum uint64
	for _, r := range seed {
		sum = sum*31 + uint64(r)
	}
	return fmt.Sprintf("sha256:%064x", sum)
}

func inventoryRun(t *testing.T, format repository.Format, match promotionrules.Match, inventory dryRunInventory, store *repository.MemoryStore, sourceID string) *dryRun {
	t.Helper()
	compiled, err := match.Compile()
	if err != nil {
		t.Fatal(err)
	}
	registry := promotionFormatRegistry(promotionModuleDeps{Store: store})
	run := &dryRun{source: repository.HostedRepository{ID: sourceID, Name: "src", Format: format}, match: compiled, now: time.Now(),
		ports: promotionDryRunPorts{inventory: inventory, quarantine: store,
			distributionDigests: func(_ context.Context, _ repository.HostedRepository, _, digest string) ([]string, error) {
				return []string{digest}, nil
			}}}
	if governance, ok := formatmodule.Capability[formatmodule.Governance](registry, format); ok {
		run.validCoordinate = governance.ValidCoordinate
	}
	if tagged, ok := formatmodule.Capability[formatmodule.Tagged](registry, format); ok && compiled.HasRefs() {
		run.tagged = tagged
	}
	return run
}

func coordinatesOf(t *testing.T, run *dryRun) []string {
	t.Helper()
	result, err := run.execute(context.Background(), 500)
	if err != nil {
		t.Fatal(err)
	}
	coordinates := []string{}
	for _, item := range result.Items {
		coordinates = append(coordinates, item.Coordinate)
	}
	sort.Strings(coordinates)
	return coordinates
}

// Every version of an npm package is its own promotable unit, addressed name@version; the glob and the version range
// see that coordinate, and the dist-tags (Tagged) are looked up for it.
func TestDryRunNPMEnumeratesEveryVersionAndMatchesRefs(t *testing.T) {
	store := repository.NewMemoryStore()
	ctx := context.Background()
	source, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "npm", Format: repository.FormatNPM})
	if err != nil {
		t.Fatal(err)
	}
	version := func(v string) repository.NPMVersion {
		return repository.NPMVersion{RepositoryID: source.ID, PackageName: "@acme/lib", Version: v, Digest: seedDigest("npm" + v), State: "visible"}
	}
	inventory := fakeInventory{npm: map[string][]repository.NPMVersion{
		"@acme/lib": {version("1.0.0"), version("1.1.0"), version("2.0.0")},
		"left-pad":  {{RepositoryID: source.ID, PackageName: "left-pad", Version: "0.1.0", Digest: seedDigest("lp")}},
	}}
	run := inventoryRun(t, repository.FormatNPM, promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "@acme/lib@*"}}, inventory, store, source.ID)
	if got := coordinatesOf(t, run); strings.Join(got, ",") != "@acme/lib@1.0.0,@acme/lib@1.1.0,@acme/lib@2.0.0" {
		t.Fatalf("coordinates = %v", got)
	}

	run = inventoryRun(t, repository.FormatNPM, promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "**"}, Version: &promotionrules.VersionRange{Range: "<2.0.0"}}, inventory, store, source.ID)
	if got := coordinatesOf(t, run); strings.Join(got, ",") != "@acme/lib@1.0.0,@acme/lib@1.1.0,left-pad@0.1.0" {
		t.Fatalf("range coordinates = %v", got)
	}
}

func TestDryRunNPMRefsMatchTheDistTagsOfEachVersion(t *testing.T) {
	store := repository.NewMemoryStore()
	ctx := context.Background()
	source, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "npm", Format: repository.FormatNPM})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		tags := map[string]string{}
		if v == "1.1.0" {
			tags["stable"] = v
		}
		if v == "2.0.0" {
			tags["next"] = v
		}
		_, err = store.PublishNPMVersion(ctx, repository.NPMVersion{RepositoryID: source.ID, PackageName: "lib", Version: v, Digest: seedDigest("lib" + v), Integrity: "sha512-x", Shasum: "x", TarballName: "lib-" + v + ".tgz", ObjectKey: "native/npm/" + v, Size: 1, Manifest: []byte("{}"), Publisher: "ci", State: "visible"}, tags)
		if err != nil {
			t.Fatal(err)
		}
	}
	inventory, ok := any(store).(dryRunInventory)
	if !ok {
		t.Fatal("the store does not provide the dry run inventory")
	}
	run := inventoryRun(t, repository.FormatNPM, promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "lib@*"}, Refs: []string{"stable"}}, inventory, store, source.ID)
	if got := coordinatesOf(t, run); strings.Join(got, ",") != "lib@1.1.0" {
		t.Fatalf("stable = %v", got)
	}
	run = inventoryRun(t, repository.FormatNPM, promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "lib@*"}, Refs: []string{"stable", "next"}}, inventory, store, source.ID)
	if got := coordinatesOf(t, run); strings.Join(got, ",") != "lib@1.1.0,lib@2.0.0" {
		t.Fatalf("stable or next = %v", got)
	}
}

// A Go module version is v-prefixed; a semver range still matches it, and the coordinate is module@version.
func TestDryRunGoMatchesRangesOverVPrefixedVersions(t *testing.T) {
	store := repository.NewMemoryStore()
	inventory := fakeInventory{goMods: map[string][]repository.GoModuleVersion{
		"example.com/m": {{Module: "example.com/m", Version: "v1.0.0"}, {Module: "example.com/m", Version: "v1.5.2"}, {Module: "example.com/m", Version: "v2.0.0"}},
	}}
	run := inventoryRun(t, repository.FormatGo, promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "example.com/m@*"}, Version: &promotionrules.VersionRange{Range: ">=1.0.0 <2.0.0"}}, inventory, store, uuid.NewString())
	if got := coordinatesOf(t, run); strings.Join(got, ",") != "example.com/m@v1.0.0,example.com/m@v1.5.2" {
		t.Fatalf("coordinates = %v", got)
	}
}

func TestDryRunCargoAndPyPIListEveryVersion(t *testing.T) {
	store := repository.NewMemoryStore()
	cargo := fakeInventory{cargo: map[string][]repository.CargoPublication{"serde": {
		{CargoIdentityClaim: repository.CargoIdentityClaim{Name: "serde", Version: "1.0.1", Digest: seedDigest("s1")}},
		{CargoIdentityClaim: repository.CargoIdentityClaim{Name: "serde", Version: "1.0.2", Digest: seedDigest("s2")}},
	}}}
	run := inventoryRun(t, repository.FormatCargo, promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "serde@*"}}, cargo, store, uuid.NewString())
	if got := coordinatesOf(t, run); strings.Join(got, ",") != "serde@1.0.1,serde@1.0.2" {
		t.Fatalf("cargo = %v", got)
	}
	pypi := fakeInventory{pypi: map[string][]repository.PyPIFile{"requests": {
		{Project: "requests", Version: "2.31.0", Filename: "requests-2.31.0.tar.gz", Digest: seedDigest("a")},
		{Project: "requests", Version: "2.31.0", Filename: "requests-2.31.0-py3-none-any.whl", Digest: seedDigest("b")},
		{Project: "requests", Version: "2.32.0", Filename: "requests-2.32.0.tar.gz", Digest: seedDigest("c")},
	}}}
	run = inventoryRun(t, repository.FormatPyPI, promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "requests@*"}}, pypi, store, uuid.NewString())
	result, err := run.execute(context.Background(), 100)
	if err != nil || result.Matched != 2 || result.Examined != 2 {
		t.Fatalf("pypi = %+v, %v", result, err)
	}
}

// OCI refs are the tags of the manifest digest; the image name is the coordinate.
func TestDryRunOCIMatchesTagsOfManifestDigests(t *testing.T) {
	store := repository.NewMemoryStore()
	ctx := context.Background()
	source, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "oci", Format: repository.FormatOCI})
	if err != nil {
		t.Fatal(err)
	}
	put := func(name, digest, tag string) {
		if _, putErr := store.PutOCIManifest(ctx, repository.OCIManifest{RepositoryID: source.ID, Name: name, Digest: digest, ObjectKey: "native/oci/" + digest, MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 1}, tag); putErr != nil {
			t.Fatal(putErr)
		}
	}
	released, untagged, other := seedDigest("released"), seedDigest("untagged"), seedDigest("other")
	put("team/app", released, "v1.0")
	put("team/app", released, "latest")
	put("team/app", untagged, seedDigest("untagged"))
	put("team/other", other, "dev")
	inventory, ok := any(store).(dryRunInventory)
	if !ok {
		t.Fatal("the store does not provide the dry run inventory")
	}
	run := inventoryRun(t, repository.FormatOCI, promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "team/*"}, Refs: []string{"v*"}}, inventory, store, source.ID)
	result, err := run.execute(ctx, 100)
	if err != nil || result.Examined != 3 || result.Matched != 1 || result.Items[0].Coordinate != "team/app" || result.Items[0].Digest != released {
		t.Fatalf("result = %+v, %v", result, err)
	}

	tags, err := run.tagged.Tags(ctx, source.ID, "team/app", released)
	sort.Strings(tags)
	if err != nil || strings.Join(tags, ",") != "latest,v1.0" {
		t.Fatalf("tags = %v, %v", tags, err)
	}
}

func TestOCIRegistersTheTaggedCapability(t *testing.T) {
	registry := promotionFormatRegistry(promotionModuleDeps{Store: repository.NewMemoryStore()})
	if _, ok := formatmodule.Capability[formatmodule.Tagged](registry, repository.FormatOCI); !ok {
		t.Fatal("OCI must provide Tagged: its rules match tags")
	}
}
