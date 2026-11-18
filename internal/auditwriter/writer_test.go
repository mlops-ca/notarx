package auditwriter

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

type fakeSink struct {
	mu          sync.Mutex
	inserted    []repository.AuditRecord
	batches     [][]repository.AuditRecord
	usage       []repository.UsageBatch
	insertErr   error
	batchErr    error
	usageErr    error
	insertGate  chan struct{} // when set, InsertAudit waits for it
	batchGate   chan struct{} // when set, InsertAuditBatch waits for it
	insertCalls atomic.Int64
}

func (f *fakeSink) InsertAudit(ctx context.Context, r repository.AuditRecord) error {
	f.insertCalls.Add(1)
	if f.insertGate != nil {
		select {
		case <-f.insertGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertErr != nil {
		return f.insertErr
	}
	f.inserted = append(f.inserted, r)
	return nil
}

func (f *fakeSink) InsertAuditBatch(ctx context.Context, rs []repository.AuditRecord) error {
	if f.batchGate != nil {
		select {
		case <-f.batchGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.batchErr != nil {
		return f.batchErr
	}
	f.batches = append(f.batches, append([]repository.AuditRecord(nil), rs...))
	return nil
}

func (f *fakeSink) FlushUsage(_ context.Context, b repository.UsageBatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.usageErr != nil {
		return f.usageErr
	}
	f.usage = append(f.usage, b)
	return nil
}

func (f *fakeSink) PruneRequestRollups(context.Context, time.Time) (int64, error) { return 0, nil }

func (f *fakeSink) batched() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.batches {
		n += len(b)
	}
	return n
}

func quiet(c Config) Config {
	c.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return c
}

func download(repo, resource string) repository.AuditRecord {
	return repository.AuditRecord{Repository: repo, Format: "raw", Resource: resource, Operation: "get", Status: 200,
		Outcome: repository.AuditResolved, Actor: "alice", Bytes: 10}
}

func mutation() repository.AuditRecord {
	return repository.AuditRecord{Repository: "r", Format: "management", Operation: "repo update", Outcome: repository.AuditResolved, Actor: "admin", Status: 200}
}

func denial(actor string) repository.AuditRecord {
	return repository.AuditRecord{Repository: "r", Format: "raw", Operation: "get", Outcome: repository.AuditAccessDenied, Actor: actor, Status: 403}
}

func TestClassRouting(t *testing.T) {
	sink := &fakeSink{}
	w := New(sink, quiet(Config{FlushInterval: time.Hour, UsageFlushInterval: time.Hour}))
	ctx := context.Background()
	for _, record := range []repository.AuditRecord{mutation(), denial("alice"), download("r", "a"), denial(repository.AnonymousActor)} {
		if err := w.RecordAudit(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.FlushNow(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.inserted) != 2 || sink.inserted[0].Class != repository.AuditClassSync || sink.inserted[1].Class != repository.AuditClassSync {
		t.Fatalf("sync class must be committed directly: %+v", sink.inserted)
	}
	if sink.batched() != 2 {
		t.Fatalf("async class (a read and an anonymous denial) must be batched: %+v", sink.batches)
	}
	for _, batch := range sink.batches {
		for _, record := range batch {
			if record.Class != repository.AuditClassAsync {
				t.Fatalf("batched record class=%q", record.Class)
			}
		}
	}
}

func TestFullBufferDropsWithoutBlocking(t *testing.T) {
	sink := &fakeSink{batchGate: make(chan struct{})}
	w := New(sink, quiet(Config{BufferSize: 8, BatchSize: 4, FlushInterval: time.Millisecond, UsageFlushInterval: time.Hour}))
	w.Start()
	defer func() {
		close(sink.batchGate)
		_ = w.Shutdown(context.Background())
	}()
	started := time.Now()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if err := w.RecordAudit(context.Background(), download("r", "a")); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("request path blocked for %s with a stuck sink", elapsed)
	}
	if w.Stats().DroppedRead == 0 {
		t.Fatalf("a full buffer must drop and count: %+v", w.Stats())
	}
	if depth := w.Stats().BufferDepth; depth > 8+4 {
		t.Fatalf("buffer depth %d exceeds the buffer plus the batch in flight", depth)
	}
}

func TestMutationAuditFailureFailsTheMutation(t *testing.T) {
	boom := errors.New("insert failed")
	sink := &fakeSink{insertErr: boom}
	w := New(sink, quiet(Config{FlushInterval: time.Hour, UsageFlushInterval: time.Hour}))
	if err := w.RecordAudit(context.Background(), mutation()); !errors.Is(err, boom) {
		t.Fatalf("a failed mutation audit must be returned so the change fails closed: %v", err)
	}
	if w.Stats().BufferDepth != 0 {
		t.Fatal("a mutation audit is never queued")
	}
}

func TestSemaphoreExhaustionQueuesThenDropsDenials(t *testing.T) {
	sink := &fakeSink{insertGate: make(chan struct{})}
	w := New(sink, quiet(Config{BufferSize: 1, SyncMaxInflight: 1, SecurityWait: 20 * time.Millisecond, FlushInterval: time.Hour, UsageFlushInterval: time.Hour}))
	ctx := context.Background()
	held := make(chan error, 1)
	go func() { held <- w.RecordAudit(ctx, denial("alice")) }()
	for sink.insertCalls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	// The semaphore is exhausted: the first denial queues, the second waits 20 ms and drops.
	if err := w.RecordAudit(ctx, denial("bob")); err != nil {
		t.Fatal(err)
	}
	if err := w.RecordAudit(ctx, denial("carol")); err != nil {
		t.Fatal(err)
	}
	if stats := w.Stats(); stats.BufferDepth != 1 || stats.DroppedSecurity != 1 {
		t.Fatalf("want one queued and one dropped security record: %+v", stats)
	}
	// The sync class is never dropped by the semaphore: a mutation still commits.
	mutationDone := make(chan error, 1)
	go func() { mutationDone <- w.RecordAudit(ctx, mutation()) }()
	close(sink.insertGate)
	if err := <-mutationDone; err != nil {
		t.Fatal(err)
	}
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	if w.Stats().DroppedSecurity != 1 {
		t.Fatal("only the overflowing denial may be dropped")
	}
}

func TestDenialModeSyncBlocksInsteadOfDropping(t *testing.T) {
	sink := &fakeSink{insertGate: make(chan struct{})}
	w := New(sink, quiet(Config{SyncMaxInflight: 1, DenialMode: DenialSync, FlushInterval: time.Hour, UsageFlushInterval: time.Hour}))
	ctx := context.Background()
	results := make(chan error, 2)
	for _, actor := range []string{"alice", "bob"} {
		go func() { results <- w.RecordAudit(ctx, denial(actor)) }()
	}
	time.Sleep(50 * time.Millisecond)
	if len(results) != 0 {
		t.Fatal("denials must block while the gate is closed")
	}
	close(sink.insertGate)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if w.Stats().DroppedSecurity != 0 || len(sink.inserted) != 2 {
		t.Fatalf("sync mode must commit every denial: dropped=%d inserted=%d", w.Stats().DroppedSecurity, len(sink.inserted))
	}
}

func TestReadModeSyncCommitsEveryRead(t *testing.T) {
	sink := &fakeSink{}
	w := New(sink, quiet(Config{ReadMode: ReadSync, FlushInterval: time.Hour, UsageFlushInterval: time.Hour}))
	for i := 0; i < 3; i++ {
		if err := w.RecordAudit(context.Background(), download("r", "a")); err != nil {
			t.Fatal(err)
		}
	}
	if len(sink.inserted) != 3 || w.Stats().BufferDepth != 0 {
		t.Fatalf("sync read mode commits before returning: %d inserted", len(sink.inserted))
	}
}

// A record the database rejects on its own is dropped and counted; it does not stall the
// records behind it.
func TestPoisonRecordDoesNotBlockTheBatch(t *testing.T) {
	sink := &poisonSink{fakeSink: &fakeSink{}}
	w := New(sink, quiet(Config{FlushInterval: time.Hour, UsageFlushInterval: time.Hour}))
	for _, resource := range []string{"a", "poison", "b"} {
		_ = w.RecordAudit(context.Background(), download("r", resource))
	}
	if err := w.FlushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(sink.inserted); got != 2 || w.Stats().DroppedInvalid != 1 || w.Stats().BufferDepth != 0 {
		t.Fatalf("inserted=%d stats=%+v", got, w.Stats())
	}
}

type poisonSink struct{ *fakeSink }

func (p *poisonSink) InsertAuditBatch(_ context.Context, rs []repository.AuditRecord) error {
	for _, r := range rs {
		if r.Resource == "poison" {
			return errors.New("invalid byte sequence")
		}
	}
	return p.fakeSink.InsertAuditBatch(context.Background(), rs)
}

func (p *poisonSink) InsertAudit(ctx context.Context, r repository.AuditRecord) error {
	if r.Resource == "poison" {
		return errors.New("invalid byte sequence")
	}
	return p.fakeSink.InsertAudit(ctx, r)
}

// A batch that fails while the writer stops is kept for the final flush, not discarded.
func TestFailedBatchIsRetainedForTheFinalFlush(t *testing.T) {
	down := errors.New("database down")
	sink := &fakeSink{batchErr: down, insertErr: down}
	w := New(sink, quiet(Config{FlushInterval: 10 * time.Millisecond, UsageFlushInterval: time.Hour}))
	w.Start()
	for i := 0; i < 3; i++ {
		_ = w.RecordAudit(context.Background(), download("r", "a"))
	}
	time.Sleep(60 * time.Millisecond) // the first attempt failed and the loop is backing off
	sink.mu.Lock()
	sink.batchErr, sink.insertErr = nil, nil
	sink.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if sink.batched() != 3 {
		t.Fatalf("the retained batch must be written at shutdown: %d", sink.batched())
	}
}

func TestShutdownFlushesBufferAndUsage(t *testing.T) {
	sink := &fakeSink{}
	w := New(sink, quiet(Config{FlushInterval: time.Hour, UsageFlushInterval: time.Hour}))
	w.Start()
	for i := 0; i < 25; i++ {
		if err := w.RecordAudit(context.Background(), download("r", "a")); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if sink.batched() != 25 {
		t.Fatalf("shutdown flushed %d of 25 records", sink.batched())
	}
	if len(sink.usage) != 1 || sink.usage[0].Usage[0].Downloads != 25 {
		t.Fatalf("shutdown must flush the usage aggregate: %+v", sink.usage)
	}
	// A record after shutdown is committed directly, not lost.
	if err := w.RecordAudit(context.Background(), download("r", "b")); err != nil || len(sink.inserted) != 1 {
		t.Fatalf("late record: err=%v inserted=%d", err, len(sink.inserted))
	}
	if len(sink.usage) != 2 || sink.usage[1].Usage[0].Resource != "b" {
		t.Fatalf("a late record must write its own usage delta: %+v", sink.usage)
	}
}

func TestShutdownReportsRecordsLostAtTheDeadline(t *testing.T) {
	sink := &fakeSink{batchErr: errors.New("database down")}
	w := New(sink, quiet(Config{FlushInterval: time.Hour, UsageFlushInterval: time.Hour}))
	w.Start()
	_ = w.RecordAudit(context.Background(), download("r", "a"))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := w.Shutdown(ctx); err == nil {
		t.Fatal("unflushed records at shutdown must be reported")
	}
}

// The crash-loss bound: a record leaves the process memory within one flush
// interval while the database is healthy, and when it is not the buffer holds at
// most BufferSize records before it drops.
func TestCrashLossBound(t *testing.T) {
	sink := &fakeSink{}
	interval := 50 * time.Millisecond
	w := New(sink, quiet(Config{BatchSize: 1000, FlushInterval: interval, UsageFlushInterval: time.Hour}))
	w.Start()
	defer func() { _ = w.Shutdown(context.Background()) }()
	for i := 0; i < 10; i++ {
		_ = w.RecordAudit(context.Background(), download("r", "a"))
	}
	deadline := time.Now().Add(20 * interval)
	for sink.batched() < 10 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if sink.batched() != 10 {
		t.Fatalf("records were not flushed within the interval bound: %d", sink.batched())
	}

	outage := errors.New("database down")
	down := &fakeSink{batchErr: outage, insertErr: outage}
	w2 := New(down, quiet(Config{BufferSize: 20, BatchSize: 5, FlushInterval: interval, UsageFlushInterval: time.Hour}))
	w2.Start()
	defer func() { _ = w2.Shutdown(context.Background()) }()
	for i := 0; i < 200; i++ {
		_ = w2.RecordAudit(context.Background(), download("r", "a"))
	}
	stats := w2.Stats()
	if stats.BufferDepth > 20+5 || stats.DroppedRead == 0 {
		t.Fatalf("with the database down the loss is bounded by the buffer: %+v", stats)
	}
}

func TestThousandDownloadsOfOneArtifactAreOneUsageUpdate(t *testing.T) {
	sink := &fakeSink{}
	w := New(sink, quiet(Config{FlushInterval: time.Hour, UsageFlushInterval: time.Hour}))
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.RecordAudit(context.Background(), download("r", "hot.bin"))
		}()
	}
	wg.Wait()
	if err := w.FlushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.usage) != 1 || len(sink.usage[0].Usage) != 1 {
		t.Fatalf("one flush with one usage row expected, got %+v", sink.usage)
	}
	if got := sink.usage[0].Usage[0]; got.Downloads != 1000 || got.Bytes != 10000 {
		t.Fatalf("aggregate=%+v", got)
	}
	var requests int64
	for _, r := range sink.usage[0].Rollups {
		requests += r.Requests
	}
	if requests != 1000 {
		t.Fatalf("rollup requests=%d", requests)
	}
}

func TestUsageIsRetriedAfterAFailedFlush(t *testing.T) {
	sink := &fakeSink{usageErr: errors.New("down")}
	w := New(sink, quiet(Config{FlushInterval: time.Hour, UsageFlushInterval: time.Hour}))
	_ = w.RecordAudit(context.Background(), download("r", "a"))
	if err := w.FlushNow(context.Background()); err == nil {
		t.Fatal("expected the usage flush error")
	}
	sink.mu.Lock()
	sink.usageErr = nil
	sink.mu.Unlock()
	_ = w.RecordAudit(context.Background(), download("r", "a"))
	if err := w.FlushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.usage) != 1 || sink.usage[0].Usage[0].Downloads != 2 {
		t.Fatalf("the failed interval must be merged into the next flush: %+v", sink.usage)
	}
}

func TestUsageKeyCapDropsNewKeysAndCounts(t *testing.T) {
	sink := &fakeSink{}
	w := New(sink, quiet(Config{UsageMaxKeys: 3, FlushInterval: time.Hour, UsageFlushInterval: time.Hour}))
	for _, resource := range []string{"a", "b", "c", "d", "e"} {
		_ = w.RecordAudit(context.Background(), download("r", resource))
	}
	if w.Stats().DroppedUsage == 0 {
		t.Fatal("keys beyond the cap must be dropped and counted")
	}
}
