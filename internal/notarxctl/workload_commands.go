package notarxctl

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// Workload identity federation commands (ADR 0019): trusted issuers, trust policies and the dry-run
// policy tester on the /workload-identity management API.
const (
	workloadIssuersPath  = managementPrefix + "/workload-identity/issuers"
	workloadPoliciesPath = managementPrefix + "/workload-identity/policies"
	workloadFileLimit    = 1 << 20
)

func workloadIssuerPath(id string) string { return workloadIssuersPath + "/" + url.PathEscape(id) }

func workloadPolicyPath(id string) string { return workloadPoliciesPath + "/" + url.PathEscape(id) }

// readWorkloadFile reads a bounded input file; it fails before any request.
func readWorkloadFile(flagName, path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, &UsageError{Message: fmt.Sprintf("--%s must name a readable file", flagName), Hint: "pass the path of an existing regular file"}
	}
	if info.Size() > workloadFileLimit {
		return nil, &UsageError{Message: fmt.Sprintf("--%s exceeds %d bytes", flagName, workloadFileLimit), Hint: "pass a smaller file"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &UsageError{Message: fmt.Sprintf("cannot read --%s", flagName), Hint: "check the file permissions"}
	}
	return data, nil
}

func readJSONObjectFile(flagName, path string) (map[string]interface{}, error) {
	data, err := readWorkloadFile(flagName, path)
	if err != nil {
		return nil, err
	}
	var object map[string]interface{}
	if json.Unmarshal(data, &object) != nil || object == nil {
		return nil, &UsageError{Message: fmt.Sprintf("--%s must hold a JSON object", flagName), Hint: "for a key set: kubectl get --raw /openid/v1/jwks > jwks.json"}
	}
	return object, nil
}

func algorithmList(values stringList) *[]adminopenapi.WorkloadAlgorithm {
	if len(values) == 0 {
		return nil
	}
	algs := make([]adminopenapi.WorkloadAlgorithm, 0, len(values))
	for _, value := range values {
		algs = append(algs, adminopenapi.WorkloadAlgorithm(value))
	}
	return &algs
}

func renderWorkloadIssuers(w io.Writer, issuers []adminopenapi.WorkloadIssuer) error {
	if len(issuers) == 0 {
		return emptyLine(w, "workload issuers")
	}
	table := [][]string{{"NAME", "KIND", "STATE", "ISSUER", "VERSION", "ID"}}
	for _, issuer := range issuers {
		table = append(table, []string{issuer.Name, string(issuer.Kind), string(issuer.State), issuer.Issuer, issuer.Version, issuer.Id.String()})
	}
	return writeColumns(w, table)
}

func renderWorkloadIssuer(w io.Writer, issuer adminopenapi.WorkloadIssuer) error {
	return writeColumns(w, [][]string{
		{"name", issuer.Name}, {"id", issuer.Id.String()}, {"kind", string(issuer.Kind)}, {"state", string(issuer.State)},
		{"issuer", issuer.Issuer}, {"audiences", strings.Join(issuer.Audiences, ",")}, {"jwks mode", string(issuer.JwksMode)},
		{"version", issuer.Version},
	})
}

func conditionClaim(condition adminopenapi.WorkloadCondition) string {
	if claim, err := condition.Claim.AsWorkloadConditionClaim1(); err == nil {
		return strings.Join(claim, ".")
	}
	if claim, err := condition.Claim.AsWorkloadConditionClaim0(); err == nil {
		return claim
	}
	return "?"
}

func renderWorkloadPolicies(w io.Writer, policies []adminopenapi.WorkloadTrustPolicy) error {
	if len(policies) == 0 {
		return emptyLine(w, "workload trust policies")
	}
	table := [][]string{{"NAME", "STATE", "SINGLE USE", "TTL", "SERVICE ACCOUNT", "VERSION", "ID"}}
	for _, policy := range policies {
		table = append(table, []string{policy.Name, string(policy.State), fmt.Sprint(policy.SingleUse), fmt.Sprintf("%ds", policy.MaxTokenTtlSeconds), "service-account:" + policy.ServiceAccountId.String(), policy.Version, policy.Id.String()})
	}
	return writeColumns(w, table)
}

func renderWorkloadPolicy(w io.Writer, policy adminopenapi.WorkloadTrustPolicy) error {
	rows := [][]string{
		{"name", policy.Name}, {"id", policy.Id.String()}, {"issuer", policy.IssuerId.String()},
		{"service account", "service-account:" + policy.ServiceAccountId.String()}, {"state", string(policy.State)},
		{"single use", fmt.Sprint(policy.SingleUse)}, {"max token ttl", fmt.Sprintf("%ds", policy.MaxTokenTtlSeconds)}, {"version", policy.Version},
	}
	for index, condition := range policy.Conditions {
		rows = append(rows, []string{fmt.Sprintf("condition %d", index), fmt.Sprintf("%s %s %s", conditionClaim(condition), condition.Op, strings.Join(condition.Values, ","))})
	}
	return writeColumns(w, rows)
}

func completeWorkloadIssuer(issuer adminopenapi.WorkloadIssuer) bool {
	return issuer.Id != (openapi_types.UUID{}) && issuer.Name != "" && issuer.Version != ""
}

func completeWorkloadPolicy(policy adminopenapi.WorkloadTrustPolicy) bool {
	return policy.Id != (openapi_types.UUID{}) && policy.Name != "" && policy.Version != ""
}

