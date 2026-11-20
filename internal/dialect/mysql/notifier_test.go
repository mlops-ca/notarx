package mysql

import (
	"context"
	"testing"
	"time"
)

func subscribe(t *testing.T, n *Notifier, channels ...string) *subscription {
	t.Helper()
	s, err := n.Subscribe(context.Background(), nil, channels...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.(*subscription)
}

func TestNotifierDefaultsAndValidation(t *testing.T) {
	n := NewNotifier(Config{})
	if n.interval != DefaultPollInterval || n.maxInterval != MaxPollInterval {
		t.Fatalf("defaults = %s %s", n.interval, n.maxInterval)
	}
	if n := NewNotifier(Config{Interval: 10 * time.Second}); n.maxInterval != 10*time.Second {
		t.Fatalf("an interval above the ceiling must raise it: %s", n.maxInterval)
	}
	if _, err := n.Subscribe(context.Background(), nil); err == nil {
		t.Error("a subscription without channels was accepted")
	}
	if _, err := n.Subscribe(context.Background(), nil, "a", ""); err == nil {
		t.Error("an empty channel was accepted")
	}
}

func TestNotifyWakesTheSubscribersOfTheChannel(t *testing.T) {
	n := NewNotifier(Config{Interval: time.Hour})
	a, b, other := subscribe(t, n, "jobs"), subscribe(t, n, "jobs", "mail"), subscribe(t, n, "unrelated")
	n.Notify("jobs")
	n.Notify("jobs") // a second wake-up while one is pending is absorbed
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, s := range []*subscription{a, b} {
		if channel, err := s.Wait(ctx); err != nil || channel != "jobs" {
			t.Fatalf("Wait = %q, %v", channel, err)
		}
	}
	short, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	if channel, err := other.Wait(short); err == nil {
		t.Fatalf("an unrelated subscriber woke on %q", channel)
	}
}

func TestIdlePollBacksOffAndResets(t *testing.T) {
	n := NewNotifier(Config{Interval: 10 * time.Millisecond, MaxInterval: 40 * time.Millisecond})
	s := subscribe(t, n, "a", "b")
	var channels []string
	for range 6 {
		channel, err := s.Wait(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		channels = append(channels, channel)
	}
	if channels[0] != "a" || channels[1] != "b" || channels[2] != "a" {
		t.Errorf("poll wake-ups must visit every channel in turn: %v", channels)
	}
	s.mu.Lock()
	idle := s.current
	s.mu.Unlock()
	if idle != 40*time.Millisecond {
		t.Fatalf("the idle interval = %s; want the ceiling", idle)
	}
	s.Reset()
	s.mu.Lock()
	reset := s.current
	s.mu.Unlock()
	if reset != 10*time.Millisecond {
		t.Fatalf("after work the interval = %s; want the cadence", reset)
	}
	n.Notify("a")
	if _, err := s.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	woken := s.current
	s.mu.Unlock()
	if woken != 10*time.Millisecond {
		t.Fatalf("a local wake-up must reset the interval: %s", woken)
	}
}

func TestWaitEndsWithItsContext(t *testing.T) {
	s := subscribe(t, NewNotifier(Config{Interval: time.Hour}), "a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Wait(ctx); err == nil {
		t.Fatal("a cancelled context did not end the wait")
	}
}

func TestCloseUnregistersAndIsIdempotent(t *testing.T) {
	n := NewNotifier(Config{Interval: time.Hour})
	s := subscribe(t, n, "a", "b")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	left := len(n.subscribers)
	n.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d channels still have subscribers", left)
	}
	n.Notify("a") // no subscriber, no panic
}

func TestJitterStaysNearTheInterval(t *testing.T) {
	for range 200 {
		if got := jittered(time.Second); got < 800*time.Millisecond || got > 1200*time.Millisecond {
			t.Fatalf("jittered = %s", got)
		}
	}
	if jittered(0) != 0 || jittered(3) != 3 {
		t.Error("a tiny interval must pass through")
	}
}
