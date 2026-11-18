package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Filesystem driver layout (ADR 0017). Everything below one root directory so
// that rename(2) is atomic between the parts:
//
//	<root>/data/<key>          object bytes
//	<root>/meta/<key>.sha256   verified digest sidecar ("sha256:<hex>")
//	<root>/tmp/                in-flight writes
//	<root>/.notarx-root        random root ID (single-node identity)
const (
	fsDataDir       = "data"
	fsMetaDir       = "meta"
	fsTmpDir        = "tmp"
	fsRootMarker    = ".notarx-root"
	fsSidecarSuffix = ".sha256"
	fsMaxKeyBytes   = 1024
	// A directory entry is at most 255 bytes on common filesystems; the digest
	// sidecar adds a suffix to the last segment.
	fsMaxSegmentBytes = 255 - len(fsSidecarSuffix)
	fsStaleTmpAge     = time.Hour
	fsLockStripes     = 256
)

// DefaultFSReserveBytes is the free space the filesystem driver keeps back
// from writes when NOTARX_OBJECT_STORE_FS_RESERVE_BYTES is not set.
const DefaultFSReserveBytes int64 = 1 << 30

// ErrInsufficientStorage is returned by a write that would leave less than the
// configured reserve of free space on the filesystem backing the driver.
var ErrInsufficientStorage = errors.New("insufficient storage: free space would fall below the reserve")

// ErrInvalidKey is returned when a key cannot be stored by the filesystem
// driver (path traversal, reserved names, symlinks and similar).
var ErrInvalidKey = errors.New("object key is not valid for the filesystem driver")

func init() { Register(DriverFilesystem, openFilesystem) }

// checkFilesystem verifies, without creating or changing anything, that the
// root exists, has the expected layout and a valid marker (when it has been
// initialised) and has free space above the reserve.
func checkFilesystem(cfg Config) error {
	info, err := os.Stat(cfg.FSRoot)
	if err != nil || !info.IsDir() {
		return errors.New("object store filesystem root does not exist or is not a directory")
	}
	if marker, readErr := os.ReadFile(path.Join(cfg.FSRoot, fsRootMarker)); readErr == nil {
		if _, parseErr := uuid.Parse(strings.TrimSpace(string(marker))); parseErr != nil {
			return errors.New("object store root marker is not a valid root ID")
		}
	}
	probe := &FileStore{rootPath: cfg.FSRoot, reserve: cfg.FSReserveBytes}
	if capacity, capErr := probe.Capacity(); capErr == nil && capacity.BelowReserve() {
		return ErrInsufficientStorage
	}
	return nil
}

func openFilesystem(_ context.Context, cfg Config) (Store, error) { return NewFileStore(cfg) }

// FileStore is the single-node filesystem driver. It is not for multiple nodes
// and not for shared network filesystems.
type FileStore struct {
	rootPath string
	root     *os.Root
	rootID   string
	fsync    bool
	reserve  int64
	now      func() time.Time
	// readOnly stores refuse every write; the backup tooling uses one to read a
	// frozen source without touching it.
	readOnly bool
	// hook, when set by tests, runs at each named commit step; an error aborts
	// the write as a crash would, leaving the on-disk state behind.
	hook  func(step string) error
	locks [fsLockStripes]sync.Mutex
}

// NewFileStore opens (creating on first use) the root directory named by
// cfg.FSRoot, validates its layout and removes temporary files left by a
// crash. It performs no database work; see VerifyIdentity.
func NewFileStore(cfg Config) (*FileStore, error) {
	return newFileStore(cfg, time.Now, "")
}

// NewFileStoreWithRootID is NewFileStore for restoring a backup: a root that has
// no marker yet is initialised with rootID instead of a new random one, so the
// restored database's recorded identity matches the restored root. A root that
// already has a different ID is refused.
func NewFileStoreWithRootID(cfg Config, rootID string) (*FileStore, error) {
	if _, err := uuid.Parse(rootID); err != nil {
		return nil, errors.New("object store root ID is not valid")
	}
	return newFileStore(cfg, time.Now, rootID)
}

func newFileStore(cfg Config, now func() time.Time, rootID string) (*FileStore, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validateFilesystem(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.FSRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create object store root: %w", err)
	}
	if err := os.Chmod(cfg.FSRoot, 0o700); err != nil {
		return nil, fmt.Errorf("restrict object store root to its owner: %w", err)
	}
	root, err := os.OpenRoot(cfg.FSRoot)
	if err != nil {
		return nil, fmt.Errorf("open object store root: %w", err)
	}
	store := &FileStore{rootPath: cfg.FSRoot, root: root, fsync: cfg.FSFsync, reserve: cfg.FSReserveBytes, now: now}
	if err = store.initialize(rootID); err != nil {
		_ = root.Close()
		return nil, err
	}
	return store, nil
}

