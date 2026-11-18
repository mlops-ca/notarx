package objectstore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recordedRequest captures what the stub saw, with credentials redacted by
// keeping only the signature scope.
type recordedRequest struct {
	method, path, authorization string
	headers                     http.Header
}

type s3Stub struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
	handler  func(http.ResponseWriter, *http.Request)
}

func newS3Stub(t *testing.T, handler func(http.ResponseWriter, *http.Request)) *s3Stub {
	t.Helper()
	stub := &s3Stub{handler: handler}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		stub.mu.Lock()
		stub.requests = append(stub.requests, recordedRequest{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Clone()})
		stub.mu.Unlock()
		if stub.handler != nil {
			stub.handler(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *s3Stub) recorded() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedRequest(nil), s.requests...)
}

func stubConfig(stub *s3Stub) Config {
	cfg := Defaults()
	cfg.Endpoint, cfg.Bucket, cfg.AccessKey, cfg.SecretKey = stub.server.URL, "notarx-bucket", "access", "secret"
	return cfg
}

func putOnce(t *testing.T, cfg Config) {
	t.Helper()
	store, err := NewS3Store(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Put(context.Background(), "a/b.bin", []byte("payload")); err != nil {
		t.Fatal(err)
	}
}

func TestS3ClientSignsWithConfiguredRegion(t *testing.T) {
	for _, region := range []string{"us-east-1", "ca-toronto-1"} {
		stub := newS3Stub(t, nil)
		cfg := stubConfig(stub)
		cfg.Region = region
		putOnce(t, cfg)
		requests := stub.recorded()
		if len(requests) != 1 {
			t.Fatalf("region %s: %d requests", region, len(requests))
		}
		scope := "/" + region + "/s3/aws4_request"
		if !strings.Contains(requests[0].authorization, scope) {
			t.Fatalf("authorization %q does not carry SigV4 scope %q", requests[0].authorization, scope)
		}
	}
}

func TestS3ClientDefaultsRegionWhenUnset(t *testing.T) {
	stub := newS3Stub(t, nil)
	cfg := stubConfig(stub)
	cfg.Region = ""
	putOnce(t, cfg)
	if !strings.Contains(stub.recorded()[0].authorization, "/us-east-1/s3/aws4_request") {
		t.Fatalf("authorization = %q", stub.recorded()[0].authorization)
	}
}

func TestS3ClientAddressingStyle(t *testing.T) {
	pathStub := newS3Stub(t, nil)
	cfg := stubConfig(pathStub)
	putOnce(t, cfg)
	if got := pathStub.recorded()[0].path; got != "/notarx-bucket/a/b.bin" {
		t.Fatalf("path-style request path = %q", got)
	}
	// Virtual-hosted addressing puts the bucket in the host name, which would need
	// DNS for a name under the stub's address, so assert the client's setting
	// instead of sending a request.
	for _, pathStyle := range []bool{true, false} {
		cfg.PathStyle = pathStyle
		client, err := NewS3Client(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got := client.Options().UsePathStyle; got != pathStyle {
			t.Fatalf("PathStyle=%t produced UsePathStyle=%t", pathStyle, got)
		}
	}
}

func checksumHeaders(headers http.Header) []string {
	var found []string
	for name := range headers {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-amz-checksum-") || lower == "x-amz-sdk-checksum-algorithm" || lower == "x-amz-trailer" {
			found = append(found, name)
		}
	}
	return found
}

func TestS3ClientChecksumModes(t *testing.T) {
	required := newS3Stub(t, nil)
	cfg := stubConfig(required)
	cfg.ChecksumMode = ChecksumWhenRequired
	putOnce(t, cfg)
	if found := checksumHeaders(required.recorded()[0].headers); len(found) != 0 {
		t.Fatalf("when_required sent checksum headers %v", found)
	}
	if encoding := required.recorded()[0].headers.Get("Content-Encoding"); strings.Contains(encoding, "aws-chunked") {
		t.Fatalf("when_required used aws-chunked encoding %q", encoding)
	}

	supported := newS3Stub(t, nil)
	cfg = stubConfig(supported)
	cfg.ChecksumMode = ChecksumWhenSupported
	putOnce(t, cfg)
	if found := checksumHeaders(supported.recorded()[0].headers); len(found) == 0 {
		t.Fatal("when_supported sent no checksum headers")
	}
}

func TestS3ClientRejectsInvalidSettings(t *testing.T) {
	base := Defaults()
	base.Endpoint, base.Bucket, base.AccessKey, base.SecretKey = "https://objects.example.test", "b", "a", "s"
	for name, mutate := range map[string]func(*Config){
		"path in endpoint": func(c *Config) { c.Endpoint = "https://objects.example.test/prefix" },
		"scheme":           func(c *Config) { c.Endpoint = "ftp://objects.example.test" },
		"credentials in url": func(c *Config) {
			c.Endpoint = "https://user:pass@objects.example.test"
		},
		"missing bucket": func(c *Config) { c.Bucket = " " },
		"missing key":    func(c *Config) { c.AccessKey = "" },
		"checksum mode":  func(c *Config) { c.ChecksumMode = "always" },
	} {
		cfg := base
		mutate(&cfg)
		if _, err := NewS3Store(cfg); err == nil {
			t.Errorf("%s: NewS3Store accepted invalid settings", name)
		}
	}
}

func TestOpenAutoCreateBucketGate(t *testing.T) {
	cases := []struct {
		name        string
		autoCreate  bool
		bucketExist bool
		wantErr     bool
		wantCreate  bool
	}{
		{"auto create makes missing bucket", true, false, false, true},
		{"auto create leaves existing bucket", true, true, false, false},
		{"check only accepts existing bucket", false, true, false, false},
		{"check only refuses missing bucket", false, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var created bool
			var mu sync.Mutex
			stub := newS3Stub(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch {
				case r.Method == http.MethodPut && r.URL.Path == "/notarx-bucket":
					created = true
					w.WriteHeader(http.StatusOK)
				case r.Method == http.MethodHead && r.URL.Path == "/notarx-bucket" && (tc.bucketExist || created):
					w.WriteHeader(http.StatusOK)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			})
			cfg := stubConfig(stub)
			cfg.AutoCreateBucket = tc.autoCreate
			store, err := Open(context.Background(), cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Open error = %v, want error %t", err, tc.wantErr)
			}
			if !tc.wantErr && store == nil {
				t.Fatal("Open returned a nil store")
			}
			mu.Lock()
			defer mu.Unlock()
			if created != tc.wantCreate {
				t.Fatalf("bucket created = %t, want %t", created, tc.wantCreate)
			}
		})
	}
}

