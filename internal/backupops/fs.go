package backupops

import (
	"context"
	"errors"
	"io"

	"github.com/mlops-ca/notarx/internal/objectstore"
)

// FilesystemSettings names the object root of a filesystem-driver gateway. As a
// source it is opened read-only and only while the gateway that owns it is
// stopped; the export carries the objects in the fs-bytes-v1 format.
type FilesystemSettings struct {
	Root string `json:"root"`
	// SegmentBytes is the target size of one tar segment; 0 selects 256 MiB.
	SegmentBytes int64 `json:"segmentBytes,omitempty"`
}

// FilesystemObjects reads the objects of a filesystem root in key order and is
// the object half of a frozen source for the fs-bytes-v1 export.
type FilesystemObjects struct {
	store        *objectstore.FileStore
	segmentBytes int64
}

// NewFilesystemObjects opens the root read-only; it must be an initialised root.
func NewFilesystemObjects(s FilesystemSettings) (*FilesystemObjects, error) {
	if s.SegmentBytes < 0 {
		return nil, errors.New("invalid explicit filesystem settings")
	}
	store, err := objectstore.OpenFileStoreReadOnly(s.Root)
	if err != nil {
		return nil, errors.New("invalid explicit filesystem settings")
	}
	return &FilesystemObjects{store: store, segmentBytes: s.SegmentBytes}, nil
}

func (o *FilesystemObjects) WalkObjects(ctx context.Context, visit func(string, int64) error) error {
	keys, err := o.store.List(ctx, "")
	if err != nil {
		return errors.New("object listing failed")
	}
	for _, key := range keys {
		info, statErr := o.store.Stat(ctx, key)
		if statErr != nil {
			return errors.New("object listing unordered or invalid")
		}
		if err = visit(key, info.Size); err != nil {
			return err
		}
	}
	return nil
}

func (o *FilesystemObjects) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	return o.store.Open(ctx, key)
}

// ObjectFormat selects fs-bytes-v1 for the export.
func (o *FilesystemObjects) ObjectFormat() string { return "fs-bytes-v1" }

// SegmentBytes is the configured tar segment size, 0 for the default.
func (o *FilesystemObjects) SegmentBytes() int64 { return o.segmentBytes }

// RootID is the identity recorded in the exported manifest.
func (o *FilesystemObjects) RootID() string { return o.store.RootID() }

func (o *FilesystemObjects) Close() error { return o.store.Close() }
