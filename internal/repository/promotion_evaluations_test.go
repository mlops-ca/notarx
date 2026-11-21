package repository

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateReasonCutsAtACharacterBoundary(t *testing.T) {
	if got := truncateReason("short"); got != "short" {
		t.Fatalf("short reason = %q", got)
	}
	exact := strings.Repeat("a", 1024)
	if got := truncateReason(exact); got != exact {
		t.Fatal("a reason of exactly 1024 bytes must be kept")
	}
	// 1023 ASCII bytes then a three-byte character straddling the bound: the cut must not split it.
	long := strings.Repeat("a", 1023) + "€€"
	got := truncateReason(long)
	if !utf8.ValidString(got) || len(got) > 1024 || got != strings.Repeat("a", 1023) {
		t.Fatalf("truncated to %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
	if got = truncateReason(strings.Repeat("é", 700)); !utf8.ValidString(got) || len(got) > 1024 || len(got) < 1022 {
		t.Fatalf("two-byte characters: %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
}

func TestPromotionEvaluationBackoffDoublesToFiveMinutes(t *testing.T) {
	for attempts, want := range map[int]string{0: "5s", 1: "5s", 2: "10s", 3: "20s", 6: "2m40s", 7: "5m0s", 20: "5m0s"} {
		if got := PromotionEvaluationBackoff(attempts).String(); got != want {
			t.Errorf("backoff(%d) = %s, want %s", attempts, got, want)
		}
	}
	for _, state := range []PromotionEvaluationState{PromotionEvaluationQueued, PromotionEvaluationSkipped, PromotionEvaluationFailed} {
		if !state.Terminal() {
			t.Errorf("%s must be terminal", state)
		}
	}
	for _, state := range []PromotionEvaluationState{PromotionEvaluationPending, PromotionEvaluationWaiting} {
		if state.Terminal() {
			t.Errorf("%s must not be terminal", state)
		}
	}
}
