package objectstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func smallMultipartConfig(f *fakeS3) Config {
	cfg := f.config()
	cfg.MultipartPartSize = MinMultipartPartSize
	cfg.MultipartThreshold = 6 << 20
	return cfg
}

func TestMultipartLimitsAndPartCount(t *testing.T) {
	for _, partSize := range []int64{MinMultipartPartSize, DefaultMultipartPartSize, 64 << 20, MaxMultipartPartSize} {
		largest := MaxMultipartObjectBytes(partSize)
		if parts := MultipartParts(largest, partSize); parts >= MaxMultipartParts {
			t.Errorf("part size %d: the largest supported object needs %d parts, want fewer than %d", partSize, parts, MaxMultipartParts)
		}
		if parts := MultipartParts(largest+1, partSize); parts != MaxMultipartParts {
			t.Errorf("part size %d: one byte more needs %d parts, want exactly the limit", partSize, parts)
		}
	}
	// At the 16 MiB default the largest object is just under 156 GiB.
	if got := MaxMultipartObjectBytes(DefaultMultipartPartSize); got != 9999*16<<20 {
		t.Fatalf("largest object at the default part size = %d", got)
	}
	if MultipartParts(5, 0) != 0 {
		t.Fatal("a zero part size has no parts")
	}
}

func TestConfigValidatesMultipartSettings(t *testing.T) {
	good := Defaults()
	good.Endpoint, good.Bucket, good.AccessKey, good.SecretKey = "http://s3:9000", "b", "a", "s"
	if good.MultipartThreshold != DefaultMultipartThreshold || good.MultipartPartSize != DefaultMultipartPartSize {
		t.Fatalf("defaults: %d %d", good.MultipartThreshold, good.MultipartPartSize)
	}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	zero := good
	zero.MultipartThreshold, zero.MultipartPartSize = 0, 0
	if err := zero.Validate(); err != nil {
		t.Fatalf("zero values must select the defaults: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"part size below the S3 minimum": func(c *Config) { c.MultipartPartSize = MinMultipartPartSize - 1 },
		"part size above the S3 maximum": func(c *Config) { c.MultipartPartSize = MaxMultipartPartSize + 1 },
		"threshold below the part size":  func(c *Config) { c.MultipartThreshold = c.MultipartPartSize - 1 },
	} {
		cfg := good
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMultipartUploadAboveTheThreshold(t *testing.T) {
	f := newFakeS3(t)
	store := f.store(smallMultipartConfig(f))
	ctx := context.Background()
	body := make([]byte, 12<<20+123)
	for i := range body {
		body[i] = byte(i * 7)
	}
	digest := digestOf(body)
	if err := store.PutVerifiedReader(ctx, "native/big/object", bytes.NewReader(body), int64(len(body)), digest); err != nil {
		t.Fatal(err)
	}
	if got := f.count("CreateMultipartUpload"); got != 1 {
		t.Fatalf("CreateMultipartUpload calls = %d", got)
	}
	wantParts := int(MultipartParts(int64(len(body)), MinMultipartPartSize))
	if got := f.count("UploadPart"); got != wantParts || wantParts != 3 {
		t.Fatalf("UploadPart calls = %d, want %d", got, wantParts)
	}
	if got := f.count("PutObject"); got != 0 {
		t.Fatalf("a multipart object also used PutObject %d times", got)
	}
	stored := f.object("native/big/object")
	if stored == nil || !bytes.Equal(stored.body, body) {
		t.Fatal("assembled object differs from the source")
	}
	if stored.metadata[digestMetadataKey] != digest {
		t.Fatalf("digest metadata = %q", stored.metadata[digestMetadataKey])
	}
	if info, err := store.Stat(ctx, "native/big/object"); err != nil || info.Digest != digest || info.Size != int64(len(body)) {
		t.Fatalf("Stat = %#v, %v", info, err)
	}
}

func TestUploadAtTheThresholdStaysASinglePut(t *testing.T) {
	f := newFakeS3(t)
	cfg := smallMultipartConfig(f)
	store := f.store(cfg)
	atThreshold := make([]byte, cfg.MultipartThreshold)
	if err := store.Put(context.Background(), "at", atThreshold); err != nil {
		t.Fatal(err)
	}
	if f.count("PutObject") != 1 || f.count("CreateMultipartUpload") != 0 {
		t.Fatalf("calls=%v", f.calls)
	}
	if err := store.Put(context.Background(), "above", make([]byte, cfg.MultipartThreshold+1)); err != nil {
		t.Fatal(err)
	}
	if f.count("CreateMultipartUpload") != 1 {
		t.Fatalf("calls=%v", f.calls)
	}
}

func TestMultipartFailureAbortsTheUpload(t *testing.T) {
	f := newFakeS3(t)
	f.failPart = 2
	store := f.store(smallMultipartConfig(f))
	err := store.Put(context.Background(), "native/big/failed", make([]byte, 12<<20))
	if err == nil {
		t.Fatal("a failed part did not fail the upload")
	}
	if f.object("native/big/failed") != nil {
		t.Fatal("a failed upload left an object")
	}
	f.mu.Lock()
	open := len(f.uploads)
	f.mu.Unlock()
	if open != 0 || f.count("AbortMultipartUpload") == 0 {
		t.Fatalf("open uploads=%d aborts=%d", open, f.count("AbortMultipartUpload"))
	}
}

// virtualObject is a seekable reader of a given length that holds no bytes, so
// the too-large path can be exercised without allocating 50 GB.
type virtualObject struct {
	size, offset int64
}

func (v *virtualObject) Read(p []byte) (int, error) {
	if v.offset >= v.size {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > v.size-v.offset {
		n = v.size - v.offset
	}
	v.offset += n
	return int(n), nil
}

func (v *virtualObject) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		v.offset = offset
	case io.SeekCurrent:
		v.offset += offset
	case io.SeekEnd:
		v.offset = v.size + offset
	}
	return v.offset, nil
}

func TestObjectsBeyondTheLargestSupportedSizeAreRefused(t *testing.T) {
	f := newFakeS3(t)
	cfg := smallMultipartConfig(f)
	store := f.store(cfg)
	tooLarge := MaxMultipartObjectBytes(cfg.MultipartPartSize) + 1
	err := store.upload(context.Background(), "huge", &virtualObject{size: tooLarge}, tooLarge, uploadHeaders{})
	if !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("upload = %v, want ErrObjectTooLarge", err)
	}
	if !strings.Contains(err.Error(), "raise the part size") {
		t.Fatalf("the error does not say what to change: %v", err)
	}
	if f.count("CreateMultipartUpload") != 0 {
		t.Fatal("an unsupported object started a multipart upload")
	}
}

func TestProbeKeepsCopyModeWhenCopyWorks(t *testing.T) {
	f := newFakeS3(t)
	store := f.store(f.config())
	if mode := store.ProbeDigestMode(context.Background()); mode != DigestModeCopy || store.DigestMode() != DigestModeCopy {
		t.Fatalf("mode = %q", mode)
	}
	if f.count("CopyObject") != 1 {
		t.Fatalf("calls=%v", f.calls)
	}
	// The probe object has a slash in its key, escaped as %2F in the copy source.
	if len(f.copySources) != 1 || !strings.Contains(f.copySources[0], "%2F.notarx-probe%2F") {
		t.Fatalf("copy source = %v, want the path-escaped bucket/key with a slash", f.copySources)
	}
	f.mu.Lock()
	left := len(f.objects)
	f.mu.Unlock()
	if left != 0 {
		t.Fatalf("the probe left %d object(s) behind", left)
	}
}

func TestProbeSelectsStreamingWhenCopyIsRejectedOrIgnored(t *testing.T) {
	for name, configure := range map[string]func(*fakeS3){
		"copy rejected":          func(f *fakeS3) { f.rejectCopy = true },
		"replace directive lost": func(f *fakeS3) { f.ignoreCopyReplace = true },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeS3(t)
			configure(f)
			store := f.store(f.config())
			if mode := store.ProbeDigestMode(context.Background()); mode != DigestModeStream || store.DigestMode() != DigestModeStream {
				t.Fatalf("mode = %q", mode)
			}
			f.mu.Lock()
			left := len(f.objects)
			f.mu.Unlock()
			if left != 0 {
				t.Fatalf("the probe left %d object(s) behind", left)
			}
		})
	}
}

