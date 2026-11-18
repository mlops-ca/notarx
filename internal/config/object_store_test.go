package config

import (
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/objectstore"
)

func clearObjectStoreEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		envObjectStoreDriver, envObjectStoreEndpoint, envObjectStoreBucket, envObjectStoreRegion,
		envObjectStoreAccessKey, envObjectStoreSecretKey, envObjectStorePathStyle, envObjectStoreChecksumMode,
		envObjectStoreAutoCreateBucket, envObjectStoreFSRoot, envObjectStoreFSFsync, envObjectStoreFSReserveBytes,
	} {
		t.Setenv(name, "")
	}
}

func setS3Env(t *testing.T) {
	t.Helper()
	clearObjectStoreEnv(t)
	t.Setenv(envObjectStoreEndpoint, "https://ns.compat.objectstorage.ca-toronto-1.oci.customer-oci.com")
	t.Setenv(envObjectStoreBucket, "notarx")
	t.Setenv(envObjectStoreAccessKey, "access")
	t.Setenv(envObjectStoreSecretKey, "secret")
}

func TestLoadObjectStoreDefaults(t *testing.T) {
	setS3Env(t)
	cfg, err := loadObjectStore()
	if err != nil {
		t.Fatal(err)
	}
	want := objectstore.Defaults()
	want.Endpoint = "https://ns.compat.objectstorage.ca-toronto-1.oci.customer-oci.com"
	want.Bucket, want.AccessKey, want.SecretKey = "notarx", "access", "secret"
	if cfg != want {
		t.Fatalf("config = %#v, want %#v", cfg, want)
	}
	if cfg.Driver != "s3" || cfg.Region != "us-east-1" || !cfg.PathStyle || cfg.ChecksumMode != "when_required" || cfg.AutoCreateBucket || !cfg.FSFsync {
		t.Fatalf("documented defaults not applied: %#v", cfg)
	}
}

func TestLoadObjectStoreOverrides(t *testing.T) {
	setS3Env(t)
	t.Setenv(envObjectStoreRegion, "ca-toronto-1")
	t.Setenv(envObjectStorePathStyle, "false")
	t.Setenv(envObjectStoreChecksumMode, "when_supported")
	t.Setenv(envObjectStoreAutoCreateBucket, "true")
	cfg, err := loadObjectStore()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Region != "ca-toronto-1" || cfg.PathStyle || cfg.ChecksumMode != objectstore.ChecksumWhenSupported || !cfg.AutoCreateBucket {
		t.Fatalf("overrides not applied: %#v", cfg)
	}
}

