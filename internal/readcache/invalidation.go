package readcache

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/mlops-ca/notarx/internal/database"
)

// Channel is the PostgreSQL NOTIFY channel of cache invalidations. The payload is
// scope||':'||id (Payload).
const Channel = "notarx_cache"

// DefaultReconnectDelay is the pause before a lost listener connects again.
const DefaultReconnectDelay = time.Second

// Stream is one live notification subscription.
type Stream interface {
	// Next blocks for the next payload; any error ends the stream.
	Next(ctx context.Context) (string, error)
	Close() error
}

// Listener keeps a subscription to the invalidation channel and applies every
// payload to the registry. Notifications sent while it was disconnected are lost,
// so every cache is flushed when a stream ends and again once the next one is
// established, before it is read.
type Listener struct {
	Registry *Registry
	// Connect opens a subscription; the channel is already LISTENed on return.
	Connect func(ctx context.Context) (Stream, error)
	// ReconnectDelay defaults to DefaultReconnectDelay.
	ReconnectDelay time.Duration
	Logger         *slog.Logger
}

// Run serves until ctx is cancelled.
func (l *Listener) Run(ctx context.Context) {
	delay := l.ReconnectDelay
	if delay <= 0 {
		delay = DefaultReconnectDelay
	}
	for ctx.Err() == nil {
		stream, err := l.Connect(ctx)
		if err != nil {
			l.Registry.FlushAll()
			if l.Logger != nil && ctx.Err() == nil {
				l.Logger.Warn("read cache listener connect failed", "error", err)
			}
		} else {
			l.Registry.FlushAll()
			l.serve(ctx, stream)
			_ = stream.Close()
			l.Registry.FlushAll()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func (l *Listener) serve(ctx context.Context, stream Stream) {
	for {
		payload, err := stream.Next(ctx)
		if err != nil {
			if l.Logger != nil && ctx.Err() == nil {
				l.Logger.Warn("read cache listener disconnected", "error", err)
			}
			return
		}
		l.Registry.HandleNotification(payload)
	}
}

type pgStream struct{ conn *sql.Conn }

func (s pgStream) Next(ctx context.Context) (string, error) {
	_, payload, err := database.WaitForNotificationPayload(ctx, s.conn)
	return payload, err
}

func (s pgStream) Close() error { return s.conn.Close() }

// PostgresConnect returns a Listener.Connect that holds one dedicated connection
// of db (the notification pool of ADR 0022).
func PostgresConnect(db *sql.DB) func(context.Context) (Stream, error) {
	return func(ctx context.Context) (Stream, error) {
		conn, err := db.Conn(ctx)
		if err != nil {
			return nil, err
		}
		if err := database.ListenChannels(ctx, conn, Channel); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return pgStream{conn: conn}, nil
	}
}

// EpochSource reads the cache_epochs table of an engine without push
// notifications (MySQL): scope to epoch.
type EpochSource interface {
	ReadEpochs(ctx context.Context) (map[string]int64, error)
}

// Poller invalidates scopes whose polled epoch moved. Because the table cannot
// prove freshness when it cannot be read, a failed poll flushes every cache; the
// first poll is the baseline and flushes too.
type Poller struct {
	Registry *Registry
	Source   EpochSource
	Interval time.Duration
	Logger   *slog.Logger
}

// DefaultPollInterval is NOTARX_CACHE_POLL_INTERVAL.
const DefaultPollInterval = 2 * time.Second

// Run serves until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	var known map[string]int64
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		known = p.Poll(ctx, known)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Poll applies one reading and returns the epochs to compare the next one with.
func (p *Poller) Poll(ctx context.Context, known map[string]int64) map[string]int64 {
	current, err := p.Source.ReadEpochs(ctx)
	if err != nil {
		p.Registry.FlushAll()
		if p.Logger != nil && ctx.Err() == nil {
			p.Logger.Warn("read cache epoch poll failed", "error", err)
		}
		return nil
	}
	if known == nil {
		p.Registry.FlushAll()
		return current
	}
	for scope, epoch := range current {
		if previous, ok := known[scope]; !ok || previous != epoch {
			p.Registry.Invalidate(scope, "")
		}
	}
	return current
}
