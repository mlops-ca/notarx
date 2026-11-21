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
