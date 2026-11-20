package repository

import (
	"context"
	"testing"
	"time"
)

func TestTerminalWriteContextSurvivesTheCancellationOfItsParent(t *testing.T) {
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), struct{}{}, "kept"))
	cancel()
	ctx, release := terminalWriteContext(parent)
	defer release()
	if err := ctx.Err(); err != nil {
		t.Fatalf("a final write must not start cancelled: %v", err)
	}
	if ctx.Value(struct{}{}) != "kept" {
		t.Fatal("request-scoped values must survive for the log lines of the write")
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > LifecycleTerminalWriteTimeout {
		t.Fatalf("the write must be bounded by %s, got deadline=%v ok=%v", LifecycleTerminalWriteTimeout, deadline, ok)
	}
}
