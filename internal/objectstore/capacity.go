package objectstore

import "math"

// Capacity is the free-space view of the filesystem driver, exposed to
// readiness and metrics.
type Capacity struct {
	TotalBytes int64
	// FreeBytes is the space available to the gateway's user.
	FreeBytes    int64
	ReserveBytes int64
}

// BelowReserve reports whether writes are currently refused.
func (c Capacity) BelowReserve() bool { return c.ReserveBytes > 0 && c.FreeBytes < c.ReserveBytes }

func saturatingMul(blocks, unit uint64) int64 {
	if unit != 0 && blocks > uint64(math.MaxInt64)/unit {
		return math.MaxInt64
	}
	return int64(blocks * unit)
}
