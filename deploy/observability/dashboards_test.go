// Package observability validates the dashboards shipped in deploy/observability.
package observability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// exporterMetrics are the postgres_exporter series the dashboards may use: the
// stat_statements collector and the pg_stat_user_tables collector.
var exporterMetrics = map[string]bool{
	"pg_stat_statements_calls_total":   true,
	"pg_stat_statements_seconds_total": true,
	"pg_stat_statements_rows_total":    true,
	"pg_stat_user_tables_n_dead_tup":   true,
	"pg_stat_user_tables_n_live_tup":   true,
}

var (
	binaryMetric = regexp.MustCompile(`notarx_[a-z0-9_]+`)
	exprMetric   = regexp.MustCompile(`\b(?:notarx|pg)_[a-z0-9_]+`)
	histogramEnd = regexp.MustCompile(`_(bucket|sum|count)$`)
)

type dashboard struct {
	UID           string  `json:"uid"`
	Title         string  `json:"title"`
	SchemaVersion int     `json:"schemaVersion"`
	Panels        []panel `json:"panels"`
}

type panel struct {
	ID      int    `json:"id"`
	Type    string `json:"type"`
	Title   string `json:"title"`
	GridPos *struct {
		W, H, X, Y int
	} `json:"gridPos"`
	Targets []struct {
		Expr string `json:"expr"`
	} `json:"targets"`
}

// binaryMetrics returns the metric names that appear as string literals in the notarx source.
func binaryMetrics(t *testing.T) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	err := filepath.WalkDir("../../internal", func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, name := range binaryMetric.FindAllString(string(data), -1) {
			names[histogramEnd.ReplaceAllString(name, "")] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestDashboardsAreValidAndUseKnownMetrics(t *testing.T) {
	files, err := filepath.Glob("grafana/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no dashboards found: %v", err)
	}
	known := binaryMetrics(t)
	for _, name := range []string{"notarx_partitions", "notarx_partition_default_rows", "notarx_retention_lag_seconds", "notarx_retention_deleted_rows_total", "notarx_audit_flush_seconds"} {
		if !known[name] {
			t.Fatalf("source scan did not find %s; the scan is broken", name)
		}
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var d dashboard
			if err := json.Unmarshal(data, &d); err != nil {
				t.Fatalf("not valid JSON: %v", err)
			}
			if d.UID == "" || d.Title == "" || d.SchemaVersion == 0 || len(d.Panels) == 0 {
				t.Fatalf("dashboard needs uid, title, schemaVersion and panels: %+v", d)
			}
			ids := map[int]bool{}
			queries := 0
			for _, p := range d.Panels {
				if p.ID == 0 || ids[p.ID] || p.Title == "" || p.GridPos == nil {
					t.Errorf("panel %q: needs a unique non-zero id, a title and a gridPos", p.Title)
				}
				ids[p.ID] = true
				if p.Type == "row" {
					continue
				}
				if len(p.Targets) == 0 {
					t.Errorf("panel %q has no query", p.Title)
				}
				for _, target := range p.Targets {
					queries++
					metrics := exprMetric.FindAllString(target.Expr, -1)
					if len(metrics) == 0 {
						t.Errorf("panel %q: query %q names no metric", p.Title, target.Expr)
					}
					for _, metric := range metrics {
						base := histogramEnd.ReplaceAllString(metric, "")
						if !known[base] && !exporterMetrics[metric] {
							t.Errorf("panel %q uses %s, which neither notarx nor postgres_exporter (as listed in this test) exposes", p.Title, metric)
						}
					}
				}
			}
			if queries == 0 {
				t.Error("dashboard has no queries")
			}
		})
	}
}

func TestDashboardsCoverTheRequiredPanels(t *testing.T) {
	data, err := os.ReadFile("grafana/notarx-database.json")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"pg_stat_statements_seconds_total", "pg_stat_statements_calls_total", "pg_stat_statements_rows_total", "pg_stat_user_tables_n_dead_tup", "notarx_partitions", "notarx_retention_lag_seconds"} {
		if !strings.Contains(text, want) {
			t.Errorf("dashboard has no panel on %s", want)
		}
	}
}
