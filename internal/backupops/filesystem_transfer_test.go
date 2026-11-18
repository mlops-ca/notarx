package backupops_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/backupmanifest"
	"github.com/mlops-ca/notarx/internal/backupops"
	"github.com/mlops-ca/notarx/internal/objectstore"
)

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func newFilesystemRoot(t *testing.T) (*objectstore.FileStore, string) {
	t.Helper()
	cfg := objectstore.Defaults()
	cfg.Driver = objectstore.DriverFilesystem
	cfg.FSRoot = filepath.Join(t.TempDir(), "objects")
	cfg.FSReserveBytes = 0
	cfg.FSFsync = false
	store, err := objectstore.NewFileStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, cfg.FSRoot
}

// fsFrozenSource is a frozen source whose objects are a real filesystem root,
// read through the production adapter; the database half is synthetic.
type fsFrozenSource struct {
	*fakeSource
	objects *backupops.FilesystemObjects
}

func (s *fsFrozenSource) WalkObjects(ctx context.Context, visit func(string, int64) error) error {
	return s.objects.WalkObjects(ctx, visit)
}
func (s *fsFrozenSource) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	return s.objects.Open(ctx, key)
}
func (s *fsFrozenSource) WalkReferences(context.Context, func(string) error) error { return nil }
func (s *fsFrozenSource) ObjectFormat() string                                     { return s.objects.ObjectFormat() }
func (s *fsFrozenSource) SegmentBytes() int64                                      { return s.objects.SegmentBytes() }
func (s *fsFrozenSource) RootID() string                                           { return s.objects.RootID() }

type fsObject struct {
	key  string
	body []byte
}

func populate(t *testing.T, store *objectstore.FileStore, count int) (objects []fsObject, total int64) {
	t.Helper()
	ctx := context.Background()
	for i := range count {
		size := (i * 37) % 700
		if i%50 == 7 {
			size = 5000 // larger than the segment limit used below
		}
		body := bytes.Repeat([]byte{byte('a' + i%26)}, size)
		key := fmt.Sprintf("native/raw/sha256/%04d/object-%d", i%10, i)
		if err := store.PutVerifiedReader(ctx, key, bytes.NewReader(body), int64(len(body)), sha(body)); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, fsObject{key, body})
		total += int64(size)
	}
	// An object with a long, non-ASCII key exercises extended tar headers.
	long := "native/go/" + strings.Repeat("módulo/", 25) + "info"
	if err := store.PutVerifiedReader(ctx, long, strings.NewReader("long key"), 8, sha([]byte("long key"))); err != nil {
		t.Fatal(err)
	}
	objects = append(objects, fsObject{long, []byte("long key")})
	return objects, total + 8
}

func exportFilesystem(t *testing.T, root string, segmentBytes int64) (directory string, source *fsFrozenSource, report backupops.TransferReport) {
	t.Helper()
	directory, source, _, report = exportFilesystemWithRelease(t, root, segmentBytes)
	return directory, source, report
}

func exportFilesystemWithRelease(t *testing.T, root string, segmentBytes int64) (directory string, source *fsFrozenSource, release backupops.Release, report backupops.TransferReport) {
	t.Helper()
	_, release, ledger := binaryRelease(t)
	objects, err := backupops.NewFilesystemObjects(backupops.FilesystemSettings{Root: root, SegmentBytes: segmentBytes})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Close() })
	source = &fsFrozenSource{fakeSource: &fakeSource{ledger: ledger}, objects: objects}
	scope := backupmanifest.WriterEvidence{ScopeID: "synthetic-scope", InventoryDeclaredComplete: true, Writers: []backupmanifest.Writer{{ID: "synthetic-writer"}}}
	directory = filepath.Join(t.TempDir(), "bundle")
	report, err = backupops.Export(context.Background(), source, directory, release, scope, "synthetic-fs-backup")
	if err != nil || report.Status != "exported" {
		t.Fatalf("export: %+v %v", report, err)
	}
	return directory, source, release, report
}

