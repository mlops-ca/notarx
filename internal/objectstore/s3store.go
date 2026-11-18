package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// digestMetadataKey is the user-metadata key that carries the verified SHA-256.
const digestMetadataKey = "notarx-sha256"

// S3Store is the S3-compatible driver: RustFS, other S3-compatible services, AWS S3 and OCI Object
// Storage through its S3 compatibility API.
type S3Store struct {
	client *s3.Client
	bucket string
}

// NewS3Store builds the S3-compatible driver from validated configuration.
// It performs no network I/O; Open adds the bucket step.
func NewS3Store(cfg Config) (*S3Store, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validateS3(); err != nil {
		return nil, err
	}
	client, err := NewS3Client(cfg)
	if err != nil {
		return nil, err
	}
	return &S3Store{client: client, bucket: cfg.Bucket}, nil
}

// NewS3StoreWithCredentials is the convenience constructor for tools and tests
// that target a local S3-compatible service: us-east-1, path-style addressing
// and the when_required checksum mode.
func NewS3StoreWithCredentials(endpoint, accessKey, secretKey, bucket string) (*S3Store, error) {
	cfg := Defaults()
	cfg.Endpoint, cfg.AccessKey, cfg.SecretKey, cfg.Bucket = endpoint, accessKey, secretKey, bucket
	return NewS3Store(cfg)
}

// Client returns the underlying S3 client for callers that need operations the
// Store port does not model, such as the backup walker.
func (s *S3Store) Client() *s3.Client { return s.client }

// Bucket returns the configured bucket name.
func (s *S3Store) Bucket() string { return s.bucket }

func (s *S3Store) EnsureBucket(ctx context.Context) error {
	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err == nil {
		return nil
	} else if !s3NotFound(err) {
		return err
	}
	if _, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(s.bucket)}); err == nil {
		return nil
	} else if _, headErr := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)}); headErr == nil {
		return nil
	} else {
		return err
	}
}

// CheckBucket verifies that the configured bucket is accessible without
// creating or modifying it.
func (s *S3Store) CheckBucket(ctx context.Context) error {
	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)}); s3NotFound(err) {
		return errors.New("configured bucket does not exist")
	} else {
		return err
	}
}

func (s *S3Store) Get(ctx context.Context, key string) ([]byte, error) {
	object, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if s3NotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer func() { _ = object.Body.Close() }()
	value, err := io.ReadAll(object.Body)
	return value, err
}

func (s *S3Store) Put(ctx context.Context, key string, value []byte) error {
	return s.PutReader(ctx, key, bytes.NewReader(value), int64(len(value)))
}

func (s *S3Store) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	object, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if s3NotFound(err) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	return object.Body, aws.ToInt64(object.ContentLength), nil
}

func (s *S3Store) Stat(ctx context.Context, key string) (Info, error) {
	info, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if s3NotFound(err) {
			return Info{}, ErrNotFound
		}
		return Info{}, err
	}
	return Info{Size: aws.ToInt64(info.ContentLength), Digest: digestMetadata(info.Metadata)}, nil
}

func (s *S3Store) OpenRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, int64, error) {
	info, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if s3NotFound(err) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	size := aws.ToInt64(info.ContentLength)
	if offset < 0 || length < 0 || offset > size || length > size-offset {
		return nil, 0, errors.New("object range is out of bounds")
	}
	if length == 0 {
		return io.NopCloser(bytes.NewReader(nil)), size, nil
	}
	object, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Range: aws.String(fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)),
	})
	if err != nil {
		return nil, 0, err
	}
	return object.Body, size, nil
}

func (s *S3Store) PutReader(ctx context.Context, key string, value io.Reader, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	body, cleanup, err := seekableObjectReader(value, size)
	if err != nil {
		return err
	}
	defer cleanup()
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: body, ContentLength: aws.Int64(size), ContentType: aws.String("application/octet-stream"),
	})
	return err
}

