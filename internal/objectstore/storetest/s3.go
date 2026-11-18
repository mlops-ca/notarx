package storetest

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/objectstore"
)

// ConfigFromEnv reads an S3 driver configuration from <prefix>ENDPOINT,
// ACCESS_KEY, SECRET_KEY, BUCKET, REGION, PATH_STYLE and CHECKSUM_MODE
// (for example the prefix TEST_OBJECT_STORE_). It returns false when the
// endpoint or credentials are unset so a test can skip. A missing bucket gets
// a unique name and the bucket is created on open; a given bucket must exist.
func ConfigFromEnv(prefix string) (objectstore.Config, bool) {
	cfg := objectstore.Defaults()
	cfg.Endpoint = os.Getenv(prefix + "ENDPOINT")
	cfg.AccessKey = os.Getenv(prefix + "ACCESS_KEY")
	cfg.SecretKey = os.Getenv(prefix + "SECRET_KEY")
	if cfg.Endpoint == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return objectstore.Config{}, false
	}
	if region := os.Getenv(prefix + "REGION"); region != "" {
		cfg.Region = region
	}
	if raw := os.Getenv(prefix + "PATH_STYLE"); raw != "" {
		if parsed, err := strconv.ParseBool(raw); err == nil {
			cfg.PathStyle = parsed
		}
	}
	if mode := os.Getenv(prefix + "CHECKSUM_MODE"); mode != "" {
		cfg.ChecksumMode = mode
	}
	cfg.Bucket = os.Getenv(prefix + "BUCKET")
	if cfg.Bucket == "" {
		cfg.Bucket = "contract-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
		cfg.AutoCreateBucket = true
	}
	return cfg, true
}

// S3Factory returns a Factory over the S3 driver. Every case runs under its own
// random key prefix in the configured bucket and removes what it wrote when it
// ends, so one bucket can serve the whole suite and a pre-created production
// style bucket is left clean.
func S3Factory(cfg objectstore.Config) Factory {
	return func(t *testing.T) objectstore.Store {
		t.Helper()
		store, err := objectstore.Open(context.Background(), cfg)
		if err != nil {
			t.Fatalf("open object store: %v", err)
		}
		isolated := Prefixed(store, "contract-"+strings.ReplaceAll(uuid.NewString(), "-", "")[:16]+"/")
		t.Cleanup(func() { Purge(context.Background(), isolated) })
		return isolated
	}
}

// Purge deletes every object a store lists. It is best effort and meant for
// test cleanup.
func Purge(ctx context.Context, store objectstore.Store) {
	keys, err := store.List(ctx, "")
	if err != nil {
		return
	}
	for _, key := range keys {
		_ = store.Delete(ctx, key)
	}
}

// Prefixed wraps store so that every key lives under prefix. List strips the
// prefix again, so the wrapper behaves like an empty store of its own.
func Prefixed(store objectstore.Store, prefix string) objectstore.Store {
	return &prefixed{Store: store, prefix: prefix}
}

type prefixed struct {
	objectstore.Store
	prefix string
}

func (p *prefixed) Get(ctx context.Context, key string) ([]byte, error) {
	return p.Store.Get(ctx, p.prefix+key)
}

func (p *prefixed) Put(ctx context.Context, key string, value []byte) error {
	return p.Store.Put(ctx, p.prefix+key, value)
}

func (p *prefixed) Stat(ctx context.Context, key string) (objectstore.Info, error) {
	return p.Store.Stat(ctx, p.prefix+key)
}

func (p *prefixed) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	return p.Store.Open(ctx, p.prefix+key)
}

func (p *prefixed) OpenRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, int64, error) {
	return p.Store.OpenRange(ctx, p.prefix+key, offset, length)
}

func (p *prefixed) PutReader(ctx context.Context, key string, value io.Reader, size int64) error {
	return p.Store.PutReader(ctx, p.prefix+key, value, size)
}

func (p *prefixed) PutVerifiedReader(ctx context.Context, key string, value io.Reader, size int64, digest string) error {
	return p.Store.PutVerifiedReader(ctx, p.prefix+key, value, size, digest)
}

func (p *prefixed) SetVerifiedDigest(ctx context.Context, key, digest string) error {
	return p.Store.SetVerifiedDigest(ctx, p.prefix+key, digest)
}

func (p *prefixed) Delete(ctx context.Context, key string) error {
	return p.Store.Delete(ctx, p.prefix+key)
}

func (p *prefixed) List(ctx context.Context, prefix string) ([]string, error) {
	keys, err := p.Store.List(ctx, p.prefix+prefix)
	if err != nil {
		return nil, err
	}
	for i, key := range keys {
		keys[i] = strings.TrimPrefix(key, p.prefix)
	}
	return keys, nil
}