// workloadWrite sends one write. Creates are not retried (no idempotency key); conditional updates and
// deletes carry If-Match and are not retried either.
func workloadWrite[T any](ctx context.Context, env *commandContext, op string, method, path string, body any, header http.Header, complete func(T) bool, name, readBack string) (T, error) {
	var out T
	client, err := env.client()
	if err != nil {
		return out, err
	}
	var encoded []byte
	if body != nil {
		if encoded, err = encodeBody(body); err != nil {
			return out, err
		}
	}
	response, err := client.sendJSON(ctx, method, path, requestOptions{body: encoded, header: header}, &out)
	if err == nil && complete != nil && !complete(out) {
		err = incompleteResponse(response, client, op)
	}
	if err != nil {
		var api *APIError
		if errors.As(err, &api) && api.HTTPStatus == http.StatusPreconditionFailed {
			api.Hint = "the resource changed since that version and was not changed; read it again and retry"
		}
		if outcomeUnknown(err) {
			return out, unknownOutcomeError(err, unknownOutcome{Operation: op, Name: name, ReadBack: readBack},
				fmt.Sprintf("the change may have been applied; read it back with %s before repeating it", readBack))
		}
		return out, err
	}
	return out, nil
}

func planWorkloadWrite(env *commandContext, method, path string, body any, header map[string]string, verified []string) error {
	if header == nil {
		header = map[string]string{}
	}
	plan := writePlan{Mode: "client-plan", Method: method, Path: path, Headers: header, Body: body, Verified: verified,
		NotVerified: []string{"caller permission (platform admin)", "the issuer, policy and service account rules, which the server validates"}}
	return env.render(plan, nil, func(w io.Writer) error { return renderPlan(w, plan) })
}

// workloadVersion returns --if-match, or reads the current version of the resource.
func workloadVersion(ctx context.Context, env *commandContext, ifMatch, path string) (string, error) {
	if env.flagSet["if-match"] {
		if strings.TrimSpace(ifMatch) == "" {
			return "", &UsageError{Message: "--if-match cannot be empty", Hint: "pass the version from the show command"}
		}
		return ifMatch, nil
	}
	client, err := env.client()
	if err != nil {
		return "", err
	}
	var current struct {
		Version string `json:"version"`
	}
	if err := client.getJSON(ctx, path, nil, &current); err != nil {
		return "", err
	}
	if current.Version == "" {
		return "", &APIError{Code: "invalid_response", Message: "the server returned no version"}
	}
	return current.Version, nil
}

func workloadIssuerListCommand() command {
	list := &cursorList[adminopenapi.WorkloadIssuer]{path: workloadIssuersPath, command: "notarxctl workload issuer list", noun: "issuers", render: renderWorkloadIssuers}
	return command{
		name: "workload issuer list", synopsis: "list trusted workload issuers",
		usage:    "notarxctl workload issuer list [--page-size N] [--cursor TOKEN | --all [--max-pages N]] [--json]",
		setFlags: list.setFlags,
		meta: commandMeta{
			Resource: "workload-issuer", Scenarios: []string{"ci", "admin"}, Stage: "m2",
			Endpoints: []string{"GET " + workloadIssuersPath}, MinimumRole: "platform admin",
			OutputSchema: "WorkloadIssuerList", Pagination: true, DryRun: "unsupported", Retry: true,
		},
		run: list.run,
	}
}

func workloadIssuerShowCommand() command {
	return command{
		name: "workload issuer show", synopsis: "show one workload issuer",
		usage: "notarxctl workload issuer show ID [--json]", positionals: 1, argsUsage: "ID",
		meta: commandMeta{
			Resource: "workload-issuer", Scenarios: []string{"ci", "admin"}, Stage: "m2",
			Endpoints: []string{"GET " + workloadIssuersPath + "/{issuerId}"}, MinimumRole: "platform admin",
			OutputSchema: "WorkloadIssuer", DryRun: "unsupported", Retry: true,
		},
		run: func(ctx context.Context, env *commandContext) error {
			client, err := env.client()
			if err != nil {
				return err
			}
			var issuer adminopenapi.WorkloadIssuer
			if err := client.getJSON(ctx, workloadIssuerPath(env.args[0]), nil, &issuer); err != nil {
				return err
			}
			if !completeWorkloadIssuer(issuer) {
				return &APIError{Code: "invalid_response", Message: "the server returned an incomplete workload issuer"}
			}
			return env.render(issuer, nil, func(w io.Writer) error { return renderWorkloadIssuer(w, issuer) })
		},
	}
}

type workloadIssuerFlags struct {
	kind, issuer, mode, jwksURI, staticFile, caFile, state string
	audiences, algs                                        stringList
	allowPrivate                                           bool
}

func (f *workloadIssuerFlags) setFlags(fs *flag.FlagSet, creating bool) {
	if creating {
		fs.StringVar(&f.kind, "kind", "", "github_actions, gitlab_ci, kubernetes or generic")
		fs.StringVar(&f.issuer, "issuer", "", "the exact iss value of the tokens (an https URL)")
	}
	fs.Var(&f.audiences, "audience", "accepted audience; repeat for several (a token must carry one)")
	fs.Var(&f.algs, "alg", "allowed signature algorithm RS256, ES256 or EdDSA; repeat for several (default all three)")
	fs.StringVar(&f.mode, "jwks-mode", "", "discovery (fetch the issuer's keys) or static (--static-jwks-file)")
	fs.StringVar(&f.jwksURI, "jwks-uri", "", "key set URL; empty uses the discovery document")
	fs.StringVar(&f.staticFile, "static-jwks-file", "", "file with the JWKS of a static issuer, for example from kubectl get --raw /openid/v1/jwks")
	fs.StringVar(&f.caFile, "ca-bundle-file", "", "PEM file of the CA certificates that sign the issuer's endpoints")
	fs.BoolVar(&f.allowPrivate, "allow-private-network", false, "let key fetches reach private addresses (self-hosted issuers only)")
	fs.StringVar(&f.state, "state", "", "active or disabled")
}

