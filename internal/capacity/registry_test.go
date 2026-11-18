package capacity

import "testing"

func TestRegistryHelpers(t *testing.T) {
	formats := Formats()
	if len(formats) != 12 {
		t.Fatalf("registry holds %d formats, want every hosted format (12)", len(formats))
	}
	for index := 1; index < len(formats); index++ {
		if formats[index-1].Format >= formats[index].Format {
			t.Fatalf("Formats() is not sorted: %v", formats)
		}
	}
	for _, entry := range formats {
		if _, ok := UsageQuery(entry.Format); !ok {
			t.Errorf("no usage query for %s", entry.Format)
		}
		if entry.Covered != Covered(entry.Format) {
			t.Errorf("Covered(%s) disagrees with the registry", entry.Format)
		}
	}
	if Covered("unknown") {
		t.Error("an unknown format must not be covered")
	}
	if _, ok := UsageQuery("unknown"); ok {
		t.Error("an unknown format has no usage query")
	}
	if got := len(CoveredFormats()); AllCovered() != (got == len(formats)) {
		t.Errorf("AllCovered disagrees with CoveredFormats (%d)", got)
	}
}
