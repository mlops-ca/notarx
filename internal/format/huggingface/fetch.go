package huggingface

import (
	"context"
	"crypto/sha1" //nolint:gosec // a git blob id is a sha1 by definition; it only verifies regular files
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/mlops-ca/notarx/internal/capacity"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/spool"
)

// Errors of the object fetch.
var (
	// ErrDigestMismatch means the bytes upstream sent are not the object the tree named. The
	// staging file is discarded and nothing reaches the cache.
	ErrDigestMismatch = errors.New("hugging face object digest mismatch")
	// ErrObjectTooLarge means the object exceeds NOTARX_HF_MAX_OBJECT_BYTES or the limit of the
	// repository.
	ErrObjectTooLarge = errors.New("hugging face object exceeds the size limit")
	// ErrFetchCapacity means every cold fetch slot of the process is busy (NOTARX_HF_MAX_CONCURRENT_FETCHES).
	ErrFetchCapacity = spool.ErrCapacity
)

// Fetch outcomes reported to Metrics.
const (
	FetchHit            = "hit"
	FetchFetched        = "fetched"
	FetchDigestMismatch = "digest_mismatch"
	FetchQuota          = "quota"
	FetchTooLarge       = "too_large"
	FetchCapacity       = "capacity"
	FetchUpstreamError  = "upstream_error"
)

const (
	// defaultHoldBack is the tail of an object withheld from every reader until the digest
	// verified: a mismatch then aborts readers before they have a complete body.
	defaultHoldBack = 256 << 10
	// defaultFetchTimeout bounds one cold fetch, however large the object.
	defaultFetchTimeout = 12 * time.Hour
)

// Metrics receives the outcome of every object request. Implementations must not block.
type Metrics interface {
	RecordHuggingFaceFetch(outcome string)
}

// Object names one file to serve and how to fetch it from upstream.
type Object struct {
	RepositoryID string // the notarx repository, for quota
	HFRepoID     string
	CommitID     string
	Path         string
	Size         int64
	// Open starts the upstream byte fetch; the fetcher closes the body.
	Open func(ctx context.Context) (*http.Response, error)
}

// Fetcher serves objects from the store, and fetches a cold object once per digest through a
// tee (ADR 0027): the bytes go to a staging file and to every waiting client at once, the
// digest is verified at the end of the stream, and only then is the object committed.
type Fetcher struct {
	Store   *Store
	Objects objectstore.Store
	Config  Config
	// Slots bounds concurrent cold fetches in this process; nil means NewFetcher's default.
	Slots *spool.Slots
	// Locker serializes a digest across nodes; the zero value is a no-op lock.
	Locker  spool.Locker
	Metrics Metrics
	// HoldBack overrides the withheld tail; zero selects 256 KiB.
	HoldBack int64
	// FetchTimeout overrides the bound of one cold fetch; zero selects 12 hours.
	FetchTimeout time.Duration

	mu      sync.Mutex
	flights map[string]*flight
}

// NewFetcher returns a fetcher whose slot limit is Config.MaxConcurrentFetches.
func NewFetcher(store *Store, objects objectstore.Store, cfg Config) *Fetcher {
	return &Fetcher{Store: store, Objects: objects, Config: cfg, Slots: spool.NewSlots(max(1, cfg.MaxConcurrentFetches))}
}

func (f *Fetcher) holdBack() int64 {
	if f.HoldBack > 0 {
		return f.HoldBack
	}
	return defaultHoldBack
}

func (f *Fetcher) record(outcome string) {
	if f.Metrics != nil {
		f.Metrics.RecordHuggingFaceFetch(outcome)
	}
}

// limit is the object size limit that applies to the repository: the lower of the deployment
// limit and the per-repository limit.
func (f *Fetcher) limit(ctx context.Context, repositoryID string) (int64, error) {
	limit := f.Config.MaxObjectBytes
	if limit <= 0 {
		limit = DefaultMaxObjectBytes
	}
	repoLimit, err := f.Store.MaxObjectBytes(ctx, repositoryID)
	if err != nil {
		return 0, err
	}
	if repoLimit > 0 && repoLimit < limit {
		limit = repoLimit
	}
	return limit, nil
}