func (f *workloadIssuerFlags) validate(env *commandContext) error {
	if f.mode != "" && f.mode != "discovery" && f.mode != "static" {
		return &UsageError{Message: "--jwks-mode must be discovery or static", Hint: "use static with --static-jwks-file for a private API server"}
	}
	if f.state != "" && f.state != "active" && f.state != "disabled" {
		return &UsageError{Message: "--state must be active or disabled", Hint: "disabling stops the issuer's tokens from authenticating"}
	}
	for _, alg := range f.algs {
		if alg != "RS256" && alg != "ES256" && alg != "EdDSA" {
			return &UsageError{Message: fmt.Sprintf("--alg %q is not allowed", alg), Hint: "use RS256, ES256 or EdDSA"}
		}
	}
	return nil
}

func workloadIssuerCreateCommand() command {
	var f workloadIssuerFlags
	return command{
		name: "workload issuer create", synopsis: "register a trusted workload issuer",
		usage:       "notarxctl workload issuer create NAME --kind KIND --issuer URL --audience AUD [--audience AUD]... [--jwks-mode discovery|static] [--jwks-uri URL] [--static-jwks-file FILE] [--ca-bundle-file FILE] [--allow-private-network] [--dry-run] [--json]",
		positionals: 1, argsUsage: "NAME",
		setFlags: func(fs *flag.FlagSet) { f.setFlags(fs, true) },
		meta: commandMeta{
			Resource: "workload-issuer", Scenarios: []string{"ci", "admin"}, Stage: "m2",
			Endpoints: []string{"POST " + workloadIssuersPath}, Mutating: true, MinimumRole: "platform admin",
			InputSchema: "CreateWorkloadIssuer", OutputSchema: "WorkloadIssuer", DryRun: "client-plan",
		},
		run: func(ctx context.Context, env *commandContext) error {
			if err := f.validate(env); err != nil {
				return err
			}
			if err := requireFlag("kind", f.kind, "for example --kind github_actions"); err != nil {
				return err
			}
			if err := requireFlag("issuer", f.issuer, "for example --issuer https://token.actions.githubusercontent.com"); err != nil {
				return err
			}
			if len(f.audiences) == 0 {
				return &UsageError{Message: "missing required flag --audience", Hint: "for example --audience https://notarx.example.com"}
			}
			mode := f.mode
			if mode == "" {
				mode = "discovery"
			}
			body := adminopenapi.CreateWorkloadIssuer{
				Name: strings.TrimSpace(env.args[0]), Kind: adminopenapi.WorkloadIssuerKind(f.kind), Issuer: strings.TrimSpace(f.issuer),
				Audiences: f.audiences, AllowedAlgs: algorithmList(f.algs), JwksMode: adminopenapi.WorkloadJWKSMode(mode), AllowPrivateNetwork: &f.allowPrivate,
			}
			if f.jwksURI != "" {
				body.JwksUri = &f.jwksURI
			}
			if f.state != "" {
				state := adminopenapi.WorkloadState(f.state)
				body.State = &state
			}
			if f.staticFile != "" {
				jwks, err := readJSONObjectFile("static-jwks-file", f.staticFile)
				if err != nil {
					return err
				}
				body.StaticJwks = &jwks
			}
			if f.caFile != "" {
				pem, err := readWorkloadFile("ca-bundle-file", f.caFile)
				if err != nil {
					return err
				}
				text := string(pem)
				body.CaBundlePem = &text
			}
			if env.globals.dryRun {
				return planWorkloadWrite(env, http.MethodPost, workloadIssuersPath, body, nil, []string{"flags and input files are well formed"})
			}
			issuer, err := workloadWrite(ctx, env, "workload issuer create", http.MethodPost, workloadIssuersPath, body, nil, completeWorkloadIssuer, body.Name, "notarxctl workload issuer list --all")
			if err != nil {
				return err
			}
			return env.render(issuer, nil, func(w io.Writer) error { return renderWorkloadIssuer(w, issuer) })
		},
	}
}

