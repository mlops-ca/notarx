// Package emailnotification owns the finite, versioned administrative mail channel.
package emailnotification

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"
)

const TemplateVersion = "2"

//go:embed template.html
var templateSource string

//go:embed quota-template.html
var quotaTemplateSource string
var mailTemplate = compileMailTemplate(templateSource)
var quotaMailTemplate = compileMailTemplate(quotaTemplateSource)

func compileMailTemplate(source string) *template.Template {
	return template.Must(template.New("notification").Funcs(template.FuncMap{
		// These no-argument functions emit only compiled layout constants. Dynamic
		// semantic values never pass through template.HTML. html/template strips
		// ordinary comments, including the conditional wrappers needed by Outlook.
		"outlookOpen": func() template.HTML {
			return `<!--[if mso]><table role="presentation" width="600" cellpadding="0" cellspacing="0"><tr><td><![endif]-->`
		},
		"outlookClose": func() template.HTML { return `<!--[if mso]></td></tr></table><![endif]-->` },
	}).Parse(source))
}

var ErrInvalidMessage = errors.New("invalid email message")

type Preview struct {
	Subject         string `json:"subject"`
	HTML            string `json:"html"`
	Text            string `json:"text"`
	TemplateVersion string `json:"templateVersion"`
}
type detail struct{ Label, Value string }
type view struct {
	Locale, Status, Color, Tint, Title, Summary, Percent, Amount, ActionLabel, Action, Note, Footer, URL, Subject, CTA, Sep string
	Details                                                                                                                 []detail
}

// ConsoleURL accepts only an explicit HTTPS origin. Request Host is never used.
func ConsoleURL(origin string) (string, error) {
	if origin == "" {
		return "", nil
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(origin, "\r\n\x00") {
		return "", ErrInvalidMessage
	}
	u.Path = "/system"
	u.RawQuery = "tab=diagnostics"
	return u.String(), nil
}

// Render previews the current fixed synthetic descriptor.
func Render(scenario, locale, eventID string, occurred time.Time, origin string) (Preview, error) {
	return renderSyntheticVersion(scenario, locale, eventID, occurred, origin, TemplateVersion)
}

// Version 1 remains intact for already queued descriptors.
func renderSyntheticVersion(scenario, locale, eventID string, occurred time.Time, origin, version string) (Preview, error) {
	if version != "1" && version != "2" {
		return Preview{}, ErrInvalidMessage
	}
	if !ValidLocale(locale) {
		return Preview{}, ErrInvalidMessage
	}
	l := newLocalizer(locale)
	v := view{Locale: locale, Percent: "12%", Amount: "30.72 / 256 GiB", Color: "#b45309", Tint: "#fff7ed", CTA: l.text("cta.diagnostics"), ActionLabel: l.text("label.action"), Sep: l.text("detail.separator")}
	v.URL, _ = ConsoleURL(origin)
	if origin != "" && v.URL == "" {
		return Preview{}, ErrInvalidMessage
	}
	v.Status = l.text("status.warning")
	v.Title = l.text("synthetic.title.warning")
	v.Summary = l.text("synthetic.summary.warning")
	v.Action = l.text("synthetic.action")
	v.Note = l.text("synthetic.note")
	v.Footer = l.text("synthetic.footer.v1")
	v.Details = []detail{{l.text("synthetic.detail.scope"), l.text("synthetic.detail.scopeValue")}, {l.text("synthetic.detail.source"), l.text("synthetic.detail.sourceValue")}, {l.text("synthetic.detail.recovery"), l.text("synthetic.detail.recoveryValue")}, {l.text("detail.eventTime"), occurred.UTC().Format("2006-01-02 15:04:05 UTC")}, {l.text("detail.eventId"), eventID}}
	switch scenario {
	case "warning":
	case "critical":
		v.Percent = "4%"
		v.Amount = "10.24 / 256 GiB"
		v.Color = "#b91c1c"
		v.Tint = "#fef2f2"
		v.Status = l.text("status.critical")
		v.Summary = l.text("synthetic.summary.critical")
	case "resolved":
		v.Percent = "26%"
		v.Amount = "66.56 / 256 GiB"
		v.Color = "#047857"
		v.Tint = "#ecfdf5"
		v.Status = l.text("status.resolved")
		v.Title = l.text("synthetic.title.resolved")
		v.Summary = l.text("synthetic.summary.resolved")
	default:
		return Preview{}, ErrInvalidMessage
	}
	v.Subject = l.text("subjectPrefix") + " " + v.Status + " · " + v.Title
	if version == "2" {
		v.Footer = l.text("synthetic.footer.v2")
	}
	return renderView(v, version)
}

func renderView(v view, version string) (Preview, error) {
	var html bytes.Buffer
	current := mailTemplate
	if version == "quota-1" {
		current = quotaMailTemplate
	}
	if err := current.Execute(&html, v); err != nil {
		return Preview{}, ErrInvalidMessage
	}
	var plain strings.Builder
	fmt.Fprintf(&plain, "%s\n\n%s\n%s\n%s\n\n", v.Subject, v.Summary, v.Percent, v.Amount)
	for _, d := range v.Details {
		fmt.Fprintf(&plain, "%s%s%s\n", d.Label, v.Sep, d.Value)
	}
	fmt.Fprintf(&plain, "\n%s\n%s\n\n%s\n", v.ActionLabel, v.Action, v.Note)
	if v.URL != "" {
		fmt.Fprintf(&plain, "\n%s\n", v.URL)
	}
	fmt.Fprintf(&plain, "\n%s\n", v.Footer)
	return Preview{Subject: v.Subject, HTML: html.String(), Text: plain.String(), TemplateVersion: version}, nil
}
