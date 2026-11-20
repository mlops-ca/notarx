package notarxctl

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/secrets/reseal"
)

// OIDC issuer commands (ADR 0020): the signing keys of notarx as a token issuer, and the audience allow-lists of the
// service accounts that may mint assertions.
const (
	issuerPath         = managementPrefix + "/issuer"
	issuerRotationPath = issuerPath + "/rotation"
)

func issuerKeyRevokePath(kid string) string {
	return issuerPath + "/keys/" + url.PathEscape(kid) + "/revoke"
}

func serviceAccountAudiencesPath(id string) string {
	return serviceAccountPath(id) + "/assertion-audiences"
}

func issuerTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func renderIssuerStatus(w io.Writer, status adminopenapi.IssuerStatus) error {
	if !status.Enabled {
		_, err := fmt.Fprintln(w, "the OIDC issuer is disabled (set NOTARX_ISSUER_ENABLED=true to enable it)")
		return err
	}
	rows := [][2]string{{"enabled", "true"}}
	add := func(label string, value *string) {
		if value != nil {
			rows = append(rows, [2]string{label, *value})
		}
	}
	add("issuer", status.IssuerUrl)
	if status.Algorithm != nil {
		rows = append(rows, [2]string{"algorithm", string(*status.Algorithm)})
	}
	add("rotation interval", status.RotationInterval)
	add("publish lead", status.PublishLead)
	add("max token lifetime", status.MaxTokenLifetime)
	if err := keyValues(w, rows); err != nil {
		return err
	}
	if len(status.Keys) == 0 {
		return emptyLine(w, "signing keys")
	}
	table := [][]string{{"KID", "ALG", "STATE", "PUBLISHED", "ACTIVATED", "RETIRED", "EXPIRES"}}
	for _, key := range status.Keys {
		table = append(table, []string{key.Kid, string(key.Alg), string(key.State), issuerTime(key.PublishedAt), issuerTime(key.ActivatedAt), issuerTime(key.RetiredAt), issuerTime(key.ExpiresAt)})
	}
	_, _ = fmt.Fprintln(w)
	return writeColumns(w, table)
}

func issuerStatusCommand() command {
	return command{
		name:     "issuer status",
		synopsis: "show the OIDC issuer and the lifecycle of its signing keys",
		usage:    "notarxctl issuer status [--json]",
		meta: commandMeta{
			Resource: "issuer", Scenarios: []string{"admin"}, Stage: "m2",
			Endpoints: []string{"GET " + issuerPath}, MinimumRole: "platform admin",
			OutputSchema: "IssuerStatus", DryRun: "unsupported", Retry: true,
		},
		run: func(ctx context.Context, env *commandContext) error {
			client, err := env.client()
			if err != nil {
				return err
			}
			var status adminopenapi.IssuerStatus
			response, err := client.sendJSON(ctx, http.MethodGet, issuerPath, requestOptions{idempotent: true}, &status)
			if err == nil && status.Keys == nil {
				err = incompleteResponse(response, client, "issuer status")
			}
			if err != nil {
				return err
			}
			return env.render(status, nil, func(w io.Writer) error { return renderIssuerStatus(w, status) })
		},
	}
}

