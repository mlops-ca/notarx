package oci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/google/uuid"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

func TestNativePromotionCopiesManifestAndMountsSourceBlob(t *testing.T) {
	ctx := context.Background()
	store, objects := repository.NewMemoryStore(), objectstore.NewMemoryStore()
	body := []byte("blob")
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if err := objects.Put(ctx, "blob", body); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateOCIUpload(ctx, repository.OCIUpload{ID: "upload", RepositoryID: "source", State: "open", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := store.StageOCIObjectIntent(ctx, repository.OCIObjectIntent{RepositoryID: "source", ObjectKey: "blob", Digest: digest, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteOCIUpload(ctx, "upload", repository.OCIBlob{Digest: digest, ObjectKey: "blob", Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"schemaVersion":2,"config":{"digest":"` + digest + `"}}`)
	manifestSum := sha256.Sum256(manifest)
	manifestDigest := "sha256:" + hex.EncodeToString(manifestSum[:])
	if err := objects.Put(ctx, "source-manifest", manifest); err != nil {
		t.Fatal(err)
	}
	if err := store.StageOCIObjectIntent(ctx, repository.OCIObjectIntent{RepositoryID: "source", ObjectKey: "source-manifest", Digest: manifestDigest, Size: int64(len(manifest))}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOCIManifest(ctx, repository.OCIManifest{RepositoryID: "source", Name: "team/widget", Digest: manifestDigest, ObjectKey: "source-manifest", Size: int64(len(manifest))}, manifestDigest); err != nil {
		t.Fatal(err)
	}
	worker := NativePromotion{Store: store, Objects: objects}
	if _, _, err := worker.Enqueue(ctx, "target", "promotion", PromotionPayload{SourceRepositoryID: "source", Name: "team/widget", Digest: manifestDigest}); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunJobs(ctx, 1); err != nil {
		t.Fatal(err)
	}
	target, err := store.GetOCIManifest(ctx, "target", "team/widget", manifestDigest)
	if err != nil || target.ObjectKey == "source-manifest" {
		t.Fatalf("target=%#v err=%v", target, err)
	}
	if _, err = store.GetOCIBlob(ctx, "target", digest); err != nil {
		t.Fatalf("mounted blob: %v", err)
	}
}

func TestManifestBlobDigestsAreTheConfigAndLayersNotTheChildManifestsOfAnIndex(t *testing.T) {
	image := newImportImage()
	if got := manifestBlobDigests(image.files[image.index]); len(got) != 0 {
		t.Fatalf("an index mounts no blob: %v", got)
	}
	if got := manifestBlobDigests(image.files[image.child]); len(got) != 2 || got[0] != image.config || got[1] != image.layer {
		t.Fatalf("a manifest mounts its config and layers: %v", got)
	}
	if got := manifestBlobDigests([]byte("not json")); got != nil {
		t.Fatalf("got %v", got)
	}
}

func TestPromotingAnIndexPublishesItsChildManifestsFirst(t *testing.T) {
	ctx := context.Background()
	store, objects, importer, source := importFixture(t, repository.FormatOCI)
	target, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "prod", Format: repository.FormatOCI})
	if err != nil {
		t.Fatal(err)
	}
	image := newImportImage()
	if err = importer.ImportUnit(ctx, source.ID, image.unit(`{"referrers":["`+image.signature+`"]}`), image.open); err != nil {
		t.Fatal(err)
	}
	worker := NativePromotion{Store: store, Objects: objects}
	if _, _, err = worker.Enqueue(ctx, target.ID, "promote-index", PromotionPayload{SourceRepositoryID: source.ID, Name: "team/app", Digest: image.index}); err != nil {
		t.Fatal(err)
	}
	if err = worker.RunJobs(ctx, 1); err != nil {
		t.Fatal(err)
	}
	for _, digest := range []string{image.index, image.child} {
		if _, err = store.GetOCIManifest(ctx, target.ID, "team/app", digest); err != nil {
			t.Fatalf("%s is missing at the target: %v", digest, err)
		}
	}
	for _, digest := range []string{image.config, image.layer} {
		if _, err = store.GetOCIBlob(ctx, target.ID, digest); err != nil {
			t.Fatalf("blob %s is missing at the target: %v", digest, err)
		}
	}
}
