package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func eventDigest(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func eventRepository(t *testing.T, store *MemoryStore, id string, format Format, repoType RepositoryType) HostedRepository {
	t.Helper()
	repo, err := store.CreateHostedRepository(context.Background(), HostedRepository{ID: id, Name: id, Format: format, Type: repoType, Endpoint: endpointFor(repoType)})
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func endpointFor(repoType RepositoryType) string {
	if repoType == RepositoryTypeProxy {
		return "https://upstream.example"
	}
	return ""
}

// publishedActors maps the dedupe key of every artifact.published event to the actor of its payload.
func publishedActors(t *testing.T, store *MemoryStore) map[string]string {
	t.Helper()
	store.mu.RLock()
	defer store.mu.RUnlock()
	out := map[string]string{}
	for _, event := range store.webhookEvents {
		if event.Type != "artifact.published" {
			continue
		}
		var payload map[string]string
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatal(err)
		}
		out[event.domainEvent().DedupeKey()] = payload["actor"]
	}
	return out
}

func requirePublished(t *testing.T, store *MemoryStore, want ...string) {
	t.Helper()
	got := publishedKeys(store)
	if len(got) != len(want) {
		t.Fatalf("artifact.published keys = %v, want %v", got, want)
	}
	for _, key := range want {
		found := false
		for _, have := range got {
			found = found || have == key
		}
		if !found {
			t.Fatalf("artifact.published keys = %v, want %v", got, want)
		}
	}
}

func eventKey(repositoryID, coordinate, digest string) string {
	return "artifact.published|" + repositoryID + "|" + coordinate + "|" + digest + "|"
}

func eventNPMVersion(repositoryID, name, version string, size int64) NPMVersion {
	return NPMVersion{RepositoryID: repositoryID, PackageName: name, Version: version, Digest: eventDigest(name + version), Integrity: "sha512-x", Shasum: "abc", ObjectKey: "npm/" + name + version, TarballName: name + "-" + version + ".tgz", Size: size, Manifest: []byte(`{"name":"` + name + `","dist":{}}`)}
}

func TestMemoryNPMPublicationsEmitArtifactPublished(t *testing.T) {
	store := NewMemoryStore()
	eventRepository(t, store, "npm-repo", FormatNPM, RepositoryTypeHosted)
	ctx := WithWriteActor(context.Background(), "alice")
	one := eventNPMVersion("npm-repo", "widget", "1.0.0", 10)
	if _, err := store.PublishNPMVersion(ctx, one, map[string]string{"latest": "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	// A rejected republish of the version leaves no second event.
	if _, err := store.PublishNPMVersion(ctx, one, nil); !errors.Is(err, ErrNameExists) {
		t.Fatalf("republish = %v", err)
	}
	if _, err := store.PublishNPMVersion(ctx, eventNPMVersion("npm-repo", "widget", "2.0.0", 10), map[string]string{"next": "9.9.9"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a tag on an unknown version = %v", err)
	}
	requirePublished(t, store, eventKey("npm-repo", "widget@1.0.0", one.Digest))
	if actors := publishedActors(t, store); actors[eventKey("npm-repo", "widget@1.0.0", one.Digest)] != "alice" {
		t.Fatalf("actors = %v", actors)
	}
	// An import emits one event per version it adds, none for a version the repository already holds.
	two := eventNPMVersion("npm-repo", "widget", "2.0.0", 10)
	closure := NPMImportClosure{RepositoryID: "npm-repo", PackageName: "widget", Versions: []NPMVersion{one, two}, DistTags: map[string]string{"latest": "1.0.0"}}
	if err := store.PublishNPMImport(WithWriteActor(context.Background(), "migration"), closure); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishNPMImport(context.Background(), closure); err != nil {
		t.Fatal(err)
	}
	requirePublished(t, store, eventKey("npm-repo", "widget@1.0.0", one.Digest), eventKey("npm-repo", "widget@2.0.0", two.Digest))
	if actors := publishedActors(t, store); actors[eventKey("npm-repo", "widget@2.0.0", two.Digest)] != "migration" {
		t.Fatalf("actors = %v", actors)
	}
}

func TestMemoryPyPIPublicationsEmitArtifactPublished(t *testing.T) {
	store := NewMemoryStore()
	eventRepository(t, store, "pypi-repo", FormatPyPI, RepositoryTypeHosted)
	ctx := WithWriteActor(context.Background(), "bob")
	file := func(version, name string, size int64) PyPIFile {
		return PyPIFile{RepositoryID: "pypi-repo", Project: "widget", Version: version, Filename: name, Digest: eventDigest(name), ObjectKey: "pypi/" + name, Size: size, Publisher: "bob"}
	}
	one := file("1.0", "widget-1.0.whl", 10)
	for range 2 {
		if _, err := store.PublishPyPIFile(ctx, one); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ReplaceRepositoryCapacityQuota(ctx, "pypi-repo", 15); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishPyPIFile(ctx, file("1.1", "widget-1.1.whl", 10)); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("publish over the quota = %v", err)
	}
	requirePublished(t, store, eventKey("pypi-repo", "widget@1.0", one.Digest))
	if _, err := store.ReplaceRepositoryCapacityQuota(ctx, "pypi-repo", 0); err != nil {
		t.Fatal(err)
	}
	wheel, sdist := file("2.0", "widget-2.0.whl", 10), file("2.0", "widget-2.0.tar.gz", 10)
	for range 2 {
		if _, err := store.PublishPyPIVersion(ctx, []PyPIFile{wheel, sdist}); err != nil {
			t.Fatal(err)
		}
	}
	requirePublished(t, store, eventKey("pypi-repo", "widget@1.0", one.Digest), eventKey("pypi-repo", "widget@2.0", wheel.Digest), eventKey("pypi-repo", "widget@2.0", sdist.Digest))
	if actors := publishedActors(t, store); actors[eventKey("pypi-repo", "widget@2.0", wheel.Digest)] != "bob" {
		t.Fatalf("actors = %v", actors)
	}
}

func eventGoPublication(repositoryID, module, version string, size int64) GoModulePublication {
	publication := GoModulePublication{Version: GoModuleVersion{RepositoryID: repositoryID, Module: module, Version: version, Publisher: "builder", PublishedAt: time.Now().UTC()}}
	for _, kind := range []string{"info", "mod", "zip"} {
		publication.Assets = append(publication.Assets, GoModuleAsset{RepositoryID: repositoryID, Module: module, Version: version, Kind: kind, Digest: eventDigest(module + version + kind), ObjectKey: "go/" + module + version + kind, Size: size})
	}
	return publication
}

func TestMemoryGoPublicationEmitsArtifactPublishedForTheZip(t *testing.T) {
	store := NewMemoryStore()
	eventRepository(t, store, "go-repo", FormatGo, RepositoryTypeHosted)
	ctx := WithWriteActor(context.Background(), "carol")
	publication := eventGoPublication("go-repo", "example.com/widget", "v1.0.0", 10)
	if _, replayed, err := store.PublishGoModule(ctx, publication); err != nil || replayed {
		t.Fatalf("publish = %v replayed=%v", err, replayed)
	}
	if _, replayed, err := store.PublishGoModule(ctx, publication); err != nil || !replayed {
		t.Fatalf("replay = %v replayed=%v", err, replayed)
	}
	if _, err := store.ReplaceRepositoryCapacityQuota(ctx, "go-repo", 40); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PublishGoModule(ctx, eventGoPublication("go-repo", "example.com/widget", "v1.1.0", 10)); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("publish over the quota = %v", err)
	}
	zip := publication.Assets[2].Digest
	requirePublished(t, store, eventKey("go-repo", "example.com/widget@v1.0.0", zip))
	if actors := publishedActors(t, store); actors[eventKey("go-repo", "example.com/widget@v1.0.0", zip)] != "carol" {
		t.Fatalf("actors = %v", actors)
	}
}

func TestMemoryCargoPublicationEmitsArtifactPublishedOnce(t *testing.T) {
	store := NewMemoryStore()
	ctx := WithWriteActor(context.Background(), "dave")
	eventRepository(t, store, "cargo-repo", FormatCargo, RepositoryTypeHosted)
	claim := cargoTestClaim("cargo-repo", "1.0.0", "a")
	reservation, _, err := store.ReserveCargoIdentity(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	publication := cargoTestPublication(t, claim, reservation, 7)
	if _, err = store.ReplaceRepositoryCapacityQuota(ctx, "cargo-repo", 5); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CommitCargoPublication(ctx, publication); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("commit over the quota = %v", err)
	}
	requirePublished(t, store)
	if _, err = store.ReplaceRepositoryCapacityQuota(ctx, "cargo-repo", 0); err != nil {
		t.Fatal(err)
	}
	for _, replay := range []bool{false, true} {
		if _, got, commitErr := store.CommitCargoPublication(ctx, publication); commitErr != nil || got != replay {
			t.Fatalf("commit replay=%v want %v err=%v", got, replay, commitErr)
		}
	}
	requirePublished(t, store, eventKey("cargo-repo", "demo-crate@1.0.0", claim.Digest))
	if actors := publishedActors(t, store); actors[eventKey("cargo-repo", "demo-crate@1.0.0", claim.Digest)] != "dave" {
		t.Fatalf("actors = %v", actors)
	}
}

func eventConanAssets(t *testing.T, store *MemoryStore, repositoryID, reference, recipeRevision, packageID, packageRevision string) []ConanAsset {
	t.Helper()
	asset := ConanAsset{RepositoryID: repositoryID, Reference: reference, RecipeRevision: recipeRevision, PackageID: packageID, PackageRevision: packageRevision, Path: "file0", ObjectKey: "conan/" + repositoryID + reference + recipeRevision + packageID + packageRevision, Digest: eventDigest(reference + recipeRevision + packageID + packageRevision), Size: 5}
	if err := store.StageConanObject(context.Background(), ConanObjectIntent{RepositoryID: repositoryID, ObjectKey: asset.ObjectKey, Digest: asset.Digest, Size: asset.Size}); err != nil {
		t.Fatal(err)
	}
	return []ConanAsset{asset}
}

func TestMemoryConanRevisionsAndPromotionEmitArtifactPublished(t *testing.T) {
	store := NewMemoryStore()
	ctx := WithWriteActor(context.Background(), "erin")
	eventRepository(t, store, "conan-src", FormatConan, RepositoryTypeHosted)
	eventRepository(t, store, "conan-dst", FormatConan, RepositoryTypeHosted)
	const reference = "pkg/1.0@user/stable"
	recipe := ConanRecipeRevision{RepositoryID: "conan-src", Reference: reference, Revision: "rrev1", Digest: eventDigest("recipe")}
	for range 2 {
		if _, err := store.PutConanRecipeRevision(ctx, recipe, eventConanAssets(t, store, "conan-src", reference, "rrev1", "", "")); err != nil {
			t.Fatal(err)
		}
	}
	pkg := ConanPackageRevision{RepositoryID: "conan-src", Reference: reference, RecipeRevision: "rrev1", PackageID: "pkga", Revision: "prev1", Digest: eventDigest("package")}
	if _, err := store.PutConanPackageRevision(ctx, pkg, eventConanAssets(t, store, "conan-src", reference, "rrev1", "pkga", "prev1")); err != nil {
		t.Fatal(err)
	}
	recipeKey := eventKey("conan-src", reference+"#rrev1", recipe.Digest)
	packageKey := eventKey("conan-src", reference+"#rrev1/pkga#prev1", pkg.Digest)
	requirePublished(t, store, recipeKey, packageKey)
	if _, err := store.PromoteConanRecipeRevision(ctx, ConanPromotion{SourceRepositoryID: "conan-src", TargetRepositoryID: "conan-dst", Reference: reference, Revision: "rrev1", Digest: recipe.Digest}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PromoteConanRecipeRevision(ctx, ConanPromotion{SourceRepositoryID: "conan-src", TargetRepositoryID: "conan-dst", Reference: reference, Revision: "rrev1", Digest: recipe.Digest}); !errors.Is(err, ErrNameExists) {
		t.Fatalf("second promotion = %v", err)
	}
	requirePublished(t, store, recipeKey, packageKey, eventKey("conan-dst", reference+"#rrev1", recipe.Digest), eventKey("conan-dst", reference+"#rrev1/pkga#prev1", pkg.Digest))
}

func TestMemoryConanSessionCommitEmitsArtifactPublishedWithTheCommitActor(t *testing.T) {
	store := NewMemoryStore()
	fixture := newConanManagedContractFixture(t, store, "package")
	fixture.stageAll(t)
	fixture.commit(t, "package-committer")
	actors := publishedActors(t, store)
	if len(actors) != 2 {
		t.Fatalf("a recipe and a package commit wrote %d events: %v", len(actors), actors)
	}
	packageKey := eventKey(fixture.repo.ID, fixture.session.Reference+"#"+fixture.session.RecipeRevision+"/"+fixture.session.PackageID+"#"+fixture.session.PackageRevision, fixture.digest())
	if actors[packageKey] != "package-committer" {
		t.Fatalf("actors = %v, want %s by package-committer", actors, packageKey)
	}
}

// aptMemorySnapshot stages one package per name and publishes a visible snapshot of them.
func aptMemorySnapshot(t *testing.T, store *MemoryStore, repo HostedRepository, sequence int64, names ...string) map[string]string {
	t.Helper()
	ctx := WithWriteActor(context.Background(), "release-operator")
	var members []APTSnapshotPackage
	var revisions []APTPackageRevision
	pool := map[string]string{}
	for _, name := range names {
		digest := eventDigest("apt-" + name)
		session := APTPublicationSession{ID: "session-" + name, RepositoryID: repo.ID, Suite: "stable", Component: "main", Publisher: "ci", ObjectName: name + "_1.0-1_amd64.deb", ExpectedIdentity: name + "@1.0-1#amd64", DeclaredDigest: digest, DeclaredSize: 8, State: APTPublicationSessionOpen, ExpiresAt: time.Now().Add(time.Hour)}
		if _, ok := store.aptPublicationSessions[session.ID]; !ok {
			if _, _, err := store.CreateAPTPublicationSessionIdempotently(ctx, session, "ci", "apt", "key-"+name, "payload-"+name); err != nil {
				t.Fatal(err)
			}
			key := "native/apt/sha256/" + strings.TrimPrefix(digest, "sha256:")
			if err := store.BeginAPTPackageUpload(ctx, session.ID, key); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CompleteAPTPackageUpload(ctx, session.ID, APTPackageRevision{ID: "revision-" + name, RepositoryID: repo.ID, Package: name, Version: "1.0-1", Architecture: "amd64", CanonicalIdentity: name + "@1.0-1#amd64", Digest: digest, ObjectKey: key, Size: 8, ObjectName: session.ObjectName, Publisher: "ci"}); err != nil {
				t.Fatal(err)
			}
		}
		revision, err := store.GetAPTPackageRevisionForSession(ctx, session.ID)
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, APTSnapshotPackage{PublicationSessionID: session.ID, PackageRevisionID: revision.ID, Component: "main", Architecture: "amd64"})
		revisions = append(revisions, revision)
		pool[APTPoolPath("main", name, session.ObjectName)] = digest
	}
	snapshot, err := store.CreateAPTRepositorySnapshot(ctx, APTRepositorySnapshot{ID: fmt.Sprintf("snapshot-%d", sequence), RepositoryID: repo.ID, Suite: "stable", Sequence: sequence, State: APTRepositorySnapshotBuilding}, members)
	if err != nil {
		t.Fatal(err)
	}
	indexBody := []byte(fmt.Sprintf("Packages %d", sequence))
	indexDigest := eventDigest(string(indexBody))
	release := []byte(fmt.Sprintf("Suite: stable\nAcquire-By-Hash: yes\nSHA256:\n %s %16d main/binary-amd64/Packages\n", strings.TrimPrefix(indexDigest, "sha256:"), len(indexBody)))
	generated := map[string][]byte{
		"dists/stable/Release": release, "dists/stable/InRelease": []byte("inrelease"), "dists/stable/Release.gpg": []byte("signature"),
		"dists/stable/main/binary-amd64/Packages":                                                     indexBody,
		"dists/stable/main/binary-amd64/by-hash/SHA256/" + strings.TrimPrefix(indexDigest, "sha256:"): indexBody,
	}
	var assets []APTSnapshotAsset
	for path, body := range generated {
		digest := eventDigest(string(body))
		assets = append(assets, APTSnapshotAsset{SnapshotID: snapshot.ID, RepositoryID: repo.ID, Path: path, Digest: digest, ObjectKey: "native/apt/sha256/" + strings.TrimPrefix(digest, "sha256:"), Size: int64(len(body)), ContentType: "text/plain"})
		switch path {
		case "dists/stable/Release":
			snapshot.ReleaseDigest = digest
		case "dists/stable/InRelease":
			snapshot.InReleaseDigest = digest
		}
	}
	for _, revision := range revisions {
		assets = append(assets, APTSnapshotAsset{SnapshotID: snapshot.ID, RepositoryID: repo.ID, Path: APTPoolPath("main", revision.Package, revision.ObjectName), Digest: revision.Digest, ObjectKey: revision.ObjectKey, Size: revision.Size, ContentType: "application/vnd.debian.binary-package"})
	}
	snapshot.State = APTRepositorySnapshotVisible
	snapshot.SignerIdentity, snapshot.KeyFingerprint, snapshot.SignatureAlgorithm = "signer", strings.Repeat("a", 40), "fixture"
	if _, err = store.PublishAPTRepositorySnapshotWithAudit(ctx, snapshot, assets, release, AuditRecord{Repository: repo.Name, Actor: "release-operator", Outcome: AuditResolved, Operation: "apt.repository_snapshot.publish", Status: 200}); err != nil {
		t.Fatal(err)
	}
	return pool
}

// A pool path emits once, in the snapshot that first binds it to its digest; a later snapshot that carries it again emits
// nothing.
func TestMemoryAPTSnapshotsEmitArtifactPublishedPerNewPoolPath(t *testing.T) {
	store := NewMemoryStore()
	repo := eventRepository(t, store, "apt-repo", FormatAPT, RepositoryTypeHosted)
	first := aptMemorySnapshot(t, store, repo, 1, "widget")
	var want []string
	for path, digest := range first {
		want = append(want, eventKey(repo.ID, path, digest))
	}
	requirePublished(t, store, want...)
	both := aptMemorySnapshot(t, store, repo, 2, "widget", "gadget")
	for path, digest := range both {
		if _, seen := first[path]; !seen {
			want = append(want, eventKey(repo.ID, path, digest))
		}
	}
	requirePublished(t, store, want...)
	for _, actor := range publishedActors(t, store) {
		if actor != "release-operator" {
			t.Fatalf("actors = %v", publishedActors(t, store))
		}
	}
	// The reconciler finds every pool path announced.
	if written, err := store.ReconcilePublishedEvents(context.Background(), time.Now().Add(-time.Hour), 100); err != nil || written != 0 {
		t.Fatalf("reconcile wrote %d (%v)", written, err)
	}
}

// The repair path covers the six formats: with the events gone it writes one per visible hosted publication, once, and a
// proxy cache write is not a publication.
func TestMemoryReconcilePublishedEventsCoversTheNativeFormats(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	eventRepository(t, store, "npm-repo", FormatNPM, RepositoryTypeHosted)
	eventRepository(t, store, "npm-proxy", FormatNPM, RepositoryTypeProxy)
	eventRepository(t, store, "pypi-repo", FormatPyPI, RepositoryTypeHosted)
	eventRepository(t, store, "go-repo", FormatGo, RepositoryTypeHosted)
	eventRepository(t, store, "cargo-repo", FormatCargo, RepositoryTypeHosted)
	eventRepository(t, store, "conan-repo", FormatConan, RepositoryTypeHosted)
	apt := eventRepository(t, store, "apt-repo", FormatAPT, RepositoryTypeHosted)

	npm := eventNPMVersion("npm-repo", "widget", "1.0.0", 10)
	if _, err := store.PublishNPMVersion(ctx, npm, nil); err != nil {
		t.Fatal(err)
	}
	cached := eventNPMVersion("npm-proxy", "left-pad", "1.0.0", 10)
	cached.State, cached.CreatedAt = "visible", time.Now().UTC()
	store.mu.Lock()
	store.npmVersions[npmVersionKey(cached.RepositoryID, cached.PackageName, cached.Version)] = cached
	store.mu.Unlock()
	pypi := PyPIFile{RepositoryID: "pypi-repo", Project: "widget", Version: "1.0", Filename: "widget-1.0.whl", Digest: eventDigest("wheel"), ObjectKey: "pypi/wheel", Size: 10, Publisher: "ci"}
	if _, err := store.PublishPyPIFile(ctx, pypi); err != nil {
		t.Fatal(err)
	}
	goModule := eventGoPublication("go-repo", "example.com/widget", "v1.0.0", 10)
	if _, _, err := store.PublishGoModule(ctx, goModule); err != nil {
		t.Fatal(err)
	}
	claim := cargoTestClaim("cargo-repo", "1.0.0", "a")
	reservation, _, err := store.ReserveCargoIdentity(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CommitCargoPublication(ctx, cargoTestPublication(t, claim, reservation, 7)); err != nil {
		t.Fatal(err)
	}
	recipe := ConanRecipeRevision{RepositoryID: "conan-repo", Reference: "pkg/1.0@user/stable", Revision: "rrev1", Digest: eventDigest("recipe")}
	if _, err = store.PutConanRecipeRevision(ctx, recipe, eventConanAssets(t, store, "conan-repo", recipe.Reference, "rrev1", "", "")); err != nil {
		t.Fatal(err)
	}
	pool := aptMemorySnapshot(t, store, apt, 1, "widget")

	want := publishedKeys(store)
	if len(want) != 5+len(pool) {
		t.Fatalf("publishing wrote %d events: %v", len(want), want)
	}
	store.mu.Lock()
	clear(store.webhookEvents)
	store.mu.Unlock()
	written, err := store.ReconcilePublishedEvents(ctx, time.Now().Add(-time.Hour), 100)
	if err != nil || written != len(want) {
		t.Fatalf("reconcile wrote %d (%v), want %d", written, err, len(want))
	}
	got := publishedKeys(store)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reconciled keys = %v, want %v", got, want)
		}
	}
	if again, err := store.ReconcilePublishedEvents(ctx, time.Now().Add(-time.Hour), 100); err != nil || again != 0 {
		t.Fatalf("a second reconcile wrote %d (%v)", again, err)
	}
	for _, key := range got {
		if strings.Contains(key, "npm-proxy") {
			t.Fatalf("a proxy cache write was announced: %s", key)
		}
	}
}
