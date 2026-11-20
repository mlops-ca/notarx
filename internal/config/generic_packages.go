package config

import (
	"fmt"
	"time"
)

// Defaults of the versioned Raw repositories (ADR 0028, Configuration).
const (
	// DefaultGenericMaxFilesPerVersion keeps the manifest of a version under 256 KiB.
	DefaultGenericMaxFilesPerVersion = 1000
	// DefaultGenericAutosealIdle is how long an open version of an idle-mode repository saw no write before the
	// sweep seals it.
	DefaultGenericAutosealIdle = 300 * time.Second
	// MinGenericAutosealIdle keeps the sweep from sealing a version in the middle of a slow upload.
	MinGenericAutosealIdle = 60 * time.Second
	// DefaultGenericAutosealInterval is the period of the sweep, lease-elected so one node runs it.
	DefaultGenericAutosealInterval = 60 * time.Second
	// MinGenericAutosealInterval keeps a mistyped interval from turning the sweep into a hot loop.
	MinGenericAutosealInterval = 10 * time.Second
	// DefaultGenericStaleOpenAfter is the age after which an open version counts as stale: reported, never removed.
	DefaultGenericStaleOpenAfter = 7 * 24 * time.Hour
)

// loadGenericPackagesConfig reads NOTARX_GENERIC_MAX_FILES_PER_VERSION, NOTARX_GENERIC_AUTOSEAL_IDLE,
// NOTARX_GENERIC_AUTOSEAL_INTERVAL and NOTARX_GENERIC_STALE_OPEN_AFTER.
func loadGenericPackagesConfig(cfg *Config) error {
	var err error
	if cfg.GenericMaxFilesPerVersion, err = positiveIntEnv("NOTARX_GENERIC_MAX_FILES_PER_VERSION", cfg.GenericMaxFilesPerVersion, false); err != nil {
		return err
	}
	if cfg.GenericAutosealIdle, err = durationEnv("NOTARX_GENERIC_AUTOSEAL_IDLE", cfg.GenericAutosealIdle); err != nil {
		return err
	}
	if cfg.GenericAutosealIdle < MinGenericAutosealIdle {
		return fmt.Errorf("NOTARX_GENERIC_AUTOSEAL_IDLE must be at least %s", MinGenericAutosealIdle)
	}
	if cfg.GenericAutosealInterval, err = durationEnv("NOTARX_GENERIC_AUTOSEAL_INTERVAL", cfg.GenericAutosealInterval); err != nil {
		return err
	}
	if cfg.GenericAutosealInterval < MinGenericAutosealInterval {
		return fmt.Errorf("NOTARX_GENERIC_AUTOSEAL_INTERVAL must be at least %s", MinGenericAutosealInterval)
	}
	if cfg.GenericStaleOpenAfter, err = durationEnv("NOTARX_GENERIC_STALE_OPEN_AFTER", cfg.GenericStaleOpenAfter); err != nil {
		return err
	}
	if cfg.GenericStaleOpenAfter <= 0 {
		return fmt.Errorf("NOTARX_GENERIC_STALE_OPEN_AFTER must be positive")
	}
	return nil
}
