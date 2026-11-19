package repository

import (
	"testing"

	"github.com/mlops-ca/notarx/internal/capacity"
)

// TestCapacityRegistryListsEveryHostedFormat fails when a format is admitted without a
// counter registry entry (ADR 0023 section 3), so a new format cannot land unbounded,
// and lists the formats whose counters are still pending.
func TestCapacityRegistryListsEveryHostedFormat(t *testing.T) {
	registered := map[string]capacity.FormatCoverage{}
	for _, entry := range capacity.Formats() {
		registered[entry.Format] = entry
		if _, ok := capacity.UsageQuery(entry.Format); !ok {
			t.Errorf("format %q has a registry entry but no usage query", entry.Format)
		}
	}
	supported := map[string]bool{}
	// Helm, NuGet and RubyGems are hosted formats with their own profiles outside
	// the generic supported list.
	admitted := append(SupportedFormats(), FormatHelm, FormatNuGet, FormatRubyGems)
	for _, format := range admitted {
		supported[string(format)] = true
		if _, ok := registered[string(format)]; !ok {
			t.Errorf("format %q is admitted but has no capacity counter coverage entry; add it to internal/capacity/registry.go", format)
		}
	}
	for format := range registered {
		if !supported[format] {
			t.Errorf("capacity registry names %q, which is not an admitted format", format)
		}
	}
	for _, entry := range capacity.Formats() {
		state := "pending"
		if entry.Covered {
			state = "covered"
		}
		t.Logf("capacity counter coverage: %-9s %-8s ", entry.Format, state)
	}
}
