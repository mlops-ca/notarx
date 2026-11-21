package config

import "testing"

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
