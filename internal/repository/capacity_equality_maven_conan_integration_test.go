//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// mavenFile is one file of a published coordinate.
type mavenFile struct {
	name string
	size int64
}

// mavenCounterPublish publishes one coordinate with the given files through a publish session
// and returns the artifact with the assets it committed.
func mavenCounterPublish(ctx context.Context, store *PostgresStore, repo HostedRepository, coordinate string, files ...mavenFile) (MavenArtifact, []MavenAsset, error) {
	session := MavenPublishSession{ID: uuid.NewString(), RepositoryID: repo.ID, Coordinate: coordinate, Publisher: "counter", PomObject: files[0].name, State: "open", ExpiresAt: time.Now().Add(time.Hour)}
	assets := make([]MavenAsset, 0, len(files))
	for _, file := range files {
		digest := uniqueDigest()
		session.Objects = append(session.Objects, MavenDeclaredObject{Name: file.name, Digest: digest, Size: file.size})
		assets = append(assets, MavenAsset{RepositoryID: repo.ID, Path: mavenArtifactPathPrefix(coordinate) + file.name, ObjectKey: "native/maven/sha256/counter-" + uuid.NewString(), Digest: digest, Size: file.size})
	}
	if _, err := store.CreateMavenPublishSession(ctx, session); err != nil {
		return MavenArtifact{}, nil, err
	}
	for index, file := range files {
		if err := store.MarkMavenPublishObject(ctx, session.ID, file.name, assets[index].ObjectKey); err != nil {
			return MavenArtifact{}, nil, err
		}
	}
	artifact, err := store.CommitMavenPublishSession(ctx, session.ID, assets)
	return artifact, assets, err
}

