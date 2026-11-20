package perfload

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// auditSnapshot is the audit writer's state in one scrape of /metrics.
type auditSnapshot struct {
	buckets     map[float64]uint64 // cumulative notarx_audit_flush_seconds buckets by upper bound
	count       uint64
	bufferDepth float64
	dropped     float64
}

func (r *runner) scrapeAudit(ctx context.Context) (auditSnapshot, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(r.cfg.BaseURL, "/")+"/metrics", nil)
	if err != nil {
		return auditSnapshot{}, err
	}
	request.Header.Set("Authorization", "Bearer "+r.cfg.Token)
	response, err := r.cfg.Client.Do(request)
	if err != nil {
		return auditSnapshot{}, fmt.Errorf("scrape /metrics: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return auditSnapshot{}, fmt.Errorf("scrape /metrics: HTTP %d", response.StatusCode)
	}
	return parseAudit(response.Body)
}

// parseAudit reads the notarx_audit_* series of a Prometheus text exposition.
func parseAudit(body io.Reader) (auditSnapshot, error) {
	snapshot := auditSnapshot{buckets: map[float64]uint64{}}
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "notarx_audit_") {
			continue
		}
		cut := strings.LastIndexByte(line, ' ')
		if cut < 0 {
			continue
		}
		series, raw := line[:cut], line[cut+1:]
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return auditSnapshot{}, fmt.Errorf("parse %q: %w", line, err)
		}
		switch {
		case strings.HasPrefix(series, "notarx_audit_flush_seconds_bucket{le=\""):
			bound := strings.TrimSuffix(strings.TrimPrefix(series, "notarx_audit_flush_seconds_bucket{le=\""), "\"}")
			if bound == "+Inf" {
				snapshot.count = uint64(value)
				continue
			}
			upper, err := strconv.ParseFloat(bound, 64)
			if err != nil {
				return auditSnapshot{}, fmt.Errorf("parse %q: %w", line, err)
			}
			snapshot.buckets[upper] = uint64(value)
		case series == "notarx_audit_buffer_depth":
			snapshot.bufferDepth = value
		case strings.HasPrefix(series, "notarx_audit_dropped_total{"):
			snapshot.dropped += value
		}
	}
	return snapshot, scanner.Err()
}

// since returns the flush summary of the interval between two scrapes: the p99 upper
// bound of the flushes recorded in it (+Inf is reported as the largest finite bound
// times two), the number of flushes and the closing buffer depth and drop count.
func (s auditSnapshot) since(before auditSnapshot) AuditFlush {
	out := AuditFlush{Flushes: s.count - min(s.count, before.count), BufferDepthAfter: s.bufferDepth, Dropped: s.dropped - before.dropped}
	if out.Flushes == 0 {
		return out
	}
	bounds := make([]float64, 0, len(s.buckets))
	for bound := range s.buckets {
		bounds = append(bounds, bound)
	}
	sort.Float64s(bounds)
	target := uint64(float64(out.Flushes)*0.99 + 0.999999)
	for _, bound := range bounds {
		if s.buckets[bound]-min(s.buckets[bound], before.buckets[bound]) >= target {
			out.P99Seconds = bound
			return out
		}
	}
	if len(bounds) > 0 {
		out.P99Seconds = bounds[len(bounds)-1] * 2
	}
	return out
}
