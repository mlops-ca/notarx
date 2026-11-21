package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/mlops-ca/notarx/internal/app"
	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/events"
	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/retention"
)

const reconcileEventsUsage = "usage: notarx reconcile-events (NOTARX_DATABASE_URL names the PostgreSQL database; NOTARX_EVENTS_RETENTION bounds the window; off falls back to 14d)"

// runReconcileEvents is the repair tool of ADR 0021 (slice 1c): it emits artifact.published for every visible publication
// of the nine formats that emit it inside the event retention window and have none, once, and prints how many events it
// wrote. No worker runs it; it is safe next to a running node because the store checks the dedupe key under a lock.
func runReconcileEvents(ctx context.Context, args []string, getenv func(string) string, lookup func(string) (string, bool), stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("reconcile-events", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprintln(stdout, reconcileEventsUsage)
			return 0
		}
		_, _ = fmt.Fprintln(stderr, reconcileEventsUsage)
		return 2
	}
	if driver := strings.TrimSpace(getenv("NOTARX_DATABASE_DRIVER")); driver != "" && driver != "postgres" {
		_, _ = fmt.Fprintln(stderr, "reconcile-events needs NOTARX_DATABASE_DRIVER=postgres")
		return 2
	}
	databaseURL := strings.TrimSpace(getenv("NOTARX_DATABASE_URL"))
	if databaseURL == "" {
		_, _ = fmt.Fprintln(stderr, "NOTARX_DATABASE_URL is not set")
		return 1
	}
	retentionConfig, err := retention.LoadConfig(lookup)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	keep, err := retentionConfig.KeepOrDefault("domain_events")
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	store, err := repository.NewPostgresStore(databaseURL)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "open database:", err)
		return 1
	}
	defer func() { _ = store.Close() }()
	promotion, err := config.LoadPromotion()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	if promotion.Enabled() {
		// A repaired event is as good as one a publication wrote: it starts the evaluations of the rules that match it. The
		// process has no long-lived cache to keep fresh, so the rule set is read through the transaction every time.
		events.SetRuleFanout(app.NewPromotionRuleFanout(readcache.NewRegistry(0)))
		defer events.SetRuleFanout(nil)
	}
	written, err := app.PublishedEventReconciler{Store: store, Retention: keep}.RunOnce(ctx)
	_, _ = fmt.Fprintf(stdout, "published events written: %d\n", written)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "reconcile-events:", err)
		return 1
	}
	return 0
}
