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
