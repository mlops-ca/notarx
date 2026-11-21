package config

import (
	"errors"
	"time"
)

// DefaultPromotionMaxRulesPerRepository is the default of NOTARX_PROMOTION_MAX_RULES_PER_REPOSITORY (ADR 0021).
const DefaultPromotionMaxRulesPerRepository = 32

// MaxPromotionMaxRulesPerRepository bounds the configurable cap: rule fan-out is a bounded insert per event, so the
// cap must stay small.
const MaxPromotionMaxRulesPerRepository = 1024

// DefaultPromotionMaxHops is the default of NOTARX_PROMOTION_MAX_HOPS (ADR 0021, Chains): the longest chain_path an
// import accepts.
const DefaultPromotionMaxHops = 8

// MaxPromotionMaxHops bounds the configurable cap: a chain of rules is a handful of stages, not a graph walk.
const MaxPromotionMaxHops = 64

// DefaultPromotionEvaluatorInterval and DefaultPromotionEvaluatorConcurrency are the defaults of
// NOTARX_PROMOTION_EVALUATOR_INTERVAL and NOTARX_PROMOTION_EVALUATOR_CONCURRENCY (ADR 0021, Evaluator).
const (
	DefaultPromotionEvaluatorInterval    = 5 * time.Second
	DefaultPromotionEvaluatorConcurrency = 4
)

// MaxPromotionEvaluatorConcurrency bounds the evaluations one node claims at once.
const MaxPromotionEvaluatorConcurrency = 64

var (
	errMaxRulesPerRepository = errors.New("NOTARX_PROMOTION_MAX_RULES_PER_REPOSITORY must be at most 1024")
	errEvaluatorConcurrency  = errors.New("NOTARX_PROMOTION_EVALUATOR_CONCURRENCY must be at most 64")
	errMaxHops               = errors.New("NOTARX_PROMOTION_MAX_HOPS must be at most 64")
)

// PromotionConfig holds the promotion rule policy of ADR 0021 ("Configuration").
type PromotionConfig struct {
	// MaxRulesPerRepository is the most promotion rules one source repository can have
	// (NOTARX_PROMOTION_MAX_RULES_PER_REPOSITORY, default 32, at most 1024).
	MaxRulesPerRepository int
	// MaxHops is the longest chain_path an import accepts, and the depth a chain of rules may have
	// (NOTARX_PROMOTION_MAX_HOPS, default 8, at most 64).
	MaxHops int
	// Disabled turns the evaluator off (NOTARX_PROMOTION_ENABLED=false): rules stay configuration, publications
	// write no evaluations and no worker runs. The default is enabled.
	Disabled bool
	// EvaluatorInterval is the poll interval of the evaluator worker between wake-ups
	// (NOTARX_PROMOTION_EVALUATOR_INTERVAL, default 5s).
	EvaluatorInterval time.Duration
	// EvaluatorConcurrency is the evaluations one node claims and evaluates at once
	// (NOTARX_PROMOTION_EVALUATOR_CONCURRENCY, default 4, at most 64).
	EvaluatorConcurrency int
}

// Enabled reports whether rules are evaluated.
func (c PromotionConfig) Enabled() bool { return !c.Disabled }

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
	if current.MaxHops == 0 {
		current.MaxHops = DefaultPromotionMaxHops
	}
	hops, err := positiveIntEnv("NOTARX_PROMOTION_MAX_HOPS", current.MaxHops, false)
	if err != nil {
		return current, err
	}
	if hops > MaxPromotionMaxHops {
		return current, errMaxHops
	}
	current.MaxHops = hops
	enabled, err := boolEnv("NOTARX_PROMOTION_ENABLED", !current.Disabled)
	if err != nil {
		return current, err
	}
	current.Disabled = !enabled
	if current.EvaluatorInterval == 0 {
		current.EvaluatorInterval = DefaultPromotionEvaluatorInterval
	}
	if current.EvaluatorInterval, err = durationEnv("NOTARX_PROMOTION_EVALUATOR_INTERVAL", current.EvaluatorInterval); err != nil {
		return current, err
	}
	if current.EvaluatorConcurrency == 0 {
		current.EvaluatorConcurrency = DefaultPromotionEvaluatorConcurrency
	}
	if current.EvaluatorConcurrency, err = positiveIntEnv("NOTARX_PROMOTION_EVALUATOR_CONCURRENCY", current.EvaluatorConcurrency, false); err != nil {
		return current, err
	}
	if current.EvaluatorConcurrency > MaxPromotionEvaluatorConcurrency {
		return current, errEvaluatorConcurrency
	}
	return current, nil
}

// PromotionEnabled reads NOTARX_PROMOTION_ENABLED through lookup, for the commands that are not the gateway: the event repair
// tool fans the events it writes out to the rules only when promotion is enabled. It parses the value exactly as the gateway
// does and reads nothing else.
func PromotionEnabled(lookup func(string) (string, bool)) (bool, error) {
	raw, _ := lookup("NOTARX_PROMOTION_ENABLED")
	return parseBoolValue("NOTARX_PROMOTION_ENABLED", raw, true)
}
