package config

import "errors"

// DefaultPromotionMaxRulesPerRepository is the default of NOTARX_PROMOTION_MAX_RULES_PER_REPOSITORY (ADR 0021).
const DefaultPromotionMaxRulesPerRepository = 32

// MaxPromotionMaxRulesPerRepository bounds the configurable cap: rule fan-out is a bounded insert per event, so the
// cap must stay small.
const MaxPromotionMaxRulesPerRepository = 1024

var errMaxRulesPerRepository = errors.New("NOTARX_PROMOTION_MAX_RULES_PER_REPOSITORY must be at most 1024")

// PromotionConfig holds the promotion rule policy of ADR 0021 ("Configuration").
type PromotionConfig struct {
	// MaxRulesPerRepository is the most promotion rules one source repository can have
	// (NOTARX_PROMOTION_MAX_RULES_PER_REPOSITORY, default 32, at most 1024).
	MaxRulesPerRepository int
}

func promotionConfigFromEnv(current PromotionConfig) (PromotionConfig, error) {
	if current.MaxRulesPerRepository == 0 {
		current.MaxRulesPerRepository = DefaultPromotionMaxRulesPerRepository
	}
	value, err := positiveIntEnv("NOTARX_PROMOTION_MAX_RULES_PER_REPOSITORY", current.MaxRulesPerRepository, false)
	if err != nil {
		return current, err
	}
	if value > MaxPromotionMaxRulesPerRepository {
		return current, errMaxRulesPerRepository
	}
	current.MaxRulesPerRepository = value
	return current, nil
}
