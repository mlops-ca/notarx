package backupops

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mlops-ca/notarx/internal/backupmanifest"
)

// tarBlock is the size of a tar header block and of the padding unit.
const tarBlock = 512

// countingWriter counts bytes written through it, so the offset of an entry's
// data inside a segment is known without parsing the archive.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// segmentWriter packs objects into tar segments for the fs-bytes-v1 format. A
// segment closes once the next object would take it past limit, so a bundle
// holds about total/limit segment files (plus one per object larger than the
// limit, which always gets a segment of its own).
type segmentWriter struct {
	directory string
	limit     int64
	file      *os.File
	counter   *countingWriter
	archive   *tar.Writer
	name      string
	finished  []backupmanifest.File
}

func newSegmentWriter(directory string, limit int64) *segmentWriter {
	if limit <= 0 {
		limit = backupmanifest.DefaultSegmentBytes
	}
	return &segmentWriter{directory: directory, limit: limit}
}

// segmentPadded is the space an entry of size bytes takes in an archive,
// excluding extended header blocks that a long name may add.
func segmentPadded(size int64) int64 {
	return tarBlock + (size+tarBlock-1)/tarBlock*tarBlock
}

func (w *segmentWriter) open() error {
	w.name = fmt.Sprintf("objects/segment-%06d.tar", len(w.finished)+1)
	file, err := os.OpenFile(filepath.Join(w.directory, w.name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	w.file = file
	w.counter = &countingWriter{w: file}
	w.archive = tar.NewWriter(w.counter)
	return nil
}

// closeSegment finishes the current segment and records its size and digest.
func (w *segmentWriter) closeSegment() error {
	if w.file == nil {
		return nil
	}
	closeErr := w.archive.Close()
	syncErr := w.file.Sync()
	fileErr := w.file.Close()
	name := w.name
	w.file, w.archive, w.counter = nil, nil, nil
	if closeErr != nil || syncErr != nil || fileErr != nil {
		return errors.New("segment write failed")
	}
	described, err := describeFile(w.directory, name)
	if err != nil {
		return err
	}
	w.finished = append(w.finished, described)
	return nil
}

// add writes one object into the current segment, starting a new one when it
// would not fit. It returns the inventory line for the object.
func (w *segmentWriter) add(key string, size int64, body io.Reader) (backupmanifest.Object, error) {
	if w.file != nil && w.counter.n > 0 && w.counter.n+segmentPadded(size) > w.limit {
		if err := w.closeSegment(); err != nil {
			return backupmanifest.Object{}, err
		}
	}
	if w.file == nil {
		if err := w.open(); err != nil {
			return backupmanifest.Object{}, err
		}
	}
	header := &tar.Header{Name: key, Mode: 0o600, Size: size, Typeflag: tar.TypeReg, Format: tar.FormatPAX}
	if err := w.archive.WriteHeader(header); err != nil {
		return backupmanifest.Object{}, err
	}
	offset := w.counter.n
	hasher := sha256.New()
	copied, err := io.Copy(io.MultiWriter(w.archive, hasher), io.LimitReader(body, size+1))
	if err != nil || copied != size {
		return backupmanifest.Object{}, errors.New("object export failed")
	}
	if err = w.archive.Flush(); err != nil {
		return backupmanifest.Object{}, err
	}
	sum := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	return backupmanifest.Object{Key: key, File: backupmanifest.File{Path: w.name, Size: &size, SHA256: sum}, Offset: &offset}, nil
}

// finish closes the last segment and returns every segment in order.
func (w *segmentWriter) finish() ([]backupmanifest.File, error) {
	if err := w.closeSegment(); err != nil {
		return nil, err
	}
	return w.finished, nil
}

// abort closes an open segment without recording it, for the failure path.
func (w *segmentWriter) abort() {
	if w.file != nil {
		_ = w.archive.Close()
		_ = w.file.Close()
		w.file, w.archive, w.counter = nil, nil, nil
	}
}