func workloadIssuerUpdateCommand() command {
	var f workloadIssuerFlags
	var ifMatch string
	return command{
		name: "workload issuer update", synopsis: "change a workload issuer (conditional on its version)",
		usage:       "notarxctl workload issuer update ID [--audience AUD]... [--alg ALG]... [--jwks-mode M] [--jwks-uri URL] [--static-jwks-file FILE] [--ca-bundle-file FILE] [--allow-private-network] [--state active|disabled] [--if-match VERSION] [--dry-run] [--json]",
		positionals: 1, argsUsage: "ID",
		setFlags: func(fs *flag.FlagSet) {
			f.setFlags(fs, false)
			fs.StringVar(&ifMatch, "if-match", "", "apply only if the issuer is still at this version (default: the version read just before)")
		},
		meta: commandMeta{
			Resource: "workload-issuer", Scenarios: []string{"ci", "admin"}, Stage: "m2",
			Endpoints: []string{"PATCH " + workloadIssuersPath + "/{issuerId}", "GET " + workloadIssuersPath + "/{issuerId}"}, Mutating: true, MinimumRole: "platform admin",
			InputSchema: "UpdateWorkloadIssuer", OutputSchema: "WorkloadIssuer", DryRun: "client-plan",
		},
		run: func(ctx context.Context, env *commandContext) error {
			if err := f.validate(env); err != nil {
				return err
			}
			body := adminopenapi.UpdateWorkloadIssuer{AllowedAlgs: algorithmList(f.algs)}
			if len(f.audiences) > 0 {
				audiences := []string(f.audiences)
				body.Audiences = &audiences
			}
			if f.mode != "" {
				mode := adminopenapi.WorkloadJWKSMode(f.mode)
				body.JwksMode = &mode
			}
			if env.flagSet["jwks-uri"] {
				body.JwksUri = &f.jwksURI
			}
			if f.state != "" {
				state := adminopenapi.WorkloadState(f.state)
				body.State = &state
			}
			if env.flagSet["allow-private-network"] {
				body.AllowPrivateNetwork = &f.allowPrivate
			}
			if f.staticFile != "" {
				jwks, err := readJSONObjectFile("static-jwks-file", f.staticFile)
				if err != nil {
					return err
				}
				body.StaticJwks = &jwks
			}
			if f.caFile != "" {
				pem, err := readWorkloadFile("ca-bundle-file", f.caFile)
				if err != nil {
					return err
				}
				text := string(pem)
				body.CaBundlePem = &text
			}
			if encoded, _ := json.Marshal(body); string(encoded) == "{}" {
				return &UsageError{Message: "nothing to update", Hint: "pass at least one of --audience, --alg, --jwks-mode, --jwks-uri, --static-jwks-file, --ca-bundle-file, --allow-private-network, --state"}
			}
			path := workloadIssuerPath(env.args[0])
			version, err := workloadVersion(ctx, env, ifMatch, path)
			if err != nil {
				return err
			}
			if env.globals.dryRun {
				return planWorkloadWrite(env, http.MethodPatch, path, body, map[string]string{"If-Match": version}, []string{"at least one change is given"})
			}
			issuer, err := workloadWrite(ctx, env, "workload issuer update", http.MethodPatch, path, body, http.Header{"If-Match": {version}}, completeWorkloadIssuer, env.args[0], "notarxctl workload issuer show "+env.args[0])
			if err != nil {
				return err
			}
			return env.render(issuer, nil, func(w io.Writer) error { return renderWorkloadIssuer(w, issuer) })
		},
	}
}

func workloadIssuerDeleteCommand() command {
	var ifMatch string
	return command{
		name: "workload issuer delete", synopsis: "delete a workload issuer that no policy references",
		usage:       "notarxctl workload issuer delete ID --yes [--if-match VERSION] [--dry-run] [--json]",
		positionals: 1, argsUsage: "ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&ifMatch, "if-match", "", "delete only if the issuer is still at this version (default: the version read just before)")
		},
		meta: commandMeta{
			Resource: "workload-issuer", Scenarios: []string{"admin"}, Stage: "m2",
			Endpoints: []string{"DELETE " + workloadIssuersPath + "/{issuerId}", "GET " + workloadIssuersPath + "/{issuerId}"}, Mutating: true, MinimumRole: "platform admin", DryRun: "client-plan",
		},
		run: func(ctx context.Context, env *commandContext) error {
			return deleteWorkloadResource(ctx, env, "issuer", workloadIssuerPath(env.args[0]), ifMatch)
		},
	}
}

func deleteWorkloadResource(ctx context.Context, env *commandContext, kind, path, ifMatch string) error {
	if !env.globals.dryRun && !env.globals.yes {
		return &UsageError{Message: fmt.Sprintf("deleting this %s needs --yes", kind), Hint: "re-run with --yes, or with --dry-run to see the request"}
	}
	version, err := workloadVersion(ctx, env, ifMatch, path)
	if err != nil {
		return err
	}
	if env.globals.dryRun {
		return planWorkloadWrite(env, http.MethodDelete, path, nil, map[string]string{"If-Match": version}, []string{"--yes is not needed for a plan"})
	}
	client, err := env.client()
	if err != nil {
		return err
	}
	if _, err := client.sendJSON(ctx, http.MethodDelete, path, requestOptions{header: http.Header{"If-Match": {version}}}, nil); err != nil {
		var api *APIError
		if errors.As(err, &api) && api.HTTPStatus == http.StatusPreconditionFailed {
			api.Hint = "the " + kind + " changed since that version and was not deleted; read it again and retry"
		}
		return err
	}
	result := map[string]any{"deleted": true, "kind": kind, "version": version}
	return env.render(result, nil, func(w io.Writer) error {
		_, err := fmt.Fprintf(w, "%s deleted\n", kind)
		return err
	})
}

