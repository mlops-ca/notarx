package workloadpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Kind is the issuer kind a policy is written for (workload_issuers.kind).
type Kind string

const (
	KindGitHubActions Kind = "github_actions"
	KindGitLabCI      Kind = "gitlab_ci"
	KindKubernetes    Kind = "kubernetes"
	KindGeneric       Kind = "generic"
)

// Op is a condition operator.
type Op string

const (
	OpEq     Op = "eq"
	OpIn     Op = "in"
	OpGlob   Op = "glob"
	OpPrefix Op = "prefix"
)

// Limits of ADR 0019.
const (
	MaxConditions   = 16
	MaxValues       = 8
	MaxValueBytes   = 256
	maxPolicyBytes  = 64 << 10
	unsupportedType = "claim is not a string, boolean or integer"
)

// Condition is one {claim, op, values} entry of a policy.
type Condition struct {
	Claim  ClaimPath
	Op     Op
	Values []string

	glob [][]globToken
}

type wireCondition struct {
	Claim  json.RawMessage `json:"claim"`
	Op     Op              `json:"op"`
	Values []string        `json:"values"`
}

// MarshalJSON writes the canonical form: the claim as an array of keys.
func (c Condition) MarshalJSON() ([]byte, error) {
	claim, err := json.Marshal([]string(c.Claim))
	if err != nil {
		return nil, err
	}
	return json.Marshal(wireCondition{Claim: claim, Op: c.Op, Values: c.Values})
}

// ParseConditions decodes and validates the shape of a condition list: the limits,
// the operators, the claim spellings and the glob syntax. It does not apply the
// per-kind mandatory conditions; Validate does.
func ParseConditions(kind Kind, raw []byte) ([]Condition, error) {
	if len(raw) > maxPolicyBytes {
		return nil, errors.New("conditions are too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire []wireCondition
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("conditions must be a JSON array of {claim, op, values}: %w", err)
	}
	if decoder.More() {
		return nil, errors.New("conditions has trailing data")
	}
	if len(wire) == 0 || len(wire) > MaxConditions {
		return nil, fmt.Errorf("a policy needs between 1 and %d conditions", MaxConditions)
	}
	conditions := make([]Condition, 0, len(wire))
	for index, item := range wire {
		condition, err := buildCondition(kind, item)
		if err != nil {
			return nil, fmt.Errorf("condition %d: %w", index, err)
		}
		conditions = append(conditions, condition)
	}
	return conditions, nil
}

func buildCondition(kind Kind, item wireCondition) (Condition, error) {
	path, err := ParseClaimPath(kind, item.Claim)
	if err != nil {
		return Condition{}, err
	}
	condition := Condition{Claim: path, Op: item.Op, Values: item.Values}
	switch item.Op {
	case OpEq:
		if len(item.Values) != 1 {
			return Condition{}, errors.New("eq takes exactly one value")
		}
	case OpIn, OpGlob, OpPrefix:
		if len(item.Values) == 0 || len(item.Values) > MaxValues {
			return Condition{}, fmt.Errorf("%s takes between 1 and %d values", item.Op, MaxValues)
		}
	default:
		return Condition{}, fmt.Errorf("unknown operator %q", item.Op)
	}
	for _, value := range item.Values {
		if value == "" || len(value) > MaxValueBytes {
			return Condition{}, fmt.Errorf("values must be non-empty and at most %d bytes", MaxValueBytes)
		}
		if item.Op != OpGlob {
			continue
		}
		tokens, err := compileGlob(value)
		if err != nil {
			return Condition{}, err
		}
		if bareWildcard(tokens) {
			return Condition{}, fmt.Errorf("glob %q matches every value and restricts nothing", value)
		}
		condition.glob = append(condition.glob, tokens)
	}
	return condition, nil
}

// CheckCanonical verifies that raw is a well-formed condition list already in stored form
// (every claim an array of keys, no shorthand, no unknown fields). Stores call it so that a
// caller that skipped Validate cannot persist a list that Evaluate would later fail to parse.
// It does not apply the per-kind mandatory conditions, which need the issuer kind and the
// write-time acknowledgements; handlers call Validate for that.
func CheckCanonical(raw []byte) error {
	if _, err := ParseConditions(KindGeneric, raw); err != nil {
		return err
	}
	// ParseConditions accepted it; what remains is that no claim uses a string spelling.
	// Key order and whitespace do not matter: jsonb returns its own.
	var wire []wireCondition
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	for _, item := range wire {
		if trimmed := bytes.TrimSpace(item.Claim); len(trimmed) == 0 || trimmed[0] != '[' {
			return errors.New("conditions are not in canonical form (claims must be arrays of keys); run Validate first")
		}
	}
	return nil
}

// Canonical returns the stored form of a condition list: claims as key arrays.
func Canonical(conditions []Condition) (json.RawMessage, error) {
	return json.Marshal(conditions)
}

// Failure explains, for operators only, why a condition did not match. It names the
// claim and the reason and never contains claim values or the policy's expected values.
type Failure struct {
	Policy    string
	Condition int
	Claim     ClaimPath
	Reason    string
}

func (f Failure) String() string {
	return fmt.Sprintf("policy %s condition %d claim %s: %s", f.Policy, f.Condition, f.Claim, f.Reason)
}

// scalar renders a string, boolean or integer claim in the form conditions compare against.
func scalar(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case bool:
		return strconv.FormatBool(v), true
	case json.Number:
		if parsed, err := v.Int64(); err == nil {
			return strconv.FormatInt(parsed, 10), true
		}
	case float64:
		if v == math.Trunc(v) && math.Abs(v) < 1<<53 {
			return strconv.FormatInt(int64(v), 10), true
		}
	case int:
		return strconv.Itoa(v), true
	case int64:
		return strconv.FormatInt(v, 10), true
	}
	return "", false
}

// match evaluates one condition. The reason is empty on success.
func (c Condition) match(claims map[string]any) (bool, string) {
	raw, found := c.Claim.lookup(claims)
	if !found {
		return false, "claim is missing"
	}
	value, ok := scalar(raw)
	if !ok {
		return false, unsupportedType
	}
	switch c.Op {
	case OpEq, OpIn:
		for _, expected := range c.Values {
			if value == expected {
				return true, ""
			}
		}
	case OpPrefix:
		for _, expected := range c.Values {
			if strings.HasPrefix(value, expected) {
				return true, ""
			}
		}
	case OpGlob:
		for _, tokens := range c.glob {
			if globMatch(tokens, value) {
				return true, ""
			}
		}
	}
	return false, "claim value does not match"
}
