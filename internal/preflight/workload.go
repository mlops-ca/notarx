package preflight

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/jose"
)

const (
	workloadProbeTimeout = 10 * time.Second
	maxWorkloadProbes    = 100
)

// WorkloadIssuer is the part of an active workload issuer (ADR 0019) a reachability probe needs.
type WorkloadIssuer struct {
	Name                string
	Issuer              string
	JWKSMode            string
	JWKSURI             string
	StaticJWKS          []byte
	CABundlePEM         string
	AllowPrivateNetwork bool
}

// listWorkloadIssuers reads the active issuers from PostgreSQL. It never reads a secret: an issuer holds
// only public keys and certificates.
func listWorkloadIssuers(ctx context.Context, cfg config.Config) ([]WorkloadIssuer, error) {
	connection, err := pgx.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = connection.Close(ctx) }()
	rows, err := connection.Query(ctx, `SELECT name, issuer, jwks_mode, COALESCE(jwks_uri, ''), COALESCE(static_jwks::text, ''),
		COALESCE(ca_bundle_pem, ''), allow_private_network FROM workload_issuers WHERE state = 'active' ORDER BY name LIMIT $1`, maxWorkloadProbes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var issuers []WorkloadIssuer
	for rows.Next() {
		var issuer WorkloadIssuer
		var static string
		if err := rows.Scan(&issuer.Name, &issuer.Issuer, &issuer.JWKSMode, &issuer.JWKSURI, &static, &issuer.CABundlePEM, &issuer.AllowPrivateNetwork); err != nil {
			return nil, err
		}
		issuer.StaticJWKS = []byte(static)
		issuers = append(issuers, issuer)
	}
	return issuers, rows.Err()
}

// checkWorkloadIssuers probes the discovery document and key set of every active workload issuer, so a
// moved or firewalled issuer is found before the first token fails. It skips while workload federation is
// off, and on MySQL, which this probe does not read. Details name the issuers and how many keys each
// published, never an error text.
func (r Runner) checkWorkloadIssuers(ctx context.Context, cfg config.Config) Result {
	const name = "workload_issuers"
	if !cfg.Workload.Enabled {
		return Result{Name: name, Status: StatusSkip, Summary: "workload federation is not enabled (NOTARX_WORKLOAD_ENABLED)"}
	}
	if isMySQL(cfg) {
		return Result{Name: name, Status: StatusSkip, Summary: "workload issuer reachability is checked on PostgreSQL only"}
	}
	issuers, err := r.WorkloadIssuers(ctx, cfg)
	if err != nil {
		return Result{Name: name, Status: StatusFail, Summary: "workload issuers could not be read"}
	}
	if len(issuers) == 0 {
		return Result{Name: name, Status: StatusSkip, Summary: "no active workload issuers are registered"}
	}
	type outcome struct {
		Name      string `json:"name"`
		Reachable bool   `json:"reachable"`
		Keys      int    `json:"keys"`
	}
	outcomes := make([]outcome, 0, len(issuers))
	unreachable := 0
	for _, issuer := range issuers {
		probeCtx, cancel := context.WithTimeout(ctx, workloadProbeTimeout)
		target := jose.Issuer{
			Issuer: issuer.Issuer, Mode: jose.JWKSMode(issuer.JWKSMode), JWKSURI: issuer.JWKSURI, StaticJWKS: issuer.StaticJWKS,
			CABundlePEM: issuer.CABundlePEM, AllowPrivateNetwork: issuer.AllowPrivateNetwork || cfg.Workload.AllowPrivateNetwork,
		}
		result, probeErr := r.WorkloadProbe(probeCtx, target)
		cancel()
		item := outcome{Name: issuer.Name, Reachable: probeErr == nil, Keys: len(result.Keys)}
		if probeErr != nil {
			unreachable++
		}
		outcomes = append(outcomes, item)
	}
	encoded, _ := json.Marshal(outcomes)
	var details []any
	_ = json.Unmarshal(encoded, &details)
	summary := Result{Name: name, Status: StatusPass, Summary: "every active workload issuer is reachable", Details: map[string]any{"issuers": details}}
	if unreachable > 0 {
		summary.Status, summary.Summary = StatusFail, "some active workload issuers are unreachable or publish no usable keys"
	}
	return summary
}