func workloadIssuerProbeCommand() command {
	var id, issuer, mode, jwksURI, staticFile, caFile string
	var allowPrivate bool
	return command{
		name: "workload issuer probe", synopsis: "fetch an issuer's discovery document and keys and report the key ids and algorithms",
		usage: "notarxctl workload issuer probe (--id ID | --issuer URL --jwks-mode discovery|static [--jwks-uri URL] [--static-jwks-file FILE] [--ca-bundle-file FILE] [--allow-private-network]) [--json]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&id, "id", "", "ID of a registered issuer")
			fs.StringVar(&issuer, "issuer", "", "issuer URL of an unsaved configuration")
			fs.StringVar(&mode, "jwks-mode", "", "discovery or static")
			fs.StringVar(&jwksURI, "jwks-uri", "", "key set URL")
			fs.StringVar(&staticFile, "static-jwks-file", "", "JWKS file for a static issuer")
			fs.StringVar(&caFile, "ca-bundle-file", "", "PEM file of the CA certificates")
			fs.BoolVar(&allowPrivate, "allow-private-network", false, "let the fetch reach private addresses")
		},
		meta: commandMeta{
			Resource: "workload-issuer", Scenarios: []string{"ci", "admin"}, Stage: "m2",
			Endpoints: []string{"POST " + workloadIssuersPath + ":probe"}, MinimumRole: "platform admin",
			InputSchema: "ProbeWorkloadIssuerRequest", OutputSchema: "WorkloadIssuerProbe", DryRun: "unsupported", Retry: true,
		},
		run: func(ctx context.Context, env *commandContext) error {
			var body adminopenapi.ProbeWorkloadIssuerRequest
			switch {
			case id != "":
				parsed, err := parseWorkloadUUID("id", id)
				if err != nil {
					return err
				}
				body.IssuerId = &parsed
			case issuer != "" && mode != "":
				kind := adminopenapi.WorkloadJWKSMode(mode)
				body.Issuer, body.JwksMode, body.AllowPrivateNetwork = &issuer, &kind, &allowPrivate
				if jwksURI != "" {
					body.JwksUri = &jwksURI
				}
				if staticFile != "" {
					jwks, err := readJSONObjectFile("static-jwks-file", staticFile)
					if err != nil {
						return err
					}
					body.StaticJwks = &jwks
				}
				if caFile != "" {
					pem, err := readWorkloadFile("ca-bundle-file", caFile)
					if err != nil {
						return err
					}
					text := string(pem)
					body.CaBundlePem = &text
				}
			default:
				return &UsageError{Message: "name a registered issuer with --id, or give --issuer and --jwks-mode", Hint: "for example notarxctl workload issuer probe --id ISSUER_ID"}
			}
			client, err := env.client()
			if err != nil {
				return err
			}
			encoded, err := encodeBody(body)
			if err != nil {
				return err
			}
			var probe adminopenapi.WorkloadIssuerProbe
			if _, err := client.sendJSON(ctx, http.MethodPost, workloadIssuersPath+":probe", requestOptions{body: encoded, idempotent: true}, &probe); err != nil {
				return err
			}
			return env.render(probe, nil, func(w io.Writer) error {
				if !probe.Reachable {
					_, err := fmt.Fprintf(w, "not reachable: %s\n", optional(probe.Error))
					return err
				}
				rows := [][]string{{"reachable", "yes"}, {"jwks uri", optional(probe.JwksUri)}, {"algorithms", strings.Join(probe.Algorithms, ",")}}
				for _, key := range probe.Keys {
					rows = append(rows, []string{"key", fmt.Sprintf("%s %s %s", optional(key.KeyId), key.KeyType, optional(key.Algorithm))})
				}
				return writeColumns(w, rows)
			})
		},
	}
}

func parseWorkloadUUID(flagName, value string) (openapi_types.UUID, error) {
	var id openapi_types.UUID
	if err := id.UnmarshalText([]byte(strings.TrimSpace(value))); err != nil {
		return id, &UsageError{Message: fmt.Sprintf("--%s must be a UUID", flagName), Hint: "notarxctl workload issuer list shows the IDs"}
	}
	return id, nil
}

func workloadPolicyListCommand() command {
	list := &cursorList[adminopenapi.WorkloadTrustPolicy]{path: workloadPoliciesPath, command: "notarxctl workload policy list", noun: "policies", render: renderWorkloadPolicies}
	return command{
		name: "workload policy list", synopsis: "list workload trust policies",
		usage:    "notarxctl workload policy list [--page-size N] [--cursor TOKEN | --all [--max-pages N]] [--json]",
		setFlags: list.setFlags,
		meta: commandMeta{
			Resource: "workload-policy", Scenarios: []string{"ci", "admin"}, Stage: "m2",
			Endpoints: []string{"GET " + workloadPoliciesPath}, MinimumRole: "platform admin",
			OutputSchema: "WorkloadTrustPolicyList", Pagination: true, DryRun: "unsupported", Retry: true,
		},
		run: list.run,
	}
}

func workloadPolicyShowCommand() command {
	return command{
		name: "workload policy show", synopsis: "show one workload trust policy",
		usage: "notarxctl workload policy show ID [--json]", positionals: 1, argsUsage: "ID",
		meta: commandMeta{
			Resource: "workload-policy", Scenarios: []string{"ci", "admin"}, Stage: "m2",
			Endpoints: []string{"GET " + workloadPoliciesPath + "/{policyId}"}, MinimumRole: "platform admin",
			OutputSchema: "WorkloadTrustPolicy", DryRun: "unsupported", Retry: true,
		},
		run: func(ctx context.Context, env *commandContext) error {
			client, err := env.client()
			if err != nil {
				return err
			}
			var policy adminopenapi.WorkloadTrustPolicy
			if err := client.getJSON(ctx, workloadPolicyPath(env.args[0]), nil, &policy); err != nil {
				return err
			}
			if !completeWorkloadPolicy(policy) {
				return &APIError{Code: "invalid_response", Message: "the server returned an incomplete workload trust policy"}
			}
			return env.render(policy, nil, func(w io.Writer) error { return renderWorkloadPolicy(w, policy) })
		},
	}
}

