package capacity

import "sort"

// FormatCoverage records whether the stores of a hosted format thread
// Reserve through every writer of their asset rows.
type FormatCoverage struct {
	Format string
	// Covered is true once every writer of the format calls Reserve and the
	// per-format equality test (publish, replace, delete, re-publish) passes.
	Covered bool
	// Enforcement names how the format enforced its quota before counters.
	Enforcement string
}

const (
	byTrigger      = "capacity trigger"
	bySessionStore = "session reservation code"
)

// registry lists every hosted format (ADR 0023 section 3). A format that lands
// without an entry fails the registry test, so a new format cannot be unbounded.
var registry = []FormatCoverage{
	{Format: "raw", Covered: true, Enforcement: byTrigger},
	{Format: "maven", Covered: true, Enforcement: byTrigger},
	{Format: "oci", Covered: true, Enforcement: byTrigger},
	{Format: "conan", Covered: true, Enforcement: byTrigger},
	{Format: "npm", Covered: true, Enforcement: byTrigger},
	{Format: "pypi", Covered: true, Enforcement: byTrigger},
	{Format: "go", Covered: true, Enforcement: byTrigger},
	{Format: "apt", Covered: true, Enforcement: byTrigger},
	{Format: "cargo", Covered: true, Enforcement: byTrigger},
	{Format: "helm", Covered: true, Enforcement: bySessionStore},
	{Format: "nuget", Covered: true, Enforcement: bySessionStore},
	{Format: "rubygems", Covered: true, Enforcement: bySessionStore},
}

// Formats returns the registry sorted by format.
func Formats() []FormatCoverage {
	out := append([]FormatCoverage(nil), registry...)
	sort.Slice(out, func(a, b int) bool { return out[a].Format < out[b].Format })
	return out
}

// Covered reports whether the counter of the format is maintained by every writer.
func Covered(format string) bool {
	for _, entry := range registry {
		if entry.Format == format {
			return entry.Covered
		}
	}
	return false
}

// CoveredFormats returns the covered format names, sorted.
func CoveredFormats() []string {
	out := []string{}
	for _, entry := range Formats() {
		if entry.Covered {
			out = append(out, entry.Format)
		}
	}
	return out
}

// AllCovered reports whether every hosted format is covered.
func AllCovered() bool {
	for _, entry := range registry {
		if !entry.Covered {
			return false
		}
	}
	return true
}
