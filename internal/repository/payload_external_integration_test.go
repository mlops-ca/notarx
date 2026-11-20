//go:build integration

package repository

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/testsupport"
)

// externalizeEverything makes every payload above one byte go to the object store, so the shared
// store contracts below run the externalised path of each payload kind. The inline path is the
// default run of the same contracts.
func externalizeEverything(t *testing.T, store *PostgresStore) *memoryPayloadStore {
	t.Helper()
	payloads := newMemoryPayloadStore()
	store.SetPayloadStore(payloads, 1)
	return payloads
}

func requireExternalized(t *testing.T, payloads *memoryPayloadStore) {
	t.Helper()
	if payloads.puts == 0 || payloads.gets == 0 {
		t.Fatalf("the run never wrote (%d) and read (%d) an externalised payload", payloads.puts, payloads.gets)
	}
}

func TestPostgresExternalizedNuGetPayloadsRoundTrip(t *testing.T) {
	store := newPostgresNuGetStore(t)
	payloads := externalizeEverything(t, store)
	testNuGetStoreContract(t, store)
	testNuGetSourceStoreContract(t, store, func(t *testing.T, repository, actor string) func() {
		return testsupport.FailSecurityAudits(t, store.db, repository, actor)
	})
	requireExternalized(t, payloads)
	var inlineNuspec int
	if err := store.db.QueryRow(`SELECT count(*) FROM native_nuget_publications WHERE body_digest IS NOT NULL AND octet_length(nuspec)<>0`).Scan(&inlineNuspec); err != nil || inlineNuspec != 0 {
		t.Fatalf("an externalised nuspec kept inline bytes: %d %v", inlineNuspec, err)
	}
}

func TestPostgresExternalizedHelmPayloadsRoundTrip(t *testing.T) {
	store := newPostgresNuGetStore(t)
	payloads := externalizeEverything(t, store)
	testHelmStoreContract(t, store)
	testHelmSourceStoreContract(t, store)
	requireExternalized(t, payloads)
	var inlineChart int
	if err := store.db.QueryRow(`SELECT count(*) FROM native_helm_publications WHERE body_digest IS NOT NULL AND octet_length(chart_yaml)<>0`).Scan(&inlineChart); err != nil || inlineChart != 0 {
		t.Fatalf("an externalised chart.yaml kept inline bytes: %d %v", inlineChart, err)
	}
}

func TestPostgresExternalizedOCIDiscoveryRoundTrip(t *testing.T) {
	store := openDiscoveryPostgres(t)
	payloads := externalizeEverything(t, store)
	testOCIDiscoveryStore(t, store, store)
	requireExternalized(t, payloads)
}

func TestPostgresExternalizedNPMManifestsRoundTrip(t *testing.T) {
	store, second, repo := openPostgresNPMDistTagStores(t)
	payloads := externalizeEverything(t, store)
	secondPayloads := externalizeEverything(t, second)
	secondPayloads.objects = payloads.objects
	exerciseNPMMetadataContract(t, store, repo.ID)
	requireExternalized(t, payloads)
	pkg, err := second.GetNPMPackage(context.Background(), repo.ID, "@scope/governed")
	if err != nil || len(pkg.TombstonedVersions) != 2 {
		t.Fatalf("the other instance cannot read the externalised manifests: %#v %v", pkg, err)
	}
	for _, version := range pkg.TombstonedVersions {
		if !strings.HasPrefix(string(version.Manifest), "{") {
			t.Fatalf("manifest of %s was not resolved: %q", version.Version, version.Manifest)
		}
	}
}

