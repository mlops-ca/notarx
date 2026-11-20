package repository

import (
	"context"
	"testing"
)

// A queue the opening scan drains ends the claim there: the shares and the remainder would find nothing.
func TestClaimFormatSharesStopsWhenTheOpeningScanDrainsTheQueue(t *testing.T) {
	calls := 0
	err := claimFormatShares(context.Background(), []Format{FormatRaw, FormatNPM, FormatPyPI}, 10, 0, func(_ context.Context, _ formatClaimPhase, _ []Format, want int) ([]Format, bool, error) {
		calls++
		return []Format{FormatRaw}, 1 < want, nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("claim made %d batches (%v), want the opening scan alone", calls, err)
	}
}

// A single-format claim with no format claims every format, as it did before format lists.
func TestFormatListOfNoFormatIsEveryFormat(t *testing.T) {
	if formatList("") != nil || len(formatList(FormatRaw)) != 1 {
		t.Fatalf("formatList: %v, %v", formatList(""), formatList(FormatRaw))
	}
}
