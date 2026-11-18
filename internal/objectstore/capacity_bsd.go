//go:build unix && !linux

package objectstore

import "golang.org/x/sys/unix"

// Capacity reports the free space on the filesystem backing the store.
func (s *FileStore) Capacity() (Capacity, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(s.rootPath, &stat); err != nil {
		return Capacity{}, err
	}
	unit := uint64(stat.Bsize)
	return Capacity{
		TotalBytes:   saturatingMul(uint64(stat.Blocks), unit),
		FreeBytes:    saturatingMul(uint64(stat.Bavail), unit),
		ReserveBytes: s.reserve,
	}, nil
}
