package emailnotification

import (
	"fmt"
	"github.com/mlops-ca/notarx/internal/quotaalert"
	"math/big"
	"strconv"
	"time"
)

func RenderQuota(e quotaalert.Event, locale, origin, version string) (Preview, error) {
	if version != quotaalert.TemplateVersion || e.Validate() != nil || !ValidLocale(locale) {
		return Preview{}, ErrInvalidMessage
	}
	link, err := ConsoleURL(origin)
	if err != nil {
		return Preview{}, err
	}
	l := newLocalizer(locale)
	percent := new(big.Rat).Mul(new(big.Rat).SetFrac(big.NewInt(e.UsedBytes), big.NewInt(e.QuotaBytes)), big.NewRat(100, 1)).FloatString(2) + "%"
	v := view{Locale: locale, Color: "#b45309", Tint: "#fff7ed", Percent: percent, URL: link, CTA: l.text("cta.diagnostics"), ActionLabel: l.text("label.action"), Sep: l.text("detail.separator")}
	v.Amount = l.text("quota.amount", "used", strconv.FormatInt(e.UsedBytes, 10), "quota", strconv.FormatInt(e.QuotaBytes, 10))
	threshold, seconds, op := e.Policy.WarningBasisPoints, e.Policy.WarningForSeconds, "≥"
	switch e.Scenario {
	case "critical":
		threshold, seconds = e.Policy.CriticalBasisPoints, e.Policy.CriticalForSeconds
		v.Color, v.Tint = "#b91c1c", "#fef2f2"
	case "resolved":
		threshold, seconds, op = e.Policy.RecoveryBelowBasisPoints, e.Policy.RecoveryForSeconds, "<"
		v.Color, v.Tint = "#047857", "#ecfdf5"
	}
	basis := func(points int) string { return fmt.Sprintf("%d.%02d%%", points/100, points%100) }
	condition := func(op string, points, seconds int) string {
		return l.text("quota.condition", "op", op, "percent", basis(points), "seconds", strconv.Itoa(seconds))
	}
	utc := func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05 UTC") }
	v.Status = l.text("status." + e.Scenario)
	v.Title = l.text("quota.title", "repository", e.RepositoryName)
	v.Summary = l.text("quota.summary", "op", op, "percent", basis(threshold), "seconds", strconv.Itoa(seconds))
	v.Action = l.text("quota.action")
	v.Note = l.text("quota.note")
	v.Footer = l.text("quota.footer")
	v.Details = []detail{
		{l.text("quota.detail.repositoryId"), e.RepositoryID},
		{l.text("quota.detail.warning"), condition("≥", e.Policy.WarningBasisPoints, e.Policy.WarningForSeconds)},
		{l.text("quota.detail.critical"), condition("≥", e.Policy.CriticalBasisPoints, e.Policy.CriticalForSeconds)},
		{l.text("quota.detail.recovery"), condition("<", e.Policy.RecoveryBelowBasisPoints, e.Policy.RecoveryForSeconds)},
		{l.text("quota.detail.evidenceSince"), utc(e.EvidenceSince)},
		{l.text("quota.detail.sampleTime"), utc(e.SampleAt)},
		{l.text("detail.eventTime"), utc(e.OccurredAt)},
		{l.text("detail.eventId"), e.ID},
		{l.text("quota.detail.episodeId"), e.EpisodeID},
		{l.text("quota.detail.rule"), e.RuleID + " / " + e.RuleVersion},
		{l.text("quota.detail.sequence"), fmt.Sprint(e.Sequence)},
	}
	if e.PreviousEventID != "" {
		v.Details = append(v.Details, detail{l.text("quota.detail.previous"), e.PreviousEventID})
	}
	v.Subject = l.text("subjectPrefix") + " " + v.Status + " · " + v.Title
	return renderView(v, quotaalert.TemplateVersion)
}

func MIMEQuota(cfg Config, recipient string, e quotaalert.Event, locale, version string) ([]byte, error) {
	p, err := RenderQuota(e, locale, cfg.ConsoleOrigin, version)
	if err != nil {
		return nil, err
	}
	return encodeMIME(cfg, recipient, e.ID, e.OccurredAt, p)
}
