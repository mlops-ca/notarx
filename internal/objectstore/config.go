package objectstore

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Driver names accepted by NOTARX_OBJECT_STORE_DRIVER (ADR 0017).
const (
	DriverS3         = "s3"
	DriverFilesystem = "filesystem"
)

// Checksum modes accepted by NOTARX_OBJECT_STORE_CHECKSUM_MODE. They map to the
// SDK RequestChecksumCalculation and ResponseChecksumValidation settings.
const (
	ChecksumWhenRequired  = "when_required"
	ChecksumWhenSupported = "when_supported"
)

// DefaultRegion is the SigV4 region used when none is configured.
const DefaultRegion = "us-east-1"

// Config is the driver-neutral object store configuration. Start from
// Defaults and override: the zero value of a boolean is a real choice (for
// example PathStyle false selects virtual-hosted addressing).
type Config struct {
	Driver           string
	Endpoint         string
	Bucket           string
	Region           string
	AccessKey        string
	SecretKey        string
	PathStyle        bool
	ChecksumMode     string
	AutoCreateBucket bool
	FSRoot           string
	FSFsync          bool
	// FSReserveBytes is the free space the filesystem driver leaves untouched;
	// writes that would dip below it fail with ErrInsufficientStorage. Zero
	// disables the check.
	FSReserveBytes int64
	// MultipartThreshold is the object size above which the s3 driver uploads
	// in parts, and MultipartPartSize the size of each part. Zero selects the
	// defaults (256 MiB and 16 MiB).
	MultipartThreshold int64
	MultipartPartSize  int64
}

// Defaults returns the documented defaults: s3 driver, us-east-1, path-style,
// when_required checksums, no bucket auto-creation, fsync on commit.
func Defaults() Config {
	return Config{Driver: DriverS3, Region: DefaultRegion, PathStyle: true, ChecksumMode: ChecksumWhenRequired, FSFsync: true, FSReserveBytes: DefaultFSReserveBytes,
		MultipartThreshold: DefaultMultipartThreshold, MultipartPartSize: DefaultMultipartPartSize}
}

// withDefaults fills the string settings a caller left empty. Booleans are not
// touched: a false PathStyle is a real choice (virtual-hosted services).
func (c Config) withDefaults() Config {
	if c.Driver == "" {
		c.Driver = DriverS3
	}
	if strings.TrimSpace(c.Region) == "" {
		c.Region = DefaultRegion
	}
	if c.ChecksumMode == "" {
		c.ChecksumMode = ChecksumWhenRequired
	}
	if c.MultipartThreshold == 0 {
		c.MultipartThreshold = DefaultMultipartThreshold
	}
	if c.MultipartPartSize == 0 {
		c.MultipartPartSize = DefaultMultipartPartSize
	}
	return c
}

// Validate checks the settings of the selected driver.
func (c Config) Validate() error {
	c = c.withDefaults()
	switch c.Driver {
	case DriverS3:
		return c.validateS3()
	case DriverFilesystem:
		return c.validateFilesystem()
	default:
		return unknownDriverError(c.Driver)
	}
}

func (c Config) validateS3() error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(c.Endpoint))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("object store endpoint must be an http or https URL without a path")
	}
	if strings.TrimSpace(c.Bucket) == "" || strings.TrimSpace(c.AccessKey) == "" || strings.TrimSpace(c.SecretKey) == "" {
		return errors.New("object store bucket and credentials are required")
	}
	if strings.TrimSpace(c.Region) == "" {
		return errors.New("object store region is required")
	}
	switch c.ChecksumMode {
	case ChecksumWhenRequired, ChecksumWhenSupported:
	default:
		return fmt.Errorf("object store checksum mode %q is invalid: use %s or %s", c.ChecksumMode, ChecksumWhenRequired, ChecksumWhenSupported)
	}
	if c.MultipartPartSize < MinMultipartPartSize || c.MultipartPartSize > MaxMultipartPartSize {
		return fmt.Errorf("object store multipart part size must be between %d and %d bytes", MinMultipartPartSize, MaxMultipartPartSize)
	}
	if c.MultipartThreshold < c.MultipartPartSize {
		return errors.New("object store multipart threshold must not be smaller than the part size")
	}
	return nil
}

