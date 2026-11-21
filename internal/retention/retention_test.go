package retention

import (
	"strings"
	"testing"
	"time"
)

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func TestRegistryIsValidAndHasNoProvenanceTables(t *testing.T) {
	seen := map[string]bool{}
	tables := map[string]bool{}
	for _, entry := range DefaultRegistry() {
		if err := entry.Validate(); err != nil {
			t.Errorf("%s: %v", entry.Name(), err)
		}
		if seen[entry.Name()] {
			t.Errorf("duplicate entry %s", entry.Name())
		}
		seen[entry.Name()] = true
		tables[entry.Table] = true
		if entry.BatchSize() != DefaultBatch {
			t.Errorf("%s: batch = %d, want %d", entry.Name(), entry.BatchSize(), DefaultBatch)
		}
	}
	for _, table := range Provenance {
		if tables[table] {
			t.Errorf("%s holds provenance or aggregates and must not be pruned", table)
		}
	}
	for _, table := range []string{"lifecycle_jobs", "webhook_deliveries", "user_sessions", "runtime_node_sessions", "scheduled_task_runs", "cache_tasks", "audit_cleanup_jobs", "replication_plans", "replication_checkpoints", "nexus_migration_entries", "email_test_deliveries", "repository_quota_alert_events", "repository_request_rollup", "hosted_repository_idempotency", "security_audit_log", "resolver_audit_log", "artifact_scan_history", "domain_events", "promotion_evaluations", "import_sessions"} {
		if !tables[table] {
			t.Errorf("registry is missing %s", table)
		}
	}
}

func TestProfileDefaults(t *testing.T) {
	standard, free := DefaultConfig(), DefaultConfig()
	free.Profile = FreeTier
	want := map[string][2]time.Duration{
		"resolver_audit_log":               {90 * day, 7 * day},
		"domain_events":                    {14 * day, 3 * day},
		"security_audit_log":               {730 * day, 90 * day},
		"artifact_scan_history":            {180 * day, 180 * day},
		"repository_request_rollup":        {400 * day, 400 * day},
		"webhook_deliveries_succeeded":     {14 * day, 3 * day},
		"webhook_deliveries_dead":          {14 * day, 3 * day},
		"lifecycle_jobs_done":              {7 * day, 1 * day},
		"lifecycle_jobs_failed":            {30 * day, 7 * day},
		"replication_plans":                {30 * day, 7 * day},
		"cache_tasks":                      {3 * day, 1 * day},
		"audit_cleanup_jobs":               {30 * day, 7 * day},
		"scheduled_task_runs":              {30 * day, 7 * day},
		"user_sessions":                    {7 * day, 1 * day},
		"runtime_node_sessions":            {7 * day, 1 * day},
		"repository_quota_alert_events":    {365 * day, 30 * day},
		"email_test_deliveries":            {7 * day, 1 * day},
		"nexus_migration_entries":          {30 * day, 7 * day},
		"hosted_repository_idempotency":    {day, day},
		"native_maven_publish_idempotency": {day, day},
	}
	for _, entry := range DefaultRegistry() {
		expected, ok := want[entry.Name()]
		if !ok {
			continue
		}
		if got, on := standard.Keep(entry); !on || got != expected[0] {
			t.Errorf("%s standard keep = %v, want %v", entry.Name(), got, expected[0])
		}
		if got, on := free.Keep(entry); !on || got != expected[1] {
			t.Errorf("%s free-tier keep = %v, want %v", entry.Name(), got, expected[1])
		}
		delete(want, entry.Name())
	}
	for name := range want {
		t.Errorf("registry has no entry %s", name)
	}
}

