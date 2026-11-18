package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func testFileConfig(t *testing.T) Config {
	t.Helper()
	cfg := Defaults()
	cfg.Driver = DriverFilesystem
	cfg.FSRoot = filepath.Join(t.TempDir(), "objects")
	cfg.FSReserveBytes = 0
	return cfg
}

func openTestFileStore(t *testing.T, cfg Config) *FileStore {
	t.Helper()
	store, err := NewFileStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestFileStoreCreatesLayoutAndStableRootID(t *testing.T) {
	cfg := testFileConfig(t)
	first := openTestFileStore(t, cfg)
	for _, dir := range []string{"data", "meta", "tmp"} {
		info, err := os.Stat(filepath.Join(cfg.FSRoot, dir))
		if err != nil || !info.IsDir() {
			t.Fatalf("%s: %v", dir, err)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(cfg.FSRoot)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("root mode = %v, %v; want 0700", info.Mode().Perm(), err)
		}
	}
	marker, err := os.ReadFile(filepath.Join(cfg.FSRoot, ".notarx-root"))
	if err != nil || strings.TrimSpace(string(marker)) != first.RootID() || first.RootID() == "" {
		t.Fatalf("marker = %q, %v; root ID %q", marker, err, first.RootID())
	}
	driver, id := first.Identity()
	if driver != DriverFilesystem || id != first.RootID() || first.Root() != cfg.FSRoot {
		t.Fatalf("identity = %q %q", driver, id)
	}
	_ = first.Close()
	second := openTestFileStore(t, cfg)
	if second.RootID() != first.RootID() {
		t.Fatalf("root ID changed across reopen: %q then %q", first.RootID(), second.RootID())
	}
}

func TestFileStoreRejectsBadRootConfigurationAndMarker(t *testing.T) {
	cfg := testFileConfig(t)
	cfg.FSRoot = "relative/objects"
	if _, err := NewFileStore(cfg); err == nil {
		t.Fatal("NewFileStore accepted a relative root")
	}
	cfg = testFileConfig(t)
	store := openTestFileStore(t, cfg)
	_ = store.Close()
	if err := os.WriteFile(filepath.Join(cfg.FSRoot, ".notarx-root"), []byte("not-a-uuid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(cfg); err == nil {
		t.Fatal("NewFileStore accepted a corrupt root marker")
	}
	cfg = testFileConfig(t)
	if err := os.MkdirAll(cfg.FSRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.FSRoot, "data"), []byte("a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(cfg); err == nil {
		t.Fatal("NewFileStore accepted a data entry that is not a directory")
	}
}

func TestOpenRegistersTheFilesystemDriver(t *testing.T) {
	cfg := testFileConfig(t)
	store, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	file, ok := store.(*FileStore)
	if !ok {
		t.Fatalf("Open returned %T", store)
	}
	_ = file.Close()
	if err = Check(context.Background(), cfg); err != nil {
		t.Fatalf("Check: %v", err)
	}
	found := false
	for _, name := range Drivers() {
		found = found || name == DriverFilesystem
	}
	if !found {
		t.Fatalf("Drivers() = %v", Drivers())
	}
	if endpoint, bucket := cfg.Binding(); endpoint != DriverFilesystem || bucket != cfg.FSRoot {
		t.Fatalf("Binding = %q %q", endpoint, bucket)
	}
}

func TestValidateFileKey(t *testing.T) {
	valid := []string{"a", "a/b", "a/b.c", "ünï/cödé", "tmpx/a", "metadata/a", "a/tmp/b", "a/meta", "x.sha256", "a b/c d", "trailing.", "a/b-c_d~e"}
	for _, key := range valid {
		if err := validateFileKey(key); err != nil {
			t.Errorf("validateFileKey(%q) = %v, want valid", key, err)
		}
	}
	invalid := map[string]string{
		"empty":             "",
		"parent":            "../escape",
		"parent in middle":  "a/../b",
		"current dir":       "a/./b",
		"dot only":          ".",
		"leading dot":       ".hidden",
		"dot segment":       "a/.hidden/b",
		"leading slash":     "/abs",
		"trailing slash":    "a/",
		"empty segment":     "a//b",
		"top-level tmp":     "tmp/x",
		"top-level meta":    "meta/x",
		"bare tmp":          "tmp",
		"nul":               "a\x00b",
		"newline":           "a\nb",
		"backslash":         "a\\b",
		"invalid utf8":      "a\xffb",
		"segment too long":  strings.Repeat("s", 300),
		"key too long":      strings.Repeat("seg/", 300),
		"root marker":       ".notarx-root",
		"marker nested dot": "a/.notarx-root",
	}
	for name, key := range invalid {
		if err := validateFileKey(key); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("%s: validateFileKey(%q) = %v, want ErrInvalidKey", name, key, err)
		}
	}
}

func TestFileStoreRefusesReservedAndTraversalKeys(t *testing.T) {
	cfg := testFileConfig(t)
	store := openTestFileStore(t, cfg)
	ctx := context.Background()
	outside := filepath.Join(filepath.Dir(cfg.FSRoot), "escaped")
	for _, key := range []string{"../escaped", "a/../../escaped", "tmp/file", "meta/file", ".notarx-root", "/etc/passwd"} {
		if err := store.Put(ctx, key, []byte("x")); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Put(%q) = %v, want ErrInvalidKey", key, err)
		}
		if _, err := store.Get(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q) = %v, want ErrNotFound", key, err)
		}
		if err := store.Delete(ctx, key); err != nil {
			t.Errorf("Delete(%q) = %v, want nil", key, err)
		}
		if err := store.SetVerifiedDigest(ctx, key, digestOf(nil)); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetVerifiedDigest(%q) = %v, want ErrNotFound", key, err)
		}
	}
	if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a traversal key wrote outside the root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.FSRoot, "tmp", "file")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a reserved key wrote into tmp")
	}
}