func (c Config) validateFilesystem() error {
	if !filepath.IsAbs(c.FSRoot) || filepath.Clean(c.FSRoot) != c.FSRoot {
		return errors.New("object store filesystem root must be a clean absolute path")
	}
	return nil
}

// Binding returns the identity of the byte target for migration bookkeeping:
// the endpoint and bucket for s3, a fixed label and the root for filesystem.
func (c Config) Binding() (endpoint, bucket string) {
	if c.withDefaults().Driver == DriverFilesystem {
		return DriverFilesystem, c.FSRoot
	}
	return c.Endpoint, c.Bucket
}

func unknownDriverError(name string) error {
	return fmt.Errorf("unknown object store driver %q: supported drivers are %s", name, strings.Join(Drivers(), ", "))
}

// NewS3Client builds the S3 client shared by the S3 driver and the backup
// tooling: configurable SigV4 region, addressing style and checksum mode.
func NewS3Client(cfg Config) (*s3.Client, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validateS3(); err != nil {
		return nil, err
	}
	endpoint, _ := url.ParseRequestURI(strings.TrimSpace(cfg.Endpoint))
	awsConfig := aws.Config{
		Region:      strings.TrimSpace(cfg.Region),
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
	}
	calculation, validation := aws.RequestChecksumCalculationWhenRequired, aws.ResponseChecksumValidationWhenRequired
	if cfg.ChecksumMode == ChecksumWhenSupported {
		calculation, validation = aws.RequestChecksumCalculationWhenSupported, aws.ResponseChecksumValidationWhenSupported
	}
	return s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint.String())
		options.UsePathStyle = cfg.PathStyle
		options.RequestChecksumCalculation = calculation
		options.ResponseChecksumValidation = validation
	}), nil
}

// Opener builds a Store from a validated Config.
type Opener func(context.Context, Config) (Store, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Opener{DriverS3: openS3}
)

// Register adds a driver to the registry. It panics on a duplicate name so a
// wiring mistake fails at start-up rather than at first use.
func Register(name string, open Opener) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists || open == nil {
		panic("objectstore: duplicate or nil driver " + name)
	}
	registry[name] = open
}

// Drivers lists the registered driver names in lexical order.
func Drivers() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Open builds the configured driver and prepares it for use: for s3 it creates
// the bucket when AutoCreateBucket is set and otherwise only checks that it
// exists. An unknown driver fails with an error naming the supported ones.
func Open(ctx context.Context, cfg Config) (Store, error) {
	cfg = cfg.withDefaults()
	registryMu.RLock()
	open, ok := registry[cfg.Driver]
	registryMu.RUnlock()
	if !ok {
		return nil, unknownDriverError(cfg.Driver)
	}
	return open(ctx, cfg)
}

// Check verifies the configured backend is usable without creating or
// modifying anything: for s3 the bucket must exist, for other drivers the
// driver's own check runs.
func Check(ctx context.Context, cfg Config) error {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}
	if cfg.Driver != DriverS3 {
		return checkDriver(ctx, cfg)
	}
	store, err := NewS3Store(cfg)
	if err != nil {
		return err
	}
	return store.CheckBucket(ctx)
}

func checkDriver(_ context.Context, cfg Config) error {
	switch cfg.Driver {
	case DriverFilesystem:
		return checkFilesystem(cfg)
	default:
		return unknownDriverError(cfg.Driver)
	}
}

func openS3(ctx context.Context, cfg Config) (Store, error) {
	store, err := NewS3Store(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.AutoCreateBucket {
		err = store.EnsureBucket(ctx)
	} else {
		err = store.CheckBucket(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("prepare object store bucket: %w", err)
	}
	// Decide once how digests are attached; the chosen mode is logged.
	store.ProbeDigestMode(ctx)
	return store, nil
}