func TestOpenUnknownDriverFailsWithSupportedList(t *testing.T) {
	cfg := Defaults()
	cfg.Driver = "azure"
	_, err := Open(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), `"azure"`) || !strings.Contains(err.Error(), DriverS3) {
		t.Fatalf("Open error = %v, want unknown driver naming supported drivers", err)
	}
	if err = cfg.Validate(); err == nil {
		t.Fatal("Validate accepted an unknown driver")
	}
	if err = Check(context.Background(), cfg); err == nil {
		t.Fatal("Check accepted an unknown driver")
	}
}

func TestCheckDoesNotCreateBucket(t *testing.T) {
	var created bool
	stub := newS3Stub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			created = true
		}
		w.WriteHeader(http.StatusNotFound)
	})
	cfg := stubConfig(stub)
	cfg.AutoCreateBucket = true
	if err := Check(context.Background(), cfg); err == nil {
		t.Fatal("Check succeeded for a missing bucket")
	}
	if created {
		t.Fatal("Check created the bucket")
	}
}

func TestS3NotFoundMapsToErrNotFound(t *testing.T) {
	stub := newS3Stub(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	store, err := NewS3Store(stubConfig(stub))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
	if _, err = store.Stat(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat error = %v, want ErrNotFound", err)
	}
}

func TestBindingDistinguishesDrivers(t *testing.T) {
	endpoint, bucket := (Config{Driver: DriverS3, Endpoint: "http://s3:9000", Bucket: "b"}).Binding()
	if endpoint != "http://s3:9000" || bucket != "b" {
		t.Fatalf("s3 binding = %q %q", endpoint, bucket)
	}
	endpoint, bucket = (Config{Driver: DriverFilesystem, FSRoot: "/srv/objects"}).Binding()
	if endpoint != DriverFilesystem || bucket != "/srv/objects" {
		t.Fatalf("filesystem binding = %q %q", endpoint, bucket)
	}
}
