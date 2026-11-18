package backupops

import (
	"errors"
	"io"
	"os"

	"github.com/mlops-ca/notarx/internal/backupmanifest"
)

// openSegment holds the one fs-bytes-v1 segment the restore is reading. The
// inventory is in key order and segments are written in key order, so a
// segment is opened, verified whole against the manifest digest, and read
// through once before the next one is needed.
type openSegment struct {
	path string
	file *os.File
	size int64
}

// object returns a reader over one object's bytes inside the segment, opening
// and verifying the segment the first time it is used.
func (s *openSegment) object(root *os.Root, declared map[string]backupmanifest.File, object backupmanifest.Object) (io.ReadSeeker, error) {
	if object.Offset == nil || object.File.Size == nil || *object.Offset < 0 || *object.File.Size < 0 {
		return nil, errors.New("invalid segment entry")
	}
	if s.path != object.File.Path {
		s.close()
		info, ok := declared[object.File.Path]
		if !ok {
			return nil, errors.New("undeclared segment")
		}
		file, err := checkedFile(root, info)
		if err != nil {
			return nil, err
		}
		s.path, s.file, s.size = object.File.Path, file, *info.Size
	}
	if *object.Offset > s.size || *object.File.Size > s.size-*object.Offset {
		return nil, errors.New("segment entry out of range")
	}
	return io.NewSectionReader(s.file, *object.Offset, *object.File.Size), nil
}

func (s *openSegment) close() {
	if s.file != nil {
		_ = s.file.Close()
	}
	s.path, s.file, s.size = "", nil, 0
}