func skipWithoutSymlinks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
}

func TestFileStoreDoesNotFollowSymlinks(t *testing.T) {
	skipWithoutSymlinks(t)
	cfg := testFileConfig(t)
	store := openTestFileStore(t, cfg)
	ctx := context.Background()
	outsideDir := t.TempDir()
	secret := filepath.Join(outsideDir, "secret")
	if err := os.WriteFile(secret, []byte("outside the root"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "real/object", []byte("inside")); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"data/file-escape": secret,                                    // file symlink out of the root
		"data/dir-escape":  outsideDir,                                // directory symlink out of the root
		"data/inside-link": filepath.Join(cfg.FSRoot, "data", "real"), // symlink that stays inside
	}
	for link, target := range links {
		if err := os.Symlink(target, filepath.Join(cfg.FSRoot, link)); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"file-escape", "dir-escape/secret", "inside-link/object"} {
		if _, err := store.Get(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q) = %v, want ErrNotFound", key, err)
		}
		if _, err := store.Stat(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Errorf("Stat(%q) = %v, want ErrNotFound", key, err)
		}
		if _, _, err := store.OpenRange(ctx, key, 0, 0); !errors.Is(err, ErrNotFound) {
			t.Errorf("OpenRange(%q) = %v, want ErrNotFound", key, err)
		}
		if err := store.Put(ctx, key, []byte("overwrite")); err == nil {
			t.Errorf("Put(%q) followed a symbolic link", key)
		}
		if err := store.Delete(ctx, key); err == nil && key != "file-escape" {
			t.Errorf("Delete(%q) followed a symbolic link", key)
		}
	}
	if content, _ := os.ReadFile(secret); string(content) != "outside the root" {
		t.Fatalf("a write reached through a symbolic link: %q", content)
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "secret2")); err == nil {
		t.Fatal("unexpected file outside the root")
	}
	keys, err := store.List(ctx, "")
	if err != nil || len(keys) != 1 || keys[0] != "real/object" {
		t.Fatalf("List = %v, %v; symbolic links must be skipped", keys, err)
	}
	// A symbolic link where a sidecar belongs is ignored rather than followed.
	if err = store.SetVerifiedDigest(ctx, "real/object", digestOf([]byte("inside"))); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(cfg.FSRoot, "meta", "real", "object.sha256")
	if err = os.Remove(sidecar); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(outsideDir, "digest"), []byte(digestOf([]byte("forged"))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(outsideDir, "digest"), sidecar); err != nil {
		t.Fatal(err)
	}
	if info, statErr := store.Stat(ctx, "real/object"); statErr != nil || info.Digest != "" {
		t.Fatalf("Stat through a sidecar symlink = %#v, %v; want unverified", info, statErr)
	}
}