func TestSecurityAuditIsIndependentOfResolverAudit(t *testing.T) {
	cfg, err := LoadConfig(lookupFrom(map[string]string{"NOTARX_RETENTION_SECURITY_AUDIT_LOG": "1000d"}))
	if err != nil {
		t.Fatal(err)
	}
	security, on, err := cfg.KeepFor("security_audit_log")
	if err != nil || !on || security != 1000*day {
		t.Fatalf("security keep = %v %v %v", security, on, err)
	}
	resolver, on, _ := cfg.KeepFor("resolver_audit_log")
	if !on || resolver != 90*day {
		t.Fatalf("resolver keep = %v, the security override must not change it", resolver)
	}
	if _, _, err := cfg.KeepFor("nope"); err == nil {
		t.Fatal("KeepFor accepted an unknown table")
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	cfg, err := LoadConfig(lookupFrom(map[string]string{
		"NOTARX_RETENTION_PROFILE":               "FreeTier",
		"NOTARX_RETENTION_LIFECYCLE_JOBS":        "36h",
		"NOTARX_RETENTION_WEBHOOK_DELIVERIES":    "off",
		"NOTARX_RETENTION_USER_SESSIONS":         "",
		"NOTARX_RETENTION_LIFECYCLE_JOBS_FAILED": "10d",
		"NOTARX_RETENTION_RUN_BUDGET":            "5s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile != FreeTier || cfg.RunBudget != 5*time.Second {
		t.Fatalf("cfg = %+v", cfg)
	}
	for _, entry := range DefaultRegistry() {
		keep, on := cfg.Keep(entry)
		switch entry.Table {
		case "lifecycle_jobs":
			want := 36 * time.Hour
			if entry.Name() == "lifecycle_jobs_failed" {
				want = 10 * day // the label key wins over the table key
			}
			if !on || keep != want {
				t.Errorf("%s: keep = %v (on %v), want %v", entry.Name(), keep, on, want)
			}
		case "webhook_deliveries":
			if on {
				t.Errorf("%s: off override ignored", entry.Name())
			}
		case "user_sessions":
			if !on || keep != 1*day {
				t.Errorf("empty override must fall back to the profile, got %v", keep)
			}
		}
	}
	for name, value := range map[string]string{"NOTARX_RETENTION_PROFILE": "huge", "NOTARX_RETENTION_CACHE_TASKS": "-1h", "NOTARX_RETENTION_RUN_BUDGET": "0s"} {
		if _, err := LoadConfig(lookupFrom(map[string]string{name: value})); err == nil {
			t.Errorf("%s=%q was accepted", name, value)
		}
	}
	if _, err := LoadConfig(lookupFrom(map[string]string{"NOTARX_RETENTION_CACHE_TASKS": "banana"})); err == nil || !strings.Contains(err.Error(), "NOTARX_RETENTION_CACHE_TASKS") {
		t.Errorf("error should name the key, got %v", err)
	}
}

func TestParseDuration(t *testing.T) {
	for value, want := range map[string]time.Duration{"30d": 30 * day, "720h": 720 * time.Hour, " 1d ": day} {
		if got, err := ParseDuration(value); err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v", value, got, err)
		}
	}
	for _, value := range []string{"", "0d", "-1d", "d", "40000d", "0s", "x"} {
		if _, err := ParseDuration(value); err == nil {
			t.Errorf("ParseDuration(%q) succeeded", value)
		}
	}
}

func TestDeleteSQLBothDialects(t *testing.T) {
	entry := Entry{Table: "lifecycle_jobs", TimeColumn: "completed_at", Terminal: "state = 'failed'", KeyColumns: []string{"id"}}
	postgres := deleteSQL(entry, Postgres)
	for _, want := range []string{"DELETE FROM \"lifecycle_jobs\"", "ctid = ANY(ARRAY(SELECT ctid", "(state = 'failed') AND \"completed_at\" < $1", "ORDER BY \"completed_at\" LIMIT $2 FOR UPDATE SKIP LOCKED"} {
		if !strings.Contains(postgres, want) {
			t.Errorf("postgres SQL %q lacks %q", postgres, want)
		}
	}
	mysql := deleteSQL(Entry{Table: "t", TimeColumn: "occurred_at", Terminal: "TRUE", KeyColumns: []string{"key", "id"}}, MySQL)
	if want := "DELETE FROM `t` WHERE (TRUE) AND `occurred_at` < ? ORDER BY `key`, `id` LIMIT ?"; mysql != want {
		t.Errorf("mysql SQL = %q, want %q", mysql, want)
	}
	if got := oldestSQL(entry, Postgres); got != "SELECT min(\"completed_at\") FROM \"lifecycle_jobs\" WHERE (state = 'failed')" {
		t.Errorf("oldest SQL = %q", got)
	}
}

func TestEntryValidateRejectsUnsafeInput(t *testing.T) {
	good := Entry{Table: "t", TimeColumn: "c", Terminal: "TRUE", KeyColumns: []string{"id"}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Entry{
		{Table: "t; DROP", TimeColumn: "c", Terminal: "TRUE", KeyColumns: []string{"id"}},
		{Table: "t", TimeColumn: "C", Terminal: "TRUE", KeyColumns: []string{"id"}},
		{Table: "t", TimeColumn: "c", Terminal: "TRUE"},
		{Table: "t", TimeColumn: "c", KeyColumns: []string{"id"}},
		{Table: "t", TimeColumn: "c", Terminal: "TRUE", KeyColumns: []string{"id"}, Standard: -1},
	} {
		if bad.Validate() == nil {
			t.Errorf("Validate accepted %+v", bad)
		}
	}
	if _, err := New(nil, DefaultConfig(), Options{}); err == nil {
		t.Error("New accepted a nil database")
	}
}

func TestScanHistoryRetentionAliasAndPrecedence(t *testing.T) {
	cfg, err := LoadConfig(lookupFrom(map[string]string{"NOTARX_SCAN_HISTORY_RETENTION": "30d"}))
	if err != nil {
		t.Fatal(err)
	}
	if keep, on, err := cfg.KeepFor("artifact_scan_history"); err != nil || !on || keep != 30*day {
		t.Fatalf("alias keep = %v %v %v", keep, on, err)
	}
	cfg, err = LoadConfig(lookupFrom(map[string]string{"NOTARX_SCAN_HISTORY_RETENTION": "30d", "NOTARX_RETENTION_ARTIFACT_SCAN_HISTORY": "off"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, on, _ := cfg.KeepFor("artifact_scan_history"); on {
		t.Fatal("the generic key must win over the alias")
	}
	if _, err := LoadConfig(lookupFrom(map[string]string{"NOTARX_SCAN_HISTORY_RETENTION": "soon"})); err == nil {
		t.Fatal("an invalid alias value must be refused")
	}
}

func TestKeepOrDefaultBoundsAnOffTable(t *testing.T) {
	cfg, err := LoadConfig(lookupFrom(map[string]string{"NOTARX_EVENTS_RETENTION": "5d"}))
	if err != nil {
		t.Fatal(err)
	}
	if keep, err := cfg.KeepOrDefault("domain_events"); err != nil || keep != 5*day {
		t.Fatalf("keep = %v %v", keep, err)
	}
	if cfg, err = LoadConfig(lookupFrom(map[string]string{"NOTARX_EVENTS_RETENTION": "off"})); err != nil {
		t.Fatal(err)
	}
	if keep, err := cfg.KeepOrDefault("domain_events"); err != nil || keep != 14*day {
		t.Fatalf("an off table falls back to the standard keep time, got %v %v", keep, err)
	}
	if _, err = cfg.KeepOrDefault("nope"); err == nil {
		t.Fatal("an unknown table was accepted")
	}
}