func mavenCounterEquality(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, check func(string)) {
	publish := func(coordinate string, files ...mavenFile) (MavenArtifact, []MavenAsset) {
		t.Helper()
		artifact, assets, err := mavenCounterPublish(ctx, store, repo, coordinate, files...)
		if err != nil {
			t.Fatalf("publish %s: %v", coordinate, err)
		}
		return artifact, assets
	}
	widget, _ := publish("org.example:widget:1.0", mavenFile{"widget-1.0.jar", 10}, mavenFile{"widget-1.0.pom", 5})
	check("publish widget")
	gadget, gadgetAssets := publish("org.example:gadget:2.0", mavenFile{"gadget-2.0.jar", 7})
	check("publish gadget")
	if _, err := store.TombstoneMavenArtifact(ctx, repo.ID, widget.ID); err != nil {
		t.Fatal(err)
	}
	check("tombstone widget")
	if _, err := store.TombstoneMavenArtifact(ctx, repo.ID, widget.ID); err != nil {
		t.Fatal(err)
	}
	check("tombstone widget again")
	if _, err := store.RestoreMavenArtifact(ctx, repo.ID, widget.ID); err != nil {
		t.Fatal(err)
	}
	check("restore widget")

	// SNAPSHOT builds share the version directory: the usage moves only when the last
	// visible build of the coordinate goes.
	first, _ := publish("org.example:snap:1.0-SNAPSHOT", mavenFile{"snap-1.0-SNAPSHOT.jar", 11})
	check("publish snapshot build 1")
	second, _ := publish("org.example:snap:1.0-SNAPSHOT", mavenFile{"snap-1.0-SNAPSHOT.jar", 13})
	check("publish snapshot build 2")
	for _, step := range []struct {
		name string
		run  func() error
	}{
		{"tombstone build 1 (build 2 keeps the directory visible)", func() error { _, err := store.TombstoneMavenArtifact(ctx, repo.ID, first.ID); return err }},
		{"tombstone build 2 (last visible build)", func() error { _, err := store.TombstoneMavenArtifact(ctx, repo.ID, second.ID); return err }},
		{"restore build 2", func() error { _, err := store.RestoreMavenArtifact(ctx, repo.ID, second.ID); return err }},
		{"restore build 1", func() error { _, err := store.RestoreMavenArtifact(ctx, repo.ID, first.ID); return err }},
	} {
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		check(step.name)
	}

	// Promotion and replication add the coordinate to another repository, whose counter
	// moves with it and whose own equality must hold.
	target := counterTestRepository(t, store, FormatMaven)
	if _, err := store.PromoteMavenArtifact(ctx, MavenPromotion{ID: uuid.NewString(), SourceRepositoryID: repo.ID, TargetRepositoryID: target.ID, Coordinate: widget.Coordinate, Digest: widget.Digest}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	check("source after promotion")
	assertCounterEqualsUsage(t, store, target, "target after promotion")
	replicated := MavenReplication{ID: uuid.NewString(), SourceRepositoryID: repo.ID, TargetRepositoryID: target.ID, Coordinate: gadget.Coordinate, Digest: gadget.Digest}
	for _, asset := range gadgetAssets {
		replicated.Assets = append(replicated.Assets, MavenReplicationAsset{Path: asset.Path, SourceObjectKey: asset.ObjectKey, ObjectKey: "native/maven/sha256/counter-" + uuid.NewString(), Digest: asset.Digest, Size: asset.Size})
	}
	if _, err := store.PublishReplicatedMavenArtifact(ctx, replicated); err != nil {
		t.Fatalf("replicate: %v", err)
	}
	check("source after replication")
	assertCounterEqualsUsage(t, store, target, "target after replication")
	if _, err := store.TombstoneMavenArtifact(ctx, target.ID, replicated.ID); err != nil {
		t.Fatal(err)
	}
	assertCounterEqualsUsage(t, store, target, "tombstone the replicated artifact")

	var used int64
	if err := store.db.QueryRow(`SELECT bytes FROM repository_usage_counters WHERE repository_id=$1::uuid`, repo.ID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, used); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mavenCounterPublish(ctx, store, repo, "org.example:big:1.0", mavenFile{"big-1.0.jar", 1}); !IsQuotaExceeded(err) {
		t.Fatalf("publish over quota = %v, want quota exceeded", err)
	}
	check("rejected publish over quota")
}

func mavenCounterEdge(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, size int64, workers int) (accepted, rejected int64) {
	var acc, rej atomic.Int64
	var wg sync.WaitGroup
	for index := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			coordinate := fmt.Sprintf("org.example:edge%d:1.0", index)
			switch _, _, err := mavenCounterPublish(ctx, store, repo, coordinate, mavenFile{fmt.Sprintf("edge%d-1.0.jar", index), size}); {
			case err == nil:
				acc.Add(1)
			case IsQuotaExceeded(err):
				rej.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	return acc.Load(), rej.Load()
}

// conanCounterAssets stages one object per size and returns the assets of a recipe revision
// (packageID empty) or of a package revision.
func conanCounterAssets(ctx context.Context, store *PostgresStore, repo HostedRepository, reference, recipeRevision, packageID, packageRevision string, sizes ...int64) ([]ConanAsset, error) {
	assets := make([]ConanAsset, 0, len(sizes))
	for index, size := range sizes {
		asset := ConanAsset{RepositoryID: repo.ID, Reference: reference, RecipeRevision: recipeRevision, PackageID: packageID, PackageRevision: packageRevision, Path: fmt.Sprintf("file%d", index), ObjectKey: "native/conan/counter/" + uuid.NewString(), Digest: uniqueDigest(), Size: size}
		if err := store.StageConanObject(ctx, ConanObjectIntent{RepositoryID: repo.ID, ObjectKey: asset.ObjectKey, Digest: asset.Digest, Size: asset.Size}); err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	return assets, nil
}

func conanCounterRecipe(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, reference, revision string, sizes ...int64) (ConanRecipeRevision, []ConanAsset, error) {
	assets, err := conanCounterAssets(ctx, store, repo, reference, revision, "", "", sizes...)
	if err != nil {
		return ConanRecipeRevision{}, nil, err
	}
	recipe, err := store.PutConanRecipeRevision(ctx, ConanRecipeRevision{RepositoryID: repo.ID, Reference: reference, Revision: revision, Digest: uniqueDigest()}, assets)
	return recipe, assets, err
}

func conanCounterEquality(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, check func(string)) {
	const reference, other = "pkg/1.0/user/stable", "lib/2.0/user/stable"
	recipe, recipeAssets, err := conanCounterRecipe(t, ctx, store, repo, reference, "rrev1", 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	check("publish recipe revision")
	if _, err = store.PutConanRecipeRevision(ctx, recipe, recipeAssets); err != nil {
		t.Fatalf("replay recipe: %v", err)
	}
	check("replay of the recipe revision")
	putPackage := func(packageID, revision string, sizes ...int64) {
		t.Helper()
		assets, err := conanCounterAssets(ctx, store, repo, reference, recipe.Revision, packageID, revision, sizes...)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.PutConanPackageRevision(ctx, ConanPackageRevision{RepositoryID: repo.ID, Reference: reference, RecipeRevision: recipe.Revision, PackageID: packageID, Revision: revision, Digest: uniqueDigest()}, assets); err != nil {
			t.Fatalf("publish package %s: %v", packageID, err)
		}
	}
	putPackage("pkga", "prev1", 100)
	check("publish package a")
	putPackage("pkgb", "prev1", 50, 25)
	check("publish package b")
	if _, err = store.TombstoneConanPackageRevision(ctx, repo.ID, reference, recipe.Revision, "pkga", "prev1"); err != nil {
		t.Fatal(err)
	}
	check("tombstone package a")
	if _, err = store.RestoreConanPackageRevision(ctx, repo.ID, reference, recipe.Revision, "pkga", "prev1"); err != nil {
		t.Fatal(err)
	}
	check("restore package a")
	if _, err = store.TombstoneConanRecipeRevision(ctx, repo.ID, reference, recipe.Revision); err != nil {
		t.Fatal(err)
	}
	check("tombstone the recipe revision with its packages")
	if _, err = store.RestoreConanRecipeRevision(ctx, repo.ID, reference, recipe.Revision); err != nil {
		t.Fatal(err)
	}
	check("restore the recipe revision with its packages")

	otherRecipe, _, err := conanCounterRecipe(t, ctx, store, repo, other, "rrev1", 7)
	if err != nil {
		t.Fatal(err)
	}
	check("publish a second reference")

	target := counterTestRepository(t, store, FormatConan)
	if _, err = store.PromoteConanRecipeRevision(ctx, ConanPromotion{SourceRepositoryID: repo.ID, TargetRepositoryID: target.ID, Reference: reference, Revision: recipe.Revision, Digest: recipe.Digest}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	check("source after promotion")
	assertCounterEqualsUsage(t, store, target, "target after promotion")
	sourceAssets, err := store.ListConanRecipeAssets(ctx, repo.ID, other, otherRecipe.Revision)
	if err != nil {
		t.Fatal(err)
	}
	targetRecipe := otherRecipe
	targetRecipe.RepositoryID = target.ID
	targetAssets := append([]ConanAsset(nil), sourceAssets...)
	for index := range targetAssets {
		targetAssets[index].RepositoryID, targetAssets[index].ObjectKey = target.ID, "native/conan/counter/"+uuid.NewString()
	}
	if _, err = store.PublishReplicatedConanRevision(ctx, ConanReplicationPublication{SourceRepositoryID: repo.ID, Recipe: targetRecipe, SourceAssets: sourceAssets, TargetAssets: targetAssets}); err != nil {
		t.Fatalf("replicate: %v", err)
	}
	check("source after replication")
	assertCounterEqualsUsage(t, store, target, "target after replication")
	if _, err = store.TombstoneConanRecipeRevision(ctx, target.ID, reference, recipe.Revision); err != nil {
		t.Fatal(err)
	}
	assertCounterEqualsUsage(t, store, target, "tombstone the promoted revision")

	var used int64
	if err = store.db.QueryRow(`SELECT bytes FROM repository_usage_counters WHERE repository_id=$1::uuid`, repo.ID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, used); err != nil {
		t.Fatal(err)
	}
	if _, _, err = conanCounterRecipe(t, ctx, store, repo, "big/1.0/user/stable", "rrev1", 1); !IsQuotaExceeded(err) {
		t.Fatalf("publish over quota = %v, want quota exceeded", err)
	}
	check("rejected publish over quota")
}

func conanCounterEdge(t *testing.T, ctx context.Context, store *PostgresStore, repo HostedRepository, size int64, workers int) (accepted, rejected int64) {
	var acc, rej atomic.Int64
	var wg sync.WaitGroup
	for index := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch _, _, err := conanCounterRecipe(t, ctx, store, repo, fmt.Sprintf("edge%d/1.0/user/stable", index), "rrev1", size); {
			case err == nil:
				acc.Add(1)
			case IsQuotaExceeded(err):
				rej.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	return acc.Load(), rej.Load()
}
