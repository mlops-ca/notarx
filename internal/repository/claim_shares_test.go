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

// The phases of one claim never examine more than QueueClaimScanCap rows together: the opening scan leaves a
// share to every other format, keeps at least one share however many formats there are, and no phase goes
// past what is left of the cap.
func TestClaimPhaseBudgetsStayWithinTheCap(t *testing.T) {
	for _, formats := range []int{1, 2, 10, QueueClaimScanCap, 2 * QueueClaimScanCap} {
		perFormat := max(QueueClaimScanCap/(formats+1), 1)
		opening := claimPhaseBudget(formatClaimOpening, formats, 0)
		if opening < perFormat {
			t.Fatalf("%d formats: opening budget %d below one share %d", formats, opening, perFormat)
		}
		if formats > 1 && formats+1 <= QueueClaimScanCap && opening+perFormat*(formats-1) > QueueClaimScanCap {
			t.Fatalf("%d formats: opening %d leaves no share for every other format", formats, opening)
		}
		scanned := opening
		for i := 1; i < formats; i++ {
			share := claimPhaseBudget(formatClaimShare, formats, scanned)
			if share > perFormat {
				t.Fatalf("%d formats: share %d above %d", formats, share, perFormat)
			}
			scanned += max(share, 0)
		}
		scanned += max(claimPhaseBudget(formatClaimRemainder, formats, scanned), 0)
		if scanned > QueueClaimScanCap {
			t.Fatalf("%d formats: the phases examine %d rows, above the cap %d", formats, scanned, QueueClaimScanCap)
		}
	}
}
