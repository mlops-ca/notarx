package preflight

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/jose"
)

func workloadRunner(issuers []WorkloadIssuer, listErr error, probe func(jose.Issuer) (jose.ProbeResult, error)) Runner {
	return Runner{
		WorkloadIssuers: func(context.Context, config.Config) ([]WorkloadIssuer, error) { return issuers, listErr },
		WorkloadProbe: func(_ context.Context, issuer jose.Issuer) (jose.ProbeResult, error) {
			return probe(issuer)
		},
	}
}

func TestWorkloadIssuerCheckIsListedAndSkippedWhileDisabled(t *testing.T) {
	found := false
	for _, name := range CheckNames() {
		found = found || name == "workload_issuers"
	}
	if !found {
		t.Fatal("workload_issuers is not a check")
	}
	report := workloadRunner(nil, nil, nil).Run(context.Background(), config.Config{}, []string{"workload_issuers"})
	if len(report.Results) != 1 || report.Results[0].Status != StatusSkip || !strings.Contains(report.Results[0].Summary, "NOTARX_WORKLOAD_ENABLED") {
		t.Fatalf("report = %+v", report.Results)
	}
}

func TestWorkloadIssuerCheckReportsReachability(t *testing.T) {
	cfg := config.Config{Workload: config.WorkloadConfig{Enabled: true}}
	issuers := []WorkloadIssuer{
		{Name: "github", Issuer: "https://token.actions.example.test", JWKSMode: "discovery"},
		{Name: "cluster", Issuer: "https://k8s.example.test", JWKSMode: "static", StaticJWKS: []byte("{}")},
	}
	ok := func(jose.Issuer) (jose.ProbeResult, error) {
		return jose.ProbeResult{Keys: []jose.ProbeKey{{KeyID: "k1"}}}, nil
	}
	result := workloadRunner(issuers, nil, ok).checkWorkloadIssuers(context.Background(), cfg)
	if result.Status != StatusPass || len(result.Details["issuers"].([]any)) != 2 {
		t.Fatalf("result = %+v", result)
	}

	down := func(issuer jose.Issuer) (jose.ProbeResult, error) {
		if issuer.Issuer == "https://k8s.example.test" {
			return jose.ProbeResult{}, errors.New("dial tcp 10.0.0.5:443: connect: connection refused secret-host")
		}
		return ok(issuer)
	}
	result = workloadRunner(issuers, nil, down).checkWorkloadIssuers(context.Background(), cfg)
	if result.Status != StatusFail {
		t.Fatalf("an unreachable issuer must fail: %+v", result)
	}
	if encoded := result.Summary + strings.Join(result.flatten(), " "); strings.Contains(encoded, "secret-host") || strings.Contains(encoded, "10.0.0.5") {
		t.Fatalf("the probe error leaked: %s", encoded)
	}
}

func (r Result) flatten() []string {
	var out []string
	for _, issuer := range r.Details["issuers"].([]any) {
		for key, value := range issuer.(map[string]any) {
			out = append(out, key, toString(value))
		}
	}
	return out
}

func toString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		return ""
	}
}

func TestWorkloadIssuerCheckEdgeCases(t *testing.T) {
	enabled := config.Config{Workload: config.WorkloadConfig{Enabled: true}}
	if result := workloadRunner(nil, nil, nil).checkWorkloadIssuers(context.Background(), enabled); result.Status != StatusSkip {
		t.Fatalf("no issuers: %+v", result)
	}
	if result := workloadRunner(nil, errors.New("db down"), nil).checkWorkloadIssuers(context.Background(), enabled); result.Status != StatusFail || strings.Contains(result.Summary, "db down") {
		t.Fatalf("read failure: %+v", result)
	}
	mysql := config.Config{DatabaseDriver: "mysql", Workload: config.WorkloadConfig{Enabled: true}}
	if result := workloadRunner(nil, nil, nil).checkWorkloadIssuers(context.Background(), mysql); result.Status != StatusSkip {
		t.Fatalf("mysql: %+v", result)
	}
}
