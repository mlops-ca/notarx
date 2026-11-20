package main

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/background"
	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/lifecycle"
)

func TestAPTReclaimWorkerRequiresBothFormatAndKindAdmission(t *testing.T) {
	for _, test := range []struct {
		name    string
		formats []string
		kinds   []string
		want    bool
	}{
		{name: "enabled", formats: []string{"apt"}, kinds: []string{"reclaim"}, want: true},
		{name: "format excluded", formats: []string{"raw"}, kinds: []string{"reclaim"}},
		{name: "kind excluded", formats: []string{"apt"}, kinds: []string{"replication"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.Config{NodeRoles: []config.NodeRole{config.NodeRoleWorker}, WorkerFormats: test.formats, WorkerKinds: test.kinds}
			if got := aptReclaimWorkerEnabled(cfg); got != test.want {
				t.Fatalf("aptReclaimWorkerEnabled()=%t want=%t", got, test.want)
			}
		})
	}
}

func TestGoReclaimWorkerRequiresBothFormatAndKindAdmission(t *testing.T) {
	for _, test := range []struct {
		name    string
		formats []string
		kinds   []string
		want    bool
	}{
		{name: "enabled", formats: []string{"go"}, kinds: []string{"reclaim"}, want: true},
		{name: "format excluded", formats: []string{"raw"}, kinds: []string{"reclaim"}},
		{name: "kind excluded", formats: []string{"go"}, kinds: []string{"replication"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.Config{NodeRoles: []config.NodeRole{config.NodeRoleWorker}, WorkerFormats: test.formats, WorkerKinds: test.kinds}
			if got := goReclaimWorkerEnabled(cfg); got != test.want {
				t.Fatalf("goReclaimWorkerEnabled()=%t want=%t", got, test.want)
			}
		})
	}
}

func TestShutdownWaitBoundStaysInsideTheTerminationGrace(t *testing.T) {
	for _, test := range []struct {
		name             string
		sinceStop, flush time.Duration
		want             time.Duration
	}{
		{name: "prompt shutdown gets the full wait", sinceStop: time.Second, flush: 10 * time.Second, want: lifecycle.ShutdownWait},
		{name: "a slow server shrinks the wait", sinceStop: 13 * time.Second, flush: 10 * time.Second, want: 5 * time.Second},
		{name: "never below a second", sinceStop: 30 * time.Second, flush: 10 * time.Second, want: time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := shutdownWaitBound(test.sinceStop, test.flush)
			if got != test.want {
				t.Fatalf("shutdownWaitBound(%s, %s) = %s, want %s", test.sinceStop, test.flush, got, test.want)
			}
			if got > lifecycle.ShutdownWait || (got > time.Second && test.sinceStop+got+test.flush > terminationGrace) {
				t.Fatalf("bound %s leaves no room inside the %s grace", got, terminationGrace)
			}
		})
	}
}

func TestAwaitBackgroundWaitsForEveryLoopAndTheBroker(t *testing.T) {
	group := &background.Group{}
	ctx, cancel := context.WithCancel(background.WithGroup(context.Background(), group))
	loopStopped := make(chan struct{})
	background.Go(ctx, func() {
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond) // a loop finishing its last write
		close(loopStopped)
	})
	brokerDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(brokerDone)
	}()
	cancel()
	if !awaitBackground(brokerDone, group, 5*time.Second) {
		t.Fatal("the loops stopped within the bound")
	}
	select {
	case <-loopStopped:
	default:
		t.Fatal("awaitBackground returned before the loop finished its last write")
	}
	if !awaitBackground(nil, group, time.Second) {
		t.Fatal("a node without a claim broker waits for its loops only")
	}
}

func TestAwaitBackgroundGivesUpAtTheBound(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	group := &background.Group{}
	background.Go(background.WithGroup(context.Background(), group), func() { <-release })
	if awaitBackground(nil, group, 20*time.Millisecond) {
		t.Fatal("a loop that ignores its context must hit the bound")
	}
	if awaitBackground(make(chan struct{}), &background.Group{}, 20*time.Millisecond) {
		t.Fatal("a broker that never stops must hit the bound")
	}
}

// terminationGrace is the Kubernetes grace period less a margin; the manifest and the constant must not drift.
func TestTerminationGraceFollowsTheKubernetesManifest(t *testing.T) {
	manifest, err := os.ReadFile("../../deploy/kubernetes/base/notarx.yaml")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`terminationGracePeriodSeconds:\s*(\d+)`).FindSubmatch(manifest)
	if match == nil {
		t.Fatal("the manifest sets no terminationGracePeriodSeconds")
	}
	seconds, err := strconv.Atoi(string(match[1]))
	if err != nil {
		t.Fatal(err)
	}
	grace := time.Duration(seconds) * time.Second
	if terminationGrace >= grace || grace-terminationGrace > 5*time.Second {
		t.Fatalf("terminationGrace=%s does not leave a small margin inside the manifest's %s", terminationGrace, grace)
	}
}

func TestSignalContextIsCancelledWithTheShutdownCause(t *testing.T) {
	ctx, stop := signalContext()
	if ctx.Err() != nil {
		t.Fatal("the context starts live")
	}
	stop()
	<-ctx.Done()
	if !errors.Is(context.Cause(ctx), background.ErrShutdown) {
		t.Fatalf("cause = %v, want background.ErrShutdown", context.Cause(ctx))
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	if !errors.Is(context.Cause(child), background.ErrShutdown) {
		t.Fatalf("a job context derived from it must carry the cause: %v", context.Cause(child))
	}
	other, cancelOther := context.WithCancel(context.Background())
	cancelOther()
	if errors.Is(context.Cause(other), background.ErrShutdown) {
		t.Fatal("a context cancelled for another reason must not carry the shutdown cause")
	}
}