func issuerRotateCommand() command {
	var algorithm string
	return command{
		name:     "issuer rotate",
		synopsis: "rotate the signing key of an algorithm at once (the next key becomes active)",
		usage:    "notarxctl issuer rotate --yes [--algorithm RS256|ES256] [--dry-run] [--json]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&algorithm, "algorithm", "", "RS256 or ES256 (default: the configured signing algorithm)")
		},
		meta: commandMeta{
			Resource: "issuer", Scenarios: []string{"admin"}, Stage: "m2",
			Endpoints: []string{"POST " + issuerRotationPath}, Mutating: true, MinimumRole: "platform admin",
			InputSchema: "RotateIssuerKeys", OutputSchema: "IssuerRotation", DryRun: "client-plan",
		},
		run: func(ctx context.Context, env *commandContext) error {
			if algorithm != "" && algorithm != "RS256" && algorithm != "ES256" {
				return &UsageError{Message: "--algorithm must be RS256 or ES256", Hint: "omit it to rotate the configured signing algorithm"}
			}
			if !env.globals.dryRun && !env.globals.yes {
				return &UsageError{Message: "rotating the signing key needs --yes: the next key signs from now on, and verifiers that have not fetched the key set yet reject new tokens",
					Hint: "wait for the publish lead to pass after the key was created (notarxctl issuer status), then re-run with --yes, or use --dry-run to see the request"}
			}
			body := adminopenapi.RotateIssuerKeys{}
			if algorithm != "" {
				value := adminopenapi.RotateIssuerKeysAlgorithm(algorithm)
				body.Algorithm = &value
			}
			if env.globals.dryRun {
				plan := writePlan{Mode: "client-plan", Method: http.MethodPost, Path: issuerRotationPath, Headers: map[string]string{}, Body: body,
					Verified: []string{"the algorithm is RS256 or ES256"}, NotVerified: []string{"caller permission (platform admin)", "that the issuer is enabled and has a next key"}}
				return env.render(plan, nil, func(w io.Writer) error { return renderPlan(w, plan) })
			}
			client, err := env.client()
			if err != nil {
				return err
			}
			encoded, err := encodeBody(body)
			if err != nil {
				return err
			}
			// Not retried: a repeat after a lost response would rotate a second time.
			var rotation adminopenapi.IssuerRotation
			response, err := client.sendJSON(ctx, http.MethodPost, issuerRotationPath, requestOptions{body: encoded}, &rotation)
			if err == nil && rotation.Activated == "" {
				err = incompleteResponse(response, client, "issuer rotation")
			}
			if err != nil {
				if outcomeUnknown(err) {
					return unknownOutcomeError(err, unknownOutcome{Operation: "issuer rotate", ReadBack: "notarxctl issuer status"},
						"the key may have been rotated; check notarxctl issuer status before rotating again")
				}
				return err
			}
			return env.render(rotation, nil, func(w io.Writer) error {
				if _, err := fmt.Fprintf(w, "%s key %s is active", rotation.Algorithm, rotation.Activated); err != nil {
					return err
				}
				if rotation.Retired != nil {
					if _, err := fmt.Fprintf(w, "; %s is retired", *rotation.Retired); err != nil {
						return err
					}
				}
				if _, err := fmt.Fprintln(w); err != nil {
					return err
				}
				if rotation.Warning != nil {
					_, err := fmt.Fprintf(w, "warning: %s\n", *rotation.Warning)
					return err
				}
				return nil
			})
		},
	}
}

func issuerRevokeCommand() command {
	return command{
		name:        "issuer revoke",
		synopsis:    "revoke a compromised signing key: it leaves the key set at once",
		usage:       "notarxctl issuer revoke KID --yes [--dry-run] [--json]",
		positionals: 1,
		argsUsage:   "KID",
		meta: commandMeta{
			Resource: "issuer", Scenarios: []string{"admin"}, Stage: "m2",
			Endpoints: []string{"POST " + issuerPath + "/keys/{kid}/revoke"}, Mutating: true, MinimumRole: "platform admin",
			DryRun: "client-plan", Retry: true,
		},
		run: func(ctx context.Context, env *commandContext) error {
			kid := strings.TrimSpace(env.args[0])
			if kid == "" {
				return &UsageError{Message: "the key id is empty", Hint: "notarxctl issuer status lists the key ids"}
			}
			if !env.globals.dryRun && !env.globals.yes {
				return &UsageError{Message: "revoking a signing key needs --yes: it leaves the key set at once, and assertions that Entra already exchanged stay valid until they expire",
					Hint: "re-run with --yes, or with --dry-run to see the request"}
			}
			path := issuerKeyRevokePath(kid)
			if env.globals.dryRun {
				plan := writePlan{Mode: "client-plan", Method: http.MethodPost, Path: path, Headers: map[string]string{},
					Verified: []string{"--yes is not needed for a plan"}, NotVerified: []string{"caller permission (platform admin)", "that the key exists"}}
				return env.render(plan, nil, func(w io.Writer) error { return renderPlan(w, plan) })
			}
			client, err := env.client()
			if err != nil {
				return err
			}
			// Revoking again only repeats the removal, so a lost response is retried.
			if _, err := client.sendJSON(ctx, http.MethodPost, path, requestOptions{idempotent: true}, nil); err != nil {
				return err
			}
			result := map[string]any{"revoked": true, "kid": kid}
			return env.render(result, nil, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "key %s revoked\n", kid)
				return err
			})
		},
	}
}