func readManifest(t *testing.T, directory string) backupmanifest.Manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m backupmanifest.Manifest
	if err = json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestFilesystemExportPacksObjectsIntoSegmentsAndRestoresThem(t *testing.T) {
	store, root := newFilesystemRoot(t)
	objects, total := populate(t, store, 240)
	const segmentBytes = 64 << 10
	directory, _, report := exportFilesystem(t, root, segmentBytes)
	if report.VerifiedObjects != int64(len(objects)) || report.VerifiedBytes != total {
		t.Fatalf("report=%+v want %d objects %d bytes", report, len(objects), total)
	}

	m := readManifest(t, directory)
	if m.Objects.Format != backupmanifest.FormatFSBytes || m.Objects.RootID != store.RootID() {
		t.Fatalf("objects=%+v want fs-bytes-v1 with root %s", m.Objects, store.RootID())
	}
	if verified := backupmanifest.Verify(context.Background(), directory, m); verified.Integrity != backupmanifest.Verified || verified.VerifiedObjects != int64(len(objects)) {
		t.Fatalf("verify=%+v", verified)
	}

	// Request count is bounded by size/segment size, not by object count: the
	// bundle holds a handful of segment files, never one file per object.
	entries, err := os.ReadDir(filepath.Join(directory, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	var segmentTotal int64
	for _, file := range m.Objects.Segments {
		segmentTotal += *file.Size
	}
	bound := int(math.Ceil(float64(segmentTotal)/float64(segmentBytes))) + 1
	if len(entries) != len(m.Objects.Segments) || len(entries) > bound+len(objects)/50+1 {
		t.Fatalf("objects/ holds %d files for %d segments (bound %d)", len(entries), len(m.Objects.Segments), bound)
	}
	if len(entries)*10 > len(objects) {
		t.Fatalf("%d files for %d objects: segments are not packing objects", len(entries), len(objects))
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "segment-") || !strings.HasSuffix(entry.Name(), ".tar") {
			t.Fatalf("unexpected bundle file %s", entry.Name())
		}
	}

	// Each segment is a readable tar archive that agrees with the inventory.
	byKey := map[string][]byte{}
	for _, o := range objects {
		byKey[o.key] = o.body
	}
	seen := 0
	for _, segment := range m.Objects.Segments {
		file, openErr := os.Open(filepath.Join(directory, segment.Path))
		if openErr != nil {
			t.Fatal(openErr)
		}
		reader := tar.NewReader(file)
		for {
			header, nextErr := reader.Next()
			if errors.Is(nextErr, io.EOF) {
				break
			}
			if nextErr != nil {
				t.Fatal(nextErr)
			}
			body, readErr := io.ReadAll(reader)
			if readErr != nil || !bytes.Equal(body, byKey[header.Name]) {
				t.Fatalf("tar entry %q differs from the object", header.Name)
			}
			seen++
		}
		_ = file.Close()
	}
	if seen != len(objects) {
		t.Fatalf("segments hold %d entries, want %d", seen, len(objects))
	}

	// Restore into a new root and compare every object, digest and the root ID.
	target := filepath.Join(t.TempDir(), "restored")
	result, err := backupops.RestoreFilesystemObjects(context.Background(), directory, target)
	if err != nil || result.Status != "restored" || result.Objects != "verified" || result.VerifiedObjects != int64(len(objects)) {
		t.Fatalf("restore: %+v %v", result, err)
	}
	restored, err := objectstore.OpenFileStoreReadOnly(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if restored.RootID() != store.RootID() {
		t.Fatalf("restored root ID %q, want the source's %q", restored.RootID(), store.RootID())
	}
	keys, err := restored.List(context.Background(), "")
	if err != nil || len(keys) != len(objects) {
		t.Fatalf("restored %d keys, %v", len(keys), err)
	}
	for _, o := range objects {
		body, getErr := restored.Get(context.Background(), o.key)
		info, statErr := restored.Stat(context.Background(), o.key)
		if getErr != nil || statErr != nil || !bytes.Equal(body, o.body) || info.Digest != sha(o.body) {
			t.Fatalf("restored %q: %v %v digest=%q", o.key, getErr, statErr, info.Digest)
		}
	}
}

func TestFilesystemExportRestoresIntoADestination(t *testing.T) {
	store, root := newFilesystemRoot(t)
	objects, _ := populate(t, store, 60)
	directory, source, release, _ := exportFilesystemWithRelease(t, root, 16<<10)
	target := &fakeTarget{ledger: source.ledger, objects: map[string][]byte{}}
	result, err := backupops.Restore(context.Background(), directory, release, target)
	if err != nil || result.Status != "restored" || result.VerifiedObjects != int64(len(objects)) {
		t.Fatalf("restore: %+v %v", result, err)
	}
	for _, o := range objects {
		if !bytes.Equal(target.objects[o.key], o.body) {
			t.Fatalf("object %q differs after restore", o.key)
		}
	}
}

func TestFilesystemExportLargerThanOneSegmentGetsItsOwnSegment(t *testing.T) {
	store, root := newFilesystemRoot(t)
	big := bytes.Repeat([]byte("x"), 20000)
	for key, body := range map[string][]byte{"a/small": []byte("s"), "b/big": big, "c/small": []byte("t")} {
		if err := store.PutVerifiedReader(context.Background(), key, bytes.NewReader(body), int64(len(body)), sha(body)); err != nil {
			t.Fatal(err)
		}
	}
	directory, _, _ := exportFilesystem(t, root, 4096)
	m := readManifest(t, directory)
	if len(m.Objects.Segments) != 3 {
		t.Fatalf("segments=%d, want one per object when the big one cannot share", len(m.Objects.Segments))
	}
	result, err := backupops.RestoreFilesystemObjects(context.Background(), directory, filepath.Join(t.TempDir(), "restored"))
	if err != nil || result.VerifiedBytes != int64(len(big))+2 {
		t.Fatalf("restore: %+v %v", result, err)
	}
}

func TestEmptyFilesystemExportAndRestore(t *testing.T) {
	_, root := newFilesystemRoot(t)
	directory, _, report := exportFilesystem(t, root, 0)
	m := readManifest(t, directory)
	if report.VerifiedObjects != 0 || len(m.Objects.Segments) != 0 || m.Objects.RootID == "" {
		t.Fatalf("report=%+v objects=%+v", report, m.Objects)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if result, err := backupops.RestoreFilesystemObjects(context.Background(), directory, target); err != nil || result.Status != "restored" {
		t.Fatalf("restore: %+v %v", result, err)
	}
	restored, err := objectstore.OpenFileStoreReadOnly(target)
	if err != nil || restored.RootID() != m.Objects.RootID {
		t.Fatalf("restored root: %v", err)
	}
	_ = restored.Close()
}

func TestRestoreFilesystemObjectsRefusesDamageAndCleansUp(t *testing.T) {
	store, root := newFilesystemRoot(t)
	populate(t, store, 30)
	directory, _, _ := exportFilesystem(t, root, 8<<10)
	m := readManifest(t, directory)

	t.Run("damaged segment", func(t *testing.T) {
		damaged := filepath.Join(t.TempDir(), "bundle")
		copyBundle(t, directory, damaged)
		path := filepath.Join(damaged, m.Objects.Segments[0].Path)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data[700] ^= 0xff
		if err = os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "restored")
		result, restoreErr := backupops.RestoreFilesystemObjects(context.Background(), damaged, target)
		if restoreErr == nil || result.Status != "failed" {
			t.Fatalf("a damaged bundle restored: %+v", result)
		}
		if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("a refused restore left a root behind: %v", statErr)
		}
	})
	t.Run("target must be new or empty", func(t *testing.T) {
		occupied := t.TempDir()
		if err := os.WriteFile(filepath.Join(occupied, "keep"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := backupops.RestoreFilesystemObjects(context.Background(), directory, occupied); err == nil {
			t.Fatal("restored into a non-empty directory")
		}
		if content, _ := os.ReadFile(filepath.Join(occupied, "keep")); string(content) != "x" {
			t.Fatal("the existing directory was modified")
		}
		if _, err := backupops.RestoreFilesystemObjects(context.Background(), directory, "relative/root"); err == nil {
			t.Fatal("restored into a relative path")
		}
		empty := t.TempDir()
		if result, err := backupops.RestoreFilesystemObjects(context.Background(), directory, empty); err != nil || result.Status != "restored" {
			t.Fatalf("an existing empty directory was refused: %+v %v", result, err)
		}
	})
	t.Run("unfinished bundle", func(t *testing.T) {
		unfinished := filepath.Join(t.TempDir(), "bundle")
		copyBundle(t, directory, unfinished)
		if err := os.WriteFile(filepath.Join(unfinished, "INCOMPLETE"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := backupops.RestoreFilesystemObjects(context.Background(), unfinished, filepath.Join(t.TempDir(), "restored")); err == nil {
			t.Fatal("an incomplete bundle restored")
		}
	})
}

func copyBundle(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Mkdir(to, 0o700); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(from, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || path == from {
			return walkErr
		}
		rel, _ := filepath.Rel(from, path)
		if entry.IsDir() {
			return os.Mkdir(filepath.Join(to, rel), 0o700)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(filepath.Join(to, rel), data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBackupCLIRestoresObjectsIntoANewFilesystemRoot(t *testing.T) {
	store, root := newFilesystemRoot(t)
	objects, _ := populate(t, store, 12)
	directory, _, _ := exportFilesystem(t, root, 0)
	target := filepath.Join(t.TempDir(), "restored")
	var out, errOut bytes.Buffer
	code := backupops.RunCLI(context.Background(), []string{"restore-objects", "--bundle", directory, "--root", target}, &out, &errOut)
	var result backupops.TransferReport
	if code != 0 || json.Unmarshal(out.Bytes(), &result) != nil || result.Status != "restored" || result.VerifiedObjects != int64(len(objects)) {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	for _, args := range [][]string{{"restore-objects"}, {"restore-objects", "--bundle", directory}, {"restore-objects", "--bundle", directory, "--root", target, "extra"}} {
		out.Reset()
		errOut.Reset()
		if code = backupops.RunCLI(context.Background(), args, &out, &errOut); code != 2 {
			t.Fatalf("args %v: code=%d", args, code)
		}
	}
	// A second restore into the now populated root is refused.
	out.Reset()
	if code = backupops.RunCLI(context.Background(), []string{"restore-objects", "--bundle", directory, "--root", target}, &out, &errOut); code != 1 {
		t.Fatalf("restore into a populated root: code=%d", code)
	}
}

func TestFilesystemObjectsRequireAnInitialisedRoot(t *testing.T) {
	if _, err := backupops.NewFilesystemObjects(backupops.FilesystemSettings{Root: t.TempDir()}); err == nil {
		t.Fatal("an uninitialised directory was accepted as a source")
	}
	if _, err := backupops.NewFilesystemObjects(backupops.FilesystemSettings{Root: "relative"}); err == nil {
		t.Fatal("a relative root was accepted")
	}
	_, root := newFilesystemRoot(t)
	if _, err := backupops.NewFilesystemObjects(backupops.FilesystemSettings{Root: root, SegmentBytes: -1}); err == nil {
		t.Fatal("a negative segment size was accepted")
	}
	source, err := backupops.NewFilesystemObjects(backupops.FilesystemSettings{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	if source.ObjectFormat() != backupmanifest.FormatFSBytes || source.SegmentBytes() != 0 || source.RootID() == "" {
		t.Fatalf("source=%s %d %q", source.ObjectFormat(), source.SegmentBytes(), source.RootID())
	}
}
