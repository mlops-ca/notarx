package raw_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"
	formatmodule "github.com/mlops-ca/notarx/internal/format"
	rawformat "github.com/mlops-ca/notarx/internal/format/raw"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

func sha(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func importFixture(t *testing.T) (*repository.MemoryStore, *objectstore.MemoryStore, *rawformat.Module, repository.HostedRepository) {
	t.Helper()
	store, objects := repository.NewMemoryStore(), objectstore.NewMemoryStore()
	repo, err := store.CreateHostedRepository(context.Background(), repository.HostedRepository{ID: uuid.NewString(), Name: "imports", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	return store, objects, rawformat.New(rawformat.Options{Store: store, Repositories: store, Objects: objects}), repo
}

func unitOf(path, content string) formatmodule.ExportedUnit {
	digest := sha(content)
	return formatmodule.ExportedUnit{Format: repository.FormatRaw, Coordinate: path, Digest: digest, Manifest: []byte(`{"path":"` + path + `"}`),
		Blobs: []formatmodule.BlobDescriptor{{Digest: digest, Size: int64(len(content))}}}
}

func opener(content string) func(formatmodule.BlobDescriptor) (io.ReadCloser, error) {
	return func(formatmodule.BlobDescriptor) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte(content))), nil
	}
}

func TestImportUnitPublishesVerifiedBytesAndRefusesWhatTheyContradict(t *testing.T) {
	ctx := context.Background()
	store, objects, module, repo := importFixture(t)
	if _, ok := any(module).(formatmodule.Importer); !ok {
		t.Fatal("the Raw module is not an Importer")
	}
	if err := module.ImportUnit(ctx, repo.ID, unitOf("a/b.txt", "hello"), opener("hello")); err != nil {
		t.Fatal(err)
	}
	asset, err := store.GetRawAsset(ctx, repo.ID, "a/b.txt")
	if err != nil || asset.Digest != sha("hello") || asset.Size != 5 || asset.ContentType != "application/octet-stream" {
		t.Fatalf("asset = %+v %v", asset, err)
	}
	if stored, getErr := objects.Get(ctx, asset.ObjectKey); getErr != nil || string(stored) != "hello" {
		t.Fatalf("object = %q %v", stored, getErr)
	}
	// A replay of the same bytes publishes nothing new; other bytes at the path are a conflict.
	if err = module.ImportUnit(ctx, repo.ID, unitOf("a/b.txt", "hello"), opener("hello")); err != nil {
		t.Fatalf("replay = %v", err)
	}
	if err = module.ImportUnit(ctx, repo.ID, unitOf("a/b.txt", "world"), opener("world")); !errors.Is(err, formatmodule.ErrImportConflict) {
		t.Fatalf("conflict = %v", err)
	}
	// The bytes the sender delivers are hashed: other bytes, or other length, publish nothing.
	for name, content := range map[string]string{"c.txt": "HELLO", "d.txt": "hello!"} {
		unit := unitOf(name, "hello")
		if err = module.ImportUnit(ctx, repo.ID, unit, opener(content)); !errors.Is(err, formatmodule.ErrImportMismatch) {
			t.Fatalf("%s = %v", name, err)
		}
		if _, getErr := store.GetRawAsset(ctx, repo.ID, name); !errors.Is(getErr, repository.ErrNotFound) {
			t.Fatalf("%s was published: %v", name, getErr)
		}
	}
	bad := map[string]func(*formatmodule.ExportedUnit){
		"coordinate differs from the manifest": func(u *formatmodule.ExportedUnit) { u.Coordinate = "other.txt" },
		"digest differs from the blob":         func(u *formatmodule.ExportedUnit) { u.Digest = sha("x") },
		"two blobs":                            func(u *formatmodule.ExportedUnit) { u.Blobs = append(u.Blobs, u.Blobs[0]) },
		"no blobs":                             func(u *formatmodule.ExportedUnit) { u.Blobs = nil },
		"unknown manifest field":               func(u *formatmodule.ExportedUnit) { u.Manifest = []byte(`{"path":"e.txt","size":5}`) },
		"trailing manifest value":              func(u *formatmodule.ExportedUnit) { u.Manifest = []byte(`{"path":"e.txt"} {}`) },
		"non canonical path": func(u *formatmodule.ExportedUnit) {
			u.Manifest = []byte(`{"path":"../e.txt"}`)
			u.Coordinate = "../e.txt"
		},
		"another format": func(u *formatmodule.ExportedUnit) { u.Format = repository.FormatMaven },
		"control character in content type": func(u *formatmodule.ExportedUnit) {
			u.Manifest = []byte(`{"path":"e.txt","contentType":"a\nb"}`)
		},
	}
	for name, mutate := range bad {
		unit := unitOf("e.txt", "hello")
		mutate(&unit)
		err = module.ImportUnit(ctx, repo.ID, unit, opener("hello"))
		if !errors.Is(err, formatmodule.ErrImportInvalidUnit) && !errors.Is(err, formatmodule.ErrImportMismatch) {
			t.Fatalf("%s = %v", name, err)
		}
	}
	if _, getErr := store.GetRawAsset(ctx, repo.ID, "e.txt"); !errors.Is(getErr, repository.ErrNotFound) {
		t.Fatalf("a refused unit was published: %v", getErr)
	}
}

func TestImportUnitOnlyFillsAFlatHostedRepository(t *testing.T) {
	ctx := context.Background()
	store, objects, _, _ := importFixture(t)
	versioned, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "versioned", Format: repository.FormatRaw, RawLayout: repository.RawLayoutVersioned})
	if err != nil {
		t.Fatal(err)
	}
	module := rawformat.New(rawformat.Options{Store: store, Repositories: store, Objects: objects})
	if err = module.ImportUnit(ctx, versioned.ID, unitOf("v.txt", "v"), opener("v")); !errors.Is(err, formatmodule.ErrImportUnsupportedLayout) {
		t.Fatalf("versioned = %v", err)
	}
	if err = module.ImportUnit(ctx, uuid.NewString(), unitOf("v.txt", "v"), opener("v")); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("unknown repository = %v", err)
	}
	if err = rawformat.New(rawformat.Options{}).ImportUnit(ctx, versioned.ID, unitOf("v.txt", "v"), opener("v")); !errors.Is(err, repository.ErrDisabled) {
		t.Fatalf("unwired module = %v", err)
	}
}