func TestFileStoreIgnoresStrayEntriesWhenListing(t *testing.T) {
	cfg := testFileConfig(t)
	store := openTestFileStore(t, cfg)
	ctx := context.Background()
	for _, key := range []string{"b/2", "a/1", "a.b/0", "a/z"} {
		if err := store.Put(ctx, key, []byte(key)); err != nil {
			t.Fatal(err)
		}
	}
	// Files an operator or tool dropped next to the data.
	if err := os.WriteFile(filepath.Join(cfg.FSRoot, "data", ".stray"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.FSRoot, "tmp", "in-flight"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := store.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	// "a.b/0" sorts before "a/1" as a key even though directory "a" is walked first.
	want := []string{"a.b/0", "a/1", "a/z", "b/2"}
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Fatalf("List = %v, want %v", keys, want)
	}
}

func TestFileStoreSidecarStatesReadAsVerifiedOrNot(t *testing.T) {
	cfg := testFileConfig(t)
	store := openTestFileStore(t, cfg)
	ctx := context.Background()
	data := []byte("sidecar states")
	if err := store.PutVerifiedReader(ctx, "k", bytes.NewReader(data), int64(len(data)), digestOf(data)); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(cfg.FSRoot, "meta", "k.sha256")
	if content, err := os.ReadFile(sidecar); err != nil || string(content) != digestOf(data) {
		t.Fatalf("sidecar = %q, %v", content, err)
	}
	// A garbled sidecar is not a digest.
	if err := os.WriteFile(sidecar, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := store.Stat(ctx, "k"); err != nil || info.Digest != "" {
		t.Fatalf("Stat with a corrupt sidecar = %#v, %v", info, err)
	}
	// A plain overwrite drops the old digest instead of keeping a stale one.
	if err := store.SetVerifiedDigest(ctx, "k", digestOf(data)); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "k", []byte("different bytes")); err != nil {
		t.Fatal(err)
	}
	if info, err := store.Stat(ctx, "k"); err != nil || info.Digest != "" || info.Size != int64(len("different bytes")) {
		t.Fatalf("Stat after a plain overwrite = %#v, %v; want no digest", info, err)
	}
	if _, err := os.Stat(sidecar); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale sidecar left behind: %v", err)
	}
	// Delete removes the sidecar too.
	if err := store.SetVerifiedDigest(ctx, "k", digestOf([]byte("different bytes"))); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sidecar); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sidecar survived Delete: %v", err)
	}
}

func TestFileStoreWithoutFsyncStillCommits(t *testing.T) {
	cfg := testFileConfig(t)
	cfg.FSFsync = false
	store := openTestFileStore(t, cfg)
	data := []byte("no fsync")
	if err := store.PutVerifiedReader(context.Background(), "a/b", bytes.NewReader(data), int64(len(data)), digestOf(data)); err != nil {
		t.Fatal(err)
	}
	info, err := store.Stat(context.Background(), "a/b")
	if err != nil || info.Digest != digestOf(data) {
		t.Fatalf("Stat = %#v, %v", info, err)
	}
}

