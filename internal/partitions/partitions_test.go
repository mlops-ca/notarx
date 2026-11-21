package partitions

import (
	"testing"
	"time"
)

func TestDefaultRegistryMatchesADR(t *testing.T) {
	registry := DefaultRegistry()
	if len(registry) != 4 {
		t.Fatalf("registry = %+v", registry)
	}
	type expectation struct {
		column    string
		retention time.Duration
	}
	want := map[string]expectation{
		"resolver_audit_log":    {"occurred_at", 90 * 24 * time.Hour},
		"security_audit_log":    {"occurred_at", 730 * 24 * time.Hour},
		"artifact_scan_history": {"scanned_at", 180 * 24 * time.Hour},
		"domain_events":         {"occurred_at", 14 * 24 * time.Hour},
	}
	for _, spec := range registry {
		if err := spec.Validate(); err != nil {
			t.Fatal(err)
		}
		if spec.Granularity != Month || spec.Column != want[spec.Table].column || spec.Retention != want[spec.Table].retention {
			t.Errorf("unexpected spec %+v", spec)
		}
	}
}

func TestSpecValidate(t *testing.T) {
	good := Spec{Table: "t_1", Column: "c", Granularity: Day}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Spec{
		{Table: "T", Column: "c", Granularity: Month},
		{Table: "t;drop", Column: "c", Granularity: Month},
		{Table: "t", Column: "", Granularity: Month},
		{Table: "t", Column: "c", Granularity: "year"},
		{Table: "t", Column: "c", Granularity: Month, Retention: -time.Hour},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v should be rejected", bad)
		}
	}
}

func TestPeriodsAndNames(t *testing.T) {
	now := time.Date(2026, 11, 18, 8, 30, 0, 0, time.FixedZone("x", 5*3600))
	month := Spec{Table: "audit", Column: "at", Granularity: Month}
	periods := month.Periods(now, 3)
	names := []string{}
	for _, p := range periods {
		names = append(names, month.Name(p))
	}
	want := []string{"audit_p202611", "audit_p202612", "audit_p202701", "audit_p202702"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
	day := Spec{Table: "audit", Column: "at", Granularity: Day}
	dayPeriods := day.Periods(time.Date(2026, 12, 30, 23, 0, 0, 0, time.UTC), 2)
	if day.Name(dayPeriods[0]) != "audit_p20261230" || day.Name(dayPeriods[2]) != "audit_p20270101" {
		t.Fatalf("day names = %s %s", day.Name(dayPeriods[0]), day.Name(dayPeriods[2]))
	}
	if month.DefaultName() != "audit_default" {
		t.Fatal(month.DefaultName())
	}
	if got := boundLiteral(periods[1]); got != "'2026-12-01 00:00:00+00'" {
		t.Fatal(got)
	}
}

func TestParseBound(t *testing.T) {
	from, to, ok := parseBound("FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00')")
	if !ok || !from.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) || !to.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("bound = %v %v %v", from, to, ok)
	}
	if _, _, ok = parseBound("DEFAULT"); ok {
		t.Fatal("DEFAULT must not parse")
	}
	if _, _, ok = parseBound("FOR VALUES FROM ('nonsense') TO ('2026-12-01 00:00:00+00')"); ok {
		t.Fatal("garbage must not parse")
	}
	if _, _, ok = parseBound("FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('nonsense')"); ok {
		t.Fatal("garbage upper bound must not parse")
	}
	if _, _, ok = parseBound("FOR VALUES FROM ('2026-11-01 00:00:00.5+00') TO ('2026-12-01 00:00:00+00')"); !ok {
		t.Fatal("fractional seconds must parse")
	}
}
