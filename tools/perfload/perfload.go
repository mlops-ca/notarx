// Package perfload drives the mixed load profile of ADR 0023 section 11 against a running
// gateway: 95 percent metadata reads and 5 percent publishes, a hot-artifact stampede and
// a hot-repository publish burst. It reports latency percentiles per scenario, requests per
// second and the audit flush histogram; scripts/performance-baseline.sh (make perf-load)
// adds the database CPU it sampled and writes docs/perf/<profile>.json.
package perfload

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// SchemaVersion is the version of the Report document (docs/performance-baseline.md).
const SchemaVersion = 1

// readShare is the share of the mixed scenario that reads; the rest publishes.
const readShare = 0.95

// payloadBytes is the size of one published object: a small artifact, the case the
// publish SLO covers.
const payloadBytes = 4096

// Target is a package that reads can hit. Targets are ordered hottest first.
type Target struct {
	Repository string
	Package    string
}

// Sizes are the request budgets of one profile.
type Sizes struct {
	Mixed       int // requests in the 95/5 scenario
	Stampede    int // concurrent reads of the single hottest packument
	Burst       int // concurrent publishes into one repository
	Concurrency int // workers per scenario
}

// ProfileSizes returns the budgets of a datagen profile name.
func ProfileSizes(profile string) (Sizes, error) {
	switch profile {
	case "tiny":
		return Sizes{Mixed: 4000, Stampede: 2000, Burst: 400, Concurrency: 16}, nil
	case "freetier":
		return Sizes{Mixed: 40000, Stampede: 10000, Burst: 2000, Concurrency: 32}, nil
	case "target":
		return Sizes{Mixed: 400000, Stampede: 100000, Burst: 10000, Concurrency: 128}, nil
	}
	return Sizes{}, fmt.Errorf("perfload: unknown profile %q (want tiny, freetier or target)", profile)
}

// Config describes one run.
type Config struct {
	Profile string
	BaseURL string
	Token   string
	// Targets are the readable packages, hottest first; reads follow a Zipf distribution
	// over them so hot artifacts exist.
	Targets []Target
	// PublishRepository is a hosted raw repository that accepts the publishes.
	PublishRepository string
	Sizes             Sizes
	Seed              int64
	Client            *http.Client
	Now               func() time.Time
}

// Latency holds percentiles in milliseconds.
type Latency struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

// ScenarioResult is the outcome of one scenario.
type ScenarioResult struct {
	Name              string  `json:"name"`
	Requests          int     `json:"requests"`
	Errors            int     `json:"errors"`
	RequestsPerSecond float64 `json:"requestsPerSecond"`
	DurationSeconds   float64 `json:"durationSeconds"`
	LatencyMS         Latency `json:"latencyMs"`
}

// AuditFlush summarises the audit writer's flush durations during the run.
type AuditFlush struct {
	P99Seconds float64 `json:"p99Seconds"`
	Flushes    uint64  `json:"flushes"`
	// BufferDepthAfter is notarx_audit_buffer_depth after the last scenario.
	BufferDepthAfter float64 `json:"bufferDepthAfter"`
	Dropped          float64 `json:"dropped"`
}

// Report is the document written to docs/perf/<profile>.json.
type Report struct {
	SchemaVersion int              `json:"schemaVersion"`
	Profile       string           `json:"profile"`
	GeneratedAt   time.Time        `json:"generatedAt"`
	Scenarios     []ScenarioResult `json:"scenarios"`
	AuditFlush    AuditFlush       `json:"auditFlush"`
}

// sample is one request outcome.
type sample struct {
	latency time.Duration
	ok      bool
}