// flight is one in-progress cold fetch shared by every request for the same object.
type flight struct {
	key  string
	size int64
	hold int64

	ready    chan struct{} // closed once the staging file exists or the fetch failed to start
	readyErr error

	mu      sync.Mutex
	changed chan struct{} // closed and replaced on every change
	written int64
	done    bool
	err     error
	file    *os.File
	refs    int
}

func newFlight(key string, size, hold int64) *flight {
	return &flight{key: key, size: size, hold: hold, ready: make(chan struct{}), changed: make(chan struct{})}
}

// signal wakes every waiting reader; the caller holds mu.
func (fl *flight) signal() {
	close(fl.changed)
	fl.changed = make(chan struct{})
}

// readable is how many bytes a reader may take: everything once the fetch verified, otherwise
// all but the withheld tail.
func (fl *flight) readable() int64 {
	if fl.done && fl.err == nil {
		return fl.written
	}
	return max(0, fl.written-fl.hold)
}

// attach registers a reader; the returned release drops the staging file after the last one.
func (fl *flight) attach() (release func()) {
	fl.mu.Lock()
	fl.refs++
	fl.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			fl.mu.Lock()
			fl.refs--
			fl.cleanupLocked()
			fl.mu.Unlock()
		})
	}
}

func (fl *flight) cleanupLocked() {
	if fl.done && fl.refs <= 0 && fl.file != nil {
		name := fl.file.Name()
		_ = fl.file.Close()
		_ = os.Remove(name)
		fl.file = nil
	}
}

// flightReader reads [off, end) of the staging file as the fetch makes the bytes readable.
type flightReader struct {
	ctx context.Context
	fl  *flight
	off int64
	end int64
}

func (r *flightReader) Read(p []byte) (int, error) {
	for {
		if r.off >= r.end {
			return 0, io.EOF
		}
		r.fl.mu.Lock()
		if r.fl.done && r.fl.err != nil {
			err := r.fl.err
			r.fl.mu.Unlock()
			return 0, err
		}
		if available := min(r.fl.readable(), r.end) - r.off; available > 0 && r.fl.file != nil {
			n := int(min(int64(len(p)), available))
			file := r.fl.file
			r.fl.mu.Unlock()
			n, err := file.ReadAt(p[:n], r.off)
			r.off += int64(n)
			if n > 0 {
				return n, nil
			}
			return 0, err
		}
		if r.fl.done {
			r.fl.mu.Unlock()
			return 0, io.ErrUnexpectedEOF
		}
		changed := r.fl.changed
		r.fl.mu.Unlock()
		select {
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		case <-changed:
		}
	}
}

func (r *flightReader) Close() error { return nil }

func (fl *flight) open(ctx context.Context, offset, length int64) (io.ReadCloser, int64, error) {
	if offset < 0 || length < 0 || offset > fl.size || length > fl.size-offset {
		return nil, 0, errors.New("range is out of bounds")
	}
	return &flightReader{ctx: ctx, fl: fl, off: offset, end: offset + length}, fl.size, nil
}

// Serve writes the object to w: from the object store when it is cached, otherwise by
// joining (or starting) the fetch of its digest. Range and If-Range are honoured in both
// cases; a Range request on a cold object is served from the staging file as the bytes
// arrive. headers are set on the response first (ETag, X-Repo-Commit, ...).
//
// written reports whether response bytes or headers were committed; when it is true and err
// is not nil the caller must abort the connection instead of writing an error response.
func (f *Fetcher) Serve(w http.ResponseWriter, r *http.Request, obj Object, headers http.Header) (written bool, err error) {
	ctx := r.Context()
	limit, err := f.limit(ctx, obj.RepositoryID)
	if err != nil {
		return false, err
	}
	if obj.Size > limit {
		f.record(FetchTooLarge)
		return false, ErrObjectTooLarge
	}
	file, err := f.Store.GetFile(ctx, obj.CommitID, obj.Path)
	if err != nil {
		return false, err
	}
	if file.Size != obj.Size {
		return false, fmt.Errorf("hugging face file size changed: %d != %d", file.Size, obj.Size)
	}
	cached := file.Cached && file.SHA256 != ""
	if !cached && file.SHA256 != "" {
		// The repository may already hold this digest from another commit.
		if held, err := f.Store.HasBlob(ctx, obj.HFRepoID, file.SHA256); err == nil && held {
			cached = true
		}
	}
	if cached {
		f.record(FetchHit)
		written, err := f.serveRanges(w, r, obj.Size, headers, func(ctx context.Context, offset, length int64) (io.ReadCloser, int64, error) {
			return f.Objects.OpenRange(ctx, BlobKey(file.SHA256), offset, length)
		})
		if err == nil || written || !errors.Is(err, objectstore.ErrNotFound) {
			return written, err
		}
		// The object is gone although the database says it is cached (collected by a racing
		// sweep, or lost): forget the blob and fetch it again.
		if err := f.Store.ForgetBlob(ctx, obj.RepositoryID, obj.HFRepoID, file.SHA256); err != nil {
			return false, err
		}
		file.Cached = false
	}
	fl, release, err := f.join(ctx, obj, file)
	if err != nil {
		return false, err
	}
	defer release()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-fl.ready:
	}
	if fl.readyErr != nil {
		return false, fl.readyErr
	}
	return f.serveRanges(w, r, obj.Size, headers, fl.open)
}

