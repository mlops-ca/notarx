-- +goose Up

-- ADR 0023 section 7 (slice 13, continuation): the quota alert evaluator wakes on notarx_evaluations instead
-- of polling alone. A rule that becomes due now (created enabled, enabled, or rescheduled for immediate
-- evaluation by a configuration change) notifies. Every write of the evaluator itself (an evaluation, or the
-- reset after a failed one) stamps evaluated_at, and the update trigger skips a row whose evaluated_at
-- changed, so the evaluator never wakes itself, whatever next_evaluate_at it wrote. The poll stays as the fallback for rules that fall due by time.
--
-- MySQL (ADR 0022): no push notifications; the idle poll is the wake-up there.

CREATE TRIGGER repository_quota_alert_rules_notify_insert AFTER INSERT ON public.repository_quota_alert_rules FOR EACH ROW WHEN (new.enabled AND NOT new.deleted AND new.next_evaluate_at <= clock_timestamp()) EXECUTE FUNCTION public.notarx_notify_queue('notarx_evaluations');
CREATE TRIGGER repository_quota_alert_rules_notify_update AFTER UPDATE OF enabled, next_evaluate_at ON public.repository_quota_alert_rules FOR EACH ROW WHEN (new.enabled AND NOT new.deleted AND new.next_evaluate_at <= clock_timestamp() AND old.evaluated_at IS NOT DISTINCT FROM new.evaluated_at AND (old.enabled IS DISTINCT FROM new.enabled OR old.next_evaluate_at IS DISTINCT FROM new.next_evaluate_at)) EXECUTE FUNCTION public.notarx_notify_queue('notarx_evaluations');