// issuerResealCommand re-encrypts the sealed private signing keys with the current key of the ring.
func issuerResealCommand() command {
	return command{
		name:     "issuer reseal",
		synopsis: "re-encrypt the sealed signing keys with the current encryption key",
		usage:    "notarxctl issuer reseal --yes [--dry-run] [--json]",
		meta: commandMeta{
			Resource: "local", Scenarios: []string{"admin"}, Stage: "m2",
			Endpoints: []string{}, Mutating: true, MinimumRole: "none",
			OutputSchema: "ResealResult", DryRun: "local-count",
		},
		run: func(ctx context.Context, env *commandContext) error {
			return runReseal(ctx, env, reseal.SigningKeys, "issuer reseal", "the sealed signing keys")
		},
	}
}

func renderAssertionAudiences(w io.Writer, items []adminopenapi.ServiceAccountAssertionAudience) error {
	if len(items) == 0 {
		return emptyLine(w, "assertion audiences")
	}
	table := [][]string{{"AUDIENCE", "MAX LIFETIME", "CREATED"}}
	for _, item := range items {
		table = append(table, []string{item.Audience, fmt.Sprintf("%ds", item.MaxLifetimeSeconds), item.CreatedAt.UTC().Format(time.RFC3339)})
	}
	return writeColumns(w, table)
}

func validAudienceFlag(audience string) error {
	if err := requireFlag("audience", audience, "for example --audience api://AzureADTokenExchange"); err != nil {
		return err
	}
	if audience != strings.TrimSpace(audience) || utf8.RuneCountInString(audience) > 255 {
		return &UsageError{Message: "--audience must be 1 to 255 characters without surrounding spaces", Hint: "the audience is matched byte for byte by Entra and by notarx peers"}
	}
	return nil
}

func assertionAudienceListCommand() command {
	return command{
		name:        "service-account assertion-audience list",
		synopsis:    "list the audiences a service account may mint assertions for",
		usage:       "notarxctl service-account assertion-audience list ID [--json]",
		positionals: 1,
		argsUsage:   "ID",
		meta: commandMeta{
			Resource: "service-account", Scenarios: []string{"admin"}, Stage: "m2",
			Endpoints: []string{"GET " + serviceAccountsPath + "/{serviceAccountId}/assertion-audiences"}, MinimumRole: "platform admin",
			OutputSchema: "ServiceAccountAssertionAudienceList", DryRun: "unsupported", Retry: true,
		},
		run: func(ctx context.Context, env *commandContext) error {
			client, err := env.client()
			if err != nil {
				return err
			}
			var list serverPage[adminopenapi.ServiceAccountAssertionAudience]
			response, err := client.sendJSON(ctx, http.MethodGet, serviceAccountAudiencesPath(env.args[0]), requestOptions{idempotent: true}, &list)
			if err == nil && list.Items == nil {
				err = incompleteResponse(response, client, "assertion audience list")
			}
			if err != nil {
				return err
			}
			meta := &envelopeMeta{Pagination: &paginationMeta{Mode: "none"}}
			return env.render(newListPayload(list.Items), meta, func(w io.Writer) error { return renderAssertionAudiences(w, list.Items) })
		},
	}
}