func TestFileStoreRemovesOnlyStaleTemporaryFiles(t *testing.T) {
	cfg := testFileConfig(t)
	store := openTestFileStore(t, cfg)
	_ = store.Close()
	old := filepath.Join(cfg.FSRoot, "tmp", "old-upload")
	recent := filepath.Join(cfg.FSRoot, "tmp", "recent-upload")
	for _, name := range []string{old, recent} {
		if err := os.WriteFile(name, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, twoHoursAgo, twoHoursAgo); err != nil {
		t.Fatal(err)
	}
	reopened := openTestFileStore(t, cfg)
	_ = reopened
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale temporary file survived start-up: %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("recent temporary file was removed: %v", err)
	}
}

// TestFileStoreCrashInjection stops a write at each commit step, as a crash
// would, restarts from the same directory and checks the invariants of ADR
// 0017: the object is either the old bytes or the new bytes, never a mix, and
// a reported digest always describes the bytes that are there.
func TestFileStoreCrashInjection(t *testing.T) {
	oldData := []byte("old object bytes")
	newData := []byte("new object bytes, a different length")
	steps := []string{"tmp-written", "tmp-synced", "sidecar-removed", "renamed", "data-synced", "digest-written", "digest-renamed"}
	scenarios := []struct {
		name      string
		seed      bool // an object with a verified digest already exists
		verified  bool // the interrupted write is verified
		sidecar   bool
		wantFresh map[string]bool // steps after which the new bytes must be visible
	}{
		{name: "fresh verified write", verified: true, wantFresh: map[string]bool{"renamed": true, "data-synced": true, "digest-written": true, "digest-renamed": true}},
		{name: "verified overwrite of a verified object", seed: true, verified: true, wantFresh: map[string]bool{"renamed": true, "data-synced": true, "digest-written": true, "digest-renamed": true}},
		{name: "plain overwrite of a verified object", seed: true, wantFresh: map[string]bool{"renamed": true, "data-synced": true}},
	}
	for _, sc := range scenarios {
		for _, step := range steps {
			t.Run(sc.name+"/"+step, func(t *testing.T) {
				cfg := testFileConfig(t)
				store := openTestFileStore(t, cfg)
				ctx := context.Background()
				if sc.seed {
					if err := store.PutVerifiedReader(ctx, "obj/key", bytes.NewReader(oldData), int64(len(oldData)), digestOf(oldData)); err != nil {
						t.Fatal(err)
					}
				}
				errCrash := errors.New("simulated crash at " + step)
				fired := false
				store.hook = func(name string) error {
					if name == step {
						fired = true
						return errCrash
					}
					return nil
				}
				var err error
				if sc.verified {
					err = store.PutVerifiedReader(ctx, "obj/key", bytes.NewReader(newData), int64(len(newData)), digestOf(newData))
				} else {
					err = store.PutReader(ctx, "obj/key", bytes.NewReader(newData), int64(len(newData)))
				}
				if !sc.verified && strings.HasPrefix(step, "digest") {
					if err != nil || fired {
						t.Fatalf("a plain write has no digest step: fired=%t err=%v", fired, err)
					}
					return
				}
				if !fired || !errors.Is(err, errCrash) {
					t.Fatalf("hook fired=%t, err=%v; want the injected crash", fired, err)
				}
				_ = store.Close()

				// Restart from whatever is on disk.
				restarted := openTestFileStore(t, cfg)
				got, getErr := restarted.Get(ctx, "obj/key")
				switch {
				case errors.Is(getErr, ErrNotFound):
					if sc.seed || sc.wantFresh[step] {
						t.Fatalf("object lost after a crash at %s", step)
					}
				case getErr != nil:
					t.Fatalf("Get after crash: %v", getErr)
				case bytes.Equal(got, newData):
					if !sc.wantFresh[step] {
						t.Fatalf("new bytes visible before the rename (step %s)", step)
					}
				case bytes.Equal(got, oldData):
					if sc.wantFresh[step] {
						t.Fatalf("old bytes still visible after the rename (step %s)", step)
					}
				default:
					t.Fatalf("object holds neither version: %q", got)
				}
				if getErr == nil {
					info, statErr := restarted.Stat(ctx, "obj/key")
					if statErr != nil {
						t.Fatal(statErr)
					}
					if info.Digest != "" && info.Digest != digestOf(got) {
						t.Fatalf("Stat digest %q does not describe the bytes %q", info.Digest, got)
					}
					if info.Size != int64(len(got)) {
						t.Fatalf("Stat size %d, bytes %d", info.Size, len(got))
					}
				}
				keys, listErr := restarted.List(ctx, "")
				if listErr != nil {
					t.Fatal(listErr)
				}
				for _, key := range keys {
					if key != "obj/key" {
						t.Fatalf("crash left an extra object %q", key)
					}
				}
				// The interrupted write leaves its temporary file; once it is
				// stale the next start removes it. The restart above kept it
				// because it is recent.
				left, _ := os.ReadDir(filepath.Join(cfg.FSRoot, "tmp"))
				if len(left) > 0 {
					old := time.Now().Add(-2 * time.Hour)
					for _, entry := range left {
						_ = os.Chtimes(filepath.Join(cfg.FSRoot, "tmp", entry.Name()), old, old)
					}
					_ = restarted.Close()
					again := openTestFileStore(t, cfg)
					_ = again
					if rest, _ := os.ReadDir(filepath.Join(cfg.FSRoot, "tmp")); len(rest) != 0 {
						t.Fatalf("stale temporary files survived start-up: %d", len(rest))
					}
				}
			})
		}
	}
}

