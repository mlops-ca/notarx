package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/mlops-ca/notarx/internal/auditwriter"
	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/repository"
)

// startAuditWriter installs the tiered audit writer (ADR 0023 section 1) behind the
// store's RecordAudit and starts its flushers.
func startAuditWriter(cfg config.Config, store *repository.PostgresStore) *auditwriter.Writer {
	writer := auditwriter.New(store, auditwriter.Config{
		BufferSize: cfg.AuditBufferSize, BatchSize: cfg.AuditBatchSize, FlushInterval: cfg.AuditFlushInterval,
		SyncMaxInflight: cfg.AuditSyncMaxInflight, DenialMode: auditwriter.DenialMode(cfg.AuditDenialMode),
		ReadMode: auditwriter.ReadMode(cfg.AuditReadMode), UsageFlushInterval: cfg.UsageFlushInterval,
		Logger: slog.Default(),
	})
	writer.Start()
	store.SetAuditRecorder(writer)
	return writer
}

// stopAuditWriter flushes the buffered audit records and usage aggregates within
// NOTARX_SHUTDOWN_FLUSH_TIMEOUT. It runs after the HTTP server has drained and
// before the database pools close, and returns the store to direct writes so a
// late background record is committed rather than lost.
func stopAuditWriter(writer *auditwriter.Writer, store *repository.PostgresStore, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := writer.Shutdown(ctx)
	store.SetAuditRecorder(nil)
	return err
}