func (s *S3Store) PutVerifiedReader(ctx context.Context, key string, value io.Reader, size int64, digest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	want, err := parseDigest(digest)
	if err != nil {
		return err
	}
	body, cleanup, err := seekableObjectReader(value, size)
	if err != nil {
		return err
	}
	defer cleanup()
	if err = verifyDigest(body, want); err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: body, ContentLength: aws.Int64(size), ContentType: aws.String("application/octet-stream"),
		Metadata: map[string]string{digestMetadataKey: digestPrefix + want},
	})
	return err
}

func seekableObjectReader(value io.Reader, size int64) (io.ReadSeeker, func(), error) {
	if size < 0 {
		return nil, func() {}, errors.New("object size is invalid")
	}
	if seeker, ok := value.(io.ReadSeeker); ok {
		remaining, err := remainingBytes(seeker)
		if err != nil {
			return nil, func() {}, err
		}
		if remaining != size {
			return nil, func() {}, ErrSizeMismatch
		}
		return seeker, func() {}, nil
	}
	spool, err := os.CreateTemp("", "notarx-object-*")
	if err != nil {
		return nil, func() {}, err
	}
	spoolPath := spool.Name()
	// Unix keeps the open descriptor usable after unlink and guarantees the
	// spool is reclaimed if the worker crashes. Platforms that cannot unlink an
	// open file fall back to removing the named file during normal cleanup.
	unlinked := os.Remove(spoolPath) == nil
	cleanup := func() {
		_ = spool.Close()
		if !unlinked {
			_ = os.Remove(spoolPath)
		}
	}
	written, copyErr := io.Copy(spool, io.LimitReader(value, size+1))
	if copyErr != nil {
		cleanup()
		return nil, func() {}, copyErr
	}
	if written != size {
		cleanup()
		return nil, func() {}, fmt.Errorf("%w: got %d want %d", ErrSizeMismatch, written, size)
	}
	if _, err = spool.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return spool, cleanup, nil
}

// remainingBytes returns how many bytes are left from the current offset and
// leaves the offset where it was.
func remainingBytes(seeker io.Seeker) (int64, error) {
	current, err := seeker.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	end, err := seeker.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	if _, err = seeker.Seek(current, io.SeekStart); err != nil {
		return 0, err
	}
	return end - current, nil
}

// verifyDigest hashes the remaining bytes of body and rewinds it, so a
// mismatch is detected before anything is uploaded.
func verifyDigest(body io.ReadSeeker, wantHex string) error {
	start, err := body.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	if _, err = io.Copy(hasher, body); err != nil {
		return err
	}
	if _, err = body.Seek(start, io.SeekStart); err != nil {
		return err
	}
	if hex.EncodeToString(hasher.Sum(nil)) != wantHex {
		return ErrDigestMismatch
	}
	return nil
}

func (s *S3Store) SetVerifiedDigest(ctx context.Context, key, digest string) error {
	info, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if s3NotFound(err) {
			return ErrNotFound
		}
		return err
	}
	metadata := make(map[string]string, len(info.Metadata)+1)
	for name, value := range info.Metadata {
		metadata[name] = value
	}
	metadata[digestMetadataKey] = digest
	_, err = s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), CopySource: aws.String(url.PathEscape(s.bucket + "/" + key)),
		MetadataDirective: types.MetadataDirectiveReplace, Metadata: metadata,
		CacheControl: info.CacheControl, ContentDisposition: info.ContentDisposition, ContentEncoding: info.ContentEncoding,
		ContentLanguage: info.ContentLanguage, ContentType: info.ContentType,
	})
	return err
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return err
}

func (s *S3Store) List(ctx context.Context, prefix string) ([]string, error) {
	keys := make([]string, 0)
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, object := range page.Contents {
			keys = append(keys, aws.ToString(object.Key))
		}
	}
	return keys, nil
}

func s3NotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "NoSuchBucket", "NoSuchKey", "NotFound":
			return true
		}
	}
	var responseError *smithyhttp.ResponseError
	return errors.As(err, &responseError) && responseError.HTTPStatusCode() == 404
}

func digestMetadata(metadata map[string]string) string {
	for key, value := range metadata {
		if strings.EqualFold(key, "Notarx-Sha256") || strings.EqualFold(key, "X-Amz-Meta-Notarx-Sha256") {
			return value
		}
	}
	return ""
}