func TestProbeThatCannotRunLeavesTheDefault(t *testing.T) {
	f := newFakeS3(t)
	f.denyWrites = true
	store := f.store(f.config())
	if mode := store.ProbeDigestMode(context.Background()); mode != DigestModeCopy {
		t.Fatalf("mode = %q, want the default when the probe cannot write", mode)
	}
}

func TestOpenRunsTheProbe(t *testing.T) {
	f := newFakeS3(t)
	f.rejectCopy = true
	cfg := f.config()
	cfg.AutoCreateBucket = true
	opened, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if mode := opened.(*S3Store).DigestMode(); mode != DigestModeStream {
		t.Fatalf("Open left mode %q", mode)
	}
}

func TestStreamingFallbackAttachesAVerifiedDigest(t *testing.T) {
	f := newFakeS3(t)
	f.rejectCopy = true
	store := f.store(f.config())
	ctx := context.Background()
	body := []byte("object that needs its digest attached later")
	if err := store.PutReader(ctx, "a/b/c", bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	if err := store.SetVerifiedDigest(ctx, "a/b/c", digestOf(body)); err != nil {
		t.Fatalf("SetVerifiedDigest with CopyObject rejected: %v", err)
	}
	if store.DigestMode() != DigestModeStream {
		t.Fatal("a rejected copy did not select the streaming mode")
	}
	info, err := store.Stat(ctx, "a/b/c")
	if err != nil || info.Digest != digestOf(body) || info.Size != int64(len(body)) {
		t.Fatalf("Stat = %#v, %v", info, err)
	}
	if got, getErr := store.Get(ctx, "a/b/c"); getErr != nil || !bytes.Equal(got, body) {
		t.Fatalf("bytes changed: %q %v", got, getErr)
	}
	if ct := f.object("a/b/c").headers.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("content type lost: %q", ct)
	}
	// The mode is sticky: the next call no longer tries CopyObject.
	copies := f.count("CopyObject")
	if err = store.SetVerifiedDigest(ctx, "a/b/c", digestOf(body)); err != nil {
		t.Fatal(err)
	}
	if f.count("CopyObject") != copies {
		t.Fatal("streaming mode still attempted CopyObject")
	}
}

