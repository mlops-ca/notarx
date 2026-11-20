package app

import (
	"context"
	"github.com/mlops-ca/notarx/internal/background"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
	"time"
)

// QuotaAlertEvaluator bounds database work; SMTP runs separately in the existing worker.
type QuotaAlertEvaluator struct {
	Store repository.RepositoryQuotaAlertStore
	Mail  repository.QuotaAlertMailConfig
}

func (e QuotaAlertEvaluator) Run(ctx context.Context) (int, error) {
	if e.Store == nil {
		return 0, nil
	}
	budget, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	mail := e.Mail
	if mail.Enabled {
		if _, err := secrets.Seal("email-readiness", "synthetic-probe"); err != nil {
			mail.Enabled = false
			mail.UnavailableCode = "encryption_key_unavailable"
		}
	}
	for n := 0; n < 100; n++ {
		worked, err := e.Store.EvaluateNextRepositoryQuotaAlert(budget, mail, 15*time.Second)
		if err != nil {
			return n, err
		}
		if !worked {
			return n, nil
		}
	}
	return 100, nil
}

// quotaAlertWakeSpacing is the shortest time between two evaluation passes started by notifications. A burst
// of rule changes (a bulk enable, an import) collapses into one pass per spacing, and one Run evaluates every
// due rule, so nothing is skipped.
const quotaAlertWakeSpacing = time.Second

func (e QuotaAlertEvaluator) Start(ctx context.Context, interval time.Duration) {
	e.start(ctx, interval, quotaAlertWakeSpacing, time.Now)
}

func (e QuotaAlertEvaluator) start(ctx context.Context, interval, spacing time.Duration, now func() time.Time) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	background.Go(ctx, func() {
		// A rule that becomes due now (created, enabled, reconfigured) wakes the evaluator; the timer is the
		// fallback for rules that fall due by time and for stores without notifications.
		wake := notificationWake(ctx, e.Store, repository.QuotaEvaluationChannel)
		timer := time.NewTimer(0)
		defer timer.Stop()
		var lastRun time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			case _, ok := <-wake:
				if !ok {
					wake = nil
					continue
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				// Too soon after the last pass: run once the spacing has passed instead, folding in every
				// notification that arrives meanwhile.
				if wait := spacing - now().Sub(lastRun); wait > 0 {
					timer.Reset(wait)
					continue
				}
			}
			if ctx.Err() != nil {
				return
			}
			lastRun = now()
			_, _ = e.Run(ctx)
			// Due time is based on the completed database evaluation. Starting
			// the next wait here avoids buffered/early ticks skipping a sample.
			timer.Reset(interval)
		}
	})
}
