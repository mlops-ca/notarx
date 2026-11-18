package emailnotification

import (
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

var placeholder = regexp.MustCompile(`\{[a-zA-Z]+\}`)

func keys(b bundle) []string {
	out := make([]string, 0, len(b))
	for key := range b {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func TestFrenchBundleIsCompleteAndKeepsPlaceholders(t *testing.T) {
	en, fr := bundles["en-CA"], bundles["fr-CA"]
	if fr == nil {
		t.Fatal("fr-CA bundle is missing")
	}
	if strings.Join(keys(en), ",") != strings.Join(keys(fr), ",") {
		t.Fatalf("fr-CA keys differ from en-CA: %v vs %v", keys(fr), keys(en))
	}
	for key, text := range en {
		got, want := placeholder.FindAllString(fr[key], -1), placeholder.FindAllString(text, -1)
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: placeholders %v, want %v", key, got, want)
		}
		if strings.TrimSpace(fr[key]) == "" {
			t.Errorf("%s: empty fr-CA text", key)
		}
	}
}

func TestFrenchTypographyRules(t *testing.T) {
	for key, text := range bundles["fr-CA"] {
		if strings.Contains(strings.ToLower(text), "e-mail") || strings.Contains(strings.ToLower(text), "email") {
			t.Errorf("%s: use courriel", key)
		}
		if regexp.MustCompile(`\p{L}'\p{L}`).MatchString(text) {
			t.Errorf("%s: use the typographic apostrophe", key)
		}
		if regexp.MustCompile(` [?!;]`).MatchString(text) {
			t.Errorf("%s: no space before ? ! ;", key)
		}
	}
}

func TestEveryLocaleRendersLocalizedPreviewAndQuota(t *testing.T) {
	for _, locale := range Locales {
		p, err := Render("critical", locale, "synthetic", time.Now(), "https://console.example.invalid")
		if err != nil || !strings.Contains(p.HTML, `lang="`+locale+`"`) || !strings.Contains(p.Subject, "[Artifact Gateway]") {
			t.Fatalf("%s: %v %s", locale, err, p.Subject)
		}
	}
	fr, _ := Render("critical", "fr-CA", "synthetic", time.Now(), "")
	en, _ := Render("critical", "en-CA", "synthetic", time.Now(), "")
	if fr.Subject == en.Subject || !strings.Contains(fr.Text, "Heure de l’événement\u00a0: ") {
		t.Fatalf("fr-CA text was not localized: %s", fr.Text)
	}
	quota, err := RenderQuota(quotaMailFixture(), "fr-CA", "", "quota-1")
	if err != nil || !strings.Contains(quota.Text, "880 / 1000 octets") {
		t.Fatalf("fr-CA quota: %v %s", err, quota.Text)
	}
}

func TestChineseFallsBackToEnglishWithoutABundle(t *testing.T) {
	zh, err := Render("warning", "zh-CN", "synthetic", time.Now(), "")
	en, _ := Render("warning", "en-CA", "synthetic", time.Now(), "")
	if err != nil || zh.Subject != en.Subject || !strings.Contains(zh.HTML, `lang="zh-CN"`) {
		t.Fatalf("zh-CN fallback: %v %s", err, zh.Subject)
	}
}

func TestPartialOverlayFallsBackKeyByKey(t *testing.T) {
	all := map[string]bundle{"en-CA": {"a": "English {x}", "b": "kept"}, "zh-CN": {"a": "overlay {x}"}}
	l := localizerFrom(all, "zh-CN")
	if l.text("a", "x", "1") != "overlay 1" || l.text("b") != "kept" || l.text("missing") != "missing" {
		t.Fatal("fallback chain is wrong")
	}
	if localizerFrom(all, "fr-CA").text("a", "x", "2") != "English 2" {
		t.Fatal("a locale without a bundle must use en-CA")
	}
}

func TestMarkupInPlaceholdersAndBundlesIsEscapedInEveryLocale(t *testing.T) {
	const markup = "<script>alert(1)</script>"
	for _, locale := range Locales {
		p, err := Render("warning", locale, markup, time.Now(), "")
		if err != nil || strings.Contains(p.HTML, "<script") || !strings.Contains(p.HTML, "&lt;script&gt;") {
			t.Fatalf("%s: preview did not escape: %v", locale, err)
		}
		e := quotaMailFixture()
		e.RepositoryName = markup
		q, err := RenderQuota(e, locale, "", "quota-1")
		if err != nil || strings.Contains(q.HTML, "<script") || !strings.Contains(q.HTML, "&lt;script&gt;") {
			t.Fatalf("%s: quota did not escape: %v", locale, err)
		}
	}
	// A bundle value is data too: markup there is escaped.
	v := view{Locale: "en-CA", Title: newLocalizerForTest().text("a"), Status: "s", Subject: "s"}
	p, err := renderView(v, "2")
	if err != nil || strings.Contains(p.HTML, "<script") {
		t.Fatalf("bundle markup was not escaped: %v", err)
	}
}

func newLocalizerForTest() localizer {
	return localizerFrom(map[string]bundle{"en-CA": {"a": "<script>alert(1)</script>"}}, "en-CA")
}

func TestLocaleRegistryIsClosed(t *testing.T) {
	for _, tag := range []string{"en", "en-US", "fr", "fr-FR", "../fr-CA", "", "zh-CN "} {
		if ValidLocale(tag) {
			t.Fatalf("%q must not be a locale", tag)
		}
		if _, err := Render("warning", tag, "synthetic", time.Now(), ""); err == nil {
			t.Fatalf("%q rendered", tag)
		}
	}
}

func TestParseDefaultLocale(t *testing.T) {
	for raw, want := range map[string]string{"": "en-CA", " fr-CA ": "fr-CA", "zh-CN": "zh-CN", "en-CA": "en-CA"} {
		if got, err := ParseDefaultLocale(raw); err != nil || got != want {
			t.Fatalf("%q: %q %v", raw, got, err)
		}
	}
	if _, err := ParseDefaultLocale("en"); err == nil {
		t.Fatal("the retired en tag must be rejected")
	}
}
