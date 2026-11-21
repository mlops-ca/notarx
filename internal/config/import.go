package config

import (
	"fmt"
	"time"

	"github.com/mlops-ca/notarx/internal/remoteinstance"
	"github.com/mlops-ca/notarx/internal/repository"
)

// Defaults of the import sessions a promotion from another instance opens (ADR 0021, "Configuration").
const (
	DefaultImportSessionTTL                  = 24 * time.Hour
	DefaultImportMaxBlobBytes                = remoteinstance.DefaultMaxBlobBytes
	DefaultImportMaxSessionBytes             = remoteinstance.DefaultMaxSessionBytes
	DefaultImportMaxBlobsPerSession          = int(remoteinstance.DefaultMaxBlobsPerSession)
	DefaultImportMaxOpenSessionsPerPrincipal = int(remoteinstance.DefaultMaxOpenSessionsPerPrincipal)
	// DefaultImportStagingQuotaBytes is twice the largest session, so two maximal sessions can stage at once.
	DefaultImportStagingQuotaBytes = 2 * DefaultImportMaxSessionBytes
	// MinImportSessionTTL keeps a session alive for a transfer of one blob of ordinary size.
	MinImportSessionTTL = time.Minute
)

// ImportConfig holds the import limits of ADR 0021 (NOTARX_IMPORT_*). The session store enforces Limits(), the import
// endpoints create sessions under Limits() and SessionTTL, and the instance document advertises Advertised().
type ImportConfig struct {
	// SessionTTL is how long an import session lives before the reaper expires it and deletes its staging objects
	// (NOTARX_IMPORT_SESSION_TTL, default 24h).
	SessionTTL time.Duration
	// MaxBlobBytes, MaxSessionBytes, MaxBlobsPerSession and MaxOpenSessionsPerPrincipal bound one blob, one session and
	// the sessions of one principal (NOTARX_IMPORT_MAX_BLOB_BYTES 10 GiB, NOTARX_IMPORT_MAX_SESSION_BYTES 50 GiB,
	// NOTARX_IMPORT_MAX_BLOBS_PER_SESSION 2000, NOTARX_IMPORT_MAX_OPEN_SESSIONS_PER_PRINCIPAL 4).
	MaxBlobBytes, MaxSessionBytes int64
	MaxBlobsPerSession            int
	MaxOpenSessionsPerPrincipal   int
	// StagingQuotaBytes caps the bytes claimed by every live session of the instance together
	// (NOTARX_IMPORT_STAGING_QUOTA_BYTES, default 100 GiB).
	StagingQuotaBytes int64
}

// Limits is the configuration as the session store takes it.
func (c ImportConfig) Limits() repository.ImportLimits {
	return repository.ImportLimits{MaxBlobBytes: c.MaxBlobBytes, MaxSessionBytes: c.MaxSessionBytes, MaxBlobsPerSession: c.MaxBlobsPerSession,
		MaxOpenSessionsPerPrincipal: c.MaxOpenSessionsPerPrincipal, StagingQuotaBytes: c.StagingQuotaBytes}
}

// Advertised is the part of the configuration a sender must respect, as GET /.well-known/notarx reports it.
func (c ImportConfig) Advertised() remoteinstance.Limits {
	return remoteinstance.Limits{MaxBlobBytes: c.MaxBlobBytes, MaxSessionBytes: c.MaxSessionBytes,
		MaxBlobsPerSession: int32(min(c.MaxBlobsPerSession, 1<<31-1)), MaxOpenSessionsPerPrincipal: int32(min(c.MaxOpenSessionsPerPrincipal, 1<<31-1))}
}

func importConfigFromEnv(current ImportConfig) (ImportConfig, error) {
	var err error
	if current.SessionTTL == 0 {
		current.SessionTTL = DefaultImportSessionTTL
	}
	if current.SessionTTL, err = durationEnv("NOTARX_IMPORT_SESSION_TTL", current.SessionTTL); err != nil {
		return current, err
	}
	if current.SessionTTL < MinImportSessionTTL {
		return current, fmt.Errorf("NOTARX_IMPORT_SESSION_TTL must be at least %s", MinImportSessionTTL)
	}
	for _, field := range []struct {
		name     string
		target   *int64
		fallback int64
	}{
		{"NOTARX_IMPORT_MAX_BLOB_BYTES", &current.MaxBlobBytes, DefaultImportMaxBlobBytes},
		{"NOTARX_IMPORT_MAX_SESSION_BYTES", &current.MaxSessionBytes, DefaultImportMaxSessionBytes},
		{"NOTARX_IMPORT_STAGING_QUOTA_BYTES", &current.StagingQuotaBytes, DefaultImportStagingQuotaBytes},
	} {
		if *field.target == 0 {
			*field.target = field.fallback
		}
		if *field.target, err = positiveInt64Env(field.name, *field.target); err != nil {
			return current, err
		}
	}
	if current.MaxBlobsPerSession == 0 {
		current.MaxBlobsPerSession = DefaultImportMaxBlobsPerSession
	}
	if current.MaxBlobsPerSession, err = positiveIntEnv("NOTARX_IMPORT_MAX_BLOBS_PER_SESSION", current.MaxBlobsPerSession, false); err != nil {
		return current, err
	}
	if current.MaxOpenSessionsPerPrincipal == 0 {
		current.MaxOpenSessionsPerPrincipal = DefaultImportMaxOpenSessionsPerPrincipal
	}
	if current.MaxOpenSessionsPerPrincipal, err = positiveIntEnv("NOTARX_IMPORT_MAX_OPEN_SESSIONS_PER_PRINCIPAL", current.MaxOpenSessionsPerPrincipal, false); err != nil {
		return current, err
	}
	// A blob larger than its session, or a session larger than the quota, could never be admitted: a mistake, not a policy.
	if current.MaxBlobBytes > current.MaxSessionBytes {
		return current, fmt.Errorf("NOTARX_IMPORT_MAX_BLOB_BYTES (%d) must not exceed NOTARX_IMPORT_MAX_SESSION_BYTES (%d)", current.MaxBlobBytes, current.MaxSessionBytes)
	}
	if current.MaxSessionBytes > current.StagingQuotaBytes {
		return current, fmt.Errorf("NOTARX_IMPORT_MAX_SESSION_BYTES (%d) must not exceed NOTARX_IMPORT_STAGING_QUOTA_BYTES (%d)", current.MaxSessionBytes, current.StagingQuotaBytes)
	}
	return current, nil
}
