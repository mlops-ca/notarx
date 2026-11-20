// Package background tracks the goroutines of a node's background loops so a stopping process can wait
// for them before it closes the stores they write to (ADR 0023 section 7).
package background

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Group counts the goroutines started through Go whose context carries it.
type Group struct {
	wg     sync.WaitGroup
	active atomic.Int64
}

type groupKey struct{}

// WithGroup returns a context whose Go calls register with group.
func WithGroup(ctx context.Context, group *Group) context.Context {
	return context.WithValue(ctx, groupKey{}, group)
}

// Go runs fn in a new goroutine. When ctx carries a Group the goroutine is counted by it, so Wait
// returns only after fn did; without one it is a plain go statement.
func Go(ctx context.Context, fn func()) {
	group, _ := ctx.Value(groupKey{}).(*Group)
	if group == nil {
		go fn()
		return
	}
	group.wg.Add(1)
	group.active.Add(1)
	go func() {
		defer group.wg.Done()
		defer group.active.Add(-1)
		fn()
	}()
}

// Wait blocks until every counted goroutine returned or bound passed and reports whether they all
// returned. A bound of zero or less does not wait. Goroutines still running are not stopped: the
// loops stop with their context, and the bound is the safety net for a call that ignores it.
func (g *Group) Wait(bound time.Duration) bool {
	if bound <= 0 {
		return g.active.Load() == 0
	}
	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
