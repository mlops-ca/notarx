package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mlops-ca/notarx/internal/objectstore"
)

// Object store environment keys (ADR 0017). They replace the former RustFS
// names (GATEWAY_ and NOTARX_ prefixed), which startup refuses (see RefuseLegacyEnv).
const (
	envObjectStoreDriver             = "NOTARX_OBJECT_STORE_DRIVER"
	envObjectStoreEndpoint           = "NOTARX_OBJECT_STORE_ENDPOINT"
	envObjectStoreBucket             = "NOTARX_OBJECT_STORE_BUCKET"
	envObjectStoreRegion             = "NOTARX_OBJECT_STORE_REGION"
	envObjectStoreAccessKey          = "NOTARX_OBJECT_STORE_ACCESS_KEY"
	envObjectStoreSecretKey          = "NOTARX_OBJECT_STORE_SECRET_KEY"
	envObjectStorePathStyle          = "NOTARX_OBJECT_STORE_PATH_STYLE"
	envObjectStoreChecksumMode       = "NOTARX_OBJECT_STORE_CHECKSUM_MODE"
	envObjectStoreAutoCreateBucket   = "NOTARX_OBJECT_STORE_AUTO_CREATE_BUCKET"
	envObjectStoreFSRoot             = "NOTARX_OBJECT_STORE_FS_ROOT"
	envObjectStoreFSFsync            = "NOTARX_OBJECT_STORE_FS_FSYNC"
	envObjectStoreFSReserveBytes     = "NOTARX_OBJECT_STORE_FS_RESERVE_BYTES"
	envObjectStoreMultipartThreshold = "NOTARX_OBJECT_STORE_MULTIPART_THRESHOLD"
	envObjectStoreMultipartPartSize  = "NOTARX_OBJECT_STORE_MULTIPART_PART_SIZE"
)

// loadObjectStore reads and validates the NOTARX_OBJECT_STORE_* keys.
func loadObjectStore() (objectstore.Config, error) {
	cfg := objectstore.Defaults()
	if driver := strings.TrimSpace(os.Getenv(envObjectStoreDriver)); driver != "" {
		cfg.Driver = driver
	}
	known := false
	for _, name := range objectstore.Drivers() {
		known = known || name == cfg.Driver
	}
	if !known {
		return objectstore.Config{}, fmt.Errorf("%s=%q is not a supported driver: use one of %s", envObjectStoreDriver, cfg.Driver, strings.Join(objectstore.Drivers(), ", "))
	}
	var err error
	if cfg.PathStyle, err = boolEnv(envObjectStorePathStyle, cfg.PathStyle); err != nil {
		return objectstore.Config{}, err
	}
	if cfg.AutoCreateBucket, err = boolEnv(envObjectStoreAutoCreateBucket, false); err != nil {
		return objectstore.Config{}, err
	}
	if cfg.FSFsync, err = boolEnv(envObjectStoreFSFsync, cfg.FSFsync); err != nil {
		return objectstore.Config{}, err
	}
	if raw := strings.TrimSpace(os.Getenv(envObjectStoreFSReserveBytes)); raw != "" {
		reserve, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || reserve < 0 {
			return objectstore.Config{}, fmt.Errorf("%s must be a non-negative number of bytes", envObjectStoreFSReserveBytes)
		}
		cfg.FSReserveBytes = reserve
	}
	if cfg.MultipartThreshold, err = byteSizeEnv(envObjectStoreMultipartThreshold, cfg.MultipartThreshold); err != nil {
		return objectstore.Config{}, err
	}
	if cfg.MultipartPartSize, err = byteSizeEnv(envObjectStoreMultipartPartSize, cfg.MultipartPartSize); err != nil {
		return objectstore.Config{}, err
	}
	cfg.Endpoint = strings.TrimSpace(os.Getenv(envObjectStoreEndpoint))
	cfg.Bucket = strings.TrimSpace(os.Getenv(envObjectStoreBucket))
	cfg.Region = value(envObjectStoreRegion, cfg.Region)
	cfg.AccessKey = os.Getenv(envObjectStoreAccessKey)
	cfg.SecretKey = os.Getenv(envObjectStoreSecretKey)
	cfg.ChecksumMode = value(envObjectStoreChecksumMode, cfg.ChecksumMode)
	cfg.FSRoot = strings.TrimSpace(os.Getenv(envObjectStoreFSRoot))
	switch cfg.Driver {
	case objectstore.DriverS3:
		if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
			return objectstore.Config{}, fmt.Errorf("%s, %s, %s, and %s are required when %s is s3", envObjectStoreEndpoint, envObjectStoreBucket, envObjectStoreAccessKey, envObjectStoreSecretKey, envObjectStoreDriver)
		}
		if _, err := url.ParseRequestURI(cfg.Endpoint); err != nil {
			return objectstore.Config{}, fmt.Errorf("%s is not a valid URL", envObjectStoreEndpoint)
		}
		if err := cfg.Validate(); err != nil {
			return objectstore.Config{}, fmt.Errorf("%s and the related object store keys are invalid: %w", envObjectStoreEndpoint, err)
		}
	case objectstore.DriverFilesystem:
		if cfg.FSRoot == "" {
			return objectstore.Config{}, fmt.Errorf("%s is required when %s is filesystem", envObjectStoreFSRoot, envObjectStoreDriver)
		}
		if !filepath.IsAbs(cfg.FSRoot) {
			return objectstore.Config{}, fmt.Errorf("%s must be an absolute path", envObjectStoreFSRoot)
		}
		cfg.FSRoot = filepath.Clean(cfg.FSRoot)
	}
	return cfg, nil
}

// byteSizeEnv parses a positive size in bytes.
func byteSizeEnv(name string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive number of bytes", name)
	}
	return value, nil
}

// boolEnv parses a strict true/false environment value.
func boolEnv(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	switch raw {
	case "":
		return fallback, nil
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("%s must be true or false", name)
}
