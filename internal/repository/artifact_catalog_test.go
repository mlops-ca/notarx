package repository

import (
	"strings"
	"testing"
)

// TestArtifactCatalogRegistryListsEveryHostedFormat fails when a hosted format is admitted
// without a catalogue coverage decision (ADR 0023 section 8), so a new format cannot join
// the repository list without deciding how its coordinates reach artifact_catalog.
func TestArtifactCatalogRegistryListsEveryHostedFormat(t *testing.T) {
	registered := map[Format]ArtifactCatalogFormat{}
	for _, entry := range ArtifactCatalogFormats() {
		if _, duplicate := registered[entry.Format]; duplicate {
			t.Errorf("format %q is registered twice", entry.Format)
		}
		registered[entry.Format] = entry
		if entry.Covered == (entry.Pending != "") {
			t.Errorf("format %q must be either covered or pending with a reason: %+v", entry.Format, entry)
		}
	}
	admitted := append(SupportedFormats(), FormatHelm, FormatNuGet, FormatRubyGems)
	for _, format := range admitted {
		if _, ok := registered[format]; !ok {
			t.Errorf("format %q is admitted but has no artifact catalogue entry; add it to artifactCatalogRegistry", format)
		}
		delete(registered, format)
	}
	for format := range registered {
		t.Errorf("artifact catalogue registry names %q, which is not an admitted format", format)
	}
}

func TestArtifactCatalogCoversEveryFormatOfTheSearchProjection(t *testing.T) {
	// The formats artifact_search_projection unions today; each must be covered.
	for _, format := range []Format{FormatMaven, FormatOCI, FormatConan, FormatRaw, FormatNPM, FormatPyPI, FormatGo, FormatAPT} {
		if !ArtifactCatalogCovered(format) {
			t.Errorf("%s is part of artifact_search_projection but not covered by the catalogue", format)
		}
	}
	if ArtifactCatalogCovered(FormatCargo) || ArtifactCatalogCovered(Format("unknown")) {
		t.Error("a format outside the projection is reported as covered")
	}
}

func TestCatalogStatementsAreBuiltPerCoveredFormatOnly(t *testing.T) {
	for _, entry := range artifactCatalogRegistry {
		lock, lockErr := catalogLockSQL(entry.Format)
		recompute, recomputeErr := catalogRecomputeSQL(entry.Format)
		expected, expectedErr := catalogExpectedSQL(entry.Format)
		if !entry.Covered {
			if lockErr == nil || recomputeErr == nil || expectedErr == nil {
				t.Errorf("%s: statements were built for an uncovered format", entry.Format)
			}
			continue
		}
		if lockErr != nil || recomputeErr != nil || expectedErr != nil {
			t.Fatalf("%s: %v %v %v", entry.Format, lockErr, recomputeErr, expectedErr)
		}
		literal := "'" + string(entry.Format) + "'"
		for name, statement := range map[string]string{"lock": lock, "recompute": recompute, "expected": expected} {
			if !strings.Contains(statement, literal) {
				t.Errorf("%s %s: the format literal %s is not in the statement", entry.Format, name, literal)
			}
		}
		if (entry.Format == FormatGo) != strings.Contains(recompute, "artifact_tombstones") {
			t.Errorf("%s: the tombstone filter belongs to Go only", entry.Format)
		}
	}
}

func TestRefreshArtifactCatalogRejectsMismatchedArguments(t *testing.T) {
	if err := refreshArtifactCatalogPairs(t.Context(), nil, FormatNPM, []string{"a"}, nil); err != nil {
		t.Fatalf("no coordinates must be a no-op: %v", err)
	}
	if err := refreshArtifactCatalogPairs(t.Context(), nil, FormatNPM, []string{"a", "b"}, []string{"x"}); err == nil {
		t.Fatal("mismatched lengths accepted")
	}
	if err := refreshArtifactCatalogPairs(t.Context(), nil, FormatCargo, []string{"a"}, []string{"x"}); err == nil {
		t.Fatal("an uncovered format was refreshed")
	}
}
