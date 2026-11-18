package repository

import "testing"

func TestMemorySiteSettingsVersionedReplacement(t *testing.T) {
	store := NewMemoryStore()
	current, err := store.GetSiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != "1" || current.SiteName != "notarx" || current.BrandMark != "NX" || current.DefaultThemeID != "notarx-dark" || len(current.EnabledThemeIDs) != 2 {
		t.Fatalf("defaults = %#v", current)
	}

	replaced, err := store.ReplaceSiteSettings(t.Context(), SiteSettings{
		SiteName: "Acme Packages", BrandMark: "AC", EnabledThemeIDs: []string{"notarx-light"}, DefaultThemeID: "notarx-light",
	}, current.Version)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Version != "2" || replaced.SiteName != "Acme Packages" || replaced.DefaultThemeID != "notarx-light" {
		t.Fatalf("replaced = %#v", replaced)
	}
	if _, err := store.ReplaceSiteSettings(t.Context(), replaced, current.Version); err != ErrVersionConflict {
		t.Fatalf("stale replace error = %v", err)
	}
}
