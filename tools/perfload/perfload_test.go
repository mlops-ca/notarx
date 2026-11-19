package perfload

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const metricsBody = `# TYPE notarx_audit_buffer_depth gauge
notarx_audit_buffer_depth 3
notarx_audit_dropped_total{class="read",reason="buffer_full"} 2
notarx_audit_dropped_total{class="security",reason="overload"} 0
notarx_audit_flush_seconds_bucket{le="0.5"} %d
notarx_audit_flush_seconds_bucket{le="1"} %d
notarx_audit_flush_seconds_bucket{le="+Inf"} %d
notarx_audit_flush_seconds_sum 1
notarx_audit_flush_seconds_count %d
other_metric 7
`

type fakeGateway struct {
	mu       sync.Mutex
	scrapes  int
	reads    atomic.Int64
	writes   atomic.Int64
	failPath string
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/metrics":
		g.mu.Lock()
		g.scrapes++
		first := g.scrapes == 1
		g.mu.Unlock()
		if first {
			_, _ = fmt.Fprintf(w, metricsBody, 0, 0, 0, 0)
		} else {
			_, _ = fmt.Fprintf(w, metricsBody, 90, 99, 100, 100)
		}
	case strings.HasPrefix(r.URL.Path, "/npm/"):
		g.reads.Add(1)
		_, _ = io.WriteString(w, "{}")
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/raw/pub/perfload/"):
		g.writes.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		if g.failPath != "" && strings.Contains(r.URL.Path, g.failPath) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func testConfig(url string) Config {
	return Config{
		Profile: "tiny", BaseURL: url, Token: "token", PublishRepository: "pub", Seed: 7,
		Targets: []Target{{"repo-a", "hot"}, {"repo-a", "warm"}, {"repo-b", "cold@scope"}},
		Sizes:   Sizes{Mixed: 400, Stampede: 50, Burst: 20, Concurrency: 8},
	}
}

func TestRunProducesEveryScenario(t *testing.T) {
	gateway := &fakeGateway{}
	server := httptest.NewServer(gateway)
	defer server.Close()
	report, err := Run(t.Context(), testConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != SchemaVersion || report.Profile != "tiny" || report.GeneratedAt.IsZero() {
		t.Fatalf("header: %+v", report)
	}
	names := []string{}
	total := 0
	for _, scenario := range report.Scenarios {
		names = append(names, scenario.Name)
		total += scenario.Requests
		if scenario.Errors != 0 {
			t.Errorf("%s: %d errors", scenario.Name, scenario.Errors)
		}
		if scenario.Requests > 0 && (scenario.LatencyMS.P50 > scenario.LatencyMS.P95 || scenario.LatencyMS.P95 > scenario.LatencyMS.P99 || scenario.LatencyMS.P99 > scenario.LatencyMS.Max) {
			t.Errorf("%s: percentiles not ordered: %+v", scenario.Name, scenario.LatencyMS)
		}
	}
	if strings.Join(names, ",") != "mixed.read,mixed.publish,hot-artifact-stampede,hot-repository-burst" {
		t.Fatalf("scenarios: %v", names)
	}
	if total != 400+50+20 {
		t.Fatalf("requests = %d, want 470", total)
	}
	// 95 percent of the mixed scenario reads (statistically); the rest publishes.
	mixedReads := report.Scenarios[0].Requests
	if mixedReads < 340 || mixedReads > 395 || mixedReads+report.Scenarios[1].Requests != 400 {
		t.Errorf("mixed split: %d reads, %d publishes", mixedReads, report.Scenarios[1].Requests)
	}
	if got := gateway.reads.Load(); got != int64(mixedReads+50) {
		t.Errorf("gateway saw %d reads, want %d", got, mixedReads+50)
	}
	if report.AuditFlush.Flushes != 100 || report.AuditFlush.P99Seconds != 1 || report.AuditFlush.BufferDepthAfter != 3 || report.AuditFlush.Dropped != 0 {
		t.Errorf("audit flush: %+v", report.AuditFlush)
	}
	if _, err := json.Marshal(report); err != nil {
		t.Fatal(err)
	}
}

func TestRunCountsFailedRequests(t *testing.T) {
	gateway := &fakeGateway{failPath: "burst"}
	server := httptest.NewServer(gateway)
	defer server.Close()
	report, err := Run(t.Context(), testConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if burst := report.Scenarios[3]; burst.Name != "hot-repository-burst" || burst.Errors != 20 {
		t.Fatalf("burst: %+v", burst)
	}
}

func TestRunRejectsBadConfiguration(t *testing.T) {
	good := testConfig("http://127.0.0.1:1")
	for name, mutate := range map[string]func(*Config){
		"no targets":    func(c *Config) { c.Targets = nil },
		"no repository": func(c *Config) { c.PublishRepository = "" },
		"no url":        func(c *Config) { c.BaseURL = "" },
		"zero sizes":    func(c *Config) { c.Sizes.Burst = 0 },
	} {
		cfg := good
		mutate(&cfg)
		if _, err := Run(t.Context(), cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRunReportsAnUnreachableGateway(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	if _, err := Run(t.Context(), testConfig(url)); err == nil {
		t.Fatal("unreachable gateway accepted")
	}
	status := httptest.NewServer(http.NotFoundHandler())
	defer status.Close()
	if _, err := Run(t.Context(), testConfig(status.URL)); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunStopsWhenCancelled(t *testing.T) {
	server := httptest.NewServer(&fakeGateway{})
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Run(ctx, testConfig(server.URL)); err == nil {
		t.Fatal("cancelled run succeeded")
	}
}

func TestProfileSizes(t *testing.T) {
	for _, name := range []string{"tiny", "freetier", "target"} {
		sizes, err := ProfileSizes(name)
		if err != nil || sizes.Mixed < 1 || sizes.Concurrency < 1 {
			t.Errorf("%s: %+v, %v", name, sizes, err)
		}
	}
	if _, err := ProfileSizes("huge"); err == nil {
		t.Error("unknown profile accepted")
	}
}

func TestPercentileAndSummarize(t *testing.T) {
	if percentile(nil, 99) != 0 {
		t.Error("percentile of nothing")
	}
	values := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if percentile(values, 50) != 5 || percentile(values, 95) != 10 || percentile(values, 1) != 1 {
		t.Errorf("percentiles: %v %v %v", percentile(values, 50), percentile(values, 95), percentile(values, 1))
	}
	result := summarize("x", []sample{{latency: 2 * time.Millisecond, ok: true}, {latency: 4 * time.Millisecond}}, time.Second)
	if result.Requests != 2 || result.Errors != 1 || result.RequestsPerSecond != 2 || result.LatencyMS.Max != 4 {
		t.Errorf("summary: %+v", result)
	}
	if empty := summarize("none", nil, 0); empty.RequestsPerSecond != 0 || empty.LatencyMS.Max != 0 {
		t.Errorf("empty summary: %+v", empty)
	}
}

func TestParseAuditErrors(t *testing.T) {
	for _, body := range []string{
		"notarx_audit_buffer_depth many\n",
		"notarx_audit_flush_seconds_bucket{le=\"soon\"} 1\n",
	} {
		if _, err := parseAudit(strings.NewReader(body)); err == nil {
			t.Errorf("%q accepted", body)
		}
	}
	snapshot, err := parseAudit(strings.NewReader("notarx_audit_novalue\n"))
	if err != nil || snapshot.count != 0 {
		t.Errorf("series without a value: %+v, %v", snapshot, err)
	}
}

func TestAuditSinceHandlesOverflowAndEmptyIntervals(t *testing.T) {
	before := auditSnapshot{buckets: map[float64]uint64{0.5: 1}, count: 1}
	after := auditSnapshot{buckets: map[float64]uint64{0.5: 1}, count: 5}
	if got := after.since(before); got.Flushes != 4 || got.P99Seconds != 1 {
		t.Errorf("flushes above every bound: %+v", got)
	}
	if got := before.since(before); got.Flushes != 0 || got.P99Seconds != 0 {
		t.Errorf("empty interval: %+v", got)
	}
	if got := (auditSnapshot{count: 2}).since(auditSnapshot{}); got.P99Seconds != 0 {
		t.Errorf("no buckets: %+v", got)
	}
}
