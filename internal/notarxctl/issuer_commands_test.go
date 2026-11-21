package notarxctl

import (
	"net/http"
	"strings"
	"testing"
)

const issuerStatusJSON = `{"enabled":true,"issuerUrl":"https://notarx.example.test","algorithm":"RS256","rotationInterval":"720h0m0s","publishLead":"24h0m0s","maxTokenLifetime":"10m0s","keys":[` +
	`{"kid":"kid-active","alg":"RS256","state":"active","createdAt":"2026-10-01T00:00:00Z","publishedAt":"2026-10-01T00:00:00Z","activatedAt":"2026-10-01T00:00:00Z"},` +
	`{"kid":"kid-next","alg":"RS256","state":"next","createdAt":"2026-10-20T00:00:00Z","publishedAt":"2026-10-20T00:00:00Z"}]}`

func TestIssuerStatusCommand(t *testing.T) {
	api := newRouteAPI(t, "")
	api.on("GET", "/api/v2/issuer", 200, issuerStatusJSON)
	code, stdout, stderr := runCLI(t, "issuer", "status")
	if code != ExitOK || !strings.Contains(stdout, "https://notarx.example.test") || !strings.Contains(stdout, "kid-active") || !strings.Contains(stdout, "kid-next") || !strings.Contains(stdout, "720h") {
		t.Fatalf("status: %d %s %s", code, stdout, stderr)
	}
	if code, stdout, _ := runCLI(t, "issuer", "status", "--json"); code != ExitOK || !strings.Contains(stdout, `"kid": "kid-active"`) {
		t.Fatalf("json status: %d %s", code, stdout)
	}
	api.on("GET", "/api/v2/issuer", 200, `{"enabled":false,"keys":[]}`)
	if code, stdout, _ := runCLI(t, "issuer", "status"); code != ExitOK || !strings.Contains(stdout, "disabled") {
		t.Fatalf("disabled status: %d %s", code, stdout)
	}
	api.on("GET", "/api/v2/issuer", 200, `{"enabled":true}`)
	if code, _, _ := runCLI(t, "issuer", "status"); code == ExitOK {
		t.Fatal("an answer without a key list is incomplete")
	}
	api.on("GET", "/api/v2/issuer", 403, `{"code":"access_denied","message":"administrator permission is required"}`)
	if code, _, stderr := runCLI(t, "issuer", "status"); code == ExitOK || !strings.Contains(stderr, "access_denied") {
		t.Fatalf("forbidden: %d %s", code, stderr)
	}
}

func TestIssuerRotateAndRevokeCommands(t *testing.T) {
	api := newRouteAPI(t, "")
	expectUsage(t, api,
		[]string{"issuer", "rotate"},
		[]string{"issuer", "rotate", "--yes", "--algorithm", "HS256"},
		[]string{"issuer", "revoke", "kid-1"},
		[]string{"issuer", "revoke", " ", "--yes"},
	)
	api.on("POST", "/api/v2/issuer/rotation", 200, `{"algorithm":"RS256","retired":"kid-active","activated":"kid-next","warning":"the promoted key has been published for less than the publish lead"}`)
	code, stdout, stderr := runCLI(t, "issuer", "rotate", "--yes", "--algorithm", "RS256")
	if code != ExitOK || !strings.Contains(stdout, "kid-next is active") || !strings.Contains(stdout, "kid-active is retired") || !strings.Contains(stdout, "warning: the promoted key") {
		t.Fatalf("rotate: %d %s %s", code, stdout, stderr)
	}
	var sent string
	for _, request := range api.recorded() {
		if request.Method == "POST" {
			sent = request.Body
		}
	}
	if !strings.Contains(sent, `"algorithm":"RS256"`) {
		t.Fatalf("body = %q", sent)
	}
	before := len(api.recorded())
	if code, stdout, _ := runCLI(t, "issuer", "rotate", "--dry-run"); code != ExitOK || !strings.Contains(stdout, "dry run") || len(api.recorded()) != before {
		t.Fatalf("rotate dry run: %d %s", code, stdout)
	}
	api.on("POST", "/api/v2/issuer/rotation", 409, `{"code":"no_next_key","message":"there is no next key to promote"}`)
	if code, _, stderr := runCLI(t, "issuer", "rotate", "--yes"); code == ExitOK || !strings.Contains(stderr, "no next key") {
		t.Fatalf("rotate conflict: %d %s", code, stderr)
	}

	api.on("POST", "/api/v2/issuer/keys/kid-1/revoke", 204, "")
	if code, stdout, stderr := runCLI(t, "issuer", "revoke", "kid-1", "--yes", "--json"); code != ExitOK || !strings.Contains(stdout, `"revoked": true`) {
		t.Fatalf("revoke: %d %s %s", code, stdout, stderr)
	}
	api.on("POST", "/api/v2/issuer/keys/nope/revoke", 404, `{"code":"key_not_found","message":"signing key not found"}`)
	if code, _, stderr := runCLI(t, "issuer", "revoke", "nope", "--yes"); code == ExitOK || !strings.Contains(stderr, "not found") {
		t.Fatalf("revoke unknown: %d %s", code, stderr)
	}
	if code, stdout, _ := runCLI(t, "issuer", "revoke", "kid-1", "--dry-run"); code != ExitOK || !strings.Contains(stdout, "/revoke") {
		t.Fatalf("revoke dry run: %d %s", code, stdout)
	}
}