// Run executes the scenarios in order and returns the report.
func Run(ctx context.Context, cfg Config) (Report, error) {
	if len(cfg.Targets) == 0 {
		return Report{}, fmt.Errorf("perfload: no read targets; seed the database with make perf-seed first")
	}
	if cfg.PublishRepository == "" || cfg.BaseURL == "" {
		return Report{}, fmt.Errorf("perfload: BaseURL and PublishRepository are required")
	}
	if cfg.Sizes.Concurrency < 1 || cfg.Sizes.Mixed < 1 || cfg.Sizes.Stampede < 1 || cfg.Sizes.Burst < 1 {
		return Report{}, fmt.Errorf("perfload: every size must be positive")
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	run := &runner{cfg: cfg, runID: fmt.Sprintf("%d", cfg.Now().UnixNano())}

	before, err := run.scrapeAudit(ctx)
	if err != nil {
		return Report{}, err
	}
	hot := cfg.Targets[0]
	rng := rand.New(rand.NewSource(cfg.Seed))
	zipf := rand.NewZipf(rng, 1.2, 1, uint64(max(len(cfg.Targets)-1, 1)))
	var zipfMu sync.Mutex
	pick := func() Target {
		zipfMu.Lock()
		defer zipfMu.Unlock()
		return cfg.Targets[int(zipf.Uint64())%len(cfg.Targets)]
	}
	var mixRNG = rand.New(rand.NewSource(cfg.Seed + 1))
	var mixMu sync.Mutex
	isRead := func() bool {
		mixMu.Lock()
		defer mixMu.Unlock()
		return mixRNG.Float64() < readShare
	}

	var report Report
	report.SchemaVersion = SchemaVersion
	report.Profile = cfg.Profile
	report.GeneratedAt = cfg.Now().UTC()

	start := cfg.Now()
	reads, writes := run.mixed(ctx, pick, isRead)
	elapsed := cfg.Now().Sub(start)
	report.Scenarios = append(report.Scenarios,
		summarize("mixed.read", reads, elapsed), summarize("mixed.publish", writes, elapsed))

	start = cfg.Now()
	stampede := run.parallel(ctx, cfg.Sizes.Stampede, func(int) sample { return run.read(ctx, hot) })
	report.Scenarios = append(report.Scenarios, summarize("hot-artifact-stampede", stampede, cfg.Now().Sub(start)))

	start = cfg.Now()
	burst := run.parallel(ctx, cfg.Sizes.Burst, func(i int) sample { return run.publish(ctx, "burst", i) })
	report.Scenarios = append(report.Scenarios, summarize("hot-repository-burst", burst, cfg.Now().Sub(start)))

	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	after, err := run.scrapeAudit(ctx)
	if err != nil {
		return Report{}, err
	}
	report.AuditFlush = after.since(before)
	return report, nil
}

type runner struct {
	cfg   Config
	runID string
}

// mixed runs the 95/5 scenario and returns the read and publish samples separately.
func (r *runner) mixed(ctx context.Context, pick func() Target, isRead func() bool) (reads, writes []sample) {
	var mu sync.Mutex
	r.parallel(ctx, r.cfg.Sizes.Mixed, func(i int) sample {
		var s sample
		read := isRead()
		if read {
			s = r.read(ctx, pick())
		} else {
			s = r.publish(ctx, "mixed", i)
		}
		mu.Lock()
		if read {
			reads = append(reads, s)
		} else {
			writes = append(writes, s)
		}
		mu.Unlock()
		return s
	})
	return reads, writes
}

// parallel runs total jobs on the configured number of workers and returns every sample.
func (r *runner) parallel(ctx context.Context, total int, job func(int) sample) []sample {
	samples := make([]sample, total)
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(r.cfg.Sizes.Concurrency, total) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				samples[i] = job(i)
			}
		}()
	}
	for i := 0; i < total && ctx.Err() == nil; i++ {
		next <- i
	}
	close(next)
	wg.Wait()
	return samples
}

func (r *runner) read(ctx context.Context, target Target) sample {
	path := "/npm/" + url.PathEscape(target.Repository) + "/" + url.PathEscape(target.Package)
	return r.do(ctx, http.MethodGet, path, nil)
}

func (r *runner) publish(ctx context.Context, scenario string, i int) sample {
	body := bytes.Repeat([]byte{byte(i)}, payloadBytes)
	path := fmt.Sprintf("/raw/%s/perfload/%s/%s-%d.bin", url.PathEscape(r.cfg.PublishRepository), r.runID, scenario, i)
	return r.do(ctx, http.MethodPut, path, body)
}

func (r *runner) do(ctx context.Context, method, path string, body []byte) sample {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(r.cfg.BaseURL, "/")+path, reader)
	if err != nil {
		return sample{}
	}
	request.Header.Set("Authorization", "Bearer "+r.cfg.Token)
	if body != nil {
		request.Header.Set("Content-Type", "application/octet-stream")
	}
	began := time.Now()
	response, err := r.cfg.Client.Do(request)
	if err != nil {
		return sample{latency: time.Since(began)}
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return sample{latency: time.Since(began), ok: response.StatusCode >= 200 && response.StatusCode < 300}
}

// summarize turns samples into a ScenarioResult; elapsed is the wall time of the scenario.
func summarize(name string, samples []sample, elapsed time.Duration) ScenarioResult {
	result := ScenarioResult{Name: name, Requests: len(samples), DurationSeconds: round(elapsed.Seconds(), 3)}
	latencies := make([]float64, 0, len(samples))
	for _, s := range samples {
		if !s.ok {
			result.Errors++
		}
		latencies = append(latencies, float64(s.latency)/float64(time.Millisecond))
	}
	if elapsed > 0 {
		result.RequestsPerSecond = round(float64(len(samples))/elapsed.Seconds(), 2)
	}
	sort.Float64s(latencies)
	result.LatencyMS = Latency{P50: percentile(latencies, 50), P95: percentile(latencies, 95), P99: percentile(latencies, 99)}
	if len(latencies) > 0 {
		result.LatencyMS.Max = round(latencies[len(latencies)-1], 3)
	}
	return result
}

// percentile uses the nearest-rank method on sorted values; it returns 0 for no values.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	return round(sorted[min(max(rank, 1), len(sorted))-1], 3)
}

func round(value float64, places int) float64 {
	scale := math.Pow10(places)
	return math.Round(value*scale) / scale
}
