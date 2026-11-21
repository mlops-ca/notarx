package objectstore

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3 is a small in-memory S3 service for the store tests: objects with
// user metadata, CopyObject, and multipart upload. Behaviours a provider may
// lack (CopyObject, metadata replacement) can be switched off.
type fakeS3 struct {
	t      *testing.T
	server *httptest.Server
	bucket string

	mu      sync.Mutex
	objects map[string]*fakeObject
	uploads map[string]*fakeUpload
	nextID  int
	calls   []string

	rejectCopy        bool // CopyObject answers 501 NotImplemented
	ignoreCopyReplace bool // CopyObject succeeds but keeps the old metadata
	denyWrites        bool // PutObject answers 403 AccessDenied
	failPart          int  // UploadPart number that answers 500, 0 for none
	copyStatus        int  // when set, CopyObject answers this status with copyCode
	copyCode          string
	copySources       []string
}

type fakeObject struct {
	body     []byte
	metadata map[string]string
	headers  http.Header
	modified time.Time
}

type fakeUpload struct {
	key      string
	metadata map[string]string
	headers  http.Header
	parts    map[int][]byte
}

func newFakeS3(t *testing.T) *fakeS3 {
	t.Helper()
	f := &fakeS3{t: t, bucket: "notarx-bucket", objects: map[string]*fakeObject{}, uploads: map[string]*fakeUpload{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeS3) config() Config {
	cfg := Defaults()
	cfg.Endpoint, cfg.Bucket, cfg.AccessKey, cfg.SecretKey = f.server.URL, f.bucket, "access", "secret"
	return cfg
}

func (f *fakeS3) store(cfg Config) *S3Store {
	f.t.Helper()
	store, err := NewS3Store(cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	return store
}

func (f *fakeS3) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if strings.HasPrefix(call, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeS3) object(key string) *fakeObject {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.objects[key]
}

func userMetadata(h http.Header) map[string]string {
	metadata := map[string]string{}
	for name, values := range h {
		if lower := strings.ToLower(name); strings.HasPrefix(lower, "x-amz-meta-") {
			metadata[strings.TrimPrefix(lower, "x-amz-meta-")] = values[0]
		}
	}
	return metadata
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, code)
}

func (f *fakeS3) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := strings.TrimPrefix(r.URL.Path, "/"+f.bucket)
	key := strings.TrimPrefix(path, "/")
	query := r.URL.Query()
	f.mu.Lock()
	defer f.mu.Unlock()
	label := r.Method
	switch {
	case query.Has("uploads"):
		label = "CreateMultipartUpload"
	case query.Has("partNumber") && r.Header.Get("x-amz-copy-source") != "":
		label = "UploadPartCopy"
	case query.Has("partNumber"):
		label = "UploadPart"
	case query.Has("uploadId") && r.Method == http.MethodPost:
		label = "CompleteMultipartUpload"
	case query.Has("uploadId") && r.Method == http.MethodDelete:
		label = "AbortMultipartUpload"
	case r.Method == http.MethodPut && r.Header.Get("x-amz-copy-source") != "":
		label = "CopyObject"
	case r.Method == http.MethodPut:
		label = "PutObject"
	}
	f.calls = append(f.calls, label+" "+key)

	if key == "" && query.Get("list-type") == "2" {
		prefix := query.Get("prefix")
		keys := make([]string, 0, len(f.objects))
		for name := range f.objects {
			if strings.HasPrefix(name, prefix) {
				keys = append(keys, name)
			}
		}
		sort.Strings(keys)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprintf(w, "<ListBucketResult><Name>%s</Name><Prefix>%s</Prefix><KeyCount>%d</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>", f.bucket, prefix, len(keys))
		for _, name := range keys {
			_, _ = fmt.Fprintf(w, "<Contents><Key>%s</Key><LastModified>%s</LastModified><Size>%d</Size></Contents>", name, f.objects[name].modified.UTC().Format("2006-01-02T15:04:05.000Z"), len(f.objects[name].body))
		}
		_, _ = io.WriteString(w, "</ListBucketResult>")
		return
	}
	if key == "" { // bucket operations
		w.WriteHeader(http.StatusOK)
		return
	}
	switch label {
	case "PutObject":
		if f.denyWrites {
			writeError(w, http.StatusForbidden, "AccessDenied")
			return
		}
		f.objects[key] = &fakeObject{body: body, metadata: userMetadata(r.Header), headers: r.Header.Clone(), modified: time.Now()}
		w.Header().Set("ETag", `"`+fmt.Sprintf("%x", md5.Sum(body))+`"`)
		w.WriteHeader(http.StatusOK)
	case "CopyObject":
		source, _ := url.PathUnescape(r.Header.Get("x-amz-copy-source"))
		f.copySources = append(f.copySources, r.Header.Get("x-amz-copy-source"))
		if f.rejectCopy {
			writeError(w, http.StatusNotImplemented, "NotImplemented")
			return
		}
		if f.copyStatus != 0 {
			writeError(w, f.copyStatus, f.copyCode)
			return
		}
		original, ok := f.objects[strings.TrimPrefix(source, f.bucket+"/")]
		if !ok {
			writeError(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		copied := &fakeObject{body: original.body, metadata: original.metadata, headers: original.headers, modified: time.Now()}
		if strings.EqualFold(r.Header.Get("x-amz-metadata-directive"), "REPLACE") && !f.ignoreCopyReplace {
			copied.metadata = userMetadata(r.Header)
		}
		f.objects[key] = copied
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<CopyObjectResult><ETag>"copied"</ETag><LastModified>2026-01-01T00:00:00.000Z</LastModified></CopyObjectResult>`)
	case "CreateMultipartUpload":
		f.nextID++
		id := "upload-" + strconv.Itoa(f.nextID)
		f.uploads[id] = &fakeUpload{key: key, metadata: userMetadata(r.Header), headers: r.Header.Clone(), parts: map[int][]byte{}}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprintf(w, "<InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>", f.bucket, key, id)
	case "UploadPart":
		number, _ := strconv.Atoi(query.Get("partNumber"))
		upload, ok := f.uploads[query.Get("uploadId")]
		if !ok {
			writeError(w, http.StatusNotFound, "NoSuchUpload")
			return
		}
		if f.failPart != 0 && number == f.failPart {
			writeError(w, http.StatusInternalServerError, "InternalError")
			return
		}
		upload.parts[number] = body
		w.Header().Set("ETag", `"`+fmt.Sprintf("%x", md5.Sum(body))+`"`)
		w.WriteHeader(http.StatusOK)
	case "UploadPartCopy":
		source, _ := url.PathUnescape(r.Header.Get("x-amz-copy-source"))
		original, ok := f.objects[strings.TrimPrefix(source, f.bucket+"/")]
		upload, uploaded := f.uploads[query.Get("uploadId")]
		if !ok || !uploaded {
			writeError(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		var first, last int
		if _, err := fmt.Sscanf(r.Header.Get("x-amz-copy-source-range"), "bytes=%d-%d", &first, &last); err != nil || last >= len(original.body) {
			writeError(w, http.StatusBadRequest, "InvalidArgument")
			return
		}
		number, _ := strconv.Atoi(query.Get("partNumber"))
		if f.failPart != 0 && number == f.failPart {
			writeError(w, http.StatusInternalServerError, "InternalError")
			return
		}
		upload.parts[number] = original.body[first : last+1]
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<CopyPartResult><ETag>"part"</ETag><LastModified>2026-01-01T00:00:00.000Z</LastModified></CopyPartResult>`)
	case "CompleteMultipartUpload":
		upload, ok := f.uploads[query.Get("uploadId")]
		if !ok {
			writeError(w, http.StatusNotFound, "NoSuchUpload")
			return
		}
		numbers := make([]int, 0, len(upload.parts))
		for number := range upload.parts {
			numbers = append(numbers, number)
		}
		sort.Ints(numbers)
		var assembled bytes.Buffer
		for _, number := range numbers {
			assembled.Write(upload.parts[number])
		}
		f.objects[upload.key] = &fakeObject{body: assembled.Bytes(), metadata: upload.metadata, headers: upload.headers}
		delete(f.uploads, query.Get("uploadId"))
		sum := md5.Sum(assembled.Bytes())
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprintf(w, `<CompleteMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><ETag>"%s-%d"</ETag></CompleteMultipartUploadResult>`, f.bucket, key, hex.EncodeToString(sum[:]), len(numbers))
	case "AbortMultipartUpload":
		delete(f.uploads, query.Get("uploadId"))
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodHead, http.MethodGet:
		object, ok := f.objects[key]
		if !ok {
			writeError(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		for name, value := range object.metadata {
			w.Header().Set("x-amz-meta-"+name, value)
		}
		if contentType := object.headers.Get("Content-Type"); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(object.body)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(object.body)
		}
	default:
		writeError(w, http.StatusBadRequest, "InvalidRequest")
	}
}
