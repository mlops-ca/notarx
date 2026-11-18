package emailnotification

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

// DefaultLocale is the reference locale and the fallback for every missing
// string (ADR 0014).
const DefaultLocale = "en-CA"

// Locales is the closed registry of email locale tags. A tag is matched against
// it and never interpolated from input.
var Locales = []string{"en-CA", "fr-CA", "zh-CN"}

// ValidLocale reports whether tag is in the closed registry.
func ValidLocale(tag string) bool {
	for _, locale := range Locales {
		if locale == tag {
			return true
		}
	}
	return false
}

// ParseDefaultLocale validates NOTARX_EMAIL_DEFAULT_LOCALE; an empty value
// selects en-CA.
func ParseDefaultLocale(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultLocale, nil
	}
	if !ValidLocale(raw) {
		return "", fmt.Errorf("NOTARX_EMAIL_DEFAULT_LOCALE must be one of %s", strings.Join(Locales, ", "))
	}
	return raw, nil
}

// The bundles are flat key to text maps. zh-CN is a community-maintained
// overlay (ADR 0014): a locale without a bundle file, or a key missing from
// one, falls back to en-CA. fr-CA completeness is a Go test.
//
//go:embed locales/*.json
var localeFiles embed.FS

type bundle map[string]string

var bundles = loadBundles(localeFiles)

func loadBundles(files embed.FS) map[string]bundle {
	out := map[string]bundle{}
	for _, locale := range Locales {
		raw, err := files.ReadFile("locales/" + locale + ".json")
		if err != nil {
			continue
		}
		var b bundle
		if err := json.Unmarshal(raw, &b); err != nil {
			panic("emailnotification: invalid locale bundle " + locale + ": " + err.Error())
		}
		out[locale] = b
	}
	if out[DefaultLocale] == nil {
		panic("emailnotification: the " + DefaultLocale + " bundle is required")
	}
	return out
}

// localizer resolves strings for one locale with the en-CA fallback.
type localizer struct{ chain []bundle }

func newLocalizer(locale string) localizer { return localizerFrom(bundles, locale) }

func localizerFrom(all map[string]bundle, locale string) localizer {
	chain := []bundle{}
	if b := all[locale]; b != nil {
		chain = append(chain, b)
	}
	if locale != DefaultLocale {
		chain = append(chain, all[DefaultLocale])
	}
	return localizer{chain: chain}
}

// text returns the string for key, substituting {name} placeholders from the
// name/value pairs in args. The values are inserted as plain text; html/template
// escapes them at render time.
func (l localizer) text(key string, args ...string) string {
	for _, b := range l.chain {
		if s, ok := b[key]; ok {
			if len(args) == 0 {
				return s
			}
			pairs := make([]string, 0, len(args))
			for i := 0; i+1 < len(args); i += 2 {
				pairs = append(pairs, "{"+args[i]+"}", args[i+1])
			}
			return strings.NewReplacer(pairs...).Replace(s)
		}
	}
	return key
}