func TestIssuerResealNeedsConfirmationAndADatabase(t *testing.T) {
	api := newRouteAPI(t, "")
	expectUsage(t, api, []string{"issuer", "reseal"})
	t.Setenv("NOTARX_DATABASE_URL", "")
	code, _, stderr := runCLI(t, "issuer", "reseal", "--yes")
	if code != ExitUsage || !strings.Contains(stderr, "NOTARX_DATABASE_URL") {
		t.Fatalf("reseal without a database: %d %s", code, stderr)
	}
	t.Setenv("NOTARX_DATABASE_DRIVER", "mysql")
	if code, _, stderr := runCLI(t, "issuer", "reseal", "--yes"); code != ExitUsage || !strings.Contains(stderr, "issuer reseal supports PostgreSQL only") {
		t.Fatalf("reseal on mysql: %d %s", code, stderr)
	}
}

func TestAssertionAudienceCommands(t *testing.T) {
	api := newRouteAPI(t, "")
	path := "/api/v2/service-accounts/" + accountID + "/assertion-audiences"
	expectUsage(t, api,
		[]string{"service-account", "assertion-audience", "add", accountID},
		[]string{"service-account", "assertion-audience", "add", accountID, "--audience", " padded "},
		[]string{"service-account", "assertion-audience", "add", accountID, "--audience", "a", "--max-lifetime", "30s"},
		[]string{"service-account", "assertion-audience", "add", accountID, "--audience", "a", "--max-lifetime", "11m"},
		[]string{"service-account", "assertion-audience", "remove", accountID, "--audience", "a"},
		[]string{"service-account", "assertion-audience", "remove", accountID, "--yes"},
	)
	api.on("GET", path, 200, `{"items":[{"serviceAccountId":"`+accountID+`","audience":"api://AzureADTokenExchange","maxLifetimeSeconds":300,"createdAt":"2026-10-01T00:00:00Z"}]}`)
	if code, stdout, stderr := runCLI(t, "service-account", "assertion-audience", "list", accountID); code != ExitOK || !strings.Contains(stdout, "api://AzureADTokenExchange") || !strings.Contains(stdout, "300s") {
		t.Fatalf("list: %d %s %s", code, stdout, stderr)
	}
	api.on("GET", path, 200, `{"items":[]}`)
	if code, stdout, _ := runCLI(t, "service-account", "assertion-audience", "list", accountID); code != ExitOK || !strings.Contains(stdout, "no assertion audiences") {
		t.Fatalf("empty list: %d %s", code, stdout)
	}
	api.on("POST", path, 201, `{"serviceAccountId":"`+accountID+`","audience":"api://AzureADTokenExchange","maxLifetimeSeconds":120,"createdAt":"2026-10-01T00:00:00Z"}`)
	if code, stdout, stderr := runCLI(t, "service-account", "assertion-audience", "add", accountID, "--audience", "api://AzureADTokenExchange", "--max-lifetime", "2m"); code != ExitOK || !strings.Contains(stdout, "120s") {
		t.Fatalf("add: %d %s %s", code, stdout, stderr)
	}
	var sent string
	for _, request := range api.recorded() {
		if request.Method == "POST" {
			sent = request.Body
		}
	}
	if !strings.Contains(sent, `"audience":"api://AzureADTokenExchange"`) || !strings.Contains(sent, `"maxLifetimeSeconds":120`) {
		t.Fatalf("body = %q", sent)
	}
	api.on("POST", path, 201, `{"serviceAccountId":"`+accountID+`","audience":"other","maxLifetimeSeconds":120,"createdAt":"2026-10-01T00:00:00Z"}`)
	if code, _, _ := runCLI(t, "service-account", "assertion-audience", "add", accountID, "--audience", "api://AzureADTokenExchange"); code == ExitOK {
		t.Fatal("an answer for another audience is incomplete")
	}
	if code, stdout, _ := runCLI(t, "service-account", "assertion-audience", "add", accountID, "--audience", "x", "--dry-run"); code != ExitOK || !strings.Contains(stdout, "dry run") {
		t.Fatalf("add dry run: %d %s", code, stdout)
	}
	api.on("DELETE", path, 204, "")
	if code, stdout, stderr := runCLI(t, "service-account", "assertion-audience", "remove", accountID, "--audience", "api://AzureADTokenExchange", "--yes", "--json"); code != ExitOK || !strings.Contains(stdout, `"removed": true`) {
		t.Fatalf("remove: %d %s %s", code, stdout, stderr)
	}
	var query string
	for _, request := range api.recorded() {
		if request.Method == http.MethodDelete {
			query = request.Query
		}
	}
	if !strings.Contains(query, "audience=api%3A%2F%2FAzureADTokenExchange") {
		t.Fatalf("query = %q", query)
	}
	if code, stdout, _ := runCLI(t, "service-account", "assertion-audience", "remove", accountID, "--audience", "x", "--dry-run"); code != ExitOK || !strings.Contains(stdout, "DELETE") {
		t.Fatalf("remove dry run: %d %s", code, stdout)
	}
}

