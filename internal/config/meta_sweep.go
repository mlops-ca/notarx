package config

import (
	"fmt"
	"time"
)

// Defaults of the sweep of unreferenced meta objects (ADR 0023 section 9).
const (
	// DefaultMetaSweepGrace is how long an unreferenced object, or one the sweep has not seen before, is kept.
	// It has to exceed the longest transaction that writes a row referencing an externalised payload.
	DefaultMetaSweepGrace = 24 * time.Hour
	// DefaultMetaSweepBatchSize is the number of objects a sweep examines between two renewals of its lease.
	DefaultMetaSweepBatchSize = 500
	// DefaultMetaSweepInterval is the time between two completed sweeps of the fleet.
	DefaultMetaSweepInterval = time.Hour
	// MinMetaSweepGrace keeps an operator from setting a grace period shorter than a publish can take.
	MinMetaSweepGrace = time.Minute
)

// loadMetaSweepConfig reads NOTARX_META_SWEEP_GRACE, NOTARX_META_SWEEP_BATCH_SIZE and NOTARX_META_SWEEP_INTERVAL.
func loadMetaSweepConfig(cfg *Config) error {
	var err error
	if cfg.MetaSweepGrace, err = durationEnv("NOTARX_META_SWEEP_GRACE", cfg.MetaSweepGrace); err != nil {
		return err
	}
	if cfg.MetaSweepGrace < MinMetaSweepGrace {
		return fmt.Errorf("NOTARX_META_SWEEP_GRACE must be at least %s", MinMetaSweepGrace)
	}
	if cfg.MetaSweepBatchSize, err = positiveIntEnv("NOTARX_META_SWEEP_BATCH_SIZE", cfg.MetaSweepBatchSize, false); err != nil {
		return err
	}
	if cfg.MetaSweepInterval, err = durationEnv("NOTARX_META_SWEEP_INTERVAL", cfg.MetaSweepInterval); err != nil {
		return err
	}
	return nil
}
