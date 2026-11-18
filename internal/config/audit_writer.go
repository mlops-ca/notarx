package config

import (
	"fmt"
	"os"
	"strings"
)

// loadAuditWriterConfig reads the NOTARX_AUDIT_*, NOTARX_USAGE_FLUSH_INTERVAL and
// NOTARX_SHUTDOWN_FLUSH_TIMEOUT settings of the audit writer (ADR 0023 section 1).
func loadAuditWriterConfig(cfg *Config) error {
	var err error
	if cfg.AuditBufferSize, err = positiveIntEnv("NOTARX_AUDIT_BUFFER_SIZE", cfg.AuditBufferSize, false); err != nil {
		return err
	}
	if cfg.AuditBatchSize, err = positiveIntEnv("NOTARX_AUDIT_BATCH_SIZE", cfg.AuditBatchSize, false); err != nil {
		return err
	}
	if cfg.AuditSyncMaxInflight, err = positiveIntEnv("NOTARX_AUDIT_SYNC_MAX_INFLIGHT", cfg.AuditSyncMaxInflight, false); err != nil {
		return err
	}
	if cfg.AuditFlushInterval, err = durationEnv("NOTARX_AUDIT_FLUSH_INTERVAL", cfg.AuditFlushInterval); err != nil {
		return err
	}
	if cfg.UsageFlushInterval, err = durationEnv("NOTARX_USAGE_FLUSH_INTERVAL", cfg.UsageFlushInterval); err != nil {
		return err
	}
	if cfg.ShutdownFlushTimeout, err = durationEnv("NOTARX_SHUTDOWN_FLUSH_TIMEOUT", cfg.ShutdownFlushTimeout); err != nil {
		return err
	}
	if cfg.AuditDenialMode, err = enumEnv("NOTARX_AUDIT_DENIAL_MODE", cfg.AuditDenialMode, "queue", "sync"); err != nil {
		return err
	}
	if cfg.AuditReadMode, err = enumEnv("NOTARX_AUDIT_READ_MODE", cfg.AuditReadMode, "async", "sync"); err != nil {
		return err
	}
	if cfg.AuditBatchSize > cfg.AuditBufferSize {
		return fmt.Errorf("NOTARX_AUDIT_BATCH_SIZE must not exceed NOTARX_AUDIT_BUFFER_SIZE")
	}
	return nil
}

func enumEnv(name, fallback string, allowed ...string) (string, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if raw == "" {
		return fallback, nil
	}
	for _, value := range allowed {
		if raw == value {
			return raw, nil
		}
	}
	return "", fmt.Errorf("%s must be one of %s", name, strings.Join(allowed, ", "))
}
