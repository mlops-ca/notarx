package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/google/uuid"
)

// Multipart defaults and limits (ADR 0017, "OCI Object Storage specifics"). The
// part-size and part-count limits of a provider are confirmed by the live probe,
// not assumed: MaxMultipartParts is the S3 protocol's own ceiling.
const (
	DefaultMultipartThreshold int64 = 256 << 20
	DefaultMultipartPartSize  int64 = 16 << 20
	MinMultipartPartSize      int64 = 5 << 20
	MaxMultipartPartSize      int64 = 5 << 30
	MaxMultipartParts               = 10000
)

// ErrObjectTooLarge is returned for an object that would need 10,000 or more
// multipart parts at the configured part size.
var ErrObjectTooLarge = errors.New("object is too large for the configured multipart part size")

// MaxMultipartObjectBytes is the largest object the part size supports: the
// part count stays below the 10,000-part limit.
func MaxMultipartObjectBytes(partSize int64) int64 { return int64(MaxMultipartParts-1) * partSize }

// MultipartParts is the number of parts an object of size bytes takes.
func MultipartParts(size, partSize int64) int64 {
	if partSize <= 0 {
		return 0
	}
	return (size + partSize - 1) / partSize
}

// Digest attachment modes reported by DigestMode.
const (
	DigestModeCopy   = "copy"
	DigestModeStream = "stream"
)

const (
	digestModeUnknown int32 = iota
	digestModeCopyOK
	digestModeStream
)

func newUploader(client *s3.Client, cfg Config) *transfermanager.Client {
	calculation := aws.RequestChecksumCalculationWhenRequired
	if cfg.ChecksumMode == ChecksumWhenSupported {
		calculation = aws.RequestChecksumCalculationWhenSupported
	}
	return transfermanager.New(client, func(o *transfermanager.Options) {
		o.PartSizeBytes = cfg.MultipartPartSize
		o.MultipartUploadThreshold = cfg.MultipartThreshold
		o.MaxUploadParts = MaxMultipartParts
		o.RequestChecksumCalculation = calculation
		o.Concurrency = 4
	})
}

// uploadHeaders are the object headers an upload sets.
type uploadHeaders struct {
	ContentType        *string
	CacheControl       *string
	ContentDisposition *string
	ContentEncoding    *string
	ContentLanguage    *string
	Metadata           map[string]string
}

// upload stores body, as one PutObject up to the multipart threshold and as a
// multipart upload through the SDK transfer manager above it.
func (s *S3Store) upload(ctx context.Context, key string, body io.ReadSeeker, size int64, headers uploadHeaders) error {
	if s.uploader == nil || size <= s.threshold {
		_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(s.bucket), Key: aws.String(key), Body: body, ContentLength: aws.Int64(size),
			ContentType: headers.ContentType, CacheControl: headers.CacheControl, ContentDisposition: headers.ContentDisposition,
			ContentEncoding: headers.ContentEncoding, ContentLanguage: headers.ContentLanguage, Metadata: headers.Metadata,
		})
		return err
	}
	if size > MaxMultipartObjectBytes(s.partSize) {
		return fmt.Errorf("%w: %d bytes exceeds %d at a part size of %d; raise the part size", ErrObjectTooLarge, size, MaxMultipartObjectBytes(s.partSize), s.partSize)
	}
	_, err := s.uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: body,
		ContentType: headers.ContentType, CacheControl: headers.CacheControl, ContentDisposition: headers.ContentDisposition,
		ContentEncoding: headers.ContentEncoding, ContentLanguage: headers.ContentLanguage, Metadata: headers.Metadata,
	})
	return err
}

// DigestMode reports how SetVerifiedDigest attaches a digest: DigestModeCopy
// (CopyObject replacing the metadata) or DigestModeStream (the object is read,
// its digest re-verified, and it is written again), which costs extra egress.
func (s *S3Store) DigestMode() string {
	if s.digestMode.Load() == digestModeStream {
		return DigestModeStream
	}
	return DigestModeCopy
}

func (s *S3Store) SetVerifiedDigest(ctx context.Context, key, digest string) error {
	if s.digestMode.Load() == digestModeStream {
		return s.restamp(ctx, key, digest)
	}
	info, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if s3NotFound(err) {
			return ErrNotFound
		}
		return err
	}
	if err = s.copyDigest(ctx, key, digest, info); err != nil {
		if !copyRejected(err) {
			return err
		}
		s.useStreamMode("copy_rejected")
		return s.restamp(ctx, key, digest)
	}
	return nil
}

