// Package auditwriter is the tiered audit writer of ADR 0023 section 1. It sits
// behind repository.AuditRecorder and decouples the request path from the audit
// tables:
//
//   - sync-class records (management mutations, publish, delete, quarantine,
//     settings, retention and key operations) are committed before RecordAudit
//     returns and a failure is returned to the caller, so the change fails closed;
//     they are never queued and never dropped;
//   - denials of authenticated principals are committed too, bounded by a per-node
//     semaphore; when it is exhausted they queue on the async path with priority
//     "security" (a bounded wait, then a counted drop), unless the denial mode is
//     sync, which blocks instead;
//   - async-class records (reads, downloads, cache hits, group resolution, anonymous
//     denials) go to a bounded in-process buffer flushed in batches with one
//     statement; a full buffer drops the record, counts it and never blocks.
//
// Usage aggregates and request rollups are accumulated in memory and flushed in
// key order, so a hot artifact costs one row update per interval and node. All
// counters are approximate by up to one flush interval per node.
package auditwriter

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/requestcontext"
)

// Sink is the storage the writer flushes to.
type Sink interface {
	repository.AuditBatchStore
	// PruneRequestRollups deletes rollup buckets that start before the cutoff.
	PruneRequestRollups(ctx context.Context, before time.Time) (int64, error)
}

// DenialMode selects what happens to a denial of an authenticated principal when
// the synchronous in-flight bound is exhausted.
type DenialMode string

const (
	// DenialQueue (the default) queues the record with priority "security".
	DenialQueue DenialMode = "queue"
	// DenialSync blocks the request until the record is committed.
	DenialSync DenialMode = "sync"
)

// ReadMode selects how read and download audit is written.
type ReadMode string

const (
	// ReadAsync (the default) buffers read audit: best-effort evidence.
	ReadAsync ReadMode = "async"
	// ReadSync commits every read audit before the response: complete, and one
	// commit per request.
	ReadSync ReadMode = "sync"
)

