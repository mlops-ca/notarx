package notarxctl

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRepoConnectPyPINetrcMatchesHostnameWithExplicitPort(t *testing.T) {
	api := newRouteAPI(t, "/gw")
	api.on("GET", repoBase, 200, `{"id":"`+testRepoID+`","name":"python-private","format":"pypi","type":"hosted","state":"active","version":"1"}`)
	code, stdout, _ := runCLI(t, "repo", "connect", testRepoID, "--json")
	if code != ExitOK {
		t.Fatalf("exit = %d, stdout = %s", code, stdout)
	}
	var guide connectGuide
	if err := json.Unmarshal(decodeEnvelope(t, stdout).Data, &guide); err != nil {
		t.Fatal(err)
	}
	server, err := url.Parse(api.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, snippet := range guide.Snippets {
		if snippet.File == ".netrc" {
			want := "machine " + server.Hostname() + "\nlogin ci\npassword ${NOTARX_TOKEN}"
			if snippet.Content != want {
				t.Fatalf("pip must match the URL hostname without its port: got %q, want %q", snippet.Content, want)
			}
			if !strings.Contains(guide.Snippets[0].Content, api.server.URL+"/gw/pypi/python-private/simple/") {
				t.Fatalf("the index URL must retain its port and prefix: %+v", guide.Snippets)
			}
			return
		}
	}
	t.Fatal("the PyPI guide must include .netrc credentials")
}

func TestStatusScannerUnhealthyFailsWithoutStrict(t *testing.T) {
	for _, state := range []string{"unhealthy", "unreachable"} {
		t.Run(state, func(t *testing.T) {
			api := statusAPI(t)
			scanner := `"scanner":{"name":"trivy","formats":["oci"],"status":"` + state + `","detail":"scanner failed","checkedAt":"2026-10-15T08:00:00Z","databaseFreshness":"fresh","databaseMaxAgeSeconds":86400},`
			api.on("GET", "/api/v2/diagnostics", 200, strings.Replace(healthyDiagnostics, `"queues":[]`, scanner+`"queues":[]`, 1))
			code, stdout, _ := runCLI(t, "status", "--json")
			envelope := decodeEnvelope(t, stdout)
			if code != ExitFailure || envelope.Error == nil || envelope.Error.Code != "unhealthy" {
				t.Fatalf("scanner %s must fail ordinary status: exit = %d, stdout = %s", state, code, stdout)
			}
			var report statusReport
			if err := json.Unmarshal(envelope.Data, &report); err != nil {
				t.Fatal(err)
			}
			if report.Overall != "unhealthy" {
				t.Fatalf("overall = %q", report.Overall)
			}
			for _, check := range report.Checks {
				if check.Name == "scanner" && check.Status != "unhealthy" {
					t.Fatalf("scanner check = %+v", check)
				}
			}
		})
	}
}

func TestRepoConnectGoNetrcRetainsExplicitPort(t *testing.T) {
	api := newRouteAPI(t, "/gw")
	api.on("GET", repoBase, 200, `{"id":"`+testRepoID+`","name":"go-private","format":"go","type":"proxy","state":"active","version":"1"}`)
	code, stdout, _ := runCLI(t, "repo", "connect", testRepoID, "--json")
	if code != ExitOK {
		t.Fatalf("exit = %d, stdout = %s", code, stdout)
	}
	var guide connectGuide
	if err := json.Unmarshal(decodeEnvelope(t, stdout).Data, &guide); err != nil {
		t.Fatal(err)
	}
	server, err := url.Parse(api.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, snippet := range guide.Snippets {
		if snippet.File == ".netrc" && strings.HasPrefix(snippet.Content, "machine "+server.Host+"\n") {
			return
		}
	}
	t.Fatalf("Go's netrc URL prefix must retain its port: %+v", guide.Snippets)
}

func TestLifecycleJobListPagesByCursor(t *testing.T) {
	global := func(id string) string {
		return `{"repositoryId":"` + testRepoID + `","repositoryName":"raw-hosted","job":` + strings.Replace(lifecycleJob, "job-1", id, 1) + `}`
	}
	for _, tc := range []struct {
		name, path, body string
		args             []string
		query            string
		complete         bool
		next             string
	}{
		// A global page can be empty yet still have a next token: the server cuts the page
		// before it drops rows of repositories the caller does not administer.
		{"global short page keeps its token", "/api/v2/lifecycle-jobs", `{"items":[],"nextPageToken":"tok-2"}`, []string{"--limit", "10"}, "limit=10", false, "tok-2"},
		{"global continued", "/api/v2/lifecycle-jobs", `{"items":[` + global("job-7") + `]}`, []string{"--cursor", "tok-2"}, "limit=500&pageToken=tok-2", true, ""},
		{"repository first page", repoBase + "/lifecycle-jobs", `{"items":[` + lifecycleJob + `],"nextPageToken":"tok-3"}`, []string{"--repo", testRepoID}, "limit=100", false, "tok-3"},
		{"repository last page", repoBase + "/lifecycle-jobs", `{"items":[` + lifecycleJob + `]}`, []string{"--repo", testRepoID, "--limit", "7", "--cursor", "tok-3"}, "limit=7&pageToken=tok-3", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newRouteAPI(t, "")
			api.on("GET", tc.path, 200, tc.body)
			args := append([]string{"lifecycle-job", "list"}, tc.args...)
			code, stdout, _ := runCLI(t, append(args, "--json")...)
			var meta envelopeMeta
			envelope := decodeEnvelope(t, stdout)
			if err := json.Unmarshal(envelope.Meta, &meta); err != nil {
				t.Fatal(err)
			}
			if code != ExitOK || meta.Pagination == nil || meta.Pagination.Mode != "cursor" || meta.Pagination.Complete == nil || *meta.Pagination.Complete != tc.complete {
				t.Fatalf("pagination meta: exit = %d, stdout = %s", code, stdout)
			}
			if got := api.recorded()[0].Query; got != tc.query {
				t.Fatalf("query = %s, want %s", got, tc.query)
			}
			var data struct {
				NextCursor *string `json:"nextCursor"`
			}
			if err := json.Unmarshal(envelope.Data, &data); err != nil {
				t.Fatal(err)
			}
			if (tc.next == "") != (data.NextCursor == nil) || (data.NextCursor != nil && *data.NextCursor != tc.next) {
				t.Fatalf("nextCursor = %v, want %q: stdout = %s", data.NextCursor, tc.next, stdout)
			}
		})
	}
}

func TestLifecycleJobListHumanOutputPrintsTheContinuation(t *testing.T) {
	api := newRouteAPI(t, "")
	api.on("GET", repoBase+"/lifecycle-jobs", 200, `{"items":[`+lifecycleJob+`],"nextPageToken":"tok-2"}`)
	code, _, stderr := runCLI(t, "lifecycle-job", "list", "--repo", testRepoID)
	want := "notarxctl lifecycle-job list --repo " + testRepoID + " --limit 100 --cursor tok-2"
	if code != ExitOK || !strings.Contains(stderr, want) {
		t.Fatalf("exit = %d, stderr = %s, want %q", code, stderr, want)
	}
}

func TestLifecycleJobShowPagesUntilFoundOrExhausted(t *testing.T) {
	api := newRouteAPI(t, "")
	api.onFunc("GET", repoBase+"/lifecycle-jobs", func(r *http.Request) (int, string) {
		switch r.URL.Query().Get("pageToken") {
		case "":
			return 200, `{"items":[` + strings.Replace(lifecycleJob, "job-1", "job-a", 1) + `],"nextPageToken":"tok-2"}`
		case "tok-2":
			return 200, `{"items":[` + strings.Replace(lifecycleJob, "job-1", "job-b", 1) + `],"nextPageToken":"tok-3"}`
		}
		return 200, `{"items":[` + strings.Replace(lifecycleJob, "job-1", "job-c", 1) + `]}`
	})
	code, stdout, _ := runCLI(t, "lifecycle-job", "show", "job-c", "--repo", testRepoID, "--json")
	if code != ExitOK || !strings.Contains(stdout, "job-c") {
		t.Fatalf("a job on the third page must be found: exit = %d, stdout = %s", code, stdout)
	}
	if got := len(api.recorded()); got != 3 {
		t.Fatalf("requests = %d, want 3 pages", got)
	}
	code, stdout, _ = runCLI(t, "lifecycle-job", "show", "job-z", "--repo", testRepoID, "--json")
	if code != ExitNotFound {
		t.Fatalf("an exhausted listing is a not-found: exit = %d, stdout = %s", code, stdout)
	}
	if got := len(api.recorded()); got != 6 {
		t.Fatalf("requests = %d, want every page read before giving up", got)
	}
}

func TestDiagnosticsHumanOutputPreservesMountObservations(t *testing.T) {
	api := statusAPI(t)
	capacity := `"localCapacity":{"checkedAt":"2026-10-15T08:00:00Z","source":"statfs","scope":"observer_mount_namespace","unit":"bytes","refreshIntervalSeconds":15,"maxSampleAgeSeconds":60,"timeoutMilliseconds":200,"mounts":[{"alias":"temporary","status":"available","sampleAt":"2026-10-15T07:59:59Z","totalBytes":4096,"availableBytes":0,"sharedWith":[]},{"alias":"logs","status":"unknown","reason":"read_failed","sharedWith":[]},{"alias":"backups","status":"stale","sampleAt":"2026-10-15T07:59:00Z","sharedWith":["temporary"]}]},`
	api.on("GET", "/api/v2/diagnostics", 200, strings.Replace(healthyDiagnostics, `"queues":[]`, capacity+`"queues":[]`, 1))
	code, stdout, _ := runCLI(t, "diagnostics")
	if code != ExitOK {
		t.Fatalf("exit = %d, stdout = %s", code, stdout)
	}
	for _, want := range []string{"observer_mount_namespace", "bytes", "temporary:", "available, total 4096 bytes, available 0 bytes", "logs:", "unknown, reason read_failed", "backups:", "stale", "2026-10-15T07:59:00Z", "shared with temporary"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("diagnostics must retain %q: stdout = %s", want, stdout)
		}
	}
}