// parseWorkloadConditions builds the conditions from --condition CLAIM:OP:VALUES flags (values of in are
// comma-separated; the value may itself contain colons) or from --conditions-file, a JSON array.
func parseWorkloadConditions(conditions stringList, file string) ([]adminopenapi.WorkloadCondition, error) {
	if file != "" && len(conditions) > 0 {
		return nil, &UsageError{Message: "use --condition or --conditions-file, not both", Hint: "the file holds the whole list"}
	}
	if file != "" {
		data, err := readWorkloadFile("conditions-file", file)
		if err != nil {
			return nil, err
		}
		var parsed []adminopenapi.WorkloadCondition
		if json.Unmarshal(data, &parsed) != nil || len(parsed) == 0 {
			return nil, &UsageError{Message: "--conditions-file must hold a non-empty JSON array of {claim, op, values}", Hint: "for example [{\"claim\":\"sub\",\"op\":\"eq\",\"values\":[\"repo:acme/app\"]}]"}
		}
		return parsed, nil
	}
	out := make([]adminopenapi.WorkloadCondition, 0, len(conditions))
	for _, text := range conditions {
		parts := strings.SplitN(text, ":", 3)
		if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
			return nil, &UsageError{Message: fmt.Sprintf("--condition %q is not CLAIM:OP:VALUE", text), Hint: "for example --condition repository_id:eq:1296269 --condition ref:eq:refs/heads/main"}
		}
		values := []string{parts[2]}
		if parts[1] == "in" {
			values = strings.Split(parts[2], ",")
		}
		var claim adminopenapi.WorkloadCondition_Claim
		if err := claim.FromWorkloadConditionClaim0(parts[0]); err != nil {
			return nil, err
		}
		out = append(out, adminopenapi.WorkloadCondition{Claim: claim, Op: adminopenapi.WorkloadConditionOp(parts[1]), Values: values})
	}
	return out, nil
}

type workloadPolicyFlags struct {
	issuer, account, conditionsFile, sampleFile, state string
	conditions                                         stringList
	maxTTL, expiresIn                                  time.Duration
	singleUse, allowAnyRef, allowAdmin                 bool
}

func (f *workloadPolicyFlags) setFlags(fs *flag.FlagSet, creating bool) {
	if creating {
		fs.StringVar(&f.issuer, "issuer", "", "ID of the workload issuer the policy applies to")
	}
	fs.StringVar(&f.account, "service-account", "", "ID of the service account the policy maps to")
	fs.Var(&f.conditions, "condition", "CLAIM:OP:VALUE (op eq, in, glob or prefix); repeat for several, all must hold")
	fs.StringVar(&f.conditionsFile, "conditions-file", "", "JSON file with the whole condition list")
	fs.DurationVar(&f.maxTTL, "max-token-ttl", 0, "cap on the lifetime of an exchanged token, for example 15m (default 15m)")
	fs.BoolVar(&f.singleUse, "single-use", false, "accept each token once; the token needs a jti (recommended for exchange-only policies)")
	fs.StringVar(&f.sampleFile, "sample-token-file", "", "file with a token of the issuer, to check that it has a jti before --single-use is saved (never stored)")
	fs.BoolVar(&f.allowAnyRef, "allow-any-ref", false, "acknowledge that no ref, environment or workflow condition is wanted")
	fs.BoolVar(&f.allowAdmin, "allow-admin", false, "allow mapping onto a service account that holds an administrator grant")
	fs.DurationVar(&f.expiresIn, "expires-in", 0, "make the policy expire after this long")
	fs.StringVar(&f.state, "state", "", "active or disabled")
}

func (f *workloadPolicyFlags) validate() error {
	if f.state != "" && f.state != "active" && f.state != "disabled" {
		return &UsageError{Message: "--state must be active or disabled", Hint: "disabling stops the policy from matching within the revocation window"}
	}
	if f.maxTTL < 0 || f.maxTTL != 0 && (f.maxTTL < time.Second || f.maxTTL > 24*time.Hour) {
		return &UsageError{Message: "--max-token-ttl must be between 1s and 24h", Hint: "for example --max-token-ttl 15m"}
	}
	if f.expiresIn < 0 {
		return &UsageError{Message: "--expires-in must be positive", Hint: "for example --expires-in 720h"}
	}
	return nil
}

func (f *workloadPolicyFlags) sampleToken() (*string, error) {
	if f.sampleFile == "" {
		return nil, nil
	}
	data, err := readWorkloadFile("sample-token-file", f.sampleFile)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(string(data))
	return &token, nil
}

