//go:build !unix

package objectstore

import "errors"

// Capacity is not available on this platform; writes are never refused for
// lack of space and readiness reports the driver as unable to measure it.
func (s *FileStore) Capacity() (Capacity, error) {
	return Capacity{}, errors.New("filesystem capacity is not supported on this platform")
}
