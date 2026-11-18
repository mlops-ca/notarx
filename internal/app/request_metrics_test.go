package app

import (
	"bufio"
	"database/sql"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/auditwriter"
	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/partitions"
	"github.com/mlops-ca/notarx/internal/retention"
)

func TestPartitionMetricsExposeDefaultPartitionRows(t *testing.T) {
	metrics := (&Metrics{}).WithPartitionStats(func() []partitions.TableStat {
		return []partitions.TableStat{{Table: "resolver_audit_log", DefaultRows: 2, Partitions: 4}, {Table: "security_audit_log", Partitions: 4}}
	})
	response := httptest.NewRecorder()
	metrics.Handler(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{
		"# TYPE notarx_partition_default_rows gauge",
		`notarx_partition_default_rows{table="resolver_audit_log"} 2`,
		`notarx_partition_default_rows{table="security_audit_log"} 0`,
		`notarx_partitions{table="security_audit_log"} 4`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics missing %q\n%s", expected, body)
		}
	}
	empty := httptest.NewRecorder()
	(&Metrics{}).Handler(empty, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(empty.Body.String(), "notarx_partition_default_rows") {
		t.Fatal("partition metrics must be absent when no manager is configured")
	}
}

func TestAuditWriterMetrics(t *testing.T) {
	var stats auditwriter.Stats
	stats.BufferDepth, stats.DroppedRead, stats.DroppedSecurity, stats.FlushErrors, stats.FlushCount = 7, 3, 2, 1, 4
	stats.FlushSecondsSum, stats.Sanitized = 0.5, 6
	stats.FlushSecondsBucket[0] = 4
	metrics := (&Metrics{}).WithAuditWriterStats(func() auditwriter.Stats { return stats })
	response := httptest.NewRecorder()
	metrics.Handler(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{
		"notarx_audit_buffer_depth 7",
		`notarx_audit_dropped_total{class="read",reason="buffer_full"} 3`,
		`notarx_audit_dropped_total{class="security",reason="overload"} 2`,
		"notarx_audit_sanitized_total 6",
		"notarx_audit_flush_errors_total 1",
		`notarx_audit_flush_seconds_bucket{le="0.005"} 4`,
		`notarx_audit_flush_seconds_bucket{le="+Inf"} 4`,
		"notarx_audit_flush_seconds_sum 0.5",
		"notarx_audit_flush_seconds_count 4",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics missing %q\n%s", expected, body)
		}
	}
	empty := httptest.NewRecorder()
	(&Metrics{}).Handler(empty, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(empty.Body.String(), "notarx_audit_buffer_depth") {
		t.Fatal("audit writer metrics must be absent when no writer is configured")
	}
}

func TestHTTPMetricsRecordBoundedClassStatusAndLatency(t *testing.T) {
	metrics := &Metrics{}
	handler := metrics.Instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v2/repositories", nil))

	response := httptest.NewRecorder()
	metrics.Handler(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{
		`notarx_http_requests_total{class="management",status="2xx"} 1`,
		`notarx_http_requests_in_flight{class="management"} 0`,
		`notarx_http_request_duration_seconds_bucket{class="management",status="2xx",le="+Inf"} 1`,
		`notarx_http_request_duration_seconds_count{class="management",status="2xx"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics missing %q\n%s", expected, body)
		}
	}
}

func TestHTTPMetricsExposeInFlightRequest(t *testing.T) {
	metrics := &Metrics{}
	started := make(chan struct{})
	release := make(chan struct{})
	handler := metrics.Instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v2/example/manifests/latest", nil))
		close(done)
	}()
	<-started
	response := httptest.NewRecorder()
	metrics.Handler(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), `notarx_http_requests_in_flight{class="oci"} 1`) {
		t.Fatalf("in-flight metric missing\n%s", response.Body.String())
	}
	close(release)
	<-done
}

func TestClassifyNPMRequest(t *testing.T) {
	if class := classifyRequest("/npm/releases/@scope/widget"); class != requestClassNPM {
		t.Fatalf("class=%v", class)
	}
}

func TestRuntimeAndDatabasePoolMetrics(t *testing.T) {
	pool, err := database.OpenPostgres("postgres://gateway:secret@localhost:5432/gateway?sslmode=disable", database.PoolConfig{MaxOpenConns: 7, MaxIdleConns: 2, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pool.Close() }()
	notifications, err := database.OpenPostgres("postgres://gateway:secret@localhost:5432/gateway?sslmode=disable", database.NotificationPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = notifications.Close() }()
	metrics := (&Metrics{}).
		WithDatabaseStats(pool.Stats).
		WithDatabasePoolStats("artifact-locks", func() sql.DBStats { return sql.DBStats{MaxOpenConnections: 4} }).
		WithDatabasePoolStats("notifications", notifications.Stats)
	response := httptest.NewRecorder()
	metrics.Handler(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{
		"notarx_runtime_heap_alloc_bytes ",
		"notarx_runtime_gc_cycles_total ",
		"notarx_runtime_goroutines ",
		`notarx_database_connections{pool="primary",state="max_open"} 7`,
		`notarx_database_connections{pool="artifact-locks",state="max_open"} 4`,
		`notarx_database_connections{pool="notifications",state="max_open"} 2`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics missing %q\n%s", expected, body)
		}
	}
}

func TestNodeInfoMetricEscapesConfiguredIdentity(t *testing.T) {
	metrics := (&Metrics{}).WithNodeIdentity("worker\"01\n", []string{"worker"})
	response := httptest.NewRecorder()
	metrics.Handler(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), `notarx_node_info{instance_id="worker\"01\n",roles="worker"} 1`) {
		t.Fatalf("node metric is missing or malformed:\n%s", response.Body.String())
	}
}

type basicResponseWriter struct{ header http.Header }

func (w *basicResponseWriter) Header() http.Header          { return w.header }
func (*basicResponseWriter) Write(body []byte) (int, error) { return len(body), nil }
func (*basicResponseWriter) WriteHeader(int)                {}

func TestMetricsResponseWriterDoesNotInventOptionalInterfaces(t *testing.T) {
	handler := (&Metrics{}).Instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Flusher); ok {
			t.Fatal("metrics response writer unexpectedly implements http.Flusher")
		}
		if _, ok := w.(http.Hijacker); ok {
			t.Fatal("metrics response writer unexpectedly implements http.Hijacker")
		}
		if _, ok := w.(http.Pusher); ok {
			t.Fatal("metrics response writer unexpectedly implements http.Pusher")
		}
		if _, ok := w.(io.ReaderFrom); ok {
			t.Fatal("metrics response writer unexpectedly implements io.ReaderFrom")
		}
	}))
	handler.ServeHTTP(&basicResponseWriter{header: make(http.Header)}, httptest.NewRequest(http.MethodGet, "/", nil))
}

type optionalResponseWriter struct{ *basicResponseWriter }

func (*optionalResponseWriter) Flush() {}
func (*optionalResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, http.ErrNotSupported
}
func (*optionalResponseWriter) Push(string, *http.PushOptions) error { return http.ErrNotSupported }
func (w *optionalResponseWriter) ReadFrom(reader io.Reader) (int64, error) {
	return io.Copy(io.Discard, reader)
}

func TestMetricsResponseWriterPreservesOptionalInterfaces(t *testing.T) {
	handler := (&Metrics{}).Instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for name, supported := range map[string]bool{
			"flusher":     implements[http.Flusher](w),
			"hijacker":    implements[http.Hijacker](w),
			"pusher":      implements[http.Pusher](w),
			"reader_from": implements[io.ReaderFrom](w),
		} {
			if !supported {
				t.Fatalf("metrics response writer hid %s", name)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	handler.ServeHTTP(&optionalResponseWriter{basicResponseWriter: &basicResponseWriter{header: make(http.Header)}}, httptest.NewRequest(http.MethodGet, "/", nil))
}

func implements[T any](value any) bool {
	_, ok := value.(T)
	return ok
}

type statusSequenceWriter struct {
	*basicResponseWriter
	statuses []int
}

func (w *statusSequenceWriter) WriteHeader(status int) {
	w.statuses = append(w.statuses, status)
}

func TestHTTPMetricsPassThroughInformationalStatusAndRecordFinalStatus(t *testing.T) {
	metrics := &Metrics{}
	underlying := &statusSequenceWriter{basicResponseWriter: &basicResponseWriter{header: make(http.Header)}}
	handler := metrics.Instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusNoContent)
	}))
	handler.ServeHTTP(underlying, httptest.NewRequest(http.MethodGet, "/api/v2/repositories", nil))
	if len(underlying.statuses) != 2 || underlying.statuses[0] != http.StatusEarlyHints || underlying.statuses[1] != http.StatusNoContent {
		t.Fatalf("forwarded statuses=%v", underlying.statuses)
	}
	response := httptest.NewRecorder()
	metrics.Handler(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), `notarx_http_requests_total{class="management",status="2xx"} 1`) {
		t.Fatal("HTTP metrics did not record the final status family")
	}
}

func TestDatabaseMetricsIgnoreUnboundedPoolLabels(t *testing.T) {
	metrics := (&Metrics{}).WithDatabasePoolStats("repository-name", func() sql.DBStats {
		return sql.DBStats{MaxOpenConnections: 99}
	})
	response := httptest.NewRecorder()
	metrics.Handler(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(response.Body.String(), "repository-name") {
		t.Fatal("database metrics exposed an unbounded pool label")
	}
}

func TestRetentionMetricsExposeDeletedRowsAndLag(t *testing.T) {
	metrics := (&Metrics{}).WithRetentionStats(func() []retention.TableStat {
		return []retention.TableStat{{Table: "lifecycle_jobs", Deleted: 12, LagSeconds: 3600}, {Table: "user_sessions"}}
	})
	recorder := httptest.NewRecorder()
	metrics.Handler(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, want := range []string{
		"# TYPE notarx_retention_deleted_rows_total counter",
		"# TYPE notarx_retention_lag_seconds gauge",
		`notarx_retention_deleted_rows_total{table="lifecycle_jobs"} 12`,
		`notarx_retention_lag_seconds{table="lifecycle_jobs"} 3600`,
		`notarx_retention_deleted_rows_total{table="user_sessions"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	empty := httptest.NewRecorder()
	(&Metrics{}).Handler(empty, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(empty.Body.String(), "notarx_retention") {
		t.Fatal("retention metrics must be absent when no janitor is configured")
	}
}