// Config holds the NOTARX_AUDIT_* and NOTARX_USAGE_* settings.
type Config struct {
	BufferSize         int           // NOTARX_AUDIT_BUFFER_SIZE
	BatchSize          int           // NOTARX_AUDIT_BATCH_SIZE
	FlushInterval      time.Duration // NOTARX_AUDIT_FLUSH_INTERVAL
	SyncMaxInflight    int           // NOTARX_AUDIT_SYNC_MAX_INFLIGHT
	DenialMode         DenialMode    // NOTARX_AUDIT_DENIAL_MODE
	ReadMode           ReadMode      // NOTARX_AUDIT_READ_MODE
	UsageFlushInterval time.Duration // NOTARX_USAGE_FLUSH_INTERVAL
	UsageMaxKeys       int           // cap of the in-memory usage map
	// SecurityWait is how long a queued denial waits for buffer space.
	SecurityWait time.Duration
	// RollupRetention is how long hourly request rollups are kept.
	RollupRetention time.Duration
	Logger          *slog.Logger
}

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{
		BufferSize: 65536, BatchSize: 1000, FlushInterval: time.Second, SyncMaxInflight: 64,
		DenialMode: DenialQueue, ReadMode: ReadAsync, UsageFlushInterval: 5 * time.Second, UsageMaxKeys: 100000,
		SecurityWait: 250 * time.Millisecond, RollupRetention: repository.RequestRollupRetention,
	}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.BufferSize <= 0 {
		c.BufferSize = d.BufferSize
	}
	if c.BatchSize <= 0 {
		c.BatchSize = d.BatchSize
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = d.FlushInterval
	}
	if c.SyncMaxInflight <= 0 {
		c.SyncMaxInflight = d.SyncMaxInflight
	}
	if c.DenialMode == "" {
		c.DenialMode = d.DenialMode
	}
	if c.ReadMode == "" {
		c.ReadMode = d.ReadMode
	}
	if c.UsageFlushInterval <= 0 {
		c.UsageFlushInterval = d.UsageFlushInterval
	}
	if c.UsageMaxKeys <= 0 {
		c.UsageMaxKeys = d.UsageMaxKeys
	}
	if c.SecurityWait <= 0 {
		c.SecurityWait = d.SecurityWait
	}
	if c.RollupRetention <= 0 {
		c.RollupRetention = d.RollupRetention
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// FlushBuckets are the upper bounds, in seconds, of notarx_audit_flush_seconds.
var FlushBuckets = [...]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Stats is a snapshot for the metrics endpoint.
type Stats struct {
	BufferDepth        int64
	DroppedRead        uint64 // buffer_full
	DroppedSecurity    uint64 // queued denial that found no buffer space
	DroppedUsage       uint64 // usage or rollup key beyond the cap
	FlushErrors        uint64
	FlushCount         uint64
	FlushSecondsSum    float64
	FlushSecondsBucket [len(FlushBuckets)]uint64 // cumulative
}

// ErrClosed is returned for a record offered after Shutdown when it cannot be committed.
var ErrClosed = errors.New("audit writer is closed")

// Writer implements repository.AuditRecorder.
type Writer struct {
	sink Sink
	cfg  Config

	buffer chan repository.AuditRecord
	wake   chan struct{}
	sem    chan struct{}

	closed atomic.Bool
	stop   chan struct{}
	done   sync.WaitGroup
	once   sync.Once

	pending atomic.Int64 // records taken from the buffer and not yet written

	aggMu     sync.Mutex
	usage     map[usageKey]*repository.UsageDelta
	rollups   map[rollupKey]*repository.RollupDelta
	usageWake chan struct{}

	droppedRead, droppedSecurity, droppedUsage, flushErrors, flushCount atomic.Uint64
	flushNanos                                                          atomic.Uint64
	flushBuckets                                                        [len(FlushBuckets)]atomic.Uint64
}

type usageKey struct{ repository, format, resource string }
type rollupKey struct {
	repository string
	bucket     time.Time
	outcome    repository.RequestOutcome
}

var _ repository.AuditRecorder = (*Writer)(nil)

// New builds a writer; call Start before use.
func New(sink Sink, cfg Config) *Writer {
	cfg = cfg.withDefaults()
	return &Writer{
		sink: sink, cfg: cfg,
		buffer:    make(chan repository.AuditRecord, cfg.BufferSize),
		wake:      make(chan struct{}, 1),
		usageWake: make(chan struct{}, 1),
		sem:       make(chan struct{}, cfg.SyncMaxInflight),
		stop:      make(chan struct{}),
		usage:     map[usageKey]*repository.UsageDelta{},
		rollups:   map[rollupKey]*repository.RollupDelta{},
	}
}

// Start launches the audit flusher and the usage flusher.
func (w *Writer) Start() {
	w.done.Add(2)
	go w.auditLoop()
	go w.usageLoop()
}

// RecordAudit implements repository.AuditRecorder.
func (w *Writer) RecordAudit(ctx context.Context, record repository.AuditRecord) error {
	record = w.prepare(ctx, record)
	if w.closed.Load() {
		// The HTTP server has drained; a late record is committed directly.
		if err := w.sink.InsertAudit(ctx, record); err != nil {
			return err
		}
		w.count(record)
		return nil
	}
	if record.Class == repository.AuditClassSync {
		if record.IsAuthenticatedDenial() {
			return w.recordDenial(ctx, record)
		}
		// A mutation or other state change: committed, fail closed, never queued.
		if err := w.sink.InsertAudit(ctx, record); err != nil {
			return err
		}
		w.count(record)
		return nil
	}
	if w.cfg.ReadMode == ReadSync {
		if err := w.sink.InsertAudit(ctx, record); err != nil {
			return err
		}
		w.count(record)
		return nil
	}
	w.count(record)
	select {
	case w.buffer <- record:
		w.signalFull()
	default:
		w.droppedRead.Add(1)
	}
	return nil
}

func (w *Writer) prepare(ctx context.Context, record repository.AuditRecord) repository.AuditRecord {
	if ids, ok := requestcontext.FromContext(ctx); ok {
		if record.RequestID == "" {
			record.RequestID = ids.RequestID
		}
		if record.TraceID == "" {
			record.TraceID = ids.TraceID
		}
	}
	if record.OccurredAt.IsZero() {
		record.OccurredAt = time.Now().UTC()
	}
	record.Class = record.Durability()
	return record
}

// recordDenial writes the denial of an authenticated principal: a committed insert
// bounded by the semaphore, else a priority-security enqueue.
func (w *Writer) recordDenial(ctx context.Context, record repository.AuditRecord) error {
	if w.cfg.DenialMode == DenialSync {
		select {
		case w.sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		return w.insertDenial(ctx, record)
	}
	select {
	case w.sem <- struct{}{}:
		return w.insertDenial(ctx, record)
	default:
	}
	timer := time.NewTimer(w.cfg.SecurityWait)
	defer timer.Stop()
	select {
	case w.buffer <- record:
		w.count(record)
		w.signalFull()
	case <-timer.C:
		w.droppedSecurity.Add(1)
		w.count(record)
		w.cfg.Logger.Error("security audit record dropped: the synchronous bound and the buffer are exhausted", "operation", record.Operation, "actor", record.Actor)
	case <-ctx.Done():
		w.droppedSecurity.Add(1)
		w.count(record)
	}
	return nil
}

func (w *Writer) insertDenial(ctx context.Context, record repository.AuditRecord) error {
	defer func() { <-w.sem }()
	if err := w.sink.InsertAudit(ctx, record); err != nil {
		return err
	}
	w.count(record)
	return nil
}

func (w *Writer) signalFull() {
	if len(w.buffer) >= w.cfg.BatchSize {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

// count adds the record to the usage and rollup aggregates. The aggregates are not
// dropped with the audit row: they are bounded only by the key cap.
func (w *Writer) count(record repository.AuditRecord) {
	batch := repository.UsageBatchOf(record)
	if len(batch.Usage) == 0 && len(batch.Rollups) == 0 {
		return
	}
	w.aggMu.Lock()
	w.addLocked(batch)
	full := len(w.usage)+len(w.rollups) >= w.cfg.UsageMaxKeys
	w.aggMu.Unlock()
	if full {
		select {
		case w.usageWake <- struct{}{}:
		default:
		}
	}
}

func (w *Writer) addLocked(batch repository.UsageBatch) {
	for _, u := range batch.Usage {
		key := usageKey{u.Repository, u.Format, u.Resource}
		if existing, ok := w.usage[key]; ok {
			existing.Downloads += u.Downloads
			existing.Bytes += u.Bytes
			if u.First.Before(existing.First) {
				existing.First = u.First
			}
			if !u.Last.Before(existing.Last) {
				existing.Last, existing.LastActor = u.Last, u.LastActor
			}
			continue
		}
		if len(w.usage) >= w.cfg.UsageMaxKeys {
			w.droppedUsage.Add(1)
			continue
		}
		copied := u
		w.usage[key] = &copied
	}
	for _, r := range batch.Rollups {
		key := rollupKey{r.Repository, r.BucketStart, r.Outcome}
		if existing, ok := w.rollups[key]; ok {
			existing.Requests += r.Requests
			existing.Bytes += r.Bytes
			continue
		}
		if len(w.rollups) >= w.cfg.UsageMaxKeys {
			w.droppedUsage.Add(1)
			continue
		}
		copied := r
		w.rollups[key] = &copied
	}
}

func (w *Writer) auditLoop() {
	defer w.done.Done()
	ticker := time.NewTicker(w.cfg.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.drain(context.Background(), false)
		case <-w.wake:
			w.drain(context.Background(), false)
		case <-w.stop:
			return
		}
	}
}

// drain writes everything in the buffer in batches. A failed batch is kept and
// retried with backoff while the loop is running; with final set it is attempted
// once per call so a shutdown deadline can end it.
func (w *Writer) drain(ctx context.Context, final bool) {
	backoff := 100 * time.Millisecond
	for {
		batch := w.take()
		if len(batch) == 0 {
			return
		}
		for {
			if err := w.flushBatch(ctx, batch); err == nil {
				w.pending.Add(-int64(len(batch)))
				break
			} else {
				w.flushErrors.Add(1)
				w.cfg.Logger.Error("audit flush failed", "records", len(batch), "error", err)
			}
			if final || ctx.Err() != nil {
				return
			}
			select {
			case <-time.After(backoff):
			case <-w.stop:
				return
			}
			if backoff < 5*time.Second {
				backoff *= 2
			}
		}
	}
}

func (w *Writer) take() []repository.AuditRecord {
	n := min(len(w.buffer), w.cfg.BatchSize)
	if n == 0 {
		return nil
	}
	batch := make([]repository.AuditRecord, 0, n)
	for len(batch) < n {
		select {
		case record := <-w.buffer:
			batch = append(batch, record)
		default:
			n = len(batch)
		}
	}
	w.pending.Add(int64(len(batch)))
	return batch
}

func (w *Writer) flushBatch(ctx context.Context, batch []repository.AuditRecord) error {
	started := time.Now()
	err := w.sink.InsertAuditBatch(ctx, batch)
	w.observeFlush(time.Since(started))
	return err
}

func (w *Writer) observeFlush(d time.Duration) {
	w.flushCount.Add(1)
	w.flushNanos.Add(uint64(d))
	for i, bound := range FlushBuckets {
		if d.Seconds() <= bound {
			w.flushBuckets[i].Add(1)
		}
	}
}

func (w *Writer) usageLoop() {
	defer w.done.Done()
	ticker := time.NewTicker(w.cfg.UsageFlushInterval)
	defer ticker.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	w.pruneRollups(context.Background())
	for {
		select {
		case <-ticker.C:
			w.flushUsage(context.Background())
		case <-w.usageWake:
			w.flushUsage(context.Background())
		case <-prune.C:
			w.pruneRollups(context.Background())
		case <-w.stop:
			return
		}
	}
}

func (w *Writer) pruneRollups(ctx context.Context) {
	if _, err := w.sink.PruneRequestRollups(ctx, time.Now().UTC().Add(-w.cfg.RollupRetention)); err != nil {
		w.cfg.Logger.Error("prune request rollups failed", "error", err)
	}
}

// flushUsage swaps the aggregates out and writes them; on failure they are merged
// back (up to the key cap) so they are retried at the next interval.
func (w *Writer) flushUsage(ctx context.Context) error {
	w.aggMu.Lock()
	usage, rollups := w.usage, w.rollups
	w.usage, w.rollups = map[usageKey]*repository.UsageDelta{}, map[rollupKey]*repository.RollupDelta{}
	w.aggMu.Unlock()
	if len(usage) == 0 && len(rollups) == 0 {
		return nil
	}
	var batch repository.UsageBatch
	for _, u := range usage {
		batch.Usage = append(batch.Usage, *u)
	}
	for _, r := range rollups {
		batch.Rollups = append(batch.Rollups, *r)
	}
	if err := w.sink.FlushUsage(ctx, batch); err != nil {
		w.flushErrors.Add(1)
		w.cfg.Logger.Error("usage flush failed", "keys", len(batch.Usage)+len(batch.Rollups), "error", err)
		w.aggMu.Lock()
		w.addLocked(batch)
		w.aggMu.Unlock()
		return err
	}
	return nil
}

// FlushNow writes the buffered audit records and the aggregates immediately. It is
// meant for tests and for operators' maintenance hooks.
func (w *Writer) FlushNow(ctx context.Context) error {
	w.drain(ctx, true)
	return w.flushUsage(ctx)
}

// Shutdown stops accepting buffered records and flushes what is left. Call it after
// the HTTP server has drained; ctx carries the NOTARX_SHUTDOWN_FLUSH_TIMEOUT
// deadline. Records still buffered when the deadline passes are lost and counted in
// the returned error.
func (w *Writer) Shutdown(ctx context.Context) error {
	var err error
	w.once.Do(func() {
		w.closed.Store(true)
		close(w.stop)
		w.done.Wait()
		w.drain(ctx, true)
		usageErr := w.flushUsage(ctx)
		if left := len(w.buffer) + int(w.pending.Load()); left > 0 {
			err = errors.Join(usageErr, errors.New("audit records left unflushed at shutdown"))
			w.cfg.Logger.Error("audit records lost at shutdown", "records", left)
			return
		}
		err = usageErr
	})
	return err
}

// Stats returns a snapshot for the metrics endpoint.
func (w *Writer) Stats() Stats {
	stats := Stats{
		BufferDepth: int64(len(w.buffer)) + w.pending.Load(),
		DroppedRead: w.droppedRead.Load(), DroppedSecurity: w.droppedSecurity.Load(), DroppedUsage: w.droppedUsage.Load(),
		FlushErrors: w.flushErrors.Load(), FlushCount: w.flushCount.Load(),
		FlushSecondsSum: float64(w.flushNanos.Load()) / float64(time.Second),
	}
	for i := range stats.FlushSecondsBucket {
		stats.FlushSecondsBucket[i] = w.flushBuckets[i].Load()
	}
	return stats
}