// ErrReadOnly is returned by writes to a store opened with OpenFileStoreReadOnly.
var ErrReadOnly = errors.New("object store is read-only")

// OpenFileStoreReadOnly opens an existing, initialised root for reading only. It
// creates nothing, removes nothing and takes no lock, so it is safe on the root
// of a stopped gateway that a backup is reading.
func OpenFileStoreReadOnly(rootPath string) (*FileStore, error) {
	cfg := Config{Driver: DriverFilesystem, FSRoot: rootPath}
	if err := cfg.validateFilesystem(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open object store root: %w", err)
	}
	store := &FileStore{rootPath: rootPath, root: root, now: time.Now, readOnly: true}
	for _, dir := range []string{fsDataDir, fsMetaDir} {
		if info, statErr := root.Lstat(dir); statErr != nil || !info.IsDir() {
			_ = root.Close()
			return nil, fmt.Errorf("object store root has no %s directory: is it an initialised filesystem root?", dir)
		}
	}
	content, err := root.ReadFile(fsRootMarker)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("read root marker: %w", err)
	}
	id := strings.TrimSpace(string(content))
	if _, parseErr := uuid.Parse(id); parseErr != nil {
		_ = root.Close()
		return nil, errors.New("object store root marker is not a valid root ID")
	}
	store.rootID = id
	return store, nil
}

func (s *FileStore) initialize(wantRootID string) error {
	for _, dir := range []string{fsDataDir, fsMetaDir, fsTmpDir} {
		if err := s.root.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("create %s directory: %w", dir, err)
		}
		info, err := s.root.Lstat(dir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("object store %s entry is not a directory", dir)
		}
	}
	id, err := s.loadOrCreateRootID(wantRootID)
	if err != nil {
		return err
	}
	if wantRootID != "" && id != wantRootID {
		return errors.New("object store root already has a different root ID")
	}
	s.rootID = id
	return s.removeStaleTmp()
}

func (s *FileStore) loadOrCreateRootID(wantRootID string) (string, error) {
	content, err := s.root.ReadFile(fsRootMarker)
	if errors.Is(err, fs.ErrNotExist) {
		id := wantRootID
		if id == "" {
			id = uuid.NewString()
		}
		file, createErr := s.root.OpenFile(fsRootMarker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(createErr, fs.ErrExist) {
			return s.loadOrCreateRootID(wantRootID) // another process created it first
		}
		if createErr != nil {
			return "", fmt.Errorf("create root marker: %w", createErr)
		}
		_, writeErr := file.WriteString(id + "\n")
		if writeErr == nil && s.fsync {
			writeErr = file.Sync()
		}
		if closeErr := file.Close(); writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			_ = s.root.Remove(fsRootMarker)
			return "", fmt.Errorf("write root marker: %w", writeErr)
		}
		if s.fsync {
			if syncErr := s.syncDir("."); syncErr != nil {
				return "", syncErr
			}
		}
		return id, nil
	}
	if err != nil {
		return "", fmt.Errorf("read root marker: %w", err)
	}
	id := strings.TrimSpace(string(content))
	if _, parseErr := uuid.Parse(id); parseErr != nil {
		return "", errors.New("object store root marker is not a valid root ID")
	}
	return id, nil
}

// removeStaleTmp deletes temporary files older than one hour: the remains of
// writes that crashed before their rename.
func (s *FileStore) removeStaleTmp() error {
	entries, err := fs.ReadDir(s.root.FS(), fsTmpDir)
	if err != nil {
		return fmt.Errorf("list temporary files: %w", err)
	}
	cutoff := s.now().Add(-fsStaleTmpAge)
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil || info.IsDir() || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = s.root.Remove(path.Join(fsTmpDir, entry.Name()))
	}
	return nil
}

// Close releases the root handle.
func (s *FileStore) Close() error { return s.root.Close() }

// RootID is the random identity stored in the root marker.
func (s *FileStore) RootID() string { return s.rootID }

// Identity reports the driver name and root ID for single-node enforcement.
func (s *FileStore) Identity() (driver, rootID string) { return DriverFilesystem, s.rootID }

