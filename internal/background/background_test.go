package background

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestGoWithoutAGroupRunsTheFunction(t *testing.T) {
	ran := make(chan struct{})
	Go(context.Background(), func() { close(ran) })
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the function never ran")
	}
}

func TestWaitReturnsAfterEveryTrackedGoroutine(t *testing.T) {
	group := &Group{}
	ctx, cancel := context.WithCancel(WithGroup(context.Background(), group))
	var finished atomic.Int32
	for range 3 {
		Go(ctx, func() {
			<-ctx.Done()
			// A loop may start more work while it stops; that work is counted too.
			Go(ctx, func() { finished.Add(1) })
			finished.Add(1)
		})
	}
	if group.Wait(0) {
		t.Fatal("Wait(0) must not report loops that are still running as done")
	}
	cancel()
	if !group.Wait(5 * time.Second) {
		t.Fatal("the loops returned within the bound")
	}
	if got := finished.Load(); got != 6 {
		t.Fatalf("finished = %d, want 6", got)
	}
	if !group.Wait(0) {
		t.Fatal("an idle group is done")
	}
}

func TestWaitGivesUpAtTheBound(t *testing.T) {
	group := &Group{}
	release := make(chan struct{})
	Go(WithGroup(context.Background(), group), func() { <-release })
	start := time.Now()
	if group.Wait(50 * time.Millisecond) {
		t.Fatal("a loop that ignores its context must not be reported done")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("Wait overran its bound")
	}
	close(release)
	if !group.Wait(5 * time.Second) {
		t.Fatal("the loop returned once released")
	}
}
