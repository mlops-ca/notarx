package spool

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseRanges(t *testing.T) {
	cases := []struct {
		name  string
		value string
		size  int64
		want  []Interval
		ok    bool
	}{
		{"single", "bytes=0-3", 10, []Interval{{0, 3}}, true},
		{"open end", "bytes=4-", 10, []Interval{{4, 9}}, true},
		{"suffix", "bytes=-3", 10, []Interval{{7, 9}}, true},
		{"clamped end", "bytes=8-99", 10, []Interval{{8, 9}}, true},
		{"coalesce overlap and adjacency", "bytes=0-2,3-4,4-6", 10, []Interval{{0, 6}}, true},
		{"disjoint", "bytes=0-1,5-6", 10, []Interval{{0, 1}, {5, 6}}, true},
		{"unsatisfiable member ignored", "bytes=0-1,50-60", 10, []Interval{{0, 1}}, true},
		{"all unsatisfiable", "bytes=50-60", 10, nil, false},
		{"reversed", "bytes=5-2", 10, nil, false},
		{"bad unit", "items=0-1", 10, nil, false},
		{"garbage", "bytes=a-b", 10, nil, false},
		{"overflow", "bytes=99999999999999999999-", 10, nil, false},
		{"empty representation", "bytes=0-1", 0, nil, false},
		{"too many members", "bytes=" + strings.Repeat("0-0,", 20) + "1-1", 10, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseRanges(tc.value, tc.size)
			if ok != tc.ok || len(got) != len(tc.want) {
				t.Fatalf("ParseRanges = %v, %v; want %v, %v", got, ok, tc.want, tc.ok)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("interval %d = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseRangeSingle(t *testing.T) {
	if start, end, ok := ParseRange("bytes=2-", 6); !ok || start != 2 || end != 5 {
		t.Fatalf("ParseRange = %d %d %v", start, end, ok)
	}
	if _, _, ok := ParseRange("bytes=0-1,3-4", 6); ok {
		t.Fatal("multi range accepted by single parser")
	}
	if _, _, ok := ParseRange("bytes=-0", 6); ok {
		t.Fatal("zero suffix accepted")
	}
}

func TestIfRangeMatches(t *testing.T) {
	if !IfRangeMatches("", `"a"`) || !IfRangeMatches(`"a"`, `"a"`) {
		t.Fatal("matching validators rejected")
	}
	if IfRangeMatches(`"b"`, `"a"`) || IfRangeMatches(`W/"a"`, `W/"a"`) || IfRangeMatches(`"a"`, "") {
		t.Fatal("mismatched or weak validators accepted")
	}
}

func TestWriteMultipart(t *testing.T) {
	body := []byte("0123456789")
	open := func(_ context.Context, offset, length int64) (io.ReadCloser, int64, error) {
		return io.NopCloser(bytes.NewReader(body[offset : offset+length])), int64(len(body)), nil
	}
	recorder := httptest.NewRecorder()
	err := WriteMultipart(recorder, context.Background(), "text/plain", 10, []Interval{{0, 1}, {5, 6}}, open)
	if err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusPartialContent || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "multipart/byteranges; boundary=") {
		t.Fatalf("status %d type %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	out := recorder.Body.String()
	if !strings.Contains(out, "Content-Range: bytes 0-1/10") || !strings.Contains(out, "Content-Range: bytes 5-6/10") || !strings.Contains(out, "01") || !strings.Contains(out, "56") {
		t.Fatalf("body = %q", out)
	}
	if recorder.Header().Get("Content-Length") != strconv.Itoa(len(out)) {
		t.Fatalf("Content-Length %s, body %d", recorder.Header().Get("Content-Length"), len(out))
	}
}

func TestWriteMultipartUnavailableBeforeHeaders(t *testing.T) {
	failing := func(context.Context, int64, int64) (io.ReadCloser, int64, error) { return nil, 0, errors.New("gone") }
	recorder := httptest.NewRecorder()
	if err := WriteMultipart(recorder, context.Background(), "", 10, []Interval{{0, 1}, {5, 6}}, failing); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if recorder.Body.Len() != 0 {
		t.Fatal("bytes written before failure")
	}
	wrongSize := func(context.Context, int64, int64) (io.ReadCloser, int64, error) {
		return io.NopCloser(strings.NewReader("")), 99, nil
	}
	if err := WriteMultipart(httptest.NewRecorder(), context.Background(), "", 10, []Interval{{0, 1}}, wrongSize); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("size mismatch err = %v", err)
	}
}

func TestWriteMultipartTruncatesWhenLaterIntervalFails(t *testing.T) {
	calls := 0
	open := func(_ context.Context, offset, length int64) (io.ReadCloser, int64, error) {
		calls++
		if calls > 1 {
			return nil, 0, errors.New("gone")
		}
		return io.NopCloser(strings.NewReader("ab")), 10, nil
	}
	recorder := httptest.NewRecorder()
	if err := WriteMultipart(recorder, context.Background(), "", 10, []Interval{{0, 1}, {5, 6}}, open); err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(recorder.Body.String(), "--\r\n") {
		t.Fatal("closing delimiter written after a failure")
	}
}

func TestContextReaderStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := ContextReader{Ctx: ctx, Reader: strings.NewReader("abc")}
	buf := make([]byte, 1)
	if _, err := reader.Read(buf); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := reader.Read(buf); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestStage(t *testing.T) {
	staged, err := Stage(strings.NewReader("hello world"), 100, "spool-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Cleanup()
	if staged.Size != 11 || staged.Digest != "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9" {
		t.Fatalf("staged = %+v", staged)
	}
	whole, size, err := staged.Open()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(whole)
	_ = whole.Close()
	if string(data) != "hello world" || size != 11 {
		t.Fatalf("Open = %q, %d", data, size)
	}
	part, _, err := staged.OpenRange(context.Background(), 6, 5)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(part)
	_ = part.Close()
	if string(data) != "world" {
		t.Fatalf("OpenRange = %q", data)
	}
	if _, _, err := staged.OpenRange(context.Background(), 8, 9); err == nil {
		t.Fatal("out of bounds range accepted")
	}
	if _, _, err := staged.OpenRange(context.Background(), -1, 1); err == nil {
		t.Fatal("negative offset accepted")
	}
}

func TestStageRejectsOversizeAndLeavesNoFile(t *testing.T) {
	if _, err := Stage(strings.NewReader("0123456789"), 4, "spool-over-*"); !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Stage(strings.NewReader("x"), -1, "spool-neg-*"); !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("negative limit err = %v", err)
	}
	if exact, err := Stage(strings.NewReader("1234"), 4, "spool-exact-*"); err != nil {
		t.Fatalf("exact fit rejected: %v", err)
	} else {
		exact.Cleanup()
	}
	matches, _ := os.ReadDir(os.TempDir())
	for _, entry := range matches {
		if strings.HasPrefix(entry.Name(), "spool-over-") {
			t.Fatalf("leftover file %s", entry.Name())
		}
	}
	Staged{}.Cleanup()
}

func TestStageReaderError(t *testing.T) {
	failing := io.MultiReader(strings.NewReader("ab"), errReader{})
	if _, err := Stage(failing, 100, "spool-err-*"); err == nil {
		t.Fatal("read error swallowed")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestSlots(t *testing.T) {
	slots := NewSlots(1)
	release, err := slots.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slots.Acquire(); !errors.Is(err, ErrCapacity) {
		t.Fatalf("second acquire = %v", err)
	}
	release()
	release()
	again, err := slots.Acquire()
	if err != nil {
		t.Fatalf("slot not released: %v", err)
	}
	again()
	var none *Slots
	free, err := none.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	free()
}

type stubCoordinator struct {
	mu       sync.Mutex
	held     bool
	renewals int
	failNext bool
}

func (c *stubCoordinator) Acquire(context.Context, string, time.Duration) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held {
		return "", false, nil
	}
	c.held = true
	return "owner", true, nil
}
func (c *stubCoordinator) Renew(context.Context, string, string, time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.renewals++
	return !c.failNext, nil
}
func (c *stubCoordinator) Release(context.Context, string, string) error {
	c.mu.Lock()
	c.held = false
	c.mu.Unlock()
	return nil
}
func (*stubCoordinator) CircuitOpen(context.Context, string) (bool, error)        { return false, nil }
func (*stubCoordinator) OpenCircuit(context.Context, string, time.Duration) error { return nil }
func (*stubCoordinator) CloseCircuit(context.Context, string) error               { return nil }

func TestLockerWithoutCoordinatorIsNoop(t *testing.T) {
	ctx, release, err := Locker{}.Acquire(context.Background(), "k")
	if err != nil || ctx == nil || release() != nil {
		t.Fatalf("no-op lock failed: %v", err)
	}
}

func TestLockerSerializesAndRenews(t *testing.T) {
	coordinator := &stubCoordinator{}
	locker := Locker{Coordinator: coordinator, Lease: time.Second, RenewEvery: 5 * time.Millisecond, Prefix: "p:"}
	_, release, err := locker.Acquire(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	waiting := make(chan error, 1)
	go func() {
		_, second, err := locker.Acquire(context.Background(), "k")
		if err == nil {
			err = second()
		}
		waiting <- err
	}()
	deadline := time.After(5 * time.Second)
	for {
		coordinator.mu.Lock()
		renewed := coordinator.renewals
		coordinator.mu.Unlock()
		if renewed > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("lock never renewed")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case err := <-waiting:
		t.Fatalf("second holder entered while the first held the lock: %v", err)
	default:
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := <-waiting; err != nil {
		t.Fatalf("second holder: %v", err)
	}
}

func TestLockerRenewalFailureCancelsAndReports(t *testing.T) {
	coordinator := &stubCoordinator{failNext: true}
	locker := Locker{Coordinator: coordinator, Lease: time.Second, RenewEvery: time.Millisecond}
	ctx, release, err := locker.Acquire(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("work context not cancelled after renewal failure")
	}
	if err := release(); err == nil {
		t.Fatal("renewal failure not reported")
	}
}

func TestLockerAcquireHonoursCancellation(t *testing.T) {
	coordinator := &stubCoordinator{held: true}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, _, err := (Locker{Coordinator: coordinator, Lease: time.Second, RenewEvery: time.Second}).Acquire(ctx, "k"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}
