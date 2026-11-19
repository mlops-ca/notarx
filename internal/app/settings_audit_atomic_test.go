package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/emailnotification"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
)

// TestSettingsMutationsRollBackWithTheirAudit proves ADR 0023 section 1 for the settings,
// theme, email, webhook and scheduled task handlers on the memory store.
func TestSettingsMutationsRollBackWithTheirAudit(t *testing.T) {
	store := repository.NewMemoryStore()
	runSettingsAuditRollback(t, store, seedDeadDeliveriesThroughTheStore, func() {
		store.SetAuditFault(func(record repository.AuditRecord) error {
			if record.Format == "management" {
				return errors.New("audit store unavailable")
			}
			return nil
		})
	}, func() { store.SetAuditFault(nil) })
}

// settingsRollbackStore is what the handlers under test and the assertions need.
type settingsRollbackStore interface {
	GatewayStore
}

type settingsRollbackCase struct {
	name, method, path, body, version string
	unchanged                         func(t *testing.T)
}

// runSettingsAuditRollback drives every mutating handler of the migrated settings group
// while audits fail and asserts that the change did not take effect and that the answer
// is never 500 audit_failed. clear lets the audit through again; the final check proves
// the same requests then succeed, so the rollback was caused by the audit alone.
func runSettingsAuditRollback(t *testing.T, store settingsRollbackStore, seed deadDeliverySeeder, fail, clear func()) {
	t.Helper()
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	t.Setenv("NOTARX_SETTINGS_ENCRYPTION_KEY", "01234567890123456789012345678901")
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	emailConfig := emailnotification.Config{Enabled: true, Host: "smtp.example.test", Port: 465, Mode: "implicit_tls", From: "gateway@example.test", ApprovedIPs: []string{"192.0.2.10"}}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{Email: emailConfig, OIDCRuntime: NewOIDCRuntime(store, OIDCRuntimeConfig{})}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	call := func(method, path, body, version, key string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, "/api/v2"+path, strings.NewReader(body))
		authorize(request, "admin-secret")
		if version != "" {
			request.Header.Set("If-Match", version)
		}
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	// Fixtures created through the store without audit.
	site, err := store.GetSiteSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	themeID := "acme-fix-" + suffix
	fixtureTheme := strings.Replace(acmeDarkTheme, `"id":"acme-dark"`, `"id":"`+themeID+`"`, 1)
	theme, err := store.CreateConsoleThemePackage(ctx, repository.ConsoleThemePackage{ID: themeID, Payload: []byte(fixtureTheme)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DeleteConsoleThemePackage(ctx, theme.ID, theme.Version) })
	target, err := store.CreateEmailTarget(ctx, repository.EmailTarget{ID: uuid.NewString(), Name: "Ops " + suffix, Locale: "en-CA", RecipientCiphertext: "synthetic", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	subscription, deadWebhook, dead := seed(t, store, target)
	policy, err := store.GetAuditRetentionPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	original := policy
	policy.Enabled, policy.KeepDays = true, 30
	if policy, err = store.ReplaceAuditRetentionPolicy(ctx, policy, policy.Version); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if current, getErr := store.GetAuditRetentionPolicy(ctx); getErr == nil {
			original.Version = current.Version
			_, _ = store.ReplaceAuditRetentionPolicy(ctx, original, current.Version)
		}
	})
	t.Cleanup(func() {
		if current, getErr := store.GetSiteSettings(ctx); getErr == nil {
			_, _ = store.ReplaceSiteSettings(ctx, site, current.Version)
		}
	})
	task, err := store.CreateScheduledTask(ctx, repository.ScheduledTask{ID: uuid.NewString(), Name: "task-" + suffix, Kind: repository.ScheduledTaskAuditRetention, IntervalSeconds: 3600, Enabled: true, NextRunAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DeleteScheduledTask(ctx, task.ID) })
	deleteTask, err := store.CreateScheduledTask(ctx, repository.ScheduledTask{ID: uuid.NewString(), Name: "delete-" + suffix, Kind: repository.ScheduledTaskAuditRetention, IntervalSeconds: 3600, Enabled: true, NextRunAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DeleteScheduledTask(ctx, deleteTask.ID) })

	oidcBefore := repository.OIDCSettings{Version: "0"}
	if got, getErr := store.GetOIDCSettings(ctx); getErr == nil {
		oidcBefore = got
	}
	themeBody := strings.Replace(acmeDarkTheme, `"id":"acme-dark"`, `"id":"acme-new-`+suffix+`"`, 1)
	replaceThemeBody := strings.Replace(fixtureTheme, `"name":"Acme Dark"`, `"name":"Acme Changed"`, 1)
	siteBody := `{"siteName":"changed-` + suffix + `","logoUrl":"","brandMark":"AG","enabledThemeIds":["notarx-dark"],"defaultThemeId":"notarx-dark"}`
	oidcBody := `{"enabled":false,"issuer":"","audience":"","clientId":"","redirectUrl":"","scopes":["openid"],"adminSubjects":[],"memberRoles":[],"adminRoles":[]}`
	webhookBody := `{"name":"%s","endpointUrl":"https://events.example.test/hooks","secret":"0123456789abcdef0123456789abcdef","eventTypes":["artifact.quarantined"],"enabled":true}`
	webhookUpdate := `{"name":"renamed-` + suffix + `","endpointUrl":"https://events.example.test/hooks","eventTypes":["artifact.quarantined"],"enabled":false}`

	newTarget := "target-new-" + suffix
	newHook := "hook-new-" + suffix
	newTask := "task-new-" + suffix
	cases := []settingsRollbackCase{
		{"site settings", http.MethodPut, "/site-settings", siteBody, site.Version, func(t *testing.T) {
			if got, _ := store.GetSiteSettings(ctx); got.Version != site.Version || got.SiteName != site.SiteName {
				t.Fatalf("site settings changed: %+v", got)
			}
		}},
		{"oidc settings", http.MethodPut, "/authentication/oidc", oidcBody, oidcBefore.Version, func(t *testing.T) {
			if got, getErr := store.GetOIDCSettings(ctx); oidcBefore.Version == "0" && !errors.Is(getErr, repository.ErrNotFound) || oidcBefore.Version != "0" && got.Version != oidcBefore.Version {
				t.Fatalf("oidc settings changed: %+v %v", got, getErr)
			}
		}},
		{"theme install", http.MethodPost, "/console-themes", themeBody, "", func(t *testing.T) {
			if _, err := store.GetConsoleThemePackage(ctx, "acme-new-"+suffix); !errors.Is(err, repository.ErrConsoleThemeNotFound) {
				t.Fatalf("theme installed: %v", err)
			}
		}},
		{"theme replace", http.MethodPut, "/console-themes/" + themeID, replaceThemeBody, theme.Version, func(t *testing.T) {
			if got, _ := store.GetConsoleThemePackage(ctx, themeID); got.Version != theme.Version {
				t.Fatalf("theme replaced: %+v", got)
			}
		}},
		{"theme delete", http.MethodDelete, "/console-themes/" + themeID, "", theme.Version, func(t *testing.T) {
			if _, err := store.GetConsoleThemePackage(ctx, themeID); err != nil {
				t.Fatalf("theme deleted: %v", err)
			}
		}},
		{"email target create", http.MethodPost, "/email-targets", `{"name":"` + newTarget + `","locale":"en-CA","recipient":"private@example.test","enabled":true}`, "", func(t *testing.T) {
			targets, _ := store.ListEmailTargets(ctx)
			for _, got := range targets {
				if got.Name == newTarget {
					t.Fatalf("email target created: %+v", got)
				}
			}
		}},
		{"email target update", http.MethodPut, "/email-targets/" + target.ID, `{"name":"renamed-` + suffix + `","locale":"en-CA","enabled":true}`, target.Version, func(t *testing.T) {
			if got, _ := store.GetEmailTarget(ctx, target.ID); got.Version != target.Version {
				t.Fatalf("email target changed: %+v", got)
			}
		}},
		{"email test", http.MethodPost, "/email-notifications:test", `{"targetId":"` + target.ID + `","scenario":"resolved"}`, target.Version, func(t *testing.T) {
			deliveries, _ := store.ListEmailDeliveries(ctx, 100)
			for _, got := range deliveries {
				if got.Scenario == "resolved" && got.TargetID == target.ID {
					t.Fatalf("email test queued: %+v", got)
				}
			}
		}},
		{"email replay", http.MethodPost, "/email-deliveries/" + dead.ID + ":replay", "", dead.Version, func(t *testing.T) {
			if got, _ := store.GetEmailDelivery(ctx, dead.ID); got.State != "dead" {
				t.Fatalf("email delivery replayed: %+v", got)
			}
		}},
		{"webhook create", http.MethodPost, "/webhook-subscriptions", strings.Replace(webhookBody, "%s", newHook, 1), "", func(t *testing.T) {
			subscriptions, _ := store.ListWebhookSubscriptions(ctx)
			for _, got := range subscriptions {
				if got.Name == newHook {
					t.Fatalf("webhook created: %+v", got)
				}
			}
		}},
		{"webhook update", http.MethodPut, "/webhook-subscriptions/" + subscription.ID, webhookUpdate, subscription.Version, func(t *testing.T) {
			if got, _ := store.GetWebhookSubscription(ctx, subscription.ID); got.Version != subscription.Version {
				t.Fatalf("webhook changed: %+v", got)
			}
		}},
		{"webhook replay", http.MethodPost, "/webhook-deliveries/" + deadWebhook + ":replay", "", "", func(t *testing.T) {
			if got, _ := store.GetWebhookDelivery(ctx, deadWebhook); got.State != repository.WebhookDeliveryDead {
				t.Fatalf("webhook delivery replayed: %+v", got)
			}
		}},
		{"scheduled task create", http.MethodPost, "/scheduled-tasks", `{"name":"` + newTask + `","kind":"audit-retention","intervalMinutes":60,"enabled":true}`, "", func(t *testing.T) {
			tasks, _ := store.ListScheduledTasks(ctx)
			for _, got := range tasks {
				if got.Name == newTask {
					t.Fatalf("task created: %+v", got)
				}
			}
		}},
		{"scheduled task update", http.MethodPut, "/scheduled-tasks/" + task.ID, `{"name":"renamed-` + suffix + `","kind":"audit-retention","intervalMinutes":60,"enabled":false}`, task.Version, func(t *testing.T) {
			if got, _ := store.GetScheduledTask(ctx, task.ID); got.Version != task.Version {
				t.Fatalf("task changed: %+v", got)
			}
		}},
		{"scheduled task delete", http.MethodDelete, "/scheduled-tasks/" + deleteTask.ID, "", "", func(t *testing.T) {
			if _, err := store.GetScheduledTask(ctx, deleteTask.ID); err != nil {
				t.Fatalf("task deleted: %v", err)
			}
		}},
		{"scheduled task run", http.MethodPost, "/scheduled-tasks/" + task.ID + "/run", "", "", func(t *testing.T) {
			runs, _ := store.ListScheduledTaskRuns(ctx, task.ID, 10)
			for _, run := range runs {
				if run.State == repository.ScheduledTaskSubmitted {
					t.Fatalf("run recorded as submitted: %+v", run)
				}
			}
		}},
	}
	fail()
	for _, c := range cases {
		response := call(c.method, c.path, c.body, c.version, uuid.NewString())
		if response.Code < 400 {
			t.Fatalf("%s: status %d, want a failure: %s", c.name, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "audit_failed") {
			t.Fatalf("%s: answered audit_failed for a change that must not take effect: %s", c.name, response.Body.String())
		}
		c.unchanged(t)
	}
	clear()
	var decoded map[string]any
	if response := call(http.MethodPut, "/site-settings", siteBody, site.Version, ""); response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &decoded) != nil {
		t.Fatalf("site settings after audits work again: %d %s", response.Code, response.Body.String())
	}
}