func TestOpenFileStoreReadOnlyReadsWithoutChangingTheRoot(t *testing.T) {
	cfg := testFileConfig(t)
	writer := openTestFileStore(t, cfg)
	ctx := context.Background()
	data := []byte("frozen source")
	if err := writer.PutVerifiedReader(ctx, "a/b", bytes.NewReader(data), int64(len(data)), digestOf(data)); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(cfg.FSRoot, "tmp", "left-behind")
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenFileStoreReadOnly(cfg.FSRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if reader.RootID() != writer.RootID() {
		t.Fatalf("root ID %q, want %q", reader.RootID(), writer.RootID())
	}
	if got, getErr := reader.Get(ctx, "a/b"); getErr != nil || !bytes.Equal(got, data) {
		t.Fatalf("Get = %q, %v", got, getErr)
	}
	if info, statErr := reader.Stat(ctx, "a/b"); statErr != nil || info.Digest != digestOf(data) {
		t.Fatalf("Stat = %#v, %v", info, statErr)
	}
	if err = reader.Put(ctx, "c", []byte("x")); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Put = %v, want ErrReadOnly", err)
	}
	if err = reader.Delete(ctx, "a/b"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Delete = %v, want ErrReadOnly", err)
	}
	if err = reader.SetVerifiedDigest(ctx, "a/b", digestOf(data)); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("SetVerifiedDigest = %v, want ErrReadOnly", err)
	}
	if _, statErr := os.Stat(stale); statErr != nil {
		t.Fatalf("a read-only open removed a temporary file: %v", statErr)
	}
	// It refuses a directory that is not an initialised root and never creates one.
	empty := filepath.Join(t.TempDir(), "empty")
	if err = os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenFileStoreReadOnly(empty); err == nil {
		t.Fatal("OpenFileStoreReadOnly accepted an uninitialised directory")
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Fatalf("a read-only open created %d entries", len(entries))
	}
	if _, err = OpenFileStoreReadOnly("relative"); err == nil {
		t.Fatal("OpenFileStoreReadOnly accepted a relative path")
	}
}

