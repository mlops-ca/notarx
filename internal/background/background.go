// Package background tracks the goroutines of a node's background loops so a stopping process can wait
// for them before it closes the stores they write to (ADR 0023 section 7).
package background

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrShutdown is the cancellation cause of the context a stopping process cancels its background work with. A
// worker that sees it knows its job was interrupted by the stop, not that it failed or timed out
// (context.Cause(ctx) reports it).
var ErrShutdown = errors.New("process is shutting down")

// Group counts the goroutines started through Go whose context carries it. Go is safe at any time,
// also while another goroutine waits and also after every counted goroutine returned.
type Group struct {
	mu      sync.Mutex
	running int
	// idle is closed when running drops to zero; it exists only while a Wait is pending.
	idle chan struct{}
}

type groupKey struct{}

// WithGroup returns a context whose Go calls register with group.
func WithGroup(ctx context.Context, group *Group) context.Context {
	return context.WithValue(ctx, groupKey{}, group)
}

// GroupOf returns the group ctx carries, or nil.
func GroupOf(ctx context.Context) *Group {
	group, _ := ctx.Value(groupKey{}).(*Group)
	return group
}

// Go runs fn in a new goroutine. When ctx carries a Group the goroutine is counted by it, so Wait
// returns only after fn did; without one it is a plain go statement.
func Go(ctx context.Context, fn func()) {
	group := GroupOf(ctx)
	if group == nil {
		go fn()
		return
	}
	group.mu.Lock()
	group.running++
	group.mu.Unlock()
	go func() {
		defer group.done()
		fn()
	}()
}

func (g *Group) done() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.running--
	if g.running == 0 && g.idle != nil {
		close(g.idle)
		g.idle = nil
	}
}

// Wait blocks until every counted goroutine returned or bound passed and reports whether they all
// returned. A bound of zero or less does not wait. Goroutines still running are not stopped: the
// loops stop with their context, and the bound is the safety net for a call that ignores it.
func (g *Group) Wait(bound time.Duration) bool {
	g.mu.Lock()
	if g.running == 0 {
		g.mu.Unlock()
		return true
	}
	if bound <= 0 {
		g.mu.Unlock()
		return false
	}
	if g.idle == nil {
		g.idle = make(chan struct{})
	}
	idle := g.idle
	g.mu.Unlock()
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case <-idle:
		return true
	case <-timer.C:
		return false
	}
}