func TestServiceAccountShowAndCreateCarryTheAssertionSubject(t *testing.T) {
	api := newRouteAPI(t, "")
	expectUsage(t, api, []string{"service-account", "create", "ci", "--assertion-subject", ""}, []string{"service-account", "create", "ci", "--assertion-subject", " x"})
	account := `{"id":"` + accountID + `","name":"ci","description":"","state":"active","version":"1","createdAt":"2026-10-01T00:00:00Z","updatedAt":"2026-10-01T00:00:00Z","assertionSubject":"notarx:service-account:ci"}`
	api.on("GET", "/api/v2/service-accounts", 200, `{"items":[`+account+`]}`)
	if code, stdout, stderr := runCLI(t, "service-account", "show", accountID); code != ExitOK || !strings.Contains(stdout, "assertion subject") || !strings.Contains(stdout, "notarx:service-account:ci") {
		t.Fatalf("show: %d %s %s", code, stdout, stderr)
	}
	api.on("POST", "/api/v2/service-accounts", 201, account)
	if code, stdout, stderr := runCLI(t, "service-account", "create", "ci", "--assertion-subject", "legacy-subject"); code != ExitOK {
		t.Fatalf("create: %d %s %s", code, stdout, stderr)
	}
	var sent string
	for _, request := range api.recorded() {
		if request.Method == "POST" {
			sent = request.Body
		}
	}
	if !strings.Contains(sent, `"assertionSubject":"legacy-subject"`) {
		t.Fatalf("body = %q", sent)
	}
}

func TestAssertionSubjectCommands(t *testing.T) {
	api := newRouteAPI(t, "")
	expectUsage(t, api,
		[]string{"service-account", "assertion-subject", "release"},
		[]string{"service-account", "assertion-subject", "release", " padded "},
		[]string{"service-account", "assertion-subject", "release", "notarx:service-account:gone"},
	)
	api.on("GET", "/api/v2/assertion-subjects", 200, `{"items":[{"subject":"notarx:service-account:gone","orphaned":true,"createdAt":"2026-10-01T00:00:00Z"},{"subject":"notarx:service-account:ci","serviceAccountId":"`+accountID+`","orphaned":false,"createdAt":"2026-10-01T00:00:00Z"}]}`)
	if code, stdout, stderr := runCLI(t, "service-account", "assertion-subject", "list"); code != ExitOK || !strings.Contains(stdout, "orphaned") || !strings.Contains(stdout, "held") || !strings.Contains(stdout, accountID) {
		t.Fatalf("list: %d %s %s", code, stdout, stderr)
	}
	api.on("GET", "/api/v2/assertion-subjects", 200, `{"items":[]}`)
	if code, stdout, _ := runCLI(t, "service-account", "assertion-subject", "list"); code != ExitOK || !strings.Contains(stdout, "no assertion subjects") {
		t.Fatalf("empty list: %d %s", code, stdout)
	}
	if code, stdout, _ := runCLI(t, "service-account", "assertion-subject", "release", "a/b c", "--dry-run"); code != ExitOK || !strings.Contains(stdout, "DELETE") || !strings.Contains(stdout, "a%2Fb%20c") {
		t.Fatalf("release dry run: %d %s", code, stdout)
	}
	api.on("DELETE", "/api/v2/assertion-subjects/notarx:service-account:gone", 204, "")
	if code, stdout, stderr := runCLI(t, "service-account", "assertion-subject", "release", "notarx:service-account:gone", "--yes", "--json"); code != ExitOK || !strings.Contains(stdout, `"released": true`) {
		t.Fatalf("release: %d %s %s", code, stdout, stderr)
	}
}
