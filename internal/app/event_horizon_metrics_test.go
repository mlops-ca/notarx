package app

import (
	"strings"
	"testing"
	"time"
)

func TestEventHorizonLagGauge(t *testing.T) {
	var lag EventHorizonLag
	lag.SetEventHorizonLag(90*time.Second + 500*time.Millisecond)
	var out strings.Builder
	lag.writeEventHorizonLagMetrics(&out)
	if !strings.Contains(out.String(), "# TYPE notarx_event_horizon_lag_seconds gauge\nnotarx_event_horizon_lag_seconds 90.500\n") {
		t.Fatalf("metrics = %q", out.String())
	}
}
