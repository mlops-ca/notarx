package config

import (
	"testing"
	"time"
)

func TestImportConfigDefaultsAndOverrides(t *testing.T) {
	got, err := importConfigFromEnv(ImportConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionTTL != 24*time.Hour || got.MaxBlobBytes != 10<<30 || got.MaxSessionBytes != 50<<30 || got.MaxBlobsPerSession != 2000 || got.MaxOpenSessionsPerPrincipal != 4 || got.StagingQuotaBytes != 100<<30 {
		t.Fatalf("defaults = %+v", got)
	}
	limits, advertised := got.Limits(), got.Advertised()
	if limits.MaxBlobBytes != 10<<30 || limits.StagingQuotaBytes != 100<<30 || advertised.MaxBlobsPerSession != 2000 || advertised.MaxOpenSessionsPerPrincipal != 4 || advertised.MaxSessionBytes != 50<<30 {
		t.Fatalf("limits = %+v advertised = %+v", limits, advertised)
	}
	t.Setenv("NOTARX_IMPORT_SESSION_TTL", "2h")
	t.Setenv("NOTARX_IMPORT_MAX_BLOB_BYTES", "1000")
	t.Setenv("NOTARX_IMPORT_MAX_SESSION_BYTES", "5000")
	t.Setenv("NOTARX_IMPORT_STAGING_QUOTA_BYTES", "9000")
	t.Setenv("NOTARX_IMPORT_MAX_BLOBS_PER_SESSION", "7")
	t.Setenv("NOTARX_IMPORT_MAX_OPEN_SESSIONS_PER_PRINCIPAL", "2")
	got, err = importConfigFromEnv(ImportConfig{})
	if err != nil || got.SessionTTL != 2*time.Hour || got.MaxBlobBytes != 1000 || got.MaxSessionBytes != 5000 || got.StagingQuotaBytes != 9000 || got.MaxBlobsPerSession != 7 || got.MaxOpenSessionsPerPrincipal != 2 {
		t.Fatalf("overrides = %+v %v", got, err)
	}
}

func TestImportConfigRejectsMistakes(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"zero blobs":         {"NOTARX_IMPORT_MAX_BLOBS_PER_SESSION": "0"},
		"negative bytes":     {"NOTARX_IMPORT_MAX_BLOB_BYTES": "-1"},
		"text":               {"NOTARX_IMPORT_MAX_SESSION_BYTES": "lots"},
		"zero sessions":      {"NOTARX_IMPORT_MAX_OPEN_SESSIONS_PER_PRINCIPAL": "0"},
		"short ttl":          {"NOTARX_IMPORT_SESSION_TTL": "5s"},
		"blob over session":  {"NOTARX_IMPORT_MAX_BLOB_BYTES": "100", "NOTARX_IMPORT_MAX_SESSION_BYTES": "50"},
		"session over quota": {"NOTARX_IMPORT_MAX_SESSION_BYTES": "100", "NOTARX_IMPORT_STAGING_QUOTA_BYTES": "50", "NOTARX_IMPORT_MAX_BLOB_BYTES": "10"},
	} {
		t.Run(name, func(t *testing.T) {
			for key, value := range env {
				t.Setenv(key, value)
			}
			if _, err := importConfigFromEnv(ImportConfig{}); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