func workloadPolicyCreateCommand() command {
	var f workloadPolicyFlags
	return command{
		name: "workload policy create", synopsis: "create a trust policy that maps a workload's token claims to a service account",
		usage:       "notarxctl workload policy create NAME --issuer ID --service-account ID ([--condition CLAIM:OP:VALUE]... | --conditions-file FILE) [--max-token-ttl D] [--single-use [--sample-token-file FILE]] [--allow-any-ref] [--allow-admin] [--expires-in D] [--dry-run] [--json]",
		positionals: 1, argsUsage: "NAME",
		setFlags: func(fs *flag.FlagSet) { f.setFlags(fs, true) },
		meta: commandMeta{
			Resource: "workload-policy", Scenarios: []string{"ci", "admin"}, Stage: "m2",
			Endpoints: []string{"POST " + workloadPoliciesPath}, Mutating: true, MinimumRole: "platform admin",
			InputSchema: "CreateWorkloadTrustPolicy", OutputSchema: "WorkloadTrustPolicy", DryRun: "client-plan",
		},
		run: func(ctx context.Context, env *commandContext) error {
			if err := f.validate(); err != nil {
				return err
			}
			issuerID, err := parseWorkloadUUID("issuer", f.issuer)
			if err != nil {
				return err
			}
			accountID, err := parseWorkloadUUID("service-account", f.account)
			if err != nil {
				return err
			}
			conditions, err := parseWorkloadConditions(f.conditions, f.conditionsFile)
			if err != nil {
				return err
			}
			if len(conditions) == 0 {
				return &UsageError{Message: "a policy needs at least one condition", Hint: "pass --condition CLAIM:OP:VALUE or --conditions-file FILE"}
			}
			sample, err := f.sampleToken()
			if err != nil {
				return err
			}
			body := adminopenapi.CreateWorkloadTrustPolicy{
				Name: strings.TrimSpace(env.args[0]), IssuerId: issuerID, ServiceAccountId: accountID, Conditions: conditions,
				SingleUse: &f.singleUse, AllowAnyRef: &f.allowAnyRef, AllowAdmin: &f.allowAdmin, SampleToken: sample,
			}
			if f.maxTTL > 0 {
				seconds := int(f.maxTTL / time.Second)
				body.MaxTokenTtlSeconds = &seconds
			}
			if f.state != "" {
				state := adminopenapi.WorkloadState(f.state)
				body.State = &state
			}
			if f.expiresIn > 0 {
				expires := time.Now().UTC().Add(f.expiresIn).Truncate(time.Second)
				body.ExpiresAt = &expires
			}
			if env.globals.dryRun {
				plan := body
				plan.SampleToken = nil // a token never appears in a plan
				return planWorkloadWrite(env, http.MethodPost, workloadPoliciesPath, plan, nil, []string{"flags and input files are well formed"})
			}
			if sample != nil {
				env.addKnownSecret(*sample)
			}
			policy, err := workloadWrite(ctx, env, "workload policy create", http.MethodPost, workloadPoliciesPath, body, nil, completeWorkloadPolicy, body.Name, "notarxctl workload policy list --all")
			if err != nil {
				return err
			}
			return env.render(policy, nil, func(w io.Writer) error { return renderWorkloadPolicy(w, policy) })
		},
	}
}

func workloadPolicyUpdateCommand() command {
	var f workloadPolicyFlags
	var ifMatch string
	var clearExpiry bool
	return command{
		name: "workload policy update", synopsis: "change a trust policy (conditional on its version)",
		usage:       "notarxctl workload policy update ID [--service-account ID] [--condition CLAIM:OP:VALUE]... [--conditions-file FILE] [--max-token-ttl D] [--single-use [--sample-token-file FILE]] [--allow-any-ref] [--allow-admin] [--expires-in D | --clear-expiry] [--state active|disabled] [--if-match VERSION] [--dry-run] [--json]",
		positionals: 1, argsUsage: "ID",
		setFlags: func(fs *flag.FlagSet) {
			f.setFlags(fs, false)
			fs.StringVar(&ifMatch, "if-match", "", "apply only if the policy is still at this version (default: the version read just before)")
			fs.BoolVar(&clearExpiry, "clear-expiry", false, "remove the policy's expiry")
		},
		meta: commandMeta{
			Resource: "workload-policy", Scenarios: []string{"ci", "admin"}, Stage: "m2",
			Endpoints: []string{"PATCH " + workloadPoliciesPath + "/{policyId}", "GET " + workloadPoliciesPath + "/{policyId}"}, Mutating: true, MinimumRole: "platform admin",
			InputSchema: "UpdateWorkloadTrustPolicy", OutputSchema: "WorkloadTrustPolicy", DryRun: "client-plan",
		},
		run: func(ctx context.Context, env *commandContext) error {
			if err := f.validate(); err != nil {
				return err
			}
			if clearExpiry && f.expiresIn > 0 {
				return &UsageError{Message: "use --expires-in or --clear-expiry, not both", Hint: "--clear-expiry removes the expiry"}
			}
			body := adminopenapi.UpdateWorkloadTrustPolicy{}
			if f.account != "" {
				accountID, err := parseWorkloadUUID("service-account", f.account)
				if err != nil {
					return err
				}
				body.ServiceAccountId = &accountID
			}
			if len(f.conditions) > 0 || f.conditionsFile != "" {
				conditions, err := parseWorkloadConditions(f.conditions, f.conditionsFile)
				if err != nil {
					return err
				}
				body.Conditions = &conditions
			}
			if f.maxTTL > 0 {
				seconds := int(f.maxTTL / time.Second)
				body.MaxTokenTtlSeconds = &seconds
			}
			if env.flagSet["single-use"] {
				body.SingleUse = &f.singleUse
			}
			if env.flagSet["allow-any-ref"] {
				body.AllowAnyRef = &f.allowAnyRef
			}
			if env.flagSet["allow-admin"] {
				body.AllowAdmin = &f.allowAdmin
			}
			sample, err := f.sampleToken()
			if err != nil {
				return err
			}
			body.SampleToken = sample
			if f.state != "" {
				state := adminopenapi.WorkloadState(f.state)
				body.State = &state
			}
			if f.expiresIn > 0 {
				expires := time.Now().UTC().Add(f.expiresIn).Truncate(time.Second)
				body.ExpiresAt = &expires
			}
			if clearExpiry {
				body.ClearExpiresAt = &clearExpiry
			}
			if encoded, _ := json.Marshal(body); string(encoded) == "{}" {
				return &UsageError{Message: "nothing to update", Hint: "pass at least one change, for example --state disabled"}
			}
			path := workloadPolicyPath(env.args[0])
			version, err := workloadVersion(ctx, env, ifMatch, path)
			if err != nil {
				return err
			}
			if env.globals.dryRun {
				plan := body
				plan.SampleToken = nil
				return planWorkloadWrite(env, http.MethodPatch, path, plan, map[string]string{"If-Match": version}, []string{"at least one change is given"})
			}
			if sample != nil {
				env.addKnownSecret(*sample)
			}
			policy, err := workloadWrite(ctx, env, "workload policy update", http.MethodPatch, path, body, http.Header{"If-Match": {version}}, completeWorkloadPolicy, env.args[0], "notarxctl workload policy show "+env.args[0])
			if err != nil {
				return err
			}
			return env.render(policy, nil, func(w io.Writer) error { return renderWorkloadPolicy(w, policy) })
		},
	}
}