// join returns the flight of the object's digest, starting it when none is running.
// flightKey identifies one fetch. It includes the repository: a joiner must not inherit the quota,
// credential or bookkeeping of another repository's fetch. A regular file has no digest before it is
// read, so it is keyed by its place in the commit.
func flightKey(obj Object, file File) string {
	if file.SHA256 != "" {
		return obj.RepositoryID + ":" + file.SHA256
	}
	return obj.RepositoryID + ":git:" + obj.CommitID + ":" + obj.Path
}

func (f *Fetcher) join(ctx context.Context, obj Object, file File) (*flight, func(), error) {
	key := flightKey(obj, file)
	f.mu.Lock()
	if f.flights == nil {
		f.flights = map[string]*flight{}
	}
	if fl, ok := f.flights[key]; ok {
		release := fl.attach()
		f.mu.Unlock()
		return fl, release, nil
	}
	if f.Slots == nil {
		f.Slots = spool.NewSlots(max(1, f.Config.MaxConcurrentFetches))
	}
	releaseSlot, err := f.Slots.Acquire()
	if err != nil {
		f.mu.Unlock()
		f.record(FetchCapacity)
		return nil, nil, err
	}
	fl := newFlight(key, obj.Size, f.holdBack())
	f.flights[key] = fl
	release := fl.attach()
	f.mu.Unlock()
	go func() {
		defer releaseSlot()
		f.run(fl, obj, file)
	}()
	return fl, release, nil
}

// end retires the flight from the table first, so a request that arrives afterwards sees the committed
// object (or starts a new fetch) instead of joining a flight that is about to close its staging file,
// then wakes every reader. A nil err is success.
func (f *Fetcher) end(fl *flight, err error) {
	f.mu.Lock()
	delete(f.flights, fl.key)
	f.mu.Unlock()
	fl.mu.Lock()
	fl.done, fl.err = true, err
	fl.cleanupLocked()
	fl.signal()
	fl.mu.Unlock()
	if err != nil {
		fl.markReady(err)
	}
}

func (fl *flight) markReady(err error) {
	select {
	case <-fl.ready:
	default:
		fl.readyErr = err
		close(fl.ready)
	}
}