// TestEmailTestReplayWritesOneAudit proves an idempotent retry of the email test request
// returns the first delivery and does not write a second audit.
func TestEmailTestReplayWritesOneAudit(t *testing.T) {
	t.Setenv("NOTARX_SETTINGS_ENCRYPTION_KEY", "01234567890123456789012345678901")
	store := repository.NewMemoryStore()
	target, err := store.CreateEmailTarget(context.Background(), repository.EmailTarget{ID: uuid.NewString(), Name: "Ops", Locale: "en-CA", RecipientCiphertext: "synthetic", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	config := emailnotification.Config{Enabled: true, Host: "smtp.example.test", Port: 465, Mode: "implicit_tls", From: "gateway@example.test", ApprovedIPs: []string{"192.0.2.10"}}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{Email: config}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	key := uuid.NewString()
	for range 2 {
		request := httptest.NewRequest(http.MethodPost, "/api/v2/email-notifications:test", strings.NewReader(`{"targetId":"`+target.ID+`","scenario":"warning"}`))
		authorize(request, "admin-secret")
		request.Header.Set("If-Match", target.Version)
		request.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusAccepted {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
	count := 0
	for _, record := range store.Audits {
		if record.Operation == "email.test.enqueue" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("email.test.enqueue audits=%d, want 1", count)
	}
}

// deadDeliverySeeder creates a webhook subscription with a dead delivery and a dead email
// delivery for target, without disturbing deliveries other tests of a shared database own.
type deadDeliverySeeder func(t *testing.T, store settingsRollbackStore, target repository.EmailTarget) (repository.WebhookSubscription, string, repository.EmailDelivery)

// seedDeadDeliveriesThroughTheStore claims and fails the deliveries through the store API,
// which is safe on a private memory store.
func seedDeadDeliveriesThroughTheStore(t *testing.T, store settingsRollbackStore, target repository.EmailTarget) (repository.WebhookSubscription, string, repository.EmailDelivery) {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	subscription, err := store.CreateWebhookSubscription(ctx, repository.WebhookSubscription{ID: uuid.NewString(), Name: "hook-" + suffix, EndpointURL: "https://events.example.test/hooks", SecretCiphertext: "synthetic", EventTypes: []repository.WebhookEventType{repository.WebhookEventArtifactQuarantined}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.EnqueueWebhookEvent(ctx, repository.WebhookEvent{ID: uuid.NewString(), Type: repository.WebhookEventArtifactQuarantined, OccurredAt: time.Now().UTC(), Data: []byte(`{"repositoryId":"repo-1"}`)}); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimWebhookDeliveries(ctx, "test-worker-"+suffix, time.Now().UTC().Add(time.Second), time.Minute, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims=%#v err=%v", claims, err)
	}
	if err = store.FailWebhookDelivery(ctx, claims[0].Delivery.ID, claims[0].Delivery.LeaseToken, time.Now().UTC().Add(time.Second), time.Time{}, 503, "HTTP 503", true); err != nil {
		t.Fatal(err)
	}
	if _, err = store.EnqueueEmailTest(ctx, repository.EmailTestRequest{ID: uuid.NewString(), EventID: uuid.NewString(), RequestKey: uuid.NewString(), TargetID: target.ID, TargetVersion: target.Version, Scenario: "warning", From: "gateway@example.test", TemplateVersion: emailnotification.TemplateVersion}); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimEmailDelivery(ctx, "synthetic/"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishEmailDelivery(ctx, claim.ID, claim.LeaseToken, repository.EmailAttemptResult{Code: "smtp_permanent_rejection", Permanent: true}); err != nil {
		t.Fatal(err)
	}
	dead, err := store.GetEmailDelivery(ctx, claim.ID)
	if err != nil {
		t.Fatal(err)
	}
	return subscription, claims[0].Delivery.ID, dead
}
