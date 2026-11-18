package backupops

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mlops-ca/notarx/internal/backupmanifest"
	"github.com/mlops-ca/notarx/internal/objectstore"
)

// filesystemDestination writes restored objects into a filesystem root.
type filesystemDestination struct{ store *objectstore.FileStore }

func (d filesystemDestination) Put(ctx context.Context, key string, r io.ReadSeeker, size int64, digest string) error {
	return d.store.PutVerifiedReader(ctx, key, r, size, digest)
}

func (d filesystemDestination) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	return d.store.Open(ctx, key)
}

// RestoreFilesystemObjects restores the object bytes of a verified bundle into
// a new filesystem root: the directory must not exist or must be empty. Each
// object is written through the filesystem driver, read back and compared with
// the bundle, and an fs-bytes-v1 bundle's root ID is written to the new root so
// that a database restored from the same backup still recognises it. The
// database itself is restored separately (pg_restore); this does not touch it.
// On failure everything this call created in root is removed.
func RestoreFilesystemObjects(ctx context.Context, directory, root string) (TransferReport, error) {
	m, err := LoadManifest(directory)
	r := report(m.BackupID)
	if err != nil {
		return fail(r, "bundle_input_invalid")
	}
	if checked := backupmanifest.Verify(ctx, directory, m); checked.Integrity != backupmanifest.Verified || checked.WriterEvidence != "declared" {
		return fail(r, "bundle_input_invalid")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fail(r, "target_root_invalid")
	}
	created := false
	switch entries, readErr := os.ReadDir(root); {
	case errors.Is(readErr, os.ErrNotExist):
		created = true
	case readErr != nil || len(entries) != 0:
		return fail(r, "target_not_empty")
	}
	cleanup := func() {
		if created {
			_ = os.RemoveAll(root)
			return
		}
		if entries, readErr := os.ReadDir(root); readErr == nil {
			for _, entry := range entries {
				_ = os.RemoveAll(filepath.Join(root, entry.Name()))
			}
		}
	}
	cfg := objectstore.Defaults()
	cfg.Driver, cfg.FSRoot = objectstore.DriverFilesystem, root
	var store *objectstore.FileStore
	if m.Objects.RootID != "" {
		store, err = objectstore.NewFileStoreWithRootID(cfg, m.Objects.RootID)
	} else {
		store, err = objectstore.NewFileStore(cfg)
	}
	if err != nil {
		cleanup()
		return fail(r, "target_root_unavailable")
	}
	defer func() { _ = store.Close() }()
	bundle, err := os.OpenRoot(directory)
	if err != nil {
		cleanup()
		return fail(r, "bundle_input_invalid")
	}
	defer func() { _ = bundle.Close() }()
	if reason := restoreObjects(ctx, bundle, m, filesystemDestination{store}, &r); reason != "" {
		cleanup()
		return fail(r, reason)
	}
	r.Database = "not_applicable"
	r.Metadata = "not_applicable"
	r.Consistency = "operator_declared_scope"
	r.Status = "restored"
	r.Objects = "verified"
	return r, nil
}

// runRestoreObjects is the "restore-objects" action of the backup CLI.
func runRestoreObjects(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("backup restore-objects", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	directory := flags.String("bundle", "", "existing backup directory")
	root := flags.String("root", "", "new or empty absolute filesystem object root")
	if err := flags.Parse(args); err != nil || *directory == "" || *root == "" || flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "usage: gateway backup restore-objects --bundle directory --root /absolute/new/object/root")
		return 2
	}
	result, err := RestoreFilesystemObjects(ctx, *directory, *root)
	if encodeErr := writeReport(stdout, result); encodeErr != nil {
		_, _ = fmt.Fprintln(stderr, "backup report could not be written")
		return 2
	}
	if err != nil {
		return 1
	}
	return 0
}
