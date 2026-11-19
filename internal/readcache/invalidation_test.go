package readcache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeStream struct {
	payloads chan string
	closed   atomic.Bool
}

func (s *fakeStream) Next(ctx context.Context) (string, error) {
	select {
	case p, ok := <-s.payloads:
		if !ok {
			return "", errors.New("connection lost")
		}
		return p, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *fakeStream) Close() error { s.closed.Store(true); return nil }

func TestListenerEvictsOnNotificationAndFlushesOnReconnect(t *testing.T) {
	reg, cache, _ := newTestCache(time.Minute, Options[string]{Match: func(v, id string) bool { return v == id }})
	streams := make(chan *fakeStream, 4)
	connected := make(chan struct{}, 4)
	listener := &Listener{Registry: reg, ReconnectDelay: time.Millisecond, Connect: func(context.Context) (Stream, error) {
		s := &fakeStream{payloads: make(chan string, 4)}
		streams <- s
		connected <- struct{}{}
		return s, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); listener.Run(ctx) }()
	defer func() { cancel(); wg.Wait() }()

	first := <-streams
	<-connected
	var calls atomic.Int32
	_, _ = cache.Get(context.Background(), "a", counting("a", &calls))
	_, _ = cache.Get(context.Background(), "b", counting("b", &calls))
	first.payloads <- Payload("thing", "a")
	waitFor(t, func() bool { return cache.Len() == 1 })

	// A dropped connection loses notifications: everything is flushed.
	close(first.payloads)
	second := <-streams
	<-connected
	waitFor(t, func() bool { return cache.Len() == 0 && first.closed.Load() })
	_ = second
}

func TestLostNotifyIsBoundedByTTL(t *testing.T) {
	_, cache, clk := newTestCache(5*time.Second, Options[string]{})
	value := "allowed"
	load := func(context.Context) (string, error) { return value, nil }
	_, _ = cache.Get(context.Background(), "k", load)
	value = "revoked" // the change happened and its NOTIFY was dropped
	if v, _ := cache.Get(context.Background(), "k", load); v != "allowed" {
		t.Fatalf("expected the stale entry inside the window, got %q", v)
	}
	clk.Advance(5 * time.Second)
	if v, _ := cache.Get(context.Background(), "k", load); v != "revoked" {
		t.Fatalf("stale entry survived the TTL: %q", v)
	}
}

type fakeEpochs struct {
	mu     sync.Mutex
	epochs map[string]int64
	err    error
}

func (f *fakeEpochs) ReadEpochs(context.Context) (map[string]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]int64{}
	for k, v := range f.epochs {
		out[k] = v
	}
	return out, nil
}

func TestPollerInvalidatesMovedScopesAndFlushesOnFailure(t *testing.T) {
	reg, cache, _ := newTestCache(time.Minute, Options[string]{})
	other := New[string, string](reg, Options[string]{Name: "other", Scope: "other"})
	source := &fakeEpochs{epochs: map[string]int64{"thing": 1, "other": 1}}
	poller := &Poller{Registry: reg, Source: source}
	var calls atomic.Int32
	known := poller.Poll(context.Background(), nil)
	_, _ = cache.Get(context.Background(), "k", counting("v", &calls))
	_, _ = other.Get(context.Background(), "k", counting("v", &calls))
	known = poller.Poll(context.Background(), known)
	if cache.Len() != 1 || other.Len() != 1 {
		t.Fatal("an unchanged epoch evicted entries")
	}
	source.epochs["thing"] = 2
	known = poller.Poll(context.Background(), known)
	if cache.Len() != 0 || other.Len() != 1 {
		t.Fatalf("moved scope not isolated: %d %d", cache.Len(), other.Len())
	}
	_, _ = cache.Get(context.Background(), "k", counting("v", &calls))
	source.err = errors.New("db down")
	if known = poller.Poll(context.Background(), known); known != nil {
		t.Fatal("a failed poll must reset the baseline")
	}
	if cache.Len() != 0 || other.Len() != 0 {
		t.Fatal("a failed poll must flush every cache")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPollerRunPollsUntilCancelled(t *testing.T) {
	reg, cache, _ := newTestCache(time.Minute, Options[string]{})
	source := &fakeEpochs{epochs: map[string]int64{"thing": 1}}
	poller := &Poller{Registry: reg, Source: source, Interval: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { poller.Run(ctx); close(done) }()
	var calls atomic.Int32
	waitFor(t, func() bool { _, _ = cache.Get(context.Background(), "k", counting("v", &calls)); return cache.Len() == 1 })
	source.mu.Lock()
	source.epochs["thing"] = 2
	source.mu.Unlock()
	waitFor(t, func() bool { return cache.Len() == 0 })
	cancel()
	<-done
}

func TestListenerFlushesWhenConnectFails(t *testing.T) {
	reg, cache, _ := newTestCache(time.Minute, Options[string]{})
	var attempts atomic.Int32
	listener := &Listener{Registry: reg, ReconnectDelay: time.Millisecond, Connect: func(context.Context) (Stream, error) {
		attempts.Add(1)
		return nil, errors.New("database unavailable")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { listener.Run(ctx); close(done) }()
	var calls atomic.Int32
	_, _ = cache.Get(context.Background(), "k", counting("v", &calls))
	waitFor(t, func() bool { return attempts.Load() >= 2 && cache.Len() == 0 })
	cancel()
	<-done
}