func TestLoadObjectStoreRejectsInvalidValues(t *testing.T) {
	cases := map[string]struct {
		set  map[string]string
		want string
	}{
		"unknown driver":    {map[string]string{envObjectStoreDriver: "azure"}, "NOTARX_OBJECT_STORE_DRIVER"},
		"bad path style":    {map[string]string{envObjectStorePathStyle: "yes"}, "NOTARX_OBJECT_STORE_PATH_STYLE must be true or false"},
		"bad auto create":   {map[string]string{envObjectStoreAutoCreateBucket: "1"}, "NOTARX_OBJECT_STORE_AUTO_CREATE_BUCKET must be true or false"},
		"bad checksum mode": {map[string]string{envObjectStoreChecksumMode: "never"}, "checksum mode"},
		"endpoint path":     {map[string]string{envObjectStoreEndpoint: "https://objects.example.test/prefix"}, "NOTARX_OBJECT_STORE_ENDPOINT"},
		"endpoint scheme":   {map[string]string{envObjectStoreEndpoint: "ftp://objects.example.test"}, "NOTARX_OBJECT_STORE_ENDPOINT"},
		"endpoint garbage":  {map[string]string{envObjectStoreEndpoint: "not a url"}, "NOTARX_OBJECT_STORE_ENDPOINT is not a valid URL"},
		"missing bucket":    {map[string]string{envObjectStoreBucket: ""}, "are required when NOTARX_OBJECT_STORE_DRIVER is s3"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			setS3Env(t)
			for key, value := range tc.set {
				t.Setenv(key, value)
			}
			_, err := loadObjectStore()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestLoadRefusesRenamedObjectStoreEnvironment(t *testing.T) {
	setValidNativeEnv(t)
	t.Setenv("NOTARX_"+legacyRustFSPrefix+"ENDPOINT", "http://rustfs:9000")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "was renamed to NOTARX_OBJECT_STORE_ENDPOINT") {
		t.Fatalf("Load() error = %v, want rename refusal", err)
	}
}

func TestLoadObjectStoreReachesConfig(t *testing.T) {
	setValidNativeEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ObjectStore.Endpoint != "http://rustfs:9000" || cfg.ObjectStore.Bucket != "notarx-cache" {
		t.Fatalf("ObjectStore = %#v", cfg.ObjectStore)
	}
}

func TestLoadObjectStoreFilesystemDriver(t *testing.T) {
	clearObjectStoreEnv(t)
	t.Setenv(envObjectStoreDriver, "filesystem")
	t.Setenv(envObjectStoreFSRoot, "/srv/notarx/objects/")
	cfg, err := loadObjectStore()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Driver != objectstore.DriverFilesystem || cfg.FSRoot != "/srv/notarx/objects" || !cfg.FSFsync || cfg.FSReserveBytes != objectstore.DefaultFSReserveBytes {
		t.Fatalf("filesystem defaults not applied: %#v", cfg)
	}
	t.Setenv(envObjectStoreFSFsync, "false")
	t.Setenv(envObjectStoreFSReserveBytes, "0")
	cfg, err = loadObjectStore()
	if err != nil || cfg.FSFsync || cfg.FSReserveBytes != 0 {
		t.Fatalf("overrides not applied: %#v, %v", cfg, err)
	}
	if endpoint, bucket := cfg.Binding(); endpoint != "filesystem" || bucket != "/srv/notarx/objects" {
		t.Fatalf("Binding = %q %q", endpoint, bucket)
	}
}

func TestLoadObjectStoreFilesystemRejectsInvalidValues(t *testing.T) {
	cases := map[string]struct {
		set  map[string]string
		want string
	}{
		"missing root":      {map[string]string{}, "NOTARX_OBJECT_STORE_FS_ROOT is required"},
		"relative root":     {map[string]string{envObjectStoreFSRoot: "objects"}, "must be an absolute path"},
		"bad fsync":         {map[string]string{envObjectStoreFSRoot: "/srv/o", envObjectStoreFSFsync: "maybe"}, "NOTARX_OBJECT_STORE_FS_FSYNC must be true or false"},
		"negative reserve":  {map[string]string{envObjectStoreFSRoot: "/srv/o", envObjectStoreFSReserveBytes: "-1"}, "non-negative number of bytes"},
		"reserve not a int": {map[string]string{envObjectStoreFSRoot: "/srv/o", envObjectStoreFSReserveBytes: "1GiB"}, "non-negative number of bytes"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clearObjectStoreEnv(t)
			t.Setenv(envObjectStoreDriver, "filesystem")
			for key, value := range tc.set {
				t.Setenv(key, value)
			}
			_, err := loadObjectStore()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestLoadAcceptsFilesystemDriverWithoutS3Keys(t *testing.T) {
	setValidNativeEnv(t)
	for _, name := range []string{envObjectStoreEndpoint, envObjectStoreBucket, envObjectStoreAccessKey, envObjectStoreSecretKey} {
		t.Setenv(name, "")
	}
	t.Setenv(envObjectStoreDriver, "filesystem")
	t.Setenv(envObjectStoreFSRoot, "/srv/notarx/objects")
	cfg, err := Load()
	if err != nil || cfg.ObjectStore.Driver != "filesystem" {
		t.Fatalf("Load() = %#v, %v", cfg.ObjectStore, err)
	}
}
