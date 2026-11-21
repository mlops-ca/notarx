package objectstore

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const noSuchKeyBody = `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`
const noSuchBucketBody = `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchBucket</Code><Message>The specified bucket does not exist.</Message></Error>`

func putStub(t *testing.T, failures int32, body string) (*s3Stub, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	stub := newS3Stub(t, nil)
	stub.handler = func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= failures {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, body)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
	return stub, &calls
}

// A PutObject answered with NoSuchKey is sent again from the start of the body, so the bytes that arrive are whole.
func TestS3PutObjectIsRepeatedAfterNoSuchKey(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	var calls atomic.Int32
	stub := newS3Stub(t, nil)
	stub.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(data))
		mu.Unlock()
		if calls.Add(1) <= 2 {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, noSuchKeyBody)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	store, err := NewS3Store(stubConfig(stub))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Put(context.Background(), "native/oci/uploads/a", []byte("payload")); err != nil {
		t.Fatalf("put = %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("requests = %d, want 3", calls.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	for i, body := range bodies {
		if !strings.Contains(body, "payload") {
			t.Fatalf("attempt %d carried %q, want the whole body", i+1, body)
		}
	}
}

func TestS3PutObjectGivesUpAfterTheAttemptsAndNeverRepeatsOtherErrors(t *testing.T) {
	stub, calls := putStub(t, 100, noSuchKeyBody)
	store, err := NewS3Store(stubConfig(stub))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Put(context.Background(), "k", []byte("payload")); err == nil || calls.Load() != putObjectAttempts {
		t.Fatalf("a persistent NoSuchKey: err=%v requests=%d, want an error after %d", err, calls.Load(), putObjectAttempts)
	}
	missing, missingCalls := putStub(t, 100, noSuchBucketBody)
	store, err = NewS3Store(stubConfig(missing))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Put(context.Background(), "k", []byte("payload")); err == nil || missingCalls.Load() != 1 {
		t.Fatalf("NoSuchBucket: err=%v requests=%d, want one request", err, missingCalls.Load())
	}
}
