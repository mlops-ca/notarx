package retention

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Profile selects the default keep times of the registry.
type Profile string

const (
	// Standard is the default profile.
	Standard Profile = "standard"
	// FreeTier is the single-node reference deployment of ADR 0017.
	FreeTier Profile = "freetier"
)

// ProfileEnv and OverridePrefix name the configuration keys (ADR 0013).
const (
	ProfileEnv     = "NOTARX_RETENTION_PROFILE"
	OverridePrefix = "NOTARX_RETENTION_"
	// RunBudgetEnv bounds one table's loop in a pass.
	RunBudgetEnv = "NOTARX_RETENTION_RUN_BUDGET"
	// DefaultRunBudget is the default of RunBudgetEnv.
	DefaultRunBudget = 60 * time.Second
)

// Override replaces the keep time of an entry (by label) or of every entry of a table.
type Override struct {
	// Off disables the table.
	Off  bool
	Keep time.Duration
}

// Config is the operator configuration of the janitor.
type Config struct {
	Profile   Profile
	Overrides map[string]Override
	RunBudget time.Duration
}

// DefaultConfig returns the standard profile without overrides.
func DefaultConfig() Config {
	return Config{Profile: Standard, Overrides: map[string]Override{}, RunBudget: DefaultRunBudget}
}

// EnvName returns the override key of a table, for example
// NOTARX_RETENTION_SECURITY_AUDIT_LOG.
func EnvName(table string) string { return OverridePrefix + strings.ToUpper(table) }

// ParseDuration reads a whole number of days with a d suffix ("30d") or a Go
// duration ("720h"); it must be positive. The value "off" is handled by the caller.
func ParseDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if days, ok := strings.CutSuffix(value, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 || n > 36500 {
			return 0, fmt.Errorf("%q is not a number of days between 1 and 36500", value)
		}
		return time.Duration(n) * day, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q is not a positive duration, a number of days such as 30d, or off", value)
	}
	return d, nil
}

// ParseProfile validates a profile name; the empty string is the standard profile.
func ParseProfile(value string) (Profile, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(Standard):
		return Standard, nil
	case string(FreeTier):
		return FreeTier, nil
	}
	return "", fmt.Errorf("%s must be standard or freetier, got %q", ProfileEnv, value)
}

// envAliases names the documented override key of a table whose ADR gave it a name other than
// NOTARX_RETENTION_<TABLE>; the generic key wins when both are set.
var envAliases = map[string]string{"artifact_scan_history": "NOTARX_SCAN_HISTORY_RETENTION", "domain_events": "NOTARX_EVENTS_RETENTION"}

// LoadConfig reads the profile, the run budget and the overrides for every table and
// every entry label of the registry from lookup (os.LookupEnv in production). A label
// override such as NOTARX_RETENTION_WEBHOOK_DELIVERIES_DEAD wins over the table key.
func LoadConfig(lookup func(string) (string, bool)) (Config, error) {
	cfg := DefaultConfig()
	if value, ok := lookup(ProfileEnv); ok {
		profile, err := ParseProfile(value)
		if err != nil {
			return Config{}, err
		}
		cfg.Profile = profile
	}
	if value, ok := lookup(RunBudgetEnv); ok && strings.TrimSpace(value) != "" {
		budget, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil || budget <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive duration, got %q", RunBudgetEnv, value)
		}
		cfg.RunBudget = budget
	}
	keys := map[string]bool{}
	for _, entry := range DefaultRegistry() {
		keys[entry.Table] = true
		keys[entry.Name()] = true
	}
	for key := range keys {
		name := EnvName(key)
		value, ok := lookup(name)
		if alias, aliased := envAliases[key]; aliased && (!ok || strings.TrimSpace(value) == "") {
			name = alias
			value, ok = lookup(name)
		}
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(value), "off") {
			cfg.Overrides[key] = Override{Off: true}
			continue
		}
		keep, err := ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", name, err)
		}
		cfg.Overrides[key] = Override{Keep: keep}
	}
	return cfg, nil
}

// Keep returns the keep time of an entry under the configuration, and false when
// the table is switched off.
func (c Config) Keep(e Entry) (time.Duration, bool) {
	for _, key := range []string{e.Name(), e.Table} {
		if o, ok := c.Overrides[key]; ok {
			if o.Off {
				return 0, false
			}
			return o.Keep, true
		}
	}
	if c.Profile == FreeTier {
		return e.FreeTier, true
	}
	return e.Standard, true
}

// KeepOrDefault is the keep time of a table, and the standard keep time of its registry entry when the table is switched
// off (kept for ever), for a caller that needs a bounded window of rows to look at.
func (c Config) KeepOrDefault(table string) (time.Duration, error) {
	for _, entry := range DefaultRegistry() {
		if entry.Table != table {
			continue
		}
		if keep, on := c.Keep(entry); on {
			return keep, nil
		}
		return entry.Standard, nil
	}
	return 0, fmt.Errorf("retention: unknown table %q", table)
}

// KeepFor is Keep by table name for the first entry of that table; the partition
// manager uses it for the audit tables.
func (c Config) KeepFor(table string) (time.Duration, bool, error) {
	for _, entry := range DefaultRegistry() {
		if entry.Table == table {
			keep, on := c.Keep(entry)
			return keep, on, nil
		}
	}
	return 0, false, fmt.Errorf("retention: unknown table %q", table)
}
