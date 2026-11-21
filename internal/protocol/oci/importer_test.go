package oci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/google/uuid"
	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// importImage is a multi-arch image: an index with one child manifest, whose config and layer are blobs, and a signature
// manifest (a referrer of the index) with its own config blob.
type importImage struct {
	files                                             map[string][]byte
	index, child, config, layer, signature, sigConfig string
}

func newImportImage() importImage {
	img := importImage{files: map[string][]byte{}}
	add := func(content string) string {
		digest := digestOf([]byte(content))
		img.files[digest] = []byte(content)
		return digest
	}
	img.config = add("{\"architecture\":\"amd64\"}")
	img.layer = add("layer bytes")
	img.child = add(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"%s","size":%d},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"%s","size":%d}]}`, img.config, len(img.files[img.config]), img.layer, len(img.layer0())))
	img.index = add(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"%s","size":%d}]}`, img.child, len(img.files[img.child])))
	img.sigConfig = add("{}")
	img.signature = add(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","artifactType":"application/vnd.example.signature","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"%s","size":2},"layers":[],"subject":{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"%s","size":10}}`, img.sigConfig, img.index))
	return img
}

func (i importImage) layer0() []byte { return i.files[i.layer] }

func (i importImage) unit(manifest string, without ...string) formatmodule.ExportedUnit {
	skip := map[string]bool{}
	for _, digest := range without {
		skip[digest] = true
	}
	unit := formatmodule.ExportedUnit{Format: repository.FormatOCI, Coordinate: "team/app", Digest: i.index, Manifest: []byte(manifest)}
	for digest, content := range i.files {
		if !skip[digest] {
			unit.Blobs = append(unit.Blobs, formatmodule.BlobDescriptor{Digest: digest, Size: int64(len(content))})
		}
	}
	return unit
}

func (i importImage) open(blob formatmodule.BlobDescriptor) (io.ReadCloser, error) {
	content, ok := i.files[blob.Digest]
	if !ok {
		return nil, objectstore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func importFixture(t *testing.T, format repository.Format) (*repository.MemoryStore, *objectstore.MemoryStore, NativeImporter, repository.HostedRepository) {
	t.Helper()
	store, objects := repository.NewMemoryStore(), objectstore.NewMemoryStore()
	repo, err := store.CreateHostedRepository(context.Background(), repository.HostedRepository{ID: uuid.NewString(), Name: "images", Format: format})
	if err != nil {
		t.Fatal(err)
	}
	return store, objects, NativeImporter{Store: store, Objects: objects}, repo
}

func TestImportUnitPublishesTheImageItsReferrersAndTagsAndReplays(t *testing.T) {
	ctx := context.Background()
	store, objects, importer, repo := importFixture(t, repository.FormatOCI)
	img := newImportImage()
	manifest := `{"tags":["v1","stable"],"referrers":["` + img.signature + `"]}`
	if err := importer.ImportUnit(ctx, repo.ID, img.unit(manifest), img.open); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"v1", "stable"} {
		if got, err := store.GetOCIManifest(ctx, repo.ID, "team/app", tag); err != nil || got.Digest != img.index {
			t.Fatalf("tag %s = %+v %v", tag, got, err)
		}
	}
	child, err := store.GetOCIManifest(ctx, repo.ID, "team/app", img.child)
	if err != nil || child.MediaType != "application/vnd.oci.image.manifest.v1+json" {
		t.Fatalf("child = %+v %v", child, err)
	}
	if stored, getErr := objects.Get(ctx, child.ObjectKey); getErr != nil || !bytes.Equal(stored, img.files[img.child]) {
		t.Fatalf("child object = %q %v", stored, getErr)
	}
	for _, digest := range []string{img.config, img.layer, img.sigConfig} {
		if blob, blobErr := store.GetOCIBlob(ctx, repo.ID, digest); blobErr != nil || blob.Size != int64(len(img.files[digest])) {
			t.Fatalf("blob %s = %+v %v", digest, blob, blobErr)
		}
	}
	referrers, err := store.ListOCIReferrers(ctx, repo.ID, "team/app", img.index, 10, "")
	if err != nil || len(referrers) != 1 || referrers[0].Digest != img.signature || referrers[0].ArtifactType != "application/vnd.example.signature" {
		t.Fatalf("referrers = %+v %v", referrers, err)
	}
	if err = importer.ImportUnit(ctx, repo.ID, img.unit(manifest), img.open); err != nil {
		t.Fatalf("replay = %v", err)
	}
}

func TestImportUnitTagRules(t *testing.T) {
	ctx := context.Background()
	store, _, importer, repo := importFixture(t, repository.FormatOCI)
	first, second := newImportImage(), importImage{files: map[string][]byte{}}
	// A second image with other bytes.
	config := []byte("{\"architecture\":\"arm64\"}")
	second.config = digestOf(config)
	second.files[second.config] = config
	second.child = digestOf([]byte(fmt.Sprintf(`{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"%s","size":%d},"layers":[]}`, second.config, len(config))))
	second.files[second.child] = []byte(fmt.Sprintf(`{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"%s","size":%d},"layers":[]}`, second.config, len(config)))
	second.index = second.child
	if err := importer.ImportUnit(ctx, repo.ID, first.unit(`{"tags":["v1"],"referrers":["`+first.signature+`"]}`), first.open); err != nil {
		t.Fatal(err)
	}
	unit := formatmodule.ExportedUnit{Format: repository.FormatOCI, Coordinate: "team/app", Digest: second.child, Manifest: []byte(`{"tags":["v1"]}`)}
	for digest, content := range second.files {
		unit.Blobs = append(unit.Blobs, formatmodule.BlobDescriptor{Digest: digest, Size: int64(len(content))})
	}
	if err := importer.ImportUnit(ctx, repo.ID, unit, second.open); !errors.Is(err, formatmodule.ErrImportConflict) {
		t.Fatalf("tag conflict = %v", err)
	}
	if _, err := store.GetOCIManifest(ctx, repo.ID, "team/app", second.child); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("a refused import published the manifest: %v", err)
	}
	if got, err := store.GetOCIManifest(ctx, repo.ID, "team/app", "v1"); err != nil || got.Digest != first.index {
		t.Fatalf("tag moved: %+v %v", got, err)
	}
	// The same unit without the contested tag is published, by digest only.
	unit.Manifest = []byte(`{}`)
	if err := importer.ImportUnit(ctx, repo.ID, unit, second.open); err != nil {
		t.Fatal(err)
	}
}

func TestImportUnitRefusesWhatTheBytesContradict(t *testing.T) {
	ctx := context.Background()
	img := newImportImage()
	good := `{"referrers":["` + img.signature + `"]}`
	cases := map[string]struct {
		mutate func(*formatmodule.ExportedUnit)
		open   func(formatmodule.BlobDescriptor) (io.ReadCloser, error)
		want   error
	}{
		"layer bytes differ from the digest": {open: func(blob formatmodule.BlobDescriptor) (io.ReadCloser, error) {
			if blob.Digest == img.layer {
				return io.NopCloser(bytes.NewReader([]byte("LAYER bytes"))), nil
			}
			return img.open(blob)
		}, want: formatmodule.ErrImportMismatch},
		"manifest bytes differ from the digest": {open: func(blob formatmodule.BlobDescriptor) (io.ReadCloser, error) {
			if blob.Digest == img.child {
				return io.NopCloser(bytes.NewReader(bytes.ToUpper(img.files[img.child]))), nil
			}
			return img.open(blob)
		}, want: formatmodule.ErrImportMismatch},
		"layer size differs from the descriptor": {mutate: func(u *formatmodule.ExportedUnit) {
			for index := range u.Blobs {
				if u.Blobs[index].Digest == img.layer {
					u.Blobs[index].Size++
				}
			}
		}, want: formatmodule.ErrImportMismatch},
		"a referenced blob is missing": {mutate: func(u *formatmodule.ExportedUnit) { *u = img.unit(good, img.layer) }, want: formatmodule.ErrImportInvalidUnit},
		"a blob no manifest references": {mutate: func(u *formatmodule.ExportedUnit) {
			extra := []byte("extra")
			u.Blobs = append(u.Blobs, formatmodule.BlobDescriptor{Digest: digestOf(extra), Size: int64(len(extra))})
		}, want: formatmodule.ErrImportInvalidUnit},
		"the coordinate is not an image name": {mutate: func(u *formatmodule.ExportedUnit) { u.Coordinate = "team/app@" + img.index }, want: formatmodule.ErrImportInvalidUnit},
		"the root is not a supplied blob":     {mutate: func(u *formatmodule.ExportedUnit) { u.Digest = digestOf([]byte("other")) }, want: formatmodule.ErrImportInvalidUnit},
		"unknown manifest field":              {mutate: func(u *formatmodule.ExportedUnit) { u.Manifest = []byte(`{"size":1}`) }, want: formatmodule.ErrImportInvalidUnit},
		"a digest as a tag":                   {mutate: func(u *formatmodule.ExportedUnit) { u.Manifest = []byte(`{"tags":["` + img.index + `"]}`) }, want: formatmodule.ErrImportInvalidUnit},
		"a referrer of something else": {mutate: func(u *formatmodule.ExportedUnit) {
			u.Manifest = []byte(`{"referrers":["` + img.config + `"]}`)
		}, want: formatmodule.ErrImportInvalidUnit},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			store, _, importer, repo := importFixture(t, repository.FormatOCI)
			unit, open := img.unit(good), img.open
			if test.mutate != nil {
				test.mutate(&unit)
			}
			if test.open != nil {
				open = test.open
			}
			if err := importer.ImportUnit(ctx, repo.ID, unit, open); !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
			if _, err := store.GetOCIManifest(ctx, repo.ID, "team/app", img.index); !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("a refused unit was published: %v", err)
			}
			if _, err := store.GetOCIBlob(ctx, repo.ID, img.layer); !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("a refused unit left a visible blob: %v", err)
			}
		})
	}
	_, _, importer, repo := importFixture(t, repository.FormatRaw)
	if err := importer.ImportUnit(ctx, repo.ID, img.unit(good), img.open); !errors.Is(err, formatmodule.ErrImportUnsupportedLayout) {
		t.Fatalf("a Raw repository = %v", err)
	}
}

func TestImportUnitReuploadsEveryBlobAndMountsOnlyWhatTheInstanceHolds(t *testing.T) {
	ctx := context.Background()
	store, objects, importer, repo := importFixture(t, repository.FormatOCI)
	other, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "other", Format: repository.FormatOCI})
	if err != nil {
		t.Fatal(err)
	}
	img := newImportImage()
	if err = importer.ImportUnit(ctx, other.ID, img.unit(`{"referrers":["`+img.signature+`"]}`), img.open); err != nil {
		t.Fatal(err)
	}
	if err = importer.ImportUnit(ctx, repo.ID, img.unit(`{"referrers":["`+img.signature+`"]}`), img.open); err != nil {
		t.Fatal(err)
	}
	blob, err := store.GetOCIBlob(ctx, repo.ID, img.layer)
	if err != nil {
		t.Fatal(err)
	}
	if stored, getErr := objects.Get(ctx, blob.ObjectKey); getErr != nil || !bytes.Equal(stored, img.files[img.layer]) {
		t.Fatalf("blob object = %q %v", stored, getErr)
	}
}

func TestReleaseSetIsTheIndexAndItsChildren(t *testing.T) {
	ctx := context.Background()
	store, objects, importer, repo := importFixture(t, repository.FormatOCI)
	img := newImportImage()
	if err := importer.ImportUnit(ctx, repo.ID, img.unit(`{"referrers":["`+img.signature+`"]}`), img.open); err != nil {
		t.Fatal(err)
	}
	releases := ReleaseSet{Store: store, Objects: objects}
	members, err := releases.ReleaseMembers(ctx, repo.ID, "team/app", img.index)
	if err != nil || len(members) != 2 || members[0].Digest != img.index || members[1].Digest != img.child || members[1].Coordinate != "team/app" {
		t.Fatalf("members = %+v %v", members, err)
	}
	if members, err = releases.ReleaseMembers(ctx, repo.ID, "team/app", img.child); err != nil || len(members) != 1 || members[0].Digest != img.child {
		t.Fatalf("a child is its own release: %+v %v", members, err)
	}
	if _, err = releases.ReleaseMembers(ctx, repo.ID, "team/app", digestOf([]byte("unknown"))); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("unknown = %v", err)
	}
	if _, err = releases.ReleaseMembers(ctx, repo.ID, "team/app", "latest"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("not a digest = %v", err)
	}
}

func TestImportUnitNeverMountsABlobOtherRepositoriesHold(t *testing.T) {
	ctx := context.Background()
	store, _, importer, repo := importFixture(t, repository.FormatOCI)
	other, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "other", Format: repository.FormatOCI})
	if err != nil {
		t.Fatal(err)
	}
	img := newImportImage()
	manifest := `{"referrers":["` + img.signature + `"]}`
	if err = importer.ImportUnit(ctx, other.ID, img.unit(manifest), img.open); err != nil {
		t.Fatal(err)
	}
	// A sender that does not hold the layer cannot publish it by naming its digest: its bytes are read and hashed.
	forged := func(blob formatmodule.BlobDescriptor) (io.ReadCloser, error) {
		if blob.Digest == img.layer {
			return io.NopCloser(bytes.NewReader([]byte("not the layer"))), nil
		}
		return img.open(blob)
	}
	if err = importer.ImportUnit(ctx, repo.ID, img.unit(manifest), forged); !errors.Is(err, formatmodule.ErrImportMismatch) {
		t.Fatalf("err = %v", err)
	}
	if _, err = store.GetOCIBlob(ctx, repo.ID, img.layer); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("the blob of another repository was mounted: %v", err)
	}
}

func TestImportUnitReadsForeignLayersAndNestedReferrersFromTheManifests(t *testing.T) {
	ctx := context.Background()
	_, _, importer, repo := importFixture(t, repository.FormatOCI)
	files := map[string][]byte{}
	add := func(content string) string {
		digest := digestOf([]byte(content))
		files[digest] = []byte(content)
		return digest
	}
	config := add("{}")
	foreign := digestOf([]byte("foreign layer"))
	image := add(fmt.Sprintf(`{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"%s","size":2},"layers":[{"mediaType":"application/vnd.oci.image.layer.nondistributable.v1.tar+gzip","digest":"%s","size":13,"urls":["https://example.invalid/layer"]}]}`, config, foreign))
	// Two referrers, the first an index that lists the second as a child.
	leaf := add(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","artifactType":"application/vnd.example.sbom","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"%s","size":2},"layers":[],"subject":{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"%s","size":1}}`, config, image))
	wrapper := add(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","artifactType":"application/vnd.example.bundle","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"%s","size":%d}],"subject":{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"%s","size":1}}`, leaf, len(files[leaf]), image))
	unit := formatmodule.ExportedUnit{Format: repository.FormatOCI, Coordinate: "team/app", Digest: image, Manifest: []byte(`{"referrers":["` + wrapper + `","` + leaf + `"]}`)}
	for digest, content := range files {
		unit.Blobs = append(unit.Blobs, formatmodule.BlobDescriptor{Digest: digest, Size: int64(len(content))})
	}
	open := func(blob formatmodule.BlobDescriptor) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(files[blob.Digest])), nil
	}
	if err := importer.ImportUnit(ctx, repo.ID, unit, open); err != nil {
		t.Fatal(err)
	}
}
