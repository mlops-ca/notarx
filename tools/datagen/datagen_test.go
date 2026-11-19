package datagen

import (
	"context"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type fakeCopier struct {
	rows map[string][][]any
	fail string
}

func (f *fakeCopier) CopyFrom(_ context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error) {
	name := table[0]
	if name == f.fail {
		return 0, errors.New("boom")
	}
	if f.rows == nil {
		f.rows = map[string][][]any{}
	}
	for src.Next() {
		values, err := src.Values()
		if err != nil {
			return 0, err
		}
		if len(values) != len(columns) {
			return 0, errors.New("column count mismatch for " + name)
		}
		f.rows[name] = append(f.rows[name], values)
	}
	return int64(len(f.rows[name])), src.Err()
}

func TestParseProfile(t *testing.T) {
	for _, name := range []string{"tiny", "freetier", "target"} {
		p, err := ParseProfile(name)
		if err != nil || p.Name != name {
			t.Fatalf("ParseProfile(%q) = %v, %v", name, p, err)
		}
	}
	if _, err := ParseProfile("huge"); err == nil {
		t.Fatal("unknown profile accepted")
	}
	if Profiles["tiny"].Versions != 100_000 || Profiles["tiny"].AuditRows != 1_000_000 ||
		Profiles["freetier"].Versions != 1_000_000 || Profiles["target"].AuditRows != 100_000_000 {
		t.Fatal("profile sizes drifted from ADR 0023 section 11")
	}
}

func TestScaled(t *testing.T) {
	p := Profiles["tiny"]
	if got := p.Scaled(1); got != p {
		t.Fatal("Scaled(1) changed the profile")
	}
	got := p.Scaled(100)
	if got.Versions != 1000 || got.AuditRows != 10_000 || got.Repositories < 1 || got.Repositories > p.Repositories {
		t.Fatalf("Scaled(100) = %+v", got)
	}
}

func TestPopularityIsSkewed(t *testing.T) {
	pop := NewPopularity(rand.New(rand.NewSource(7)), 1000)
	hot, total := 0, 20000
	for range total {
		rank := pop.Next()
		if rank < 0 || rank >= 1000 {
			t.Fatalf("rank %d out of range", rank)
		}
		if rank < 10 {
			hot++
		}
	}
	if hot < total/4 {
		t.Fatalf("top 1%% of ranks drew only %d of %d samples", hot, total)
	}
	if NewPopularity(rand.New(rand.NewSource(1)), 0).Next() > 1 {
		t.Fatal("degenerate sampler out of range")
	}
}

func TestSeedWritesConsistentRows(t *testing.T) {
	p := Profile{Name: "unit", Versions: 500, AuditRows: 300, Repositories: 5, AuditWindow: time.Hour}
	copier := &fakeCopier{}
	counts, err := Seed(context.Background(), copier, p, 3, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if counts.Repositories != 5 || counts.Versions != 500 || counts.AuditRows != 300 || counts.Packages < 500/(2*averageVersionsPerPackage) {
		t.Fatalf("counts = %+v", counts)
	}
	seen := map[string]bool{}
	for _, row := range copier.rows["native_npm_versions"] {
		id := row[1].(string) + "@" + row[2].(string)
		if seen[id] {
			t.Fatalf("duplicate version %s", id)
		}
		seen[id] = true
		if size := row[6].(int64); size <= 0 {
			t.Fatalf("size %d violates the cached-object check", size)
		}
	}
	if again, err := Seed(context.Background(), &fakeCopier{}, p, 3, time.Unix(1_700_000_000, 0)); err != nil || again != counts {
		t.Fatalf("seed is not deterministic: %+v vs %+v (%v)", again, counts, err)
	}
}

func TestSeedErrors(t *testing.T) {
	good := Profile{Name: "unit", Versions: 10, AuditRows: 10, Repositories: 2, AuditWindow: time.Hour}
	for _, table := range []string{"hosted_repositories", "native_npm_packages", "native_npm_versions", "resolver_audit_log"} {
		if _, err := Seed(context.Background(), &fakeCopier{fail: table}, good, 1, time.Now()); err == nil {
			t.Errorf("failure of %s not reported", table)
		}
	}
	if _, err := Seed(context.Background(), &fakeCopier{}, Profile{Name: "bad"}, 1, time.Now()); err == nil {
		t.Error("empty profile accepted")
	}
}