// run is the fetch: it outlives the request that started it, so a client that disconnects
// does not waste the transfer other clients, or the cache, can use.
func (f *Fetcher) run(fl *flight, obj Object, file File) {
	timeout := f.FetchTimeout
	if timeout <= 0 {
		timeout = defaultFetchTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ctx, unlock, err := f.Locker.Acquire(ctx, "huggingface-fetch:"+fl.key)
	if err != nil {
		f.record(FetchUpstreamError)
		f.end(fl, err)
		return
	}
	defer func() { _ = unlock() }()
	// Another node may have finished this object while this one waited for the lock.
	if current, err := f.Store.GetFile(ctx, obj.CommitID, obj.Path); err == nil && current.Cached && current.SHA256 != "" {
		f.serveFromStoreOnce(fl, current.SHA256)
		return
	}
	if err := f.Store.Headroom(ctx, obj.RepositoryID, obj.Size); err != nil {
		f.record(FetchQuota)
		f.end(fl, err)
		return
	}
	response, err := obj.Open(ctx)
	if err != nil {
		f.record(FetchUpstreamError)
		f.end(fl, err)
		return
	}
	defer response.Body.Close()
	if response.ContentLength >= 0 && response.ContentLength != obj.Size {
		f.record(FetchUpstreamError)
		f.end(fl, fmt.Errorf("%w: upstream announced %d bytes, the tree says %d", ErrDigestMismatch, response.ContentLength, obj.Size))
		return
	}
	staging, err := os.CreateTemp("", "notarx-hf-*")
	if err != nil {
		f.end(fl, err)
		return
	}
	fl.mu.Lock()
	fl.file = staging
	fl.mu.Unlock()
	fl.markReady(nil)

	sum := sha256.New()
	var blob hash.Hash
	if file.SHA256 == "" && file.GitBlobID != "" {
		blob = sha1.New() //nolint:gosec
		fmt.Fprintf(blob, "blob %d\x00", obj.Size)
	}
	buffer := make([]byte, spool.StreamCopyBufferSize)
	var total int64
	for total < obj.Size {
		n, readErr := response.Body.Read(buffer[:min(int64(len(buffer)), obj.Size-total)])
		if n > 0 {
			if _, err := staging.WriteAt(buffer[:n], total); err != nil {
				f.record(FetchUpstreamError)
				f.end(fl, err)
				return
			}
			sum.Write(buffer[:n])
			if blob != nil {
				blob.Write(buffer[:n])
			}
			total += int64(n)
			fl.mu.Lock()
			fl.written = total
			fl.signal()
			fl.mu.Unlock()
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) && total == obj.Size {
				break
			}
			if errors.Is(readErr, io.EOF) {
				readErr = io.ErrUnexpectedEOF
			}
			f.record(FetchUpstreamError)
			f.end(fl, readErr)
			return
		}
	}
	// A body longer than the tree says is as wrong as a short one.
	if extra, _ := response.Body.Read(buffer[:1]); extra > 0 {
		f.record(FetchDigestMismatch)
		f.end(fl, fmt.Errorf("%w: upstream sent more than %d bytes", ErrDigestMismatch, obj.Size))
		return
	}
	digest := hex.EncodeToString(sum.Sum(nil))
	if (file.SHA256 != "" && digest != file.SHA256) || (blob != nil && hex.EncodeToString(blob.Sum(nil)) != file.GitBlobID) {
		f.record(FetchDigestMismatch)
		f.end(fl, ErrDigestMismatch)
		return
	}
	if err := f.commit(ctx, obj, digest, staging); err != nil {
		if errors.Is(err, capacity.ErrQuotaExceeded) {
			f.record(FetchQuota)
		} else {
			f.record(FetchUpstreamError)
		}
		f.end(fl, err)
		return
	}
	f.record(FetchFetched)
	f.end(fl, nil)
}

// serveFromStoreOnce ends a flight whose object another node cached while this one waited for the
// lock: the verified object is copied into the staging file, so the waiting readers share the
// one code path.
func (f *Fetcher) serveFromStoreOnce(fl *flight, digest string) {
	staging, err := os.CreateTemp("", "notarx-hf-*")
	if err != nil {
		f.end(fl, err)
		return
	}
	reader, _, err := f.Objects.Open(context.Background(), BlobKey(digest))
	if err != nil {
		_ = staging.Close()
		_ = os.Remove(staging.Name())
		f.end(fl, err)
		return
	}
	defer reader.Close()
	fl.mu.Lock()
	fl.file = staging
	fl.mu.Unlock()
	fl.markReady(nil)
	var total int64
	buffer := make([]byte, spool.StreamCopyBufferSize)
	for {
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if _, err := staging.WriteAt(buffer[:n], total); err != nil {
				f.end(fl, err)
				return
			}
			total += int64(n)
			fl.mu.Lock()
			fl.written = total
			fl.signal()
			fl.mu.Unlock()
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				f.end(fl, readErr)
				return
			}
			break
		}
	}
	fl.mu.Lock()
	complete := total == fl.size
	fl.mu.Unlock()
	if !complete {
		f.end(fl, io.ErrUnexpectedEOF)
		return
	}
	f.end(fl, nil)
}

