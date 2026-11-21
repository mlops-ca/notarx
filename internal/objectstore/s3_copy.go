package objectstore

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// singleCopyLimit is the largest object one CopyObject request copies (the S3 limit); a larger one is copied part by
// part with UploadPartCopy. copyPartSize is the size of those parts. Both are variables so a test can lower them.
var (
	singleCopyLimit int64 = MaxMultipartPartSize
	copyPartSize    int64 = 512 << 20
)

// CopyObject implements Copier: the object is copied inside the service, with its metadata (the verified digest
// included), so no byte reaches this node and no local temporary space is used. Objects above singleCopyLimit are copied
// as a multipart upload of UploadPartCopy ranges, which needs the headers of the source (one HEAD).
func (s *S3Store) CopyObject(ctx context.Context, src, dst string, size int64) error {
	source := aws.String(copySource(s.bucket, src))
	if size <= singleCopyLimit {
		_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(dst), CopySource: source, MetadataDirective: types.MetadataDirectiveCopy})
		return copyError(err)
	}
	if size > int64(MaxMultipartParts-1)*copyPartSize {
		return fmt.Errorf("%w: %d bytes exceeds %d at a copy part size of %d", ErrObjectTooLarge, size, int64(MaxMultipartParts-1)*copyPartSize, copyPartSize)
	}
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(src)})
	if err != nil {
		return copyError(err)
	}
	return copyError(s.copyParts(ctx, source, dst, size, head))
}

// copySource is the x-amz-copy-source value of bucket/key. url.PathEscape leaves '+' as it is, which the service reads as a
// space, so it is escaped as well.
func copySource(bucket, key string) string {
	return strings.ReplaceAll(url.PathEscape(bucket+"/"+key), "+", "%2B")
}

// copyError maps the answer of a copy: a missing source is ErrNotFound, and a service that does not implement the copy or
// refuses this one (a source over its single-copy limit, say) is ErrCopyUnavailable, so the caller streams the object.
func copyError(err error) error {
	switch {
	case err == nil:
		return nil
	case s3NotFound(err):
		return ErrNotFound
	case copyUnsupported(err) || copyRefused(err):
		return errors.Join(ErrCopyUnavailable, err)
	}
	return err
}

// copyParts copies size bytes of source into dst as a multipart upload, carrying the headers and metadata of head.
func (s *S3Store) copyParts(ctx context.Context, source *string, dst string, size int64, head *s3.HeadObjectOutput) (err error) {
	created, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(s.bucket), Key: aws.String(dst), Metadata: head.Metadata,
		ContentType: head.ContentType, CacheControl: head.CacheControl, ContentDisposition: head.ContentDisposition,
		ContentEncoding: head.ContentEncoding, ContentLanguage: head.ContentLanguage,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			// The request may have been cancelled; an abandoned upload would keep its parts and be billed.
			_, abortErr := s.client.AbortMultipartUpload(context.WithoutCancel(ctx), &s3.AbortMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(dst), UploadId: created.UploadId})
			err = errors.Join(err, abortErr)
		}
	}()
	var parts []types.CompletedPart
	for offset, number := int64(0), int32(1); offset < size; offset, number = offset+copyPartSize, number+1 {
		last := min(offset+copyPartSize, size) - 1
		copied, copyErr := s.client.UploadPartCopy(ctx, &s3.UploadPartCopyInput{
			Bucket: aws.String(s.bucket), Key: aws.String(dst), CopySource: source, UploadId: created.UploadId,
			PartNumber: aws.Int32(number), CopySourceRange: aws.String(fmt.Sprintf("bytes=%d-%d", offset, last)),
		})
		if copyErr != nil {
			return copyErr
		}
		parts = append(parts, types.CompletedPart{ETag: copied.CopyPartResult.ETag, PartNumber: aws.Int32(number)})
	}
	_, err = s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(s.bucket), Key: aws.String(dst), UploadId: created.UploadId, MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	return err
}