func TestFileStoreFailedWritesLeaveNoTemporaryFiles(t *testing.T) {
	cfg := testFileConfig(t)
	store := openTestFileStore(t, cfg)
	ctx := context.Background()
	data := []byte("some bytes")
	_ = store.PutReader(ctx, "a", bytes.NewReader(data), int64(len(data))+3)
	_ = store.PutVerifiedReader(ctx, "a", bytes.NewReader(data), int64(len(data)), digestOf([]byte("other")))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_ = store.PutReader(cancelled, "a", bytes.NewReader(data), int64(len(data)))
	entries, err := os.ReadDir(filepath.Join(cfg.FSRoot, "tmp"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("tmp = %v, %v; failed writes must clean up", entries, err)
	}
}

func TestFileStoreRefusesWritesBelowTheReserve(t *testing.T) {
	cfg := testFileConfig(t)
	cfg.FSReserveBytes = 1 << 60 // far more than any test volume has free
	store := openTestFileStore(t, cfg)
	capacity, err := store.Capacity()
	if err != nil {
		t.Skipf("capacity is not measurable here: %v", err)
	}
	if capacity.TotalBytes <= 0 || capacity.FreeBytes <= 0 || capacity.FreeBytes > capacity.TotalBytes || capacity.ReserveBytes != 1<<60 {
		t.Fatalf("Capacity = %#v", capacity)
	}
	if !capacity.BelowReserve() {
		t.Fatal("BelowReserve = false with an enormous reserve")
	}
	err = store.Put(context.Background(), "k", []byte("x"))
	if !errors.Is(err, ErrInsufficientStorage) {
		t.Fatalf("Put = %v, want ErrInsufficientStorage", err)
	}
	if _, getErr := store.Get(context.Background(), "k"); !errors.Is(getErr, ErrNotFound) {
		t.Fatalf("a refused write left an object: %v", getErr)
	}
	// A modest reserve leaves room for the write.
	cfg.FSReserveBytes = 1
	if err = openTestFileStore(t, cfg).Put(context.Background(), "k", []byte("x")); err != nil {
		t.Fatalf("Put with a small reserve = %v", err)
	}
	if (Capacity{FreeBytes: 10, ReserveBytes: 0}).BelowReserve() {
		t.Fatal("a zero reserve must disable the check")
	}
}

func TestFileStoreConcurrentWritersToDifferentKeys(t *testing.T) {
	cfg := testFileConfig(t)
	store := openTestFileStore(t, cfg)
	ctx := context.Background()
	var group sync.WaitGroup
	errs := make(chan error, 64)
	for i := range 64 {
		group.Add(1)
		go func() {
			defer group.Done()
			data := bytes.Repeat([]byte{byte(i)}, 512+i)
			key := fmt.Sprintf("dir%d/obj%d", i%4, i)
			errs <- store.PutVerifiedReader(ctx, key, bytes.NewReader(data), int64(len(data)), digestOf(data))
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	keys, err := store.List(ctx, "")
	if err != nil || len(keys) != 64 {
		t.Fatalf("List = %d keys, %v", len(keys), err)
	}
}

func TestFileStoreKeyConflictsFailWithoutDamage(t *testing.T) {
	cfg := testFileConfig(t)
	store := openTestFileStore(t, cfg)
	ctx := context.Background()
	if err := store.Put(ctx, "a", []byte("file")); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "a/b", []byte("nested")); err == nil {
		t.Fatal("Put under an existing file key succeeded")
	}
	if got, err := store.Get(ctx, "a"); err != nil || string(got) != "file" {
		t.Fatalf("Get(a) = %q, %v", got, err)
	}
	if _, err := store.Get(ctx, "a/b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(a/b) = %v, want ErrNotFound", err)
	}
	if err := store.Put(ctx, "dir/x", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "dir", []byte("over a directory")); err == nil {
		t.Fatal("Put over a directory key succeeded")
	}
	if _, err := store.Get(ctx, "dir"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(dir) = %v, want ErrNotFound", err)
	}
}

func TestFileStoreUnsupportedConfigFailsOpen(t *testing.T) {
	cfg := testFileConfig(t)
	cfg.Driver = "tape"
	if _, err := Open(context.Background(), cfg); err == nil {
		t.Fatal("Open accepted an unknown driver")
	}
}