// commit stores the verified bytes and records the blob. The object goes in first, under a
// key that holds exactly this digest; the database transaction that follows reserves the
// bytes as its last statement, so an over-quota commit leaves nothing counted.
func (f *Fetcher) commit(ctx context.Context, obj Object, digest string, staging *os.File) error {
	reader := io.NewSectionReader(staging, 0, obj.Size)
	if err := f.Objects.PutVerifiedReader(ctx, BlobKey(digest), reader, obj.Size, "sha256:"+digest); err != nil {
		return err
	}
	if err := f.Store.CommitBlob(ctx, obj.RepositoryID, obj.HFRepoID, obj.CommitID, obj.Path, digest, obj.Size); err != nil {
		if errors.Is(err, capacity.ErrQuotaExceeded) {
			// Nothing counted this digest, so no repository holds it: remove the object again.
			if held, holdErr := f.Store.BlobHeldAnywhere(ctx, digest); holdErr == nil && !held {
				_ = f.Objects.Delete(context.WithoutCancel(ctx), BlobKey(digest))
			}
		}
		return err
	}
	return nil
}

// serveRanges writes a 200, 206 or 416 response for a representation of size bytes read
// through open. It is the Raw range grammar (single and multiple ranges, If-Range) shared
// through internal/spool. The response headers are applied only once the source has been
// opened, so a failure to open leaves the writer untouched for an error response.
func (f *Fetcher) serveRanges(w http.ResponseWriter, r *http.Request, size int64, raw http.Header, open spool.RangeOpener) (bool, error) {
	// Header names are canonicalized, so a caller's map literal and Get agree.
	headers := http.Header{}
	for name, values := range raw {
		for _, value := range values {
			headers.Add(name, value)
		}
	}
	apply := func(target http.Header) {
		for name, values := range headers {
			if name == "Content-Type" && target.Get("Content-Type") != "" {
				continue
			}
			target.Del(name)
			for _, value := range values {
				target.Add(name, value)
			}
		}
		target.Set("Accept-Ranges", "bytes")
	}
	ranges := r.Header.Values("Range")
	if r.Method != http.MethodGet {
		ranges = nil
	}
	if len(ranges) > 0 && (len(r.Header.Values("If-Range")) > 1 || !spool.IfRangeMatches(r.Header.Get("If-Range"), headers.Get("ETag"))) {
		ranges = nil
	}
	if len(ranges) > 0 {
		intervals, ok := spool.ParseRanges(ranges[0], size)
		if len(ranges) != 1 || !ok {
			apply(w.Header())
			w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return true, nil
		}
		if len(intervals) > 1 {
			lazy := &headerWriter{ResponseWriter: w, apply: func() { apply(w.Header()) }}
			if err := spool.WriteMultipart(lazy, r.Context(), headers.Get("Content-Type"), size, intervals, open); err != nil {
				return false, err
			}
			return true, nil
		}
		start, end := intervals[0].Start, intervals[0].End
		length := end - start + 1
		body, _, err := open(r.Context(), start, length)
		if err != nil {
			return false, err
		}
		defer body.Close()
		apply(w.Header())
		w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(end, 10)+"/"+strconv.FormatInt(size, 10))
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, err = io.CopyN(newFlushWriter(w), body, length)
		return true, err
	}
	if r.Method == http.MethodHead {
		apply(w.Header())
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusOK)
		return true, nil
	}
	body, _, err := open(r.Context(), 0, size)
	if err != nil {
		return false, err
	}
	defer body.Close()
	apply(w.Header())
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	_, err = io.CopyN(newFlushWriter(w), body, size)
	return true, err
}

// headerWriter applies the response headers on the first write, after the multipart writer has
// opened its first range and cannot fail any more.
type headerWriter struct {
	http.ResponseWriter
	apply   func()
	applied bool
}

func (h *headerWriter) once() {
	if !h.applied {
		h.applied = true
		h.apply()
	}
}

func (h *headerWriter) WriteHeader(code int) {
	h.once()
	h.ResponseWriter.WriteHeader(code)
}

func (h *headerWriter) Write(p []byte) (int, error) {
	h.once()
	return h.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (h *headerWriter) Unwrap() http.ResponseWriter { return h.ResponseWriter }

// flushWriter pushes every chunk to the client at once: a cold object is streamed as the fetch
// delivers it, and the client must see the headers and the first bytes without waiting for a
// buffer to fill.
type flushWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

func newFlushWriter(w http.ResponseWriter) flushWriter {
	writer := flushWriter{w: w, rc: http.NewResponseController(w)}
	_ = writer.rc.Flush()
	return writer
}

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err == nil {
		_ = f.rc.Flush()
	}
	return n, err
}