// newCargoProxyFixture opens a store with an active Cargo proxy repository and its config.
func newCargoProxyFixture(t *testing.T) (*PostgresStore, HostedRepository, time.Time) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "cargo-external-" + uuid.NewString()[:8],
		Format: FormatCargo, Type: RepositoryTypeProxy, Endpoint: "https://index.example.test"})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM hosted_repositories WHERE id=$1`, repo.ID)
		_ = store.Close()
	})
	now := time.Now().UTC()
	if err := store.PutCargoProxyConfig(ctx, CargoProxyConfig{RepositoryID: repo.ID, SourceVersion: repo.Version, DownloadTemplate: "https://static.example.test/crates", FetchedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return store, repo, now
}

func TestPostgresCargoProxyIndexAboveAndBelowTheThreshold(t *testing.T) {
	store, repo, now := newCargoProxyFixture(t)
	ctx := context.Background()
	payloads := newMemoryPayloadStore()
	store.SetPayloadStore(payloads, 1000)
	body := func(name string, versions int) []byte {
		var out bytes.Buffer
		for i := range versions {
			fmt.Fprintf(&out, `{"name":%q,"vers":"1.0.%d","cksum":%q,"yanked":false}`+"\n", name, i, strings.Repeat("a", 64))
		}
		return out.Bytes()
	}
	for name, versions := range map[string]int{"small": 1, "large": 20} {
		index := CargoProxyIndex{RepositoryID: repo.ID, SourceVersion: repo.Version, Name: name, Body: body(name, versions), Status: 200, FetchedAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := store.PutCargoProxyIndex(ctx, index); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		saved, err := store.GetCargoProxyIndex(ctx, repo.ID, name)
		if err != nil || !bytes.Equal(saved.Body, index.Body) {
			t.Fatalf("%s did not round trip: %q %v", name, saved.Body, err)
		}
		var external bool
		var inlineBytes int
		if err := store.db.QueryRowContext(ctx, `SELECT body_digest IS NOT NULL,octet_length(body) FROM native_cargo_proxy_indexes WHERE repository_id=$1::uuid AND name=$2`, repo.ID, name).Scan(&external, &inlineBytes); err != nil {
			t.Fatal(err)
		}
		if external != (versions > 1) || (external && inlineBytes != 0) || (!external && inlineBytes != len(index.Body)) {
			t.Fatalf("%s: external=%v inline bytes=%d for a %d byte body", name, external, inlineBytes, len(index.Body))
		}
	}
	if payloads.puts != 1 {
		t.Fatalf("only the large body belongs in the object store, got %d writes", payloads.puts)
	}
	found, err := store.SearchCargoProxyCrates(ctx, repo.ID, "large", 10, "")
	if err != nil || len(found) != 1 {
		t.Fatalf("search must read an externalised index: %+v %v", found, err)
	}
}

func TestPostgresArtifactIntelligenceFindingsAboveAndBelowTheThreshold(t *testing.T) {
	fixture := newPostgresArtifactScanFixture(t)
	payloads := newMemoryPayloadStore()
	fixture.store.SetPayloadStore(payloads, 1000)
	finding := func(i int) ArtifactVulnerabilityFinding {
		return ArtifactVulnerabilityFinding{ID: fmt.Sprintf("CVE-2026-%04d", i), Source: "nvd", Severity: ArtifactVulnerabilitySeverityCritical,
			Component: "pkg:generic/widget@1.0.0", Version: "1.0.0", FixedVersion: "1.0.1", Location: "widget.bin"}
	}
	for name, count := range map[string]int{"small": 1, "large": 40} {
		findings := make([]ArtifactVulnerabilityFinding, count)
		for i := range findings {
			findings[i] = finding(i)
		}
		coordinate, digest := "releases/"+name+".bin", "sha256:"+strings.Repeat(map[string]string{"small": "1", "large": "2"}[name], 64)
		written, err := fixture.store.ReplaceArtifactIntelligence(fixture.ctx, ArtifactIntelligence{
			RepositoryID: fixture.repo.ID, Format: FormatRaw, Coordinate: coordinate, Digest: digest,
			Vulnerability: &ArtifactVulnerabilitySummary{Scanner: "grype", Status: "affected", Critical: count, Findings: findings},
			UpdatedBy:     "scanner:grype",
		}, "")
		if err != nil || written.Version != "1" {
			t.Fatalf("%s: %#v %v", name, written, err)
		}
		loaded, err := fixture.store.GetArtifactIntelligence(fixture.ctx, fixture.repo.ID, FormatRaw, coordinate, digest)
		if err != nil || loaded.Vulnerability == nil || len(loaded.Vulnerability.Findings) != count || loaded.Vulnerability.Critical != count {
			t.Fatalf("%s did not round trip: %#v %v", name, loaded.Vulnerability, err)
		}
		var external bool
		var inlineCritical int
		var inlineFindings int
		if err := fixture.store.db.QueryRowContext(fixture.ctx, `SELECT body_digest IS NOT NULL,(vulnerability->>'critical')::int,COALESCE(jsonb_array_length(vulnerability->'findings'),0) FROM artifact_intelligence WHERE repository_id=$1::uuid AND coordinate=$2 AND digest=$3`, fixture.repo.ID, coordinate, digest).Scan(&external, &inlineCritical, &inlineFindings); err != nil {
			t.Fatal(err)
		}
		if external != (count > 1) || inlineCritical != count || (external && inlineFindings != 0) || (!external && inlineFindings != count) {
			t.Fatalf("%s: external=%v inline critical=%d inline findings=%d", name, external, inlineCritical, inlineFindings)
		}
		// Replacing an externalised result with a small one clears the reference.
		if count > 1 {
			replaced, err := fixture.store.ReplaceArtifactIntelligence(fixture.ctx, ArtifactIntelligence{
				RepositoryID: fixture.repo.ID, Format: FormatRaw, Coordinate: coordinate, Digest: digest,
				Vulnerability: &ArtifactVulnerabilitySummary{Scanner: "grype", Status: "clean"}, UpdatedBy: "scanner:grype",
			}, written.Version)
			if err != nil || replaced.Vulnerability == nil || len(replaced.Vulnerability.Findings) != 0 || replaced.Vulnerability.Status != "clean" {
				t.Fatalf("replace with a small result: %#v %v", replaced.Vulnerability, err)
			}
		}
	}
	if payloads.puts != 1 {
		t.Fatalf("only the large result belongs in the object store, got %d writes", payloads.puts)
	}
}

func TestPostgresScanSBOMBodyAboveAndBelowTheThreshold(t *testing.T) {
	store := samlStore(t)
	payloads := newMemoryPayloadStore()
	store.SetPayloadStore(payloads, 64)
	ctx := context.Background()
	repo, artifact := ids.New(), "sha256:"+strings.Repeat("c", 64)
	small := []byte(`{"bomFormat":"CycloneDX"}`)
	large := []byte(`{"bomFormat":"CycloneDX","components":[` + strings.Repeat(`{"name":"component","version":"1.0.0"},`, 8) + `{"name":"last"}]}`)
	for name, body := range map[string][]byte{"small": small, "large": large} {
		kind := ScanSBOMKindCycloneDX
		source := ScanSBOMSourceGenerated
		if name == "large" {
			source = ScanSBOMSourceUploaded
		}
		put, err := store.PutScanSBOM(ctx, ScanSBOM{RepositoryID: repo, ArtifactDigest: artifact, Kind: kind, Source: source, BodyDigest: "sha256:" + strings.Repeat("0", 64), BodySize: int64(len(body)), Body: body, ComponentCount: 9})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := store.GetScanSBOM(ctx, repo, artifact, kind, source)
		if err != nil {
			t.Fatal(err)
		}
		if (name == "large") != (got.BodyKey != "" && len(got.Body) == 0) || (name == "small") != (got.BodyKey == "" && len(got.Body) > 0) {
			t.Fatalf("%s: stored as key=%q inline=%d bytes (put returned key=%q)", name, got.BodyKey, len(got.Body), put.BodyKey)
		}
		resolved, err := store.ScanSBOMBody(ctx, got)
		if err != nil || !bytes.Equal(bytes.ReplaceAll(resolved, []byte(" "), nil), bytes.ReplaceAll(body, []byte(" "), nil)) {
			t.Fatalf("%s body did not resolve: %q %v", name, resolved, err)
		}
	}
	if payloads.puts != 1 {
		t.Fatalf("only the large SBOM belongs in the object store, got %d writes", payloads.puts)
	}
}

// connectionWatchPayloadStore is a payload store that counts the calls made while a pool connection is checked out
// beyond the baseline: a call inside a transaction (or any other statement) holds one, so a call with the pool
// idle proves no transaction, and so no row lock, waits on the object store. The advisory locks a handler takes use
// a separate pool, and the test must drive the store from one goroutine, since another operation's transaction would
// also hold a connection.
type connectionWatchPayloadStore struct {
	*memoryPayloadStore
	db       *sql.DB
	baseline int
	inTx     atomic.Int64
}

func watchPayloadStore(t *testing.T, store *PostgresStore, inlineMax int) *connectionWatchPayloadStore {
	t.Helper()
	watch := &connectionWatchPayloadStore{memoryPayloadStore: newMemoryPayloadStore(), db: store.db, baseline: store.db.Stats().InUse}
	store.SetPayloadStore(watch, inlineMax)
	return watch
}

func (w *connectionWatchPayloadStore) observe() {
	if w.db.Stats().InUse > w.baseline {
		w.inTx.Add(1)
	}
}

func (w *connectionWatchPayloadStore) Put(ctx context.Context, data []byte) (string, string, error) {
	w.observe()
	return w.memoryPayloadStore.Put(ctx, data)
}

func (w *connectionWatchPayloadStore) Get(ctx context.Context, digest string) ([]byte, error) {
	w.observe()
	return w.memoryPayloadStore.Get(ctx, digest)
}

func requireNoPayloadCallInTransaction(t *testing.T, watch *connectionWatchPayloadStore) {
	t.Helper()
	if watch.puts == 0 {
		t.Fatal("the run never wrote an externalised payload")
	}
	if n := watch.inTx.Load(); n != 0 {
		t.Fatalf("%d object store calls ran while a database connection was checked out", n)
	}
}

func TestPostgresHelmAndNuGetCommitsNeverCallThePayloadStoreInATransaction(t *testing.T) {
	store := newPostgresNuGetStore(t)
	watch := watchPayloadStore(t, store, 1)
	ctx := t.Context()
	for _, kind := range []RepositoryType{RepositoryTypeHosted, RepositoryTypeProxy} {
		repo, published := seedHelmGovernance(t, store, kind)
		// A replayed commit returns the stored publication, its chart.yaml read after the transaction.
		replayed, replay, err := store.CommitHelmPublication(ctx, published.SessionID, "publisher")
		if err != nil || !replay || !bytes.Equal(replayed.ChartYAML, []byte("original chart metadata")) {
			t.Fatalf("%s replay: %q replay=%v %v", kind, replayed.ChartYAML, replay, err)
		}
		if _, err = store.TransitionHelmGovernance(ctx, HelmGovernanceChange{RepositoryID: repo.ID, Name: published.Name, Version: published.Version, ExpectedDigest: published.Digest, ExpectedVersion: "1", Action: "quarantine", Actor: "operator", Reason: "Confirmed fixture quarantine"}); err != nil {
			t.Fatalf("%s transition: %v", kind, err)
		}
	}
	repo, published := seedNuGetGovernance(t, store)
	replayed, replay, err := store.CommitNuGetPublication(ctx, published.SessionID, "publisher")
	if err != nil || !replay || !bytes.Equal(replayed.Nuspec, []byte("original nuspec")) {
		t.Fatalf("nuget replay: %q replay=%v %v", replayed.Nuspec, replay, err)
	}
	if _, err = store.TransitionNuGetGovernance(ctx, NuGetGovernanceChange{RepositoryID: repo.ID, PackageID: published.PackageID, Version: published.Version, ExpectedDigest: published.Digest, ExpectedVersion: "1", Action: "quarantine", Actor: "operator", Reason: "Confirmed fixture decision"}); err != nil {
		t.Fatalf("nuget transition: %v", err)
	}
	if watch.gets == 0 {
		t.Fatal("the replayed commits never read an externalised payload")
	}
	requireNoPayloadCallInTransaction(t, watch)
}

func TestPostgresReplicationNeverCallsThePayloadStoreInATransaction(t *testing.T) {
	t.Run("nuget", func(t *testing.T) {
		store := newPostgresNuGetStore(t)
		watch := watchPayloadStore(t, store, 1)
		testNuGetReplicationStoreContract(t, store)
		requireNoPayloadCallInTransaction(t, watch)
	})
	t.Run("helm", func(t *testing.T) {
		store := newPostgresNuGetStore(t)
		watch := watchPayloadStore(t, store, 1)
		testHelmReplicationStoreContract(t, store)
		requireNoPayloadCallInTransaction(t, watch)
	})
}

func TestPostgresNPMNeverCallsThePayloadStoreInATransaction(t *testing.T) {
	store, _, repo := openPostgresNPMDistTagStores(t)
	watch := watchPayloadStore(t, store, 1)
	exerciseNPMMetadataContract(t, store, repo.ID)
	requireNoPayloadCallInTransaction(t, watch)
}

func TestPostgresNPMMetadataAndTombstoneSurviveAMissingManifestObject(t *testing.T) {
	store, _, repo := openPostgresNPMDistTagStores(t)
	payloads := externalizeEverything(t, store)
	ctx := context.Background()
	const name = "@scope/missing"
	publishNPMDistTagFixture(t, store, repo.ID, name, "1.0.0", map[string]string{"latest": "1.0.0"})
	publishNPMDistTagFixture(t, store, repo.ID, name, "1.1.0", map[string]string{"next": "1.1.0"})
	var revision int64
	if err := store.db.QueryRowContext(ctx, `SELECT revision FROM native_npm_packages WHERE repository_id=$1::uuid AND name=$2`, repo.ID, name).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	payloads.mu.Lock()
	clear(payloads.objects)
	payloads.mu.Unlock()
	if _, err := store.GetNPMPackage(ctx, repo.ID, name); err == nil {
		t.Fatal("a read that needs the manifests must fail when their objects are gone")
	}
	mutation := NPMMetadataMutation{RepositoryID: repo.ID, PackageName: name, ExpectedRevision: revision,
		Deprecations: map[string]string{"1.0.0": "use 1.1.0"}, Audit: npmDistTagMutation(repo.ID, name, "", "").Audit}
	mutation.Audit.Operation = "npm.deprecate"
	changed, err := store.UpdateNPMMetadata(ctx, mutation)
	if err != nil || !changed.Changed {
		t.Fatalf("deprecate without the manifest objects: %#v %v", changed, err)
	}
	mutation.ExpectedRevision, mutation.Deprecations = changed.Revision, nil
	mutation.Unpublish, mutation.DistTags = []string{"1.0.0"}, map[string]string{"latest": "1.1.0", "next": "1.1.0"}
	mutation.Audit.Operation = "npm.unpublish"
	if changed, err = store.UpdateNPMMetadata(ctx, mutation); err != nil || !changed.Changed {
		t.Fatalf("unpublish without the manifest objects: %#v %v", changed, err)
	}
	if _, err = store.TombstoneNPMVersion(ctx, repo.ID, name, "1.1.0"); err != nil {
		t.Fatalf("tombstone without the manifest object: %v", err)
	}
	if _, err = store.RestoreNPMVersion(ctx, NPMRestoreRequest{RepositoryID: repo.ID, PackageName: name, Version: "1.1.0"}); err != nil {
		t.Fatalf("restore without the manifest object: %v", err)
	}
	if _, err = store.GetNPMVersion(ctx, repo.ID, name, "1.1.0"); err == nil {
		t.Fatal("only the caller that needs the manifest fails")
	}
}

func TestPostgresCargoProxyWritesNeverCallThePayloadStoreInATransaction(t *testing.T) {
	store, repo, now := newCargoProxyFixture(t)
	ctx := context.Background()
	watch := watchPayloadStore(t, store, 100)
	checksum := strings.Repeat("a", 64)
	body := []byte(`{"name":"demo","vers":"1.0.0","cksum":"` + checksum + `","yanked":false,"deps":[],"features":{},` + strings.Repeat(" ", 200) + `"v":2}` + "\n")
	index := CargoProxyIndex{RepositoryID: repo.ID, SourceVersion: repo.Version, Name: "demo", Body: body, Status: 200, FetchedAt: now, ExpiresAt: now.Add(time.Hour)}
	for _, step := range []string{"first write", "refresh against the stored body"} {
		if err := store.PutCargoProxyIndex(ctx, index); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
	}
	crate := CargoProxyCrate{RepositoryID: repo.ID, SourceVersion: repo.Version, Name: "demo", Version: "1.0.0", Checksum: checksum,
		ObjectKey: "native/cargo-proxy/sha256/" + checksum, Size: 23, CachedAt: now}
	if err := store.PutCargoProxyCrate(ctx, crate); err != nil {
		t.Fatal(err)
	}
	if watch.gets == 0 {
		t.Fatal("the refresh and the crate write never validated against an externalised body")
	}
	requireNoPayloadCallInTransaction(t, watch)
}

func TestPostgresNPMProxyCacheAndImportNeverCallThePayloadStoreInATransaction(t *testing.T) {
	store, _, repo := openPostgresNPMDistTagStores(t)
	ctx := context.Background()
	watch := watchPayloadStore(t, store, 1)
	const name = "@scope/imported"
	manifest := func(extra string) []byte {
		return []byte(`{"name":"` + name + `","version":"1.0.0","dist":{"tarball":"https://registry.example/imported.tgz"}` + extra + `}`)
	}
	version := NPMVersion{RepositoryID: repo.ID, PackageName: name, Version: "1.0.0", Digest: uniqueDigest(), Integrity: "sha512-YQ==", Shasum: strings.Repeat("b", 40),
		TarballName: "imported-1.0.0.tgz", ObjectKey: "native/npm/sha256/" + uuid.NewString(), Size: 8, Manifest: manifest(""), Publisher: "importer"}
	closure := NPMImportClosure{RepositoryID: repo.ID, PackageName: name, Versions: []NPMVersion{version}, DistTags: map[string]string{"latest": "1.0.0"},
		Audit: npmDistTagMutation(repo.ID, name, "", "").Audit}
	// Importing the identical content again compares the stored manifest, which is read before the transaction.
	for _, step := range []string{"first import", "identical re-import"} {
		if err := store.PublishNPMImport(ctx, closure); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
	}
	changed := closure
	changed.Versions = []NPMVersion{version}
	changed.Versions[0].Manifest = manifest(`,"description":"changed"`)
	if err := store.PublishNPMImport(ctx, changed); !errors.Is(err, ErrNameExists) {
		t.Fatalf("a different manifest for a stored version must conflict: %v", err)
	}
	if watch.gets == 0 {
		t.Fatal("the re-import never compared against an externalised manifest")
	}

	proxy, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "npm-proxy-external-" + uuid.NewString()[:8], Format: FormatNPM, Type: RepositoryTypeProxy, Endpoint: "https://proxy.example"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testsupport.PurgeRepository(t, store.db, proxy.ID) })
	listed := NPMVersion{Version: "1.0.0", UpstreamTarball: "https://registry.example/widget.tgz", TarballName: "widget.tgz", Manifest: []byte(`{"name":"widget","version":"1.0.0"}`)}
	if _, err = store.SyncNPMProxyPackage(ctx, NPMPackage{RepositoryID: proxy.ID, SourceVersion: proxy.Version, Name: "widget", SourceEndpoint: proxy.Endpoint, Versions: []NPMVersion{listed}, DistTags: map[string]string{"latest": "1.0.0"}}); err != nil {
		t.Fatal(err)
	}
	cached, err := store.GetNPMVersion(ctx, proxy.ID, "widget", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	cached.Digest, cached.ObjectKey, cached.Size, cached.SourceVersion = uniqueDigest(), "npm/"+uuid.NewString(), 40, proxy.Version
	// Caching the tarball needs the version's identity, not its manifest, so it works without the manifest object.
	watch.mu.Lock()
	clear(watch.objects)
	watch.mu.Unlock()
	if _, err = store.CacheNPMProxyTarball(ctx, cached); err != nil {
		t.Fatalf("cache the tarball without the manifest object: %v", err)
	}
	requireNoPayloadCallInTransaction(t, watch)
}
