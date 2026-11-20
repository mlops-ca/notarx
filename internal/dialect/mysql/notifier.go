package mysql

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/mlops-ca/notarx/internal/dialect"
)

// Polling defaults (ADR 0022): NOTARX_DATABASE_POLL_INTERVAL is the cadence while work flows, an
// idle poller backs off with jitter up to MaxPollInterval and starts again from the cadence on work.
const (
	DefaultPollInterval = 500 * time.Millisecond
	MaxPollInterval     = 5 * time.Second
)

// Config tunes the polling Notifier; the zero value is the defaults.
type Config struct {
	Interval    time.Duration
	MaxInterval time.Duration
}

// Notifier is the MySQL form of LISTEN/NOTIFY: there is none, so a subscription wakes its worker at the
// poll cadence and the worker looks at the queue tables itself. A same-process enqueue calls Notify,
// which wakes the subscribers of the channel at once, so single-node latency stays low; across
// replicas the latency is up to MaxInterval. No notification table is written (ADR 0022).
type Notifier struct {
	interval, maxInterval time.Duration
	mu                    sync.Mutex
	subscribers           map[string]map[*subscription]struct{}
}

// NewNotifier returns a Notifier; zero or inverted durations fall back to the defaults.
func NewNotifier(config Config) *Notifier {
	if config.Interval <= 0 {
		config.Interval = DefaultPollInterval
	}
	if config.MaxInterval < config.Interval {
		config.MaxInterval = max(MaxPollInterval, config.Interval)
	}
	return &Notifier{interval: config.Interval, maxInterval: config.MaxInterval, subscribers: map[string]map[*subscription]struct{}{}}
}

// Notify wakes the subscribers of channel in this process.
func (n *Notifier) Notify(channel string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for s := range n.subscribers[channel] {
		select {
		case s.wake <- channel:
		default: // a wake-up is already pending
		}
	}
}

// Subscribe registers for channels. The database is not touched: the worker polls its own tables.
func (n *Notifier) Subscribe(_ context.Context, _ *sql.DB, channels ...string) (dialect.Subscription, error) {
	if len(channels) == 0 {
		return nil, errors.New("subscribe needs at least one channel")
	}
	for _, channel := range channels {
		if channel == "" {
			return nil, errors.New("notification channel is empty")
		}
	}
	s := &subscription{notifier: n, channels: append([]string(nil), channels...), wake: make(chan string, 1), current: n.interval}
	n.mu.Lock()
	for _, channel := range channels {
		if n.subscribers[channel] == nil {
			n.subscribers[channel] = map[*subscription]struct{}{}
		}
		n.subscribers[channel][s] = struct{}{}
	}
	n.mu.Unlock()
	return s, nil
}

type subscription struct {
	notifier *Notifier
	channels []string
	wake     chan string
	mu       sync.Mutex
	current  time.Duration
	next     int
	closed   bool
}

// Wait returns a channel name when Notify woke it or the poll interval passed; the caller then looks at
// its queue. Each idle return doubles the interval (with jitter) up to the ceiling; a wake-up or Reset
// brings it back to the cadence. It returns ctx.Err() when the context ends.
func (s *subscription) Wait(ctx context.Context) (string, error) {
	s.mu.Lock()
	wait := s.current
	s.mu.Unlock()
	timer := time.NewTimer(jittered(wait))
	defer timer.Stop()
	select {
	case channel := <-s.wake:
		s.Reset()
		return channel, nil
	case <-timer.C:
		s.mu.Lock()
		s.current = min(s.current*2, s.notifier.maxInterval)
		channel := s.channels[s.next%len(s.channels)]
		s.next++
		s.mu.Unlock()
		return channel, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Reset returns the poll interval to the cadence. A worker calls it after it found work, so a busy
// queue is polled at the cadence and an idle one backs off.
func (s *subscription) Reset() {
	s.mu.Lock()
	s.current = s.notifier.interval
	s.mu.Unlock()
}

// Close unregisters the subscription; it is safe to call twice.
func (s *subscription) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.notifier.mu.Lock()
	defer s.notifier.mu.Unlock()
	for _, channel := range s.channels {
		delete(s.notifier.subscribers[channel], s)
		if len(s.notifier.subscribers[channel]) == 0 {
			delete(s.notifier.subscribers, channel)
		}
	}
	return nil
}

// jittered spreads d by up to 20 percent either way, so replicas do not poll in step.
func jittered(d time.Duration) time.Duration {
	spread := int64(d) / 5
	if spread <= 0 {
		return d
	}
	return d - time.Duration(spread) + time.Duration(rand.Int64N(2*spread+1))
}
