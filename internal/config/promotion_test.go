package config

import (
	"testing"
	"time"
)

func TestPromotionConfigMaxRulesPerRepository(t *testing.T) {
	got, err := promotionConfigFromEnv(PromotionConfig{})
	if err != nil || got.MaxRulesPerRepository != DefaultPromotionMaxRulesPerRepository {
		t.Fatalf("default = %+v, %v", got, err)
	}
	t.Setenv("NOTARX_PROMOTION_MAX_RULES_PER_REPOSITORY", "5")
	if got, err = promotionConfigFromEnv(PromotionConfig{}); err != nil || got.MaxRulesPerRepository != 5 {
		t.Fatalf("5 = %+v, %v", got, err)
	}
	for _, bad := range []string{"0", "-1", "many", "1025"} {
		t.Setenv("NOTARX_PROMOTION_MAX_RULES_PER_REPOSITORY", bad)
		if _, err = promotionConfigFromEnv(PromotionConfig{}); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}

func TestPromotionConfigMaxHops(t *testing.T) {
	got, err := promotionConfigFromEnv(PromotionConfig{})
	if err != nil || got.MaxHops != DefaultPromotionMaxHops {
		t.Fatalf("default = %+v, %v", got, err)
	}
	t.Setenv("NOTARX_PROMOTION_MAX_HOPS", "3")
	if got, err = promotionConfigFromEnv(PromotionConfig{}); err != nil || got.MaxHops != 3 {
		t.Fatalf("3 = %+v, %v", got, err)
	}
	for _, bad := range []string{"0", "-1", "many", "65"} {
		t.Setenv("NOTARX_PROMOTION_MAX_HOPS", bad)
		if _, err = promotionConfigFromEnv(PromotionConfig{}); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}

func TestPromotionConfigEvaluator(t *testing.T) {
	got, err := promotionConfigFromEnv(PromotionConfig{})
	if err != nil || !got.Enabled() || got.EvaluatorInterval != 5*time.Second || got.EvaluatorConcurrency != 4 {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	t.Setenv("NOTARX_PROMOTION_ENABLED", "false")
	t.Setenv("NOTARX_PROMOTION_EVALUATOR_INTERVAL", "2s")
	t.Setenv("NOTARX_PROMOTION_EVALUATOR_CONCURRENCY", "8")
	if got, err = promotionConfigFromEnv(PromotionConfig{}); err != nil || got.Enabled() || got.EvaluatorInterval != 2*time.Second || got.EvaluatorConcurrency != 8 {
		t.Fatalf("overrides = %+v, %v", got, err)
	}
	for name, bad := range map[string]string{"NOTARX_PROMOTION_ENABLED": "yes", "NOTARX_PROMOTION_EVALUATOR_INTERVAL": "0", "NOTARX_PROMOTION_EVALUATOR_CONCURRENCY": "65"} {
		t.Setenv("NOTARX_PROMOTION_ENABLED", "true")
		t.Setenv("NOTARX_PROMOTION_EVALUATOR_INTERVAL", "5s")
		t.Setenv("NOTARX_PROMOTION_EVALUATOR_CONCURRENCY", "4")
		t.Setenv(name, bad)
		if _, err = promotionConfigFromEnv(PromotionConfig{}); err == nil {
			t.Fatalf("%s=%q must be refused", name, bad)
		}
	}
}

func TestPromotionEnabledReadsOnlyTheFlagThroughTheLookup(t *testing.T) {
	lookup := func(value string, set bool) func(string) (string, bool) {
		return func(name string) (string, bool) {
			if name != "NOTARX_PROMOTION_ENABLED" {
				t.Fatalf("read %s", name)
			}
			return value, set
		}
	}
	for value, want := range map[string]bool{"": true, "true": true, " true ": true, "false": false} {
		if got, err := PromotionEnabled(lookup(value, value != "")); err != nil || got != want {
			t.Errorf("%q = %v, %v; want %v", value, got, err, want)
		}
	}
	if _, err := PromotionEnabled(lookup("maybe", true)); err == nil {
		t.Fatal("an invalid value must be refused, as the gateway refuses it")
	}
}