// Root is the absolute directory the store was opened on.
func (s *FileStore) Root() string { return s.rootPath }

// ReserveBytes is the free space writes must leave on the filesystem.
func (s *FileStore) ReserveBytes() int64 { return s.reserve }

func (s *FileStore) step(name string) error {
	if s.hook == nil {
		return nil
	}
	return s.hook(name)
}

// validateFileKey applies the filesystem rules on top of what every driver
// accepts: no traversal, no empty or dot-leading segments, no reserved
// top-level names, and names the filesystem can hold.
func validateFileKey(key string) error {
	if key == "" || len(key) > fsMaxKeyBytes || !utf8.ValidString(key) {
		return fmt.Errorf("%w: empty, too long or not UTF-8", ErrInvalidKey)
	}
	segments := strings.Split(key, "/")
	for index, segment := range segments {
		switch {
		case segment == "":
			return fmt.Errorf("%w: empty path segment", ErrInvalidKey)
		case strings.HasPrefix(segment, "."):
			return fmt.Errorf("%w: segments must not start with a dot", ErrInvalidKey)
		case len(segment) > fsMaxSegmentBytes:
			return fmt.Errorf("%w: path segment is too long", ErrInvalidKey)
		case index == 0 && (segment == fsTmpDir || segment == fsMetaDir):
			return fmt.Errorf("%w: %q is a reserved top-level name", ErrInvalidKey, segment)
		}
		for _, r := range segment {
			if r < 0x20 || r == 0x7f || r == '\\' {
				return fmt.Errorf("%w: control characters and backslashes are not allowed", ErrInvalidKey)
			}
		}
	}
	return nil
}

func dataPath(key string) string    { return path.Join(fsDataDir, key) }
func sidecarPath(key string) string { return path.Join(fsMetaDir, key+fsSidecarSuffix) }

// checkNoSymlinks verifies that no component of rel below the root is a
// symbolic link. os.Root already refuses links that leave the root; this also
// refuses links that stay inside it, so a key always names one real file.
func (s *FileStore) checkNoSymlinks(rel string) error {
	current := ""
	for _, part := range strings.Split(rel, "/") {
		current = path.Join(current, part)
		info, err := s.root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: symbolic links are not followed", ErrInvalidKey)
		}
	}
	return nil
}

func (s *FileStore) lockFor(key string) *sync.Mutex {
	sum := sha256.Sum256([]byte(key))
	return &s.locks[int(sum[0])%fsLockStripes]
}

