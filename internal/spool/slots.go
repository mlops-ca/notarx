package spool

import (
	"errors"
	"sync"
)

// ErrCapacity reports that every staging slot is in use.
var ErrCapacity = errors.New("spool capacity is full")

// Slots bounds the number of concurrent cache-miss staging lifecycles owned by
// one process. Admission is fail-fast so a saturated Gateway does not start
// another potentially large upstream transfer.
type Slots struct{ slots chan struct{} }

// NewSlots returns a limiter of n concurrent lifecycles (n must be positive).
func NewSlots(n int) *Slots { return &Slots{slots: make(chan struct{}, n)} }

// Acquire reserves one slot. The returned release is idempotent. A nil Slots
// admits everything.
func (s *Slots) Acquire() (func(), error) {
	if s == nil || s.slots == nil {
		return func() {}, nil
	}
	select {
	case s.slots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-s.slots }) }, nil
	default:
		return nil, ErrCapacity
	}
}
