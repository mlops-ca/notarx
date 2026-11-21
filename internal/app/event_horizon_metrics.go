package app

import (
	"io"
	"strconv"
	"sync/atomic"
	"time"
)

// EventHorizonLag holds the notarx_event_horizon_lag_seconds gauge, embedded in Metrics (ADR 0021, Domain events).
type EventHorizonLag struct {
	horizonLagMillis atomic.Int64
}

// SetEventHorizonLag records how long the oldest emitting transaction has held the event read horizon back; it is
// the Dispatcher's ObserveLag.
func (c *EventHorizonLag) SetEventHorizonLag(lag time.Duration) {
	c.horizonLagMillis.Store(lag.Milliseconds())
}

func (c *EventHorizonLag) writeEventHorizonLagMetrics(w io.Writer) {
	_, _ = w.Write([]byte("# HELP notarx_event_horizon_lag_seconds Age of the oldest open transaction that has emitted a domain event; events at or after it are not delivered to consumers until it ends.\n" +
		"# TYPE notarx_event_horizon_lag_seconds gauge\n" +
		"notarx_event_horizon_lag_seconds " + strconv.FormatFloat(float64(c.horizonLagMillis.Load())/1000, 'f', 3, 64) + "\n"))
}
