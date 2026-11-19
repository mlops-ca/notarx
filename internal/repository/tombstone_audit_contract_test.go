package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// tombstoneAuditStore is what the tombstone contract needs from a store; the memory and
// PostgreSQL stores both satisfy it.
type tombstoneAuditStore interface {
	HostedRepositoryStore
	ArtifactTombstoneStore
	NativeNPMStore
	NativePyPIStore
	NativeGoStore
	NativeCargoStore
	NativeConanStore
	NativeOCIStore
	NativeMavenStore
	ListAudits(context.Context, AuditQuery) ([]AuditRecord, error)
}

func TestMemoryTombstonesRollBackWithTheirAudit(t *testing.T) {
	store := NewMemoryStore()
	runTombstoneAuditContract(t, store, func(_, actor string) {
		store.SetAuditFault(func(record AuditRecord) error {
			if record.Actor == actor {
				return errors.New("audit store unavailable")
			}
			return nil
		})
	}, func() { store.SetAuditFault(nil) })
}

// runTombstoneAuditContract proves ADR 0023 section 1 for every tombstone store method a
// management handler calls: with the audit insert failing the artifact stays visible and no
// tombstone is written; with it working the tombstone and its audit commit together.
// failAudits must make every audit insert by the actor on the repository fail until
// clearFaults runs.
func runTombstoneAuditContract(t *testing.T, store tombstoneAuditStore, failAudits func(repositoryName, actor string), clearFaults func()) {
	t.Helper()
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	hex := func(seed string) string { return strings.Repeat(seed, 64)[:64] }
	type target struct {
		name       string
		format     Format
		coordinate string
		tombstone  func(audit ...AuditRecord) error
	}
	var targets []target
	newRepo := func(format Format) HostedRepository {
		t.Helper()
		repo, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "tomb-" + string(format) + "-" + suffix, Format: format})
		if err != nil {
			t.Fatal(err)
		}
		return repo
	}
	repos := map[string]HostedRepository{}
	add := func(name string, repo HostedRepository, coordinate string, tombstone func(audit ...AuditRecord) error) {
		repos[name] = repo
		targets = append(targets, target{name: name, format: repo.Format, coordinate: coordinate, tombstone: tombstone})
	}

	npm := newRepo(FormatNPM)
	npmVersion := NPMVersion{
		RepositoryID: npm.ID, PackageName: "@scope/widget", Version: "1.2.3",
		Digest:    "sha256:" + hex("a"),
		Integrity: "sha512-YQ==", Shasum: strings.Repeat("b", 40),
		TarballName: "scope-widget-1.2.3.tgz", ObjectKey: "native/npm/sha256/" + suffix, Size: 8,
		Manifest: json.RawMessage(`{"name":"@scope/widget","version":"1.2.3"}`), Publisher: "release-bot",
	}
	if _, err := store.PublishNPMVersion(ctx, npmVersion, map[string]string{"latest": npmVersion.Version}); err != nil {
		t.Fatal(err)
	}
	add("npm", npm, "@scope/widget@1.2.3", func(audit ...AuditRecord) error {
		_, err := store.TombstoneNPMVersion(ctx, npm.ID, "@scope/widget", "1.2.3", audit...)
		return err
	})

	pypi := newRepo(FormatPyPI)
	if _, err := store.PublishPyPIFile(ctx, PyPIFile{RepositoryID: pypi.ID, Project: "widget", Version: "1.0", Filename: "widget-1.0.tar.gz", Digest: "sha256:" + hex("c"), ObjectKey: "native/pypi/" + suffix, Size: 42}); err != nil {
		t.Fatal(err)
	}
	add("pypi", pypi, "widget@1.0", func(audit ...AuditRecord) error {
		_, err := store.TombstonePyPIVersion(ctx, pypi.ID, "widget", "1.0", audit...)
		return err
	})

	goRepo := newRepo(FormatGo)
	const modulePath, goVersion = "example.com/team/widget", "v1.2.3"
	goAssets := []GoModuleAsset{
		{RepositoryID: goRepo.ID, Module: modulePath, Version: goVersion, Kind: "info", Digest: "sha256:" + hex("d"), ObjectKey: "native/go/info/" + suffix, Size: 1},
		{RepositoryID: goRepo.ID, Module: modulePath, Version: goVersion, Kind: "mod", Digest: "sha256:" + hex("e"), ObjectKey: "native/go/mod/" + suffix, Size: 2},
		{RepositoryID: goRepo.ID, Module: modulePath, Version: goVersion, Kind: "zip", Digest: "sha256:" + hex("f"), ObjectKey: "native/go/zip/" + suffix, Size: 3},
	}
	if _, _, err := store.PublishGoModule(ctx, GoModulePublication{
		Version: GoModuleVersion{RepositoryID: goRepo.ID, Module: modulePath, Version: goVersion, Publisher: "builder", PublishedAt: time.Now().UTC()},
		Assets:  goAssets,
	}); err != nil {
		t.Fatal(err)
	}
	add("go", goRepo, goModuleCoordinate(modulePath, goVersion), func(audit ...AuditRecord) error {
		_, err := store.TombstoneGoModuleVersion(ctx, goRepo.ID, modulePath, goVersion, audit...)
		return err
	})

	cargoRepo := newRepo(FormatCargo)
	claim := cargoTestClaim(cargoRepo.ID, "1.0.0", "a")
	reservation, _, err := store.ReserveCargoIdentity(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CommitCargoPublication(ctx, cargoTestPublication(t, claim, reservation, 7)); err != nil {
		t.Fatal(err)
	}
	add("cargo", cargoRepo, cargoPublicationCoordinate(claim.Name, claim.Version), func(audit ...AuditRecord) error {
		_, err := store.TombstoneCargoPublication(ctx, cargoRepo.ID, claim.Name, claim.Version, audit...)
		return err
	})

	conan := newRepo(FormatConan)
	recipeObject := ConanObjectIntent{RepositoryID: conan.ID, ObjectKey: "native/conan/objects/recipe-" + suffix, Digest: "sha256:" + hex("1"), Size: 3}
	if err = store.StageConanObject(ctx, recipeObject); err != nil {
		t.Fatal(err)
	}
	recipe := ConanRecipeRevision{RepositoryID: conan.ID, Reference: "pkg/1.0/user/stable", Revision: "rrev", Digest: recipeObject.Digest}
	if _, err = store.PutConanRecipeRevision(ctx, recipe, []ConanAsset{{RepositoryID: conan.ID, Reference: recipe.Reference, RecipeRevision: recipe.Revision, Path: "conanfile.py", ObjectKey: recipeObject.ObjectKey, Digest: recipeObject.Digest, Size: recipeObject.Size}}); err != nil {
		t.Fatal(err)
	}
	packageObject := ConanObjectIntent{RepositoryID: conan.ID, ObjectKey: "native/conan/objects/package-" + suffix, Digest: "sha256:" + hex("2"), Size: 7}
	if err = store.StageConanObject(ctx, packageObject); err != nil {
		t.Fatal(err)
	}
	pkg := ConanPackageRevision{RepositoryID: conan.ID, Reference: recipe.Reference, RecipeRevision: recipe.Revision, PackageID: "package-id", Revision: "prev", Digest: packageObject.Digest}
	if _, err = store.PutConanPackageRevision(ctx, pkg, []ConanAsset{{RepositoryID: conan.ID, Reference: recipe.Reference, RecipeRevision: recipe.Revision, PackageID: pkg.PackageID, PackageRevision: pkg.Revision, Path: "package.tgz", ObjectKey: packageObject.ObjectKey, Digest: packageObject.Digest, Size: packageObject.Size}}); err != nil {
		t.Fatal(err)
	}
	add("conan package revision", conan, recipe.Reference+"#"+recipe.Revision+"/"+pkg.PackageID+"#"+pkg.Revision, func(audit ...AuditRecord) error {
		_, err := store.TombstoneConanPackageRevision(ctx, conan.ID, recipe.Reference, recipe.Revision, pkg.PackageID, pkg.Revision, audit...)
		return err
	})
	add("conan recipe revision", conan, recipe.Reference+"#"+recipe.Revision, func(audit ...AuditRecord) error {
		_, err := store.TombstoneConanRecipeRevision(ctx, conan.ID, recipe.Reference, recipe.Revision, audit...)
		return err
	})

	oci := newRepo(FormatOCI)
	manifestDigest := "sha256:" + hex("3")
	manifestKey := "native/oci/manifests/" + suffix
	if err = store.StageOCIObjectIntent(ctx, OCIObjectIntent{RepositoryID: oci.ID, ObjectKey: manifestKey, Digest: manifestDigest, Size: 42}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.PutOCIManifest(ctx, OCIManifest{RepositoryID: oci.ID, Name: "widget", Digest: manifestDigest, ObjectKey: manifestKey, MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 42}, "latest"); err != nil {
		t.Fatal(err)
	}
	add("oci", oci, "widget@"+manifestDigest, func(audit ...AuditRecord) error {
		return store.DeleteOCIManifest(ctx, oci.ID, "widget", manifestDigest, audit...)
	})

	maven := newRepo(FormatMaven)
	session := MavenPublishSession{ID: uuid.NewString(), RepositoryID: maven.ID, Coordinate: "org.example:widget:1.0.0", Publisher: "release-bot", PomObject: "widget-1.0.0.jar", State: "open", ExpiresAt: time.Now().Add(time.Hour), Objects: []MavenDeclaredObject{{Name: "widget-1.0.0.jar", Digest: "sha256:" + hex("4"), Size: 1}}}
	if _, err = store.CreateMavenPublishSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	mavenKey := "native/maven/sha256/" + suffix
	if err = store.MarkMavenPublishObject(ctx, session.ID, session.PomObject, mavenKey); err != nil {
		t.Fatal(err)
	}
	artifact, err := store.CommitMavenPublishSession(ctx, session.ID, []MavenAsset{{RepositoryID: maven.ID, Path: "org/example/widget/1.0.0/widget-1.0.0.jar", ObjectKey: mavenKey, Digest: session.Objects[0].Digest, Size: 1}})
	if err != nil {
		t.Fatal(err)
	}
	add("maven", maven, artifact.Coordinate, func(audit ...AuditRecord) error {
		_, err := store.TombstoneMavenArtifact(ctx, maven.ID, artifact.ID, audit...)
		return err
	})

	actor := "tombstone-" + suffix
	record := func(name string) AuditRecord {
		return AuditRecord{Repository: repos[name].Name, GroupName: repos[name].Name, Actor: actor, Outcome: AuditResolved, OccurredAt: time.Now().UTC(), Format: "management", Resource: name, Operation: "artifact.tombstone", Status: 204, CacheDisposition: "bypass"}
	}
	failed := map[string]bool{}
	for _, repo := range repos {
		if !failed[repo.ID] {
			failed[repo.ID] = true
			failAudits(repo.Name, actor)
		}
	}
	for _, c := range targets {
		t.Run(c.name+" rolls back", func(t *testing.T) {
			if err := c.tombstone(record(c.name)); err == nil {
				t.Fatal("the tombstone succeeded although its audit could not be written")
			}
			if _, err := store.GetArtifactTombstone(ctx, repos[c.name].ID, c.format, c.coordinate); !errors.Is(err, ErrNotFound) {
				t.Fatalf("a failed audit left a tombstone: %v", err)
			}
		})
	}
	clearFaults()
	for _, c := range targets {
		t.Run(c.name+" commits with its audit", func(t *testing.T) {
			if err := c.tombstone(record(c.name)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.GetArtifactTombstone(ctx, repos[c.name].ID, c.format, c.coordinate); err != nil {
				t.Fatalf("tombstone missing: %v", err)
			}
			audits, err := store.ListAudits(ctx, AuditQuery{Actor: actor, Repository: repos[c.name].Name, Operation: "artifact.tombstone", Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, audit := range audits {
				found = found || audit.Resource == c.name
			}
			if !found {
				t.Fatalf("audit missing: %+v", audits)
			}
		})
	}
}