func TestStreamingFallbackRefusesAWrongDigestAndMissingObjects(t *testing.T) {
	f := newFakeS3(t)
	f.rejectCopy = true
	store := f.store(f.config())
	ctx := context.Background()
	body := []byte("the real bytes")
	if err := store.Put(ctx, "k", body); err != nil {
		t.Fatal(err)
	}
	if err := store.SetVerifiedDigest(ctx, "k", digestOf([]byte("other bytes"))); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("SetVerifiedDigest with a wrong digest = %v, want ErrDigestMismatch", err)
	}
	if info, err := store.Stat(ctx, "k"); err != nil || info.Digest != "" {
		t.Fatalf("a refused digest was recorded: %#v %v", info, err)
	}
	if err := store.SetVerifiedDigest(ctx, "k", "not-a-digest"); err == nil {
		t.Fatal("a malformed digest was accepted by the fallback")
	}
	if err := store.SetVerifiedDigest(ctx, "missing", digestOf(body)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetVerifiedDigest on a missing key = %v, want ErrNotFound", err)
	}
}

func TestStreamingFallbackKeepsExistingMetadata(t *testing.T) {
	f := newFakeS3(t)
	f.rejectCopy = true
	store := f.store(f.config())
	ctx := context.Background()
	f.mu.Lock()
	f.objects["m"] = &fakeObject{body: []byte("x"), metadata: map[string]string{"owner": "team-a"}, headers: map[string][]string{"Content-Type": {"text/plain"}}}
	f.mu.Unlock()
	if err := store.SetVerifiedDigest(ctx, "m", digestOf([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	stored := f.object("m")
	if stored.metadata["owner"] != "team-a" || stored.metadata[digestMetadataKey] != digestOf([]byte("x")) {
		t.Fatalf("metadata = %v", stored.metadata)
	}
	if stored.headers.Get("Content-Type") != "text/plain" {
		t.Fatalf("content type = %q", stored.headers.Get("Content-Type"))
	}
}

func TestCopyRejectedClassifiesProviderAnswers(t *testing.T) {
	f := newFakeS3(t)
	store := f.store(f.config())
	ctx := context.Background()
	if err := store.Put(ctx, "k", []byte("x")); err != nil {
		t.Fatal(err)
	}
	// A refusal selects streaming; a missing object and a transport error do not.
	if err := store.SetVerifiedDigest(ctx, "absent", digestOf([]byte("x"))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing object: %v", err)
	}
	if store.DigestMode() != DigestModeCopy {
		t.Fatal("a missing object changed the mode")
	}
	for _, err := range []error{nil, errors.New("connection reset")} {
		if copyRejected(err) {
			t.Fatalf("copyRejected(%v) = true", err)
		}
	}
}

func TestCopyModeFallsBackWhenTheServiceRefusesLater(t *testing.T) {
	f := newFakeS3(t)
	store := f.store(f.config()) // no probe: the first use finds the refusal
	ctx := context.Background()
	body := []byte("late refusal")
	if err := store.Put(ctx, "k", body); err != nil {
		t.Fatal(err)
	}
	f.rejectCopy = true
	if err := store.SetVerifiedDigest(ctx, "k", digestOf(body)); err != nil {
		t.Fatalf("SetVerifiedDigest = %v", err)
	}
	if store.DigestMode() != DigestModeStream {
		t.Fatal("the late refusal did not switch modes")
	}
	if info, err := store.Stat(ctx, "k"); err != nil || info.Digest != digestOf(body) {
		t.Fatalf("Stat = %#v %v", info, err)
	}
}