func workloadPolicyDeleteCommand() command {
	var ifMatch string
	return command{
		name: "workload policy delete", synopsis: "delete a trust policy",
		usage:       "notarxctl workload policy delete ID --yes [--if-match VERSION] [--dry-run] [--json]",
		positionals: 1, argsUsage: "ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&ifMatch, "if-match", "", "delete only if the policy is still at this version (default: the version read just before)")
		},
		meta: commandMeta{
			Resource: "workload-policy", Scenarios: []string{"admin"}, Stage: "m2",
			Endpoints: []string{"DELETE " + workloadPoliciesPath + "/{policyId}", "GET " + workloadPoliciesPath + "/{policyId}"}, Mutating: true, MinimumRole: "platform admin", DryRun: "client-plan",
		},
		run: func(ctx context.Context, env *commandContext) error {
			return deleteWorkloadResource(ctx, env, "policy", workloadPolicyPath(env.args[0]), ifMatch)
		},
	}
}

func workloadPolicyTestCommand() command {
	var tokenFile, claimsFile, issuer, policy string
	return command{
		name: "workload policy test", synopsis: "dry-run a token or claim set against the policies and report the failing condition",
		usage: "notarxctl workload policy test (--token-file FILE | --claims-file FILE) [--issuer ID | --policy ID] [--json]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&tokenFile, "token-file", "", "file with a workload JWT; its signature is checked against the issuer")
			fs.StringVar(&claimsFile, "claims-file", "", "file with a JSON object of claims (not verified)")
			fs.StringVar(&issuer, "issuer", "", "ID of the issuer whose policies are tested (default: from the iss claim)")
			fs.StringVar(&policy, "policy", "", "ID of the one policy to test")
		},
		meta: commandMeta{
			Resource: "workload-policy", Scenarios: []string{"ci", "admin"}, Stage: "m2",
			Endpoints: []string{"POST " + workloadPoliciesPath + ":test"}, MinimumRole: "platform admin",
			InputSchema: "TestWorkloadPolicyRequest", OutputSchema: "WorkloadPolicyTest", DryRun: "unsupported", Retry: true,
		},
		run: func(ctx context.Context, env *commandContext) error {
			if (tokenFile == "") == (claimsFile == "") {
				return &UsageError{Message: "pass exactly one of --token-file and --claims-file", Hint: "a token is verified; a claim set is only evaluated"}
			}
			var body adminopenapi.TestWorkloadPolicyRequest
			if tokenFile != "" {
				data, err := readWorkloadFile("token-file", tokenFile)
				if err != nil {
					return err
				}
				token := strings.TrimSpace(string(data))
				env.addKnownSecret(token)
				body.Token = &token
			} else {
				claims, err := readJSONObjectFile("claims-file", claimsFile)
				if err != nil {
					return err
				}
				body.Claims = &claims
			}
			if issuer != "" {
				id, err := parseWorkloadUUID("issuer", issuer)
				if err != nil {
					return err
				}
				body.IssuerId = &id
			}
			if policy != "" {
				id, err := parseWorkloadUUID("policy", policy)
				if err != nil {
					return err
				}
				body.PolicyId = &id
			}
			client, err := env.client()
			if err != nil {
				return err
			}
			encoded, err := encodeBody(body)
			if err != nil {
				return err
			}
			var result adminopenapi.WorkloadPolicyTest
			if _, err := client.sendJSON(ctx, http.MethodPost, workloadPoliciesPath+":test", requestOptions{body: encoded, idempotent: true}, &result); err != nil {
				return err
			}
			return env.render(result, nil, func(w io.Writer) error { return renderWorkloadPolicyTest(w, result) })
		},
	}
}

func renderWorkloadPolicyTest(w io.Writer, result adminopenapi.WorkloadPolicyTest) error {
	verified := "not verified"
	if result.SignatureVerified {
		verified = "verified"
	} else if result.VerificationError != nil {
		verified = "not verified: " + *result.VerificationError
	}
	if _, err := fmt.Fprintf(w, "signature: %s\n", verified); err != nil {
		return err
	}
	for _, item := range result.Results {
		line := item.PolicyName + ": every condition holds"
		if !item.Matched {
			line = item.PolicyName + ": does not match"
			if item.Failure != nil {
				line = fmt.Sprintf("%s: condition %d on %s failed: %s", item.PolicyName, item.Failure.ConditionIndex, strings.Join(item.Failure.Claim, "."), item.Failure.Reason)
			}
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	if result.MatchedPolicyId != nil {
		if _, err := fmt.Fprintf(w, "matched policy: %s\n", result.MatchedPolicyId.String()); err != nil {
			return err
		}
	}
	for _, warning := range result.Warnings {
		if _, err := fmt.Fprintf(w, "warning: %s\n", warning); err != nil {
			return err
		}
	}
	return nil
}
