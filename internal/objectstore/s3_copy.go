package objectstore

import (
	"context"
	"errors"
	"fmt"
	"net/url"

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
// as a multipart upload of UploadPartCopy ranges.
func (s *S3Store) CopyObject(ctx context.Context, src, dst string) error {
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(src)})
	if err != nil {
		if s3NotFound(err) {
			return ErrNotFound
		}
		return err
	}
	source := aws.String(url.PathEscape(s.bucket + "/" + src))
	size := aws.ToInt64(head.ContentLength)
	if size <= singleCopyLimit {
		_, err = s.client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(dst), CopySource: source, MetadataDirective: types.MetadataDirectiveCopy})
		if s3NotFound(err) {
			return ErrNotFound
		}
		return err
	}
	if size > int64(MaxMultipartParts-1)*copyPartSize {
		return fmt.Errorf("%w: %d bytes exceeds %d at a copy part size of %d", ErrObjectTooLarge, size, int64(MaxMultipartParts-1)*copyPartSize, copyPartSize)
	}
	return s.copyParts(ctx, source, dst, size, head)
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
