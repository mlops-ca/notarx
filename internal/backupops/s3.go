package backupops

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/mlops-ca/notarx/internal/objectstore"
)

// S3Settings describes an S3-compatible backup or restore target. Zero Region,
// PathStyle and ChecksumMode values select the local-service defaults
// (us-east-1, path-style, when_required) so existing specs keep working; set
// them explicitly for OCI or real-region S3.
type S3Settings struct {
	Endpoint     string `json:"endpoint"`
	Bucket       string `json:"bucket"`
	AccessKey    string `json:"accessKey"`
	SecretKey    string `json:"secretKey"`
	Region       string `json:"region,omitempty"`
	PathStyle    *bool  `json:"pathStyle,omitempty"`
	ChecksumMode string `json:"checksumMode,omitempty"`
}

type s3Objects struct {
	store  *objectstore.S3Store
	client *s3.Client
	bucket string
}

// objectStoreConfig maps the settings onto the shared object store config.
func (s S3Settings) objectStoreConfig() objectstore.Config {
	cfg := objectstore.Defaults()
	cfg.Endpoint, cfg.Bucket, cfg.AccessKey, cfg.SecretKey = s.Endpoint, s.Bucket, s.AccessKey, s.SecretKey
	if s.Region != "" {
		cfg.Region = s.Region
	}
	if s.PathStyle != nil {
		cfg.PathStyle = *s.PathStyle
	}
	if s.ChecksumMode != "" {
		cfg.ChecksumMode = s.ChecksumMode
	}
	return cfg
}

func newS3(s S3Settings) (*s3Objects, error) {
	endpoint, err := url.Parse(s.Endpoint)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || s.Bucket == "" || s.AccessKey == "" || s.SecretKey == "" {
		return nil, errors.New("invalid explicit S3 settings")
	}
	store, err := objectstore.NewS3Store(s.objectStoreConfig())
	if err != nil {
		return nil, errors.New("object settings unavailable")
	}
	return &s3Objects{store: store, client: store.Client(), bucket: s.Bucket}, nil
}
func (s *s3Objects) WalkObjects(ctx context.Context, visit func(string, int64) error) error {
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), MaxKeys: aws.Int32(1000)}, func(o *s3.ListObjectsV2PaginatorOptions) { o.StopOnDuplicateToken = true })
	previous := ""
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return errors.New("object listing failed")
		}
		for _, object := range page.Contents {
			key := aws.ToString(object.Key)
			if strings.HasPrefix(key, objectstore.ProbePrefix) {
				continue // the store's own start-up probe objects are not artifact bytes
			}
			if key == "" || key <= previous || object.Size == nil {
				return errors.New("object listing unordered or invalid")
			}
			previous = key
			if err = visit(key, *object.Size); err != nil {
				return err
			}
		}
		if aws.ToBool(page.IsTruncated) && (!pages.HasMorePages() || aws.ToString(page.NextContinuationToken) == "") {
			return errors.New("object listing incomplete")
		}
	}
	return nil
}
func (s *s3Objects) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	return s.store.Open(ctx, key)
}
func (s *s3Objects) Put(ctx context.Context, key string, r io.ReadSeeker, size int64, digest string) error {
	return s.store.PutVerifiedReader(ctx, key, r, size, digest)
}
func (s *s3Objects) Empty(ctx context.Context) error {
	page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), MaxKeys: aws.Int32(1)})
	if err != nil || len(page.Contents) != 0 || aws.ToBool(page.IsTruncated) {
		return errors.New("target object store not empty")
	}
	return nil
}