func (s *FileStore) syncDir(rel string) error {
	dir, err := s.root.OpenFile(rel, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	if closeErr := dir.Close(); syncErr == nil {
		syncErr = closeErr
	}
	return syncErr
}

// mkdirAll creates rel and its parents, syncing the parent of every directory
// it creates so the new entries survive a crash.
func (s *FileStore) mkdirAll(rel string) error {
	current := ""
	for _, part := range strings.Split(rel, "/") {
		parent := current
		current = path.Join(current, part)
		err := s.root.Mkdir(current, 0o700)
		switch {
		case err == nil:
			if s.fsync {
				if syncErr := s.syncDir(dirOrRoot(parent)); syncErr != nil {
					return syncErr
				}
			}
		case errors.Is(err, fs.ErrExist):
			info, statErr := s.root.Lstat(current)
			if statErr != nil || !info.IsDir() {
				return fmt.Errorf("%w: %q is not a directory", ErrInvalidKey, current)
			}
		default:
			return err
		}
	}
	return nil
}

func dirOrRoot(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

// ctxReader stops a long copy when the context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// checkCapacity refuses a write of size bytes when it would leave less than
// the reserve free. An unreadable or unsupported filesystem does not block
// writes: the readiness check reports it instead.
func (s *FileStore) checkCapacity(size int64) error {
	if s.reserve <= 0 {
		return nil
	}
	capacity, err := s.Capacity()
	if err != nil {
		return nil
	}
	if capacity.FreeBytes-size < s.reserve {
		return ErrInsufficientStorage
	}
	return nil
}

func (s *FileStore) Get(ctx context.Context, key string) ([]byte, error) {
	reader, _, err := s.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	return io.ReadAll(ctxReader{ctx, reader})
}

func (s *FileStore) Put(ctx context.Context, key string, value []byte) error {
	return s.write(ctx, key, bytes.NewReader(value), int64(len(value)), "")
}

func (s *FileStore) PutReader(ctx context.Context, key string, value io.Reader, size int64) error {
	return s.write(ctx, key, value, size, "")
}

func (s *FileStore) PutVerifiedReader(ctx context.Context, key string, value io.Reader, size int64, digest string) error {
	want, err := parseDigest(digest)
	if err != nil {
		return err
	}
	return s.write(ctx, key, value, size, want)
}

// write is the commit sequence of ADR 0017: stream into tmp/, hash while
// writing, compare size and digest, fsync, rename into data/, fsync the parent
// directory, then write the digest sidecar the same way. wantHex is empty for
// an unverified write. A stale sidecar is removed before the new bytes appear
// so a crash can never pair new bytes with an old digest. The per-key lock
// makes same-key writers last-writer-wins with bytes and digest from one writer.
func (s *FileStore) write(ctx context.Context, key string, value io.Reader, size int64, wantHex string) error {
	if s.readOnly {
		return ErrReadOnly
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateFileKey(key); err != nil {
		return err
	}
	if size < 0 {
		return errors.New("object size is invalid")
	}
	if err := s.checkCapacity(size); err != nil {
		return err
	}
	// A hook error stands for a crash: nothing is cleaned up, so tests can
	// inspect (and restart from) the state a real crash would leave.
	crashed := false
	inject := func(name string) error {
		err := s.step(name)
		if err != nil {
			crashed = true
		}
		return err
	}
	tmpName := path.Join(fsTmpDir, uuid.NewString())
	defer func() {
		if !crashed {
			_ = s.root.Remove(tmpName)
		}
	}()
	sum, err := s.stream(ctx, tmpName, value, size, inject)
	if err != nil {
		return err
	}
	if wantHex != "" && hex.EncodeToString(sum) != wantHex {
		return ErrDigestMismatch
	}
	lock := s.lockFor(key)
	lock.Lock()
	defer lock.Unlock()
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = s.checkNoSymlinks(dataPath(key)); err != nil {
		return err
	}
	if err = s.removeSidecar(key); err != nil {
		return err
	}
	if err = inject("sidecar-removed"); err != nil {
		return err
	}
	if err = s.mkdirAll(path.Dir(dataPath(key))); err != nil {
		return err
	}
	if err = s.root.Rename(tmpName, dataPath(key)); err != nil {
		return fmt.Errorf("commit object: %w", err)
	}
	if err = inject("renamed"); err != nil {
		return err
	}
	if s.fsync {
		if err = s.syncDir(path.Dir(dataPath(key))); err != nil {
			return err
		}
	}
	if err = inject("data-synced"); err != nil {
		return err
	}
	if wantHex == "" {
		return nil
	}
	return s.writeSidecar(key, digestPrefix+wantHex, inject)
}

// stream copies value into the temporary file, hashing as it goes, and checks
// the size. It returns the SHA-256 of the bytes.
func (s *FileStore) stream(ctx context.Context, tmpName string, value io.Reader, size int64, inject func(string) error) ([]byte, error) {
	file, err := s.root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create temporary file: %w", err)
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(ctxReader{ctx, value}, size+1))
	if err == nil && written != size {
		err = fmt.Errorf("%w: got %d want %d", ErrSizeMismatch, written, size)
	}
	if err == nil {
		err = inject("tmp-written")
	}
	if err == nil && s.fsync {
		err = file.Sync()
	}
	if err == nil {
		err = inject("tmp-synced")
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	return hasher.Sum(nil), nil
}

func (s *FileStore) removeSidecar(key string) error {
	err := s.root.Remove(sidecarPath(key))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err == nil && s.fsync {
		return s.syncDir(path.Dir(sidecarPath(key)))
	}
	return nil
}

// writeSidecar writes the digest file by tmp-and-rename, so a reader sees the
// old sidecar, no sidecar, or the new one, never a partial file. inject marks
// the crash points for tests and is nil for callers outside a commit.
func (s *FileStore) writeSidecar(key, digest string, inject func(string) error) error {
	crashed := false
	step := func(name string) error {
		if inject == nil {
			return nil
		}
		err := inject(name)
		if err != nil {
			crashed = true
		}
		return err
	}
	tmpName := path.Join(fsTmpDir, uuid.NewString()+fsSidecarSuffix)
	defer func() {
		if !crashed {
			_ = s.root.Remove(tmpName)
		}
	}()
	file, err := s.root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create digest file: %w", err)
	}
	_, err = file.WriteString(digest)
	if err == nil && s.fsync {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = step("digest-written"); err != nil {
		return err
	}
	if err = s.mkdirAll(path.Dir(sidecarPath(key))); err != nil {
		return err
	}
	if err = s.checkNoSymlinks(sidecarPath(key)); err != nil {
		return err
	}
	if err = s.root.Rename(tmpName, sidecarPath(key)); err != nil {
		return fmt.Errorf("commit digest: %w", err)
	}
	if err = step("digest-renamed"); err != nil {
		return err
	}
	if s.fsync {
		return s.syncDir(path.Dir(sidecarPath(key)))
	}
	return nil
}

func (s *FileStore) openFile(key string) (*os.File, os.FileInfo, error) {
	if err := validateFileKey(key); err != nil {
		// A key the driver cannot store is, for reads, simply absent.
		return nil, nil, ErrNotFound
	}
	if err := s.checkNoSymlinks(dataPath(key)); err != nil {
		return nil, nil, ErrNotFound
	}
	file, err := s.root.OpenFile(dataPath(key), os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, ErrNotFound
	}
	return file, info, nil
}

func (s *FileStore) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	file, info, err := s.openFile(key)
	if err != nil {
		return nil, 0, err
	}
	return file, info.Size(), nil
}

type sectionReadCloser struct {
	*io.SectionReader
	file *os.File
}

func (r sectionReadCloser) Close() error { return r.file.Close() }

func (s *FileStore) OpenRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	file, info, err := s.openFile(key)
	if err != nil {
		return nil, 0, err
	}
	size := info.Size()
	if offset < 0 || length < 0 || offset > size || length > size-offset {
		_ = file.Close()
		return nil, 0, errors.New("object range is out of bounds")
	}
	return sectionReadCloser{io.NewSectionReader(file, offset, length), file}, size, nil
}

func (s *FileStore) Stat(ctx context.Context, key string) (Info, error) {
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	file, info, err := s.openFile(key)
	if err != nil {
		return Info{}, err
	}
	_ = file.Close()
	return Info{Size: info.Size(), Digest: s.readSidecar(key)}, nil
}

// readSidecar returns the recorded digest, or "" when there is none or the
// file is not a well-formed digest (an object without one reads as unverified).
func (s *FileStore) readSidecar(key string) string {
	if s.checkNoSymlinks(sidecarPath(key)) != nil {
		return ""
	}
	content, err := s.root.ReadFile(sidecarPath(key))
	if err != nil {
		return ""
	}
	digest := strings.TrimSpace(string(content))
	if _, parseErr := parseDigest(digest); parseErr != nil {
		return ""
	}
	return digest
}

func (s *FileStore) SetVerifiedDigest(ctx context.Context, key, digest string) error {
	if s.readOnly {
		return ErrReadOnly
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateFileKey(key); err != nil {
		return ErrNotFound
	}
	lock := s.lockFor(key)
	lock.Lock()
	defer lock.Unlock()
	file, _, err := s.openFile(key)
	if err != nil {
		return err
	}
	_ = file.Close()
	return s.writeSidecar(key, digest, nil)
}

func (s *FileStore) Delete(ctx context.Context, key string) error {
	if s.readOnly {
		return ErrReadOnly
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateFileKey(key); err != nil {
		return nil
	}
	lock := s.lockFor(key)
	lock.Lock()
	defer lock.Unlock()
	if err := s.checkNoSymlinks(dataPath(key)); err != nil {
		return err
	}
	if err := s.removeSidecar(key); err != nil {
		return err
	}
	if err := s.step("delete-sidecar-removed"); err != nil {
		return err
	}
	if err := s.root.Remove(dataPath(key)); err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		return err
	}
	if s.fsync {
		if err := s.syncDir(path.Dir(dataPath(key))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// List returns the keys under prefix in lexicographic order, skipping tmp/ and
// meta/ (which are outside data/) and anything that is not a regular file.
func (s *FileStore) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keys := make([]string, 0)
	err := fs.WalkDir(s.root.FS(), fsDataDir, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == fsDataDir {
			return nil
		}
		key := strings.TrimPrefix(name, fsDataDir+"/")
		if entry.IsDir() {
			// Descend only where a key under prefix can live.
			if !strings.HasPrefix(key+"/", prefix) && !strings.HasPrefix(prefix, key+"/") {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || !strings.HasPrefix(key, prefix) || validateFileKey(key) != nil {
			return nil
		}
		keys = append(keys, key)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	return keys, nil
}