// copyDigest rewrites the metadata with CopyObject and the REPLACE directive.
// The copy source is the path-escaped "bucket/key", which encodes "/" as %2F;
// the probe and the live test cover keys that contain slashes.
func (s *S3Store) copyDigest(ctx context.Context, key, digest string, info *s3.HeadObjectOutput) error {
	metadata := make(map[string]string, len(info.Metadata)+1)
	for name, value := range info.Metadata {
		metadata[name] = value
	}
	metadata[digestMetadataKey] = digest
	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), CopySource: aws.String(url.PathEscape(s.bucket + "/" + key)),
		MetadataDirective: types.MetadataDirectiveReplace, Metadata: metadata,
		CacheControl: info.CacheControl, ContentDisposition: info.ContentDisposition, ContentEncoding: info.ContentEncoding,
		ContentLanguage: info.ContentLanguage, ContentType: info.ContentType,
	})
	return err
}

func (s *S3Store) useStreamMode(reason string) {
	if s.digestMode.Swap(digestModeStream) != digestModeStream {
		slog.Warn("object store digest attachment falls back to streaming", "mode", DigestModeStream, "reason", reason)
	}
}

// restamp is the copy-free fallback of SetVerifiedDigest: it reads the object,
// verifies that its bytes hash to digest, and writes it again with the digest
// attached. A digest that does not describe the bytes is refused.
func (s *S3Store) restamp(ctx context.Context, key, digest string) error {
	want, err := parseDigest(digest)
	if err != nil {
		return err
	}
	info, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if s3NotFound(err) {
			return ErrNotFound
		}
		return err
	}
	object, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if s3NotFound(err) {
			return ErrNotFound
		}
		return err
	}
	defer func() { _ = object.Body.Close() }()
	size := aws.ToInt64(info.ContentLength)
	body, cleanup, err := seekableObjectReader(object.Body, size)
	if err != nil {
		return err
	}
	defer cleanup()
	if err = verifyDigest(body, want); err != nil {
		return err
	}
	metadata := make(map[string]string, len(info.Metadata)+1)
	for name, value := range info.Metadata {
		metadata[name] = value
	}
	metadata[digestMetadataKey] = digestPrefix + want
	return s.upload(ctx, key, body, size, uploadHeaders{
		ContentType: info.ContentType, CacheControl: info.CacheControl, ContentDisposition: info.ContentDisposition,
		ContentEncoding: info.ContentEncoding, ContentLanguage: info.ContentLanguage, Metadata: metadata,
	})
}

// copyRejected reports whether the service answered a CopyObject with a refusal
// that means "not supported here" (as opposed to missing object, denied access
// or a network failure), which selects the streaming fallback.
func copyRejected(err error) bool {
	if err == nil || s3NotFound(err) {
		return false
	}
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "NotImplemented", "XNotImplemented", "InvalidRequest", "InvalidArgument", "MethodNotAllowed", "UnsupportedOperation", "InvalidDirective", "MalformedXML":
			return true
		case "AccessDenied", "SlowDown", "ServiceUnavailable", "InternalError", "RequestTimeout", "ThrottlingException":
			return false
		}
	}
	var responseError *smithyhttp.ResponseError
	if errors.As(err, &responseError) {
		switch responseError.HTTPStatusCode() {
		case 400, 405, 501:
			return true
		}
	}
	return false
}

// ProbeDigestMode decides, once at start-up, whether CopyObject with the REPLACE
// metadata directive works on this service, using a throw-away object whose key
// contains a slash. It records the result, logs the chosen mode, and returns it.
// A probe that cannot run (no write access, a network error) leaves the default,
// copy, in place: a real refusal at use time still falls back.
func (s *S3Store) ProbeDigestMode(ctx context.Context) string {
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	key := ".notarx-probe/" + uuid.NewString()
	payload := []byte("notarx capability probe")
	sum := sha256.Sum256(payload)
	digest := digestPrefix + hex.EncodeToString(sum[:])
	_, err := s.client.PutObject(probeCtx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(payload), ContentLength: aws.Int64(int64(len(payload))),
		ContentType: aws.String("application/octet-stream"),
	})
	if err != nil {
		slog.Info("object store digest mode", "mode", s.DigestMode(), "probe", "unavailable")
		return s.DigestMode()
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		_, _ = s.client.DeleteObject(cleanup, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	}()
	info, err := s.client.HeadObject(probeCtx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		slog.Info("object store digest mode", "mode", s.DigestMode(), "probe", "unavailable")
		return s.DigestMode()
	}
	copyErr := s.copyDigest(probeCtx, key, digest, info)
	switch {
	case copyErr == nil:
		after, headErr := s.client.HeadObject(probeCtx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
		if headErr == nil && digestMetadata(after.Metadata) != digest {
			// Accepted but ignored: the metadata did not change.
			s.useStreamMode("copy_ignored_metadata")
		} else if headErr == nil {
			s.digestMode.Store(digestModeCopyOK)
		}
	case copyRejected(copyErr):
		s.useStreamMode("copy_rejected")
	}
	mode := s.DigestMode()
	slog.Info("object store digest mode", "mode", mode, "probe", "completed")
	return mode
}