func assertionAudienceAddCommand() command {
	var audience string
	var maxLifetime time.Duration
	return command{
		name:        "service-account assertion-audience add",
		synopsis:    "allow a service account to mint assertions for an audience, or change its maximum lifetime",
		usage:       "notarxctl service-account assertion-audience add ID --audience AUDIENCE [--max-lifetime DURATION] [--dry-run] [--json]",
		positionals: 1,
		argsUsage:   "ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&audience, "audience", "", "the audience, for example api://AzureADTokenExchange for Microsoft Entra (no wildcards)")
			fs.DurationVar(&maxLifetime, "max-lifetime", 10*time.Minute, "longest assertion lifetime for this audience, from 1m to 10m")
		},
		meta: commandMeta{
			Resource: "service-account", Scenarios: []string{"admin"}, Stage: "m2",
			Endpoints: []string{"POST " + serviceAccountsPath + "/{serviceAccountId}/assertion-audiences"}, Mutating: true, MinimumRole: "platform admin",
			InputSchema: "PutServiceAccountAssertionAudience", OutputSchema: "ServiceAccountAssertionAudience", DryRun: "client-plan", Retry: true,
		},
		run: func(ctx context.Context, env *commandContext) error {
			if err := validAudienceFlag(audience); err != nil {
				return err
			}
			if maxLifetime < time.Minute || maxLifetime > 10*time.Minute || maxLifetime%time.Second != 0 {
				return &UsageError{Message: "--max-lifetime must be between 1m and 10m in whole seconds", Hint: "for example --max-lifetime 5m"}
			}
			seconds := int(maxLifetime / time.Second)
			body := adminopenapi.PutServiceAccountAssertionAudience{Audience: audience, MaxLifetimeSeconds: &seconds}
			path := serviceAccountAudiencesPath(env.args[0])
			if env.globals.dryRun {
				plan := writePlan{Mode: "client-plan", Method: http.MethodPost, Path: path, Headers: map[string]string{}, Body: body,
					Verified: []string{"the audience and lifetime are well formed"}, NotVerified: []string{"caller permission (platform admin)", "that the service account exists"}}
				return env.render(plan, nil, func(w io.Writer) error { return renderPlan(w, plan) })
			}
			client, err := env.client()
			if err != nil {
				return err
			}
			encoded, err := encodeBody(body)
			if err != nil {
				return err
			}
			// Adding the same audience again only replaces its lifetime, so a lost response is retried.
			var entry adminopenapi.ServiceAccountAssertionAudience
			response, err := client.sendJSON(ctx, http.MethodPost, path, requestOptions{body: encoded, idempotent: true}, &entry)
			if err == nil && entry.Audience != audience {
				err = incompleteResponse(response, client, "assertion audience")
			}
			if err != nil {
				return err
			}
			return env.render(entry, nil, func(w io.Writer) error {
				return renderAssertionAudiences(w, []adminopenapi.ServiceAccountAssertionAudience{entry})
			})
		},
	}
}

func assertionAudienceRemoveCommand() command {
	var audience string
	return command{
		name:        "service-account assertion-audience remove",
		synopsis:    "stop a service account from minting assertions for an audience",
		usage:       "notarxctl service-account assertion-audience remove ID --audience AUDIENCE --yes [--dry-run] [--json]",
		positionals: 1,
		argsUsage:   "ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&audience, "audience", "", "the audience to remove")
		},
		meta: commandMeta{
			Resource: "service-account", Scenarios: []string{"admin"}, Stage: "m2",
			Endpoints: []string{"DELETE " + serviceAccountsPath + "/{serviceAccountId}/assertion-audiences"}, Mutating: true, MinimumRole: "platform admin",
			DryRun: "client-plan",
		},
		run: func(ctx context.Context, env *commandContext) error {
			if err := validAudienceFlag(audience); err != nil {
				return err
			}
			if !env.globals.dryRun && !env.globals.yes {
				return &UsageError{Message: "removing an audience needs --yes: the service account can no longer mint assertions for it", Hint: "re-run with --yes, or with --dry-run to see the request"}
			}
			path := serviceAccountAudiencesPath(env.args[0])
			query := url.Values{"audience": {audience}}
			if env.globals.dryRun {
				plan := writePlan{Mode: "client-plan", Method: http.MethodDelete, Path: path + "?" + query.Encode(), Headers: map[string]string{},
					Verified: []string{"--yes is not needed for a plan"}, NotVerified: []string{"caller permission (platform admin)", "that the audience is on the list"}}
				return env.render(plan, nil, func(w io.Writer) error { return renderPlan(w, plan) })
			}
			client, err := env.client()
			if err != nil {
				return err
			}
			// Not retried: a repeat after a lost response answers 404 for the entry it removed.
			if _, err := client.sendJSON(ctx, http.MethodDelete, path, requestOptions{query: query}, nil); err != nil {
				return err
			}
			raw, _ := json.Marshal(map[string]any{"removed": true, "audience": audience})
			return env.render(json.RawMessage(raw), nil, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "audience %s removed\n", audience)
				return err
			})
		},
	}
}
