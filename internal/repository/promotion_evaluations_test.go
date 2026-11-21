package repository

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRecordedReasonsAreCutAtACharacterBoundary(t *testing.T) {
	if got := truncateRunes("short", promotionReasonMax); got != "short" {
		t.Fatalf("short reason = %q", got)
	}
	long := strings.Repeat("é", 2000)
	got := truncateRunes(long, promotionReasonMax)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != promotionReasonMax {
		t.Fatalf("cut to %d characters, valid=%v", utf8.RuneCountInString(got), utf8.ValidString(got))
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
