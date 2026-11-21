# notarxctl command map

[简体中文](cli-command-map.zh-CN.md) · [Documentation index](README.md) · [Project README](../README.md)

`notarxctl` exposes one command surface for two navigation views: CI (onboarding,
identity, publication, retrieval and verification, audit) and platform
management (identity and authority, repositories and Groups, data governance,
configuration policy, logs/audit/diagnostics/capacity, alerts and
notifications, asynchronous operations and local entry points). Resource
commands, HTTP/JSON behaviour, credentials, and permissions exist once; agents
use the same introspection for both views.

This page classifies the full contract; it does not describe the installed
binary. Apart from the #125 skeleton (`version`, `info`, `commands`) and the
rows whose phase says `implemented` (the #130 repository and Group commands,
the #131 artifact and format views, the #132 Raw and Maven publishing
commands, the #223 versioned generic package commands, the #133 service account, credential,
and API key commands, the #557 user group commands, the #134 grant, role, template, access check, and
anonymous-switch commands, the #336 quota alert and email commands (M2), the #335 user, session, and identity commands, and the read-only #135 audit, status,
diagnostics, runtime-node and runtime-log, overview, and lifecycle-job
commands, each
accepted against a real Gateway by `make cli-e2e`), every row is a plan. A
row may claim implemented/verified only once its command is
registered, its documentation and tests exist, and it passed acceptance
against a real Gateway.

Baseline: `api/openapi/management-runtime-v1.json` at commit
`a504ba48cd93371badcb1f2781b120b359e57b69` (127 paths / 177 operations), extended by the four Conan session operations in #366 (131 paths / 181 operations), extended by #381 source discovery (currently 136 paths / 189 operations), plus
the protocol endpoints of `api/openapi/native-hosted-v1.json` the CLI needs to
reach. Counts are re-derived by the drift guard below, not frozen here; the
map is updated first whenever the contract changes. Last reviewed: 2026-10-11
(issue #126 supplement).

## Phase legend

- M0 — foundation: registration, configuration, secure output, introspection,
  testing, distribution, and this map (#125–#128).
- M1 — first-release plan (#129–#136 plus #335); the first release is not the
  whole platform-management surface.
- M2 — later plan (#137–#139 plus #336, and the advanced rows marked in
  #131/#134).
- N/A — explicitly not exposed by the CLI (for example the compatible
  whole-table grant replacement PUT); the API itself stays.
- LOCAL — already present in the local `gateway` binary on the host; it does
  not mean `notarxctl` implements a matching remote command.
- BLOCKED — no management API or backend capability has shipped; the row
  records the dependency, and no invented HTTP endpoint may claim coverage.

## Permission legend (the server is the final authority)

The server remains the final authority; the default identity and account state
must be rejected truthfully, and the CLI adds no authorization:

- PUBLIC: public read; I: the current authenticated actor; I/A: self lookup,
  where impersonating another subject needs platform admin.
- A: platform admin; R/W/RA/RI: the repository's
  repositories:read/write/admin/intelligence (the server keeps its existing
  platform-admin behaviour).
- R*: the read authorization of the repository/Group browse helper plus an
  explicitly configured anonymous read branch; it must not be anonymous by
  default.
- FR/FA: server-side filter over readable/manageable repositories; ST:
  repository tasks need RA, platform tasks need A, and lists filter by
  manageable scope.
- G: the existing Group membership management gate (platform A, or admin of
  every bound member; unknown or unbound members need A); RA×2: source and
  target authorizations are governed separately.

Basis: `internal/app/hosted_repository_api.go`,
`repository_access_api.go`, `hosted_group_api.go`, `scheduled_tasks_api.go`
and the domain handlers; [accepted ADR 0006](adr/0006-account-levels-and-repository-grants.md).

## Acceptance code legend

Every acceptance code must be verified with synthetic input for the mapped
operationId, known literal expectations, and a real isolated Gateway E2E — not
just an HTTP 200:

- Q: exact schema/fields/visibility, real pagination range and cursor, empty
  results and permission denial, honouring the #125 partial-result/unknown
  contract.
- W: read state/audit back through existing read entry points after a change;
  no permission, wrong input, dangerous confirmations, and client-plan dry-run
  produce no business change.
- C: W plus real If-Match/version conflicts; a 412 must not overwrite the new
  value.
- S: one-time secrets delivered through a secure file; a missing destination
  is refused before the request; default output and logs carry no secret; a
  failed write does not re-issue.
- G: single-row authorization concurrency without lost updates, template
  apply effects and CAS, effective-access and anonymous-combination gates
  (#134).
- J: accepted and terminal states are separate, bounded wait on the original
  ID, failure/timeout/retry and real cancel support, declared idempotency/CAS
  (#125/#137).
- U: publish sessions and per-object size/digest, staged idempotent read-back,
  unknown commit and partial failure (#132).
- L: local/session logs, cursors, restart/503 and bounded NDJSON, redaction
  and administrator gates (#135).
- D: uses a real server preview or an explicit client plan; configuration,
  objects, and notification queues stay unchanged; its output must not claim
  full execution acceptance.
- N: this direct command is not offered; the guard keeps the explicit reason
  on the row and the CLI does not reach the same compatible PUT through
  another path.

## Management contract operation map

The rows below cover the complete operation set of the baseline
`api/openapi/management-runtime-v1.json`; the drift guard re-derives the set
from the file rather than trusting this count. Paths are relative to `/api/v2`,
the contract's server base. Planned commands omit the leading `notarxctl`.

| API (all under /api/v2) / operationId | Planned command (notarxctl omitted) | Owning issue | Phase | Permission | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `GET /migrations/nexus/schedules`<br>`listNexusMigrationSchedules` | N: migration schedule API/Console only; CLI deferred | #443 | N/A | A | N: no CLI command registered; use the supported management API or Console |
| `POST /migrations/nexus/schedules`<br>`createNexusMigrationSchedule` | N: migration schedule API/Console only; CLI deferred | #443 | N/A | A | N: no CLI command registered; use the supported management API or Console |
| `GET /migrations/nexus/schedules/{scheduleId}`<br>`getNexusMigrationSchedule` | N: migration schedule API/Console only; CLI deferred | #443 | N/A | A | N: no CLI command registered; use the supported management API or Console |
| `POST /migrations/nexus/schedules/{scheduleId}/pause`<br>`pauseNexusMigrationSchedule` | N: migration schedule API/Console only; CLI deferred | #443 | N/A | A | N: no CLI command registered; use the supported management API or Console |
| `POST /migrations/nexus/schedules/{scheduleId}/resume`<br>`resumeNexusMigrationSchedule` | N: migration schedule API/Console only; CLI deferred | #443 | N/A | A | N: no CLI command registered; use the supported management API or Console |
| `POST /migrations/nexus/schedules/{scheduleId}/stop`<br>`stopNexusMigrationSchedule` | N: migration schedule API/Console only; CLI deferred | #443 | N/A | A | N: no CLI command registered; use the supported management API or Console |
| `POST /migrations/nexus/schedules/{scheduleId}/capacity`<br>`refreshNexusMigrationScheduleCapacity` | N: migration schedule API/Console only; CLI deferred | #443 | N/A | A | N: no CLI command registered; use the supported management API or Console |
| `GET /migrations/nexus/capabilities`<br>`getNexusMigrationCapabilities` | N: API-only migration batch service; CLI deferred | #382 | N/A | A | N: no CLI command is registered; use the supported management API |
| `GET /migrations/nexus/batches`<br>`listNexusMigrationBatches` | N: API-only migration batch service; CLI deferred | #382 | N/A | A | N: no CLI command is registered; use the supported management API |
| `POST /migrations/nexus/batches`<br>`startNexusMigrationBatch` | N: API-only migration batch service; CLI deferred | #382 | N/A | A | N: no CLI command is registered; use the supported management API |
| `GET /migrations/nexus/batches/{batchId}`<br>`getNexusMigrationBatch` | N: API-only migration batch service; CLI deferred | #382 | N/A | A | N: no CLI command is registered; use the supported management API |
| `GET /migrations/nexus/batches/{batchId}/entries`<br>`listNexusMigrationEntries` | N: API-only migration batch service; CLI deferred | #382 | N/A | A | N: no CLI command is registered; use the supported management API |
| `POST /migrations/nexus/batches/{batchId}/pause`<br>`pauseNexusMigrationBatch` | N: API-only migration batch service; CLI deferred | #382 | N/A | A | N: no CLI command is registered; use the supported management API |
| `POST /migrations/nexus/batches/{batchId}/resume`<br>`resumeNexusMigrationBatch` | N: API-only migration batch service; CLI deferred | #382 | N/A | A | N: no CLI command is registered; use the supported management API |
| `POST /migrations/nexus/batches/{batchId}/retry`<br>`retryNexusMigrationBatch` | N: API-only migration batch service; CLI deferred | #382 | N/A | A | N: no CLI command is registered; use the supported management API |
| `POST /migrations/nexus/batches/{batchId}/cancel`<br>`cancelNexusMigrationBatch` | N: API-only migration batch service; CLI deferred | #382 | N/A | A | N: no CLI command is registered; use the supported management API |
| `GET /migration-sources`<br>`listMigrationSources` | N: API-only source service; CLI deferred | #381 | N/A | A | N: no CLI command is registered; use the supported management API |
| `POST /migration-sources`<br>`createMigrationSource` | N: API-only source service; CLI deferred | #381 | N/A | A | N: no CLI command is registered; use the supported management API |
| `GET /migration-sources/{sourceId}`<br>`getMigrationSource` | N: API-only source service; CLI deferred | #381 | N/A | A | N: no CLI command is registered; use the supported management API |
| `PUT /migration-sources/{sourceId}`<br>`updateMigrationSource` | N: API-only source service; CLI deferred | #381 | N/A | A | N: no CLI command is registered; use the supported management API |
| `DELETE /migration-sources/{sourceId}`<br>`deleteMigrationSource` | N: API-only source service; CLI deferred | #381 | N/A | A | N: no CLI command is registered; use the supported management API |
| `POST /migration-sources/{sourceId}/preflight`<br>`preflightMigrationSource` | N: API-only source service; CLI deferred | #381 | N/A | A | N: no CLI command is registered; use the supported management API |
| `POST /migration-sources/{sourceId}/discovery`<br>`discoverMigrationSource` | N: API-only source service; CLI deferred | #381 | N/A | A | N: no CLI command is registered; use the supported management API |
| `POST /migration-sources/{sourceId}/mapping-preview`<br>`previewMigrationSourceMapping` | N: API-only source service; CLI deferred | #381 | N/A | A | N: no CLI command is registered; use the supported management API |
| `GET /repository-quota-alert-rules/{ruleId}/events`<br>`listRepositoryQuotaAlertEvents` | `quota-alert events <rule>` | #336 | M2 implemented | A | Q |
| `GET /repository-quota-alert-rules/{ruleId}`<br>`getRepositoryQuotaAlertRule` | `quota-alert show <rule>` | #336 | M2 implemented | A | Q |
| `PUT /repository-quota-alert-rules/{ruleId}`<br>`updateRepositoryQuotaAlertRule` | `quota-alert update <rule>` | #336 | M2 implemented | A | C |
| `DELETE /repository-quota-alert-rules/{ruleId}`<br>`deleteRepositoryQuotaAlertRule` | `quota-alert delete <rule>` | #336 | M2 implemented | A | C |
| `POST /repository-quota-alert-rules`<br>`createRepositoryQuotaAlertRule` | `quota-alert create` | #336 | M2 implemented | A | W |
| `GET /repository-quota-alert-rules`<br>`listRepositoryQuotaAlertRules` | `quota-alert list` | #336 | M2 implemented | A | Q |
| `GET /email-notifications`<br>`getEmailNotificationCapability` | `email capability` | #336 | M2 implemented | A | Q |
| `POST /email-notifications:test`<br>`testEmailNotification` | `email test` | #336 | M2 implemented | A | J |
| `GET /email-targets`<br>`listEmailTargets` | `email target list` | #336 | M2 implemented | A | Q |
| `POST /email-targets`<br>`createEmailTarget` | `email target create` | #336 | M2 implemented | A | W |
| `GET /email-targets/{targetId}`<br>`getEmailTarget` | `email target show <id>` | #336 | M2 implemented | A | Q |
| `PUT /email-targets/{targetId}`<br>`updateEmailTarget` | `email target update <id>` | #336 | M2 implemented | A | C |
| `GET /email-queue/diagnostics`<br>`getEmailQueueDiagnostics` | N: administrator read-only API; CLI excluded from this slice | #467 | N/A | A | N: use the management API; no CLI command added |
| `GET /email-deliveries`<br>`listEmailDeliveries` | `email delivery list` | #336 | M2 implemented | A | Q |
| `GET /email-deliveries/{deliveryId}`<br>`getEmailDelivery` | `email delivery show <id>` | #336 | M2 implemented | A | Q |
| `POST /email-deliveries/{deliveryId}:replay`<br>`replayEmailDelivery` | `email delivery replay <id>` | #336 | M2 implemented | A | J |
| `POST /email-notifications:preview`<br>`previewEmailNotification` | `email preview` | #336 | M2 implemented | A | D |
| `GET /formats`<br>`listFormatProfiles` | `formats list` (info reuses the same read) | #125 | M0 | I | Q |
| `GET /identity`<br>`getCurrentIdentity` | `auth whoami` (info reuses the same read) | #129 | M1 | I | Q |
| `GET /artifact-search`<br>`searchArtifacts` | `artifact search --global` | #131 | M1 implemented | FR | Q |
| `GET /public-access-policy`<br>`getPublicAccessPolicy` | `access public show` | #134 | M1 implemented | A | Q |
| `PUT /public-access-policy`<br>`replacePublicAccessPolicy` | `access public set` | #134 | M1 implemented | A | C |
| `GET /site-settings`<br>`getSiteSettings` | `site-settings show` (theme list reuses availableThemes) | #139 | M2 | PUBLIC | Q |
| `PUT /site-settings`<br>`replaceSiteSettings` | `site-settings set` | #139 | M2 | A | C |
| `POST /console-themes:validate`<br>`validateConsoleThemePackage` | `theme validate --remote` | #139 | M2 | A | W |
| `POST /console-themes`<br>`installConsoleThemePackage` | `theme install --remote` | #139 | M2 | A | W |
| `PUT /console-themes/{themeId}`<br>`replaceConsoleThemePackage` | `theme replace --remote <id>` | #139 | M2 | A | C |
| `DELETE /console-themes/{themeId}`<br>`deleteConsoleThemePackage` | `theme delete --remote <id>` | #139 | M2 | A | C |
| `GET /security-settings`<br>`getSecuritySettings` | `security-settings show` | #139 | M2 | A | Q |
| `GET /workload-identity/issuers`<br>`listWorkloadIssuers` | `workload issuer list` | #68 | M2 implemented | A | Q |
| `POST /workload-identity/issuers`<br>`createWorkloadIssuer` | `workload issuer create` | #68 | M2 implemented | A | W |
| `GET /workload-identity/issuers/{issuerId}`<br>`getWorkloadIssuer` | `workload issuer show <id>` | #68 | M2 implemented | A | Q |
| `PATCH /workload-identity/issuers/{issuerId}`<br>`updateWorkloadIssuer` | `workload issuer update <id>` | #68 | M2 implemented | A | C |
| `DELETE /workload-identity/issuers/{issuerId}`<br>`deleteWorkloadIssuer` | `workload issuer delete <id>` | #68 | M2 implemented | A | W |
| `POST /workload-identity/issuers:probe`<br>`probeWorkloadIssuer` | `workload issuer probe` | #68 | M2 implemented | A | Q |
| `GET /workload-identity/policies`<br>`listWorkloadTrustPolicies` | `workload policy list` | #68 | M2 implemented | A | Q |
| `POST /workload-identity/policies`<br>`createWorkloadTrustPolicy` | `workload policy create` | #68 | M2 implemented | A | W |
| `GET /workload-identity/policies/{policyId}`<br>`getWorkloadTrustPolicy` | `workload policy show <id>` | #68 | M2 implemented | A | Q |
| `PATCH /workload-identity/policies/{policyId}`<br>`updateWorkloadTrustPolicy` | `workload policy update <id>` | #68 | M2 implemented | A | C |
| `DELETE /workload-identity/policies/{policyId}`<br>`deleteWorkloadTrustPolicy` | `workload policy delete <id>` | #68 | M2 implemented | A | W |
| `POST /workload-identity/policies:test`<br>`testWorkloadTrustPolicy` | `workload policy test` | #68 | M2 implemented | A | Q |
| `POST /auth/token-exchange`<br>`exchangeWorkloadToken` | `auth login --workload-token-file FILE` (RFC 8693 exchange; ADR 0019) | #68 | M2 implemented | anonymous | S |
| `GET /authentication/providers`<br>`listIdentityProviders` | `idp list` | #139 | M2 | A | Q |
| `POST /authentication/providers`<br>`createIdentityProvider` | `idp create` | #139 | M2 | A | W |
| `GET /authentication/providers/{providerId}`<br>`getIdentityProvider` | `idp show <id>` | #139 | M2 | A | Q |
| `PUT /authentication/providers/{providerId}`<br>`replaceIdentityProvider` | `idp update <id>` | #139 | M2 | A | C |
| `DELETE /authentication/providers/{providerId}`<br>`deleteIdentityProvider` | `idp delete <id>` | #139 | M2 | A | C |
| `POST /authentication/providers/{providerId}:test`<br>`testIdentityProvider` | `idp test <id>` | #139 | M2 | A | W |
| `POST /authentication/providers/{providerId}:import-metadata`<br>`importIdentityProviderMetadata` | `idp import-metadata <id>` | #426 | M2 | A | W |
| `POST /authentication/providers/{providerId}:rotate-sp-key`<br>`rotateIdentityProviderSPKey` | `idp rotate-sp-key <id>` | #427 | M2 | A | W |
| `GET /authentication/providers/{providerId}/scim-tokens`<br>`listSCIMTokens` | N: SCIM token and account link API/Console only; CLI deferred | #545 | N/A | A | N: no CLI command registered; use the supported management API or Console |
| `POST /authentication/providers/{providerId}/scim-tokens`<br>`createSCIMToken` | N: SCIM token and account link API/Console only; CLI deferred | #545 | N/A | A | N: no CLI command registered; use the supported management API or Console |
| `DELETE /authentication/providers/{providerId}/scim-tokens/{tokenId}`<br>`revokeSCIMToken` | N: SCIM token and account link API/Console only; CLI deferred | #545 | N/A | A | N: no CLI command registered; use the supported management API or Console |
| `POST /authentication/providers/{providerId}/links`<br>`linkIdentityProviderAccount` | N: SCIM token and account link API/Console only; CLI deferred | #545 | N/A | A | N: no CLI command registered; use the supported management API or Console |
| `GET /auth/idp`<br>`listLoginIdentityProviders` | N: browser sign-in chooser; no CLI | #56 | N/A | PUBLIC | N: browser-only; no CLI command registered |
| `GET /auth/oidc/{slug}/login`<br>`startOIDCLogin` | N: browser Authorization Code redirect; no CLI | #56 | N/A | PUBLIC | N: browser-only; no CLI command registered |
| `GET /auth/oidc/{slug}/callback`<br>`completeOIDCLogin` | N: browser Authorization Code callback; no CLI | #56 | N/A | PUBLIC | N: browser-only; no CLI command registered |
| `GET /auth/saml/{slug}/login`<br>`startSAMLLogin` | N: browser SAML AuthnRequest redirect; no CLI | #425 | N/A | PUBLIC | N: browser-only; no CLI command registered |
| `POST /auth/saml/{slug}/acs`<br>`completeSAMLLogin` | N: browser SAML assertion consumer service; no CLI | #425 | N/A | PUBLIC | N: browser-only; no CLI command registered |
| `GET /auth/saml/{slug}/metadata`<br>`getSAMLServiceProviderMetadata` | N: identity-provider-facing SP metadata; no CLI | #425 | N/A | PUBLIC | N: IdP-facing XML document; no CLI command registered |
| `GET /webhook-subscriptions`<br>`listWebhookSubscriptions` | `webhook list` | #137 | M2 | A | Q |
| `POST /webhook-subscriptions`<br>`createWebhookSubscription` | `webhook create` | #137 | M2 | A | W |
| `GET /webhook-subscriptions/{subscriptionId}`<br>`getWebhookSubscription` | `webhook show <id>` | #137 | M2 | A | Q |
| `PUT /webhook-subscriptions/{subscriptionId}`<br>`updateWebhookSubscription` | `webhook update <id>` | #137 | M2 | A | C |
| `GET /remote-instances`<br>`listRemoteInstances` | `remote list` | #98 | M2 | A | Q |
| `POST /remote-instances`<br>`createRemoteInstance` | `remote add` | #98 | M2 | A | W |
| `GET /remote-instances/{remoteInstanceId}`<br>`getRemoteInstance` | `remote show <id>` | #98 | M2 | A | Q |
| `PUT /remote-instances/{remoteInstanceId}`<br>`updateRemoteInstance` | `remote update <id>` | #98 | M2 | A | C |
| `DELETE /remote-instances/{remoteInstanceId}`<br>`deleteRemoteInstance` | `remote delete <id>` | #98 | M2 | A | W |
| `POST /remote-instances/{remoteInstanceId}/test`<br>`testRemoteInstance` | `remote test <id>` | #98 | M2 | A | W |
| `GET /webhook-deliveries`<br>`listWebhookDeliveries` | `webhook delivery list` | #137 | M2 | A | Q |
| `GET /webhook-deliveries/{deliveryId}`<br>`getWebhookDelivery` | `webhook delivery show <id>` | #137 | M2 | A | Q |
| `POST /webhook-deliveries/{deliveryId}:replay`<br>`replayWebhookDelivery` | `webhook delivery replay <id>` | #137 | M2 | A | W |
| `GET /runtime/nodes`<br>`listRuntimeNodes` | `runtime nodes` | #135 | M1 implemented | A | Q |
| `GET /diagnostics`<br>`getDiagnostics` | `diagnostics` (status reuses the same read) | #135 | M1 implemented | A | Q |
| `GET /runtime/logs`<br>`listRuntimeLogs` | `runtime logs` | #135 | M1 implemented | A | L |
| `GET /api-keys`<br>`listApiKeys` | `api-key list` | #133 | M1 implemented | A | Q |
| `POST /api-keys`<br>`createApiKey` | `api-key create` | #133 | M1 implemented | A | S |
| `DELETE /api-keys/{apiKeyId}`<br>`revokeApiKey` | `api-key revoke <id>` | #133 | M1 implemented | A | W |
| `GET /service-accounts`<br>`listServiceAccounts` | `service-account list` (show derives from the list by ID) | #133 | M1 implemented | A | Q |
| `POST /service-accounts`<br>`createServiceAccount` | `service-account create` | #133 | M1 implemented | A | W |
| `PUT /service-accounts/{serviceAccountId}`<br>`updateServiceAccount` | `service-account update <id>` | #133 | M1 implemented | A | C |
| `GET /service-accounts/{serviceAccountId}/credentials`<br>`listServiceAccountCredentials` | `credential list --service-account <id>` | #133 | M1 implemented | A | Q |
| `POST /service-accounts/{serviceAccountId}/credentials`<br>`createServiceAccountCredential` | `credential issue --service-account <id>` | #133 | M1 implemented | A | S |
| `DELETE /service-accounts/{serviceAccountId}/credentials/{credentialId}`<br>`revokeServiceAccountCredential` | `credential revoke <id> --service-account <id>` | #133 | M1 implemented | A | W |
| `POST /service-accounts/{serviceAccountId}/assertions`<br>`mintServiceAccountAssertion` | N: the service account mints its own assertions from workload tooling; no operator command (ADR 0020) | #72 | N/A | I | N: no CLI command is registered; call the API with the service account credential |
| `GET /service-accounts/{serviceAccountId}/assertion-audiences`<br>`listServiceAccountAssertionAudiences` | `service-account assertion-audience list <id>` | #73 | M2 implemented | A | Q |
| `POST /service-accounts/{serviceAccountId}/assertion-audiences`<br>`putServiceAccountAssertionAudience` | `service-account assertion-audience add <id>` | #73 | M2 implemented | A | W |
| `DELETE /service-accounts/{serviceAccountId}/assertion-audiences`<br>`deleteServiceAccountAssertionAudience` | `service-account assertion-audience remove <id>` | #73 | M2 implemented | A | W |
| `GET /assertion-subjects`<br>`listAssertionSubjects` | `service-account assertion-subject list` | #559 | M2 implemented | A | Q |
| `DELETE /assertion-subjects/{subject}`<br>`releaseAssertionSubject` | `service-account assertion-subject release <subject>` | #559 | M2 implemented | A | W |
| `GET /issuer`<br>`getIssuerStatus` | `issuer status` | #73 | M2 implemented | A | Q |
| `POST /issuer/rotation`<br>`rotateIssuerKeys` | `issuer rotate` | #73 | M2 implemented | A | W |
| `POST /issuer/keys/{kid}/revoke`<br>`revokeIssuerKey` | `issuer revoke <kid>` | #73 | M2 implemented | A | W |
| `GET /users`<br>`listUsers` | `user list` | #335 | M1 implemented | A | Q |
| `POST /users`<br>`createUser` | `user create` | #335 | M1 implemented | A | W |
| `GET /users/{userId}`<br>`getUser` | `user show <id>` | #335 | M1 implemented | A | Q |
| `PATCH /users/{userId}`<br>`updateUser` | `user update <id>` | #335 | M1 implemented | A | W |
| `DELETE /users/{userId}`<br>`deleteUser` | `user delete <id>` | #335 | M1 implemented | A | W |
| `POST /users/{userId}/password`<br>`resetUserPassword` | `user reset-password <id>` | #335 | M1 implemented | A | C |
| `POST /users/{userId}/sessions:revoke`<br>`revokeUserSessions` | `user sessions revoke <id> --all` | #335 | M1 implemented | A | C |
| `GET /users/{userId}/sessions`<br>`listUserSessions` | `user sessions list <id>` | #335 | M1 implemented | A | Q |
| `DELETE /users/{userId}/sessions/{sessionId}`<br>`revokeUserSession` | `user sessions revoke <id> --session <sid>` | #335 | M1 implemented | A | W |
| `GET /users/{userId}/identities`<br>`listUserIdentities` | `user identities list <id>` | #335 | M1 implemented | A | Q |
| `POST /users/{userId}/identities`<br>`createUserIdentity` | `user identities link <id>` | #335 | M1 implemented | A | W |
| `DELETE /users/{userId}/identities/{identityId}`<br>`deleteUserIdentity` | `user identities unlink <id> --identity <iid>` | #335 | M1 implemented | A | W |
| `GET /audits`<br>`listAudits` | `audit list --bounded` | #135 | M1 implemented | A | Q |
| `GET /audits/page`<br>`listAuditPage` | `audit list --cursor <cursor>` | #135 | M1 implemented | A | Q |
| `GET /overview-statistics`<br>`getOverviewStatistics` | `overview show` | #135 | M1 implemented | FR | Q |
| `GET /audit-retention-policy`<br>`getAuditRetentionPolicy` | `audit retention show` | #138 | M2 | A | Q |
| `PUT /audit-retention-policy`<br>`replaceAuditRetentionPolicy` | `audit retention set` | #138 | M2 | A | C |
| `POST /audit-retention:execute`<br>`executeAuditRetention` | `audit retention execute` | #138 | M2 | A | W |
| `GET /audit-retention/jobs`<br>`listAuditRetentionJobs` | `audit retention jobs` | #138 | M2 | A | Q |
| `GET /repositories`<br>`listRepositories` | `repo list` | #130 | M1 implemented | FR | Q |
| `POST /repositories`<br>`createRepository` | `repo create` | #130 | M1 implemented | A | W |
| `GET /repository-grants`<br>`listRepositoryGrants` | `grant list --all-repositories` | #134 | M1 implemented | FA | Q |
| `GET /authorization-templates`<br>`listAuthorizationTemplates` | `template list` | #134 | M1 implemented | A | Q |
| `POST /authorization-templates`<br>`createAuthorizationTemplate` | `template create` | #134 | M2 implemented | A | W |
| `GET /authorization-templates/{templateId}`<br>`getAuthorizationTemplate` | `template show <id>` | #134 | M1 implemented | A | Q |
| `PUT /authorization-templates/{templateId}`<br>`updateAuthorizationTemplate` | `template update <id>` | #134 | M2 implemented | A | C |
| `DELETE /authorization-templates/{templateId}`<br>`deleteAuthorizationTemplate` | `template delete <id>` | #134 | M2 implemented | A | W |
| `POST /authorization-templates/{templateId}/apply`<br>`applyAuthorizationTemplate` | `template apply <id> --repo <id>` | #134 | M1 implemented | RA | G |
| `GET /authorization-roles`<br>`listAuthorizationRoles` | `role list` | #134 | M1 implemented | A | Q |
| `POST /authorization-roles`<br>`createAuthorizationRole` | `role create` | #134 | M2 implemented | A | W |
| `GET /authorization-roles/{roleId}`<br>`getAuthorizationRole` | `role show <id>` | #134 | M1 implemented | A | Q |
| `PUT /authorization-roles/{roleId}`<br>`updateAuthorizationRole` | `role update <id>` | #134 | M2 implemented | A | C |
| `DELETE /authorization-roles/{roleId}`<br>`deleteAuthorizationRole` | `role delete <id>` | #134 | M2 implemented | A | W |
| `GET /user-groups`<br>`listUserGroups` | `user-group list` | #557 | M2 implemented | A | Q |
| `POST /user-groups`<br>`createUserGroup` | `user-group create` | #557 | M2 implemented | A | W |
| `GET /user-groups/{groupId}`<br>`getUserGroup` | `user-group show <id>` | #557 | M2 implemented | A | Q |
| `PUT /user-groups/{groupId}`<br>`renameUserGroup` | `user-group rename <id> <name>` | #557 | M2 implemented | A | C |
| `DELETE /user-groups/{groupId}`<br>`deleteUserGroup` | `user-group delete <id>` | #557 | M2 implemented | A | W |
| `PUT /user-groups/{groupId}/global-role`<br>`setUserGroupGlobalRole` | `user-group set-role <id>` | #557 | M2 implemented | A | C |
| `GET /user-groups/{groupId}/members`<br>`listUserGroupMembers` | `user-group member list <id>` | #557 | M2 implemented | A | Q |
| `POST /user-groups/{groupId}/members`<br>`addUserGroupMember` | `user-group member add <id> <user>` | #557 | M2 implemented | A | W |
| `DELETE /user-groups/{groupId}/members/{userId}`<br>`removeUserGroupMember` | `user-group member remove <id> <user>` | #557 | M2 implemented | A | W |
| `GET /repository-capacities`<br>`listRepositoryCapacities` | `capacity list` | #138 | M2 | FA | Q |
| `GET /lifecycle-jobs`<br>`listLifecycleJobs` | `lifecycle-job list --all-repositories` | #135 | M1 implemented | FA | Q |
| `GET /scheduled-tasks`<br>`listScheduledTasks` | `scheduled-task list` | #137 | M2 | ST | Q |
| `POST /scheduled-tasks`<br>`createScheduledTask` | `scheduled-task create` | #137 | M2 | ST | W |
| `GET /scheduled-tasks/{taskId}`<br>`getScheduledTask` | `scheduled-task show <id>` | #137 | M2 | ST | Q |
| `PUT /scheduled-tasks/{taskId}`<br>`updateScheduledTask` | `scheduled-task update <id>` | #137 | M2 | ST | C |
| `DELETE /scheduled-tasks/{taskId}`<br>`deleteScheduledTask` | `scheduled-task delete <id>` | #137 | M2 | ST | W |
| `POST /scheduled-tasks/{taskId}/run`<br>`runScheduledTask` | `scheduled-task run <id>` | #137 | M2 | ST | J |
| `GET /scheduled-tasks/{taskId}/runs`<br>`listScheduledTaskRuns` | `scheduled-task runs <id>` | #137 | M2 | ST | Q |
| `GET /repositories/{repositoryId}`<br>`getRepository` | `repo show <id>` | #130 | M1 implemented | R | Q |
| `DELETE /repositories/{repositoryId}`<br>`deleteRepository` | `repo delete <id>` (asynchronous deleting → deleted) | #130 | M1 implemented | RA | J |
| `PATCH /repositories/{repositoryId}`<br>`updateRepository` | `repo update <id>` | #130 | M1 implemented | RA | C |
| `POST /repositories/{repositoryId}/egress-proxy:test`<br>`testEgressProxy` | `egress-proxy test --repo <id>` | #139 | M2 | RA | W |
| `GET /repositories/{repositoryId}/grants`<br>`listGrants` | `grant list --repo <id>` | #134 | M1 implemented | RA | Q |
| `PUT /repositories/{repositoryId}/grants`<br>`replaceGrants` | not exposed as a whole-table PUT; use single-row grant writes or `template apply` | #134 | N/A | RA | N |
| `POST /repositories/{repositoryId}/grants`<br>`upsertGrant` | `grant upsert --repo <id>` | #134 | M1 implemented | RA | G |
| `DELETE /repositories/{repositoryId}/grants`<br>`deleteGrant` | `grant delete --repo <id>` | #134 | M1 implemented | RA | G |
| `GET /repositories/{repositoryId}/retention-policy`<br>`getRetentionPolicy` | `policy retention show --repo <id>` | #139 | M2 | R | Q |
| `PUT /repositories/{repositoryId}/retention-policy`<br>`replaceRetentionPolicy` | `policy retention set --repo <id>` | #139 | M2 | RA | C |
| `GET /repositories/{repositoryId}/security-policy`<br>`getSecurityPolicy` | `policy security show --repo <id>` | #139 | M2 | RA | Q |
| `PUT /repositories/{repositoryId}/security-policy`<br>`replaceSecurityPolicy` | `policy security set --repo <id>` | #139 | M2 | RA | C |
| `POST /repositories/{repositoryId}/security-policy:evaluate`<br>`evaluateSecurityPolicy` | `policy security evaluate --repo <id>` | #139 | M2 | RA | D |
| `GET /repositories/{repositoryId}/quarantine-read-policy`<br>`getQuarantineReadPolicy` | `policy quarantine-read show --repo <id>` | #139 | M2 | RA | Q |
| `PUT /repositories/{repositoryId}/quarantine-read-policy`<br>`replaceQuarantineReadPolicy` | `policy quarantine-read set --repo <id>` | #139 | M2 | RA | C |
| `GET /repositories/{repositoryId}/capacity`<br>`getRepositoryCapacity` | `capacity show --repo <id>` | #138 | M2 | R | Q |
| `PUT /repositories/{repositoryId}/capacity`<br>`replaceRepositoryCapacity` | `capacity quota set --repo <id>` | #138 | M2 | RA | W |
| `POST /repositories/{repositoryId}/retention:dry-run`<br>`dryRunRepositoryRetention` | `retention preview --repo <id>` | #138 | M2 | RA | D |
| `POST /repositories/{repositoryId}/retention:execute`<br>`executeRepositoryRetention` | `retention execute --repo <id>` | #138 | M2 | RA | C |
| `POST /repositories/{repositoryId}/promotions`<br>`createRepositoryPromotion` | `promotion create --repo <source> --target <target>` | #138 | M2 | RA×2 | W |
| `POST /repositories/{repositoryId}/replications`<br>`createRepositoryReplication` | `replication create --repo <source> --target <target>` | #138 | M2 | RA×2 | J |
| `GET /repositories/{repositoryId}/replications`<br>`listRepositoryReplications` | `replication list --repo <id>` | #138 | M2 | RA | Q |
| `GET /repositories/{repositoryId}/replications/{replicationPlanId}`<br>`getRepositoryReplication` | `replication show <plan> --repo <id>` | #138 | M2 | RA | Q |
| `DELETE /repositories/{repositoryId}/replications/{replicationPlanId}`<br>`deleteRepositoryReplication` | `replication cancel <plan> --repo <id>` | #138 | M2 | RA | W |
| `POST /repositories/{repositoryId}/restore`<br>`restoreRepositoryArtifact` | `artifact restore --repo <id>` | #138 | M2 | RA | W |
| `GET /repositories/{repositoryId}/lifecycle-jobs`<br>`listRepositoryLifecycleJobs` | `lifecycle-job list --repo <id>` (show/wait page the same list by ID until found or exhausted) | #135 | M1 implemented | RA | Q |
| `GET /repositories/{repositoryId}/artifact-scans`<br>`getRepositoryArtifactScanStatus` | `artifact scan status --repo <id>` | #137 | M2 | RI | Q |
| `POST /repositories/{repositoryId}/artifact-scans`<br>`createRepositoryArtifactScan` | `artifact scan enqueue --repo <id>` | #137 | M2 | RI | J |
| `POST /repositories/{repositoryId}/artifact-scans:reconcile`<br>`reconcileRepositoryArtifactScans` | `artifact scan reconcile --repo <id>` | #137 | M2 | RI | W |
| `POST /repositories/{repositoryId}/lifecycle-jobs:reconcile-intelligence`<br>`reconcileRepositoryArtifactIntelligence` | `lifecycle-job reconcile-intelligence --repo <id>` | #137 | M2 | RA | W |
| `POST /repositories/{repositoryId}/lifecycle-jobs/{lifecycleJobId}/run`<br>`runRepositoryLifecycleJobNow` | `lifecycle-job run --repo <id> --id <job>` | #137 | M2 | RA | W |
| `POST /repositories/{repositoryId}/lifecycle-jobs/{lifecycleJobId}/retry`<br>`retryRepositoryLifecycleJob` | `lifecycle-job retry --repo <id> --id <job>` | #137 | M2 | RA | W |
| `POST /repositories/{repositoryId}/lifecycle-jobs/{lifecycleJobId}/cancel`<br>`cancelRepositoryLifecycleJob` | `lifecycle-job cancel --repo <id> --id <job>` | #137 | M2 | RA | W |
| `GET /repositories/{repositoryId}/tombstones`<br>`listRepositoryTombstones` | `tombstone list --repo <id>` | #138 | M2 | RA | Q |
| `POST /repositories/{repositoryId}/tombstones`<br>`tombstoneRepositoryArtifact` | `artifact tombstone --repo <id>` | #138 | M2 | RA | W |
| `GET /repositories/{repositoryId}/capabilities`<br>`getRepositoryCapabilities` | `repo capabilities <id>` | #130 | M1 implemented | R | Q |
| `GET /repositories/{repositoryId}/packages`<br>`listRawPackages` | `generic list --repo <id>` | #223 | M2 implemented | R | Q |
| `GET /repositories/{repositoryId}/packages/{name}/versions`<br>`listRawVersions` | `generic list --repo <id> --name <package>` | #223 | M2 implemented | R | Q: state, property and stale-open (`--older-than-days`) filters, keyset cursor |
| `GET /repositories/{repositoryId}/packages/{name}/versions/{version}`<br>`getRawVersion` | `generic show --repo <id>` | #223 | M2 implemented | R | Q |
| `PUT /repositories/{repositoryId}/packages/{name}/versions/{version}`<br>`createRawVersion` | `generic push --repo <id>` | #223 | M2 implemented | W | U: properties set before the upload; an idempotent create |
| `PUT /repositories/{repositoryId}/packages/{name}/versions/{version}/properties`<br>`setRawVersionProperties` | `generic properties --repo <id>` | #223 | M2 implemented | W | W: open versions only; a sealed version answers 409 |
| `POST /repositories/{repositoryId}/packages/{name}/versions/{version}:seal`<br>`sealRawVersion` | `generic seal --repo <id>` | #223 | M2 implemented | W | U: expected file list and checksums file verified by the server; seal twice returns one digest |
| `GET /repositories/{repositoryId}/packages/{name}/versions/{version}/manifest`<br>`getRawVersionManifest` | `generic show --repo <id> --manifest` | #223 | M2 implemented | R | Q: the digest of the printed bytes is the manifest digest |
| `GET /repositories/{repositoryId}/packages/{name}/versions/{version}/checksums`<br>`getRawVersionChecksums` | `generic show --repo <id> --checksums` | #223 | M2 implemented | R | Q: verified with `sha256sum -c` |
| `GET /repositories/{repositoryId}/effective-access`<br>`getRepositoryEffectiveAccess` | `access check --repo <id>` | #134 | M1 implemented | I/A | Q |
| `GET /groups`<br>`listGroups` | `group list` | #130 | M1 implemented | A | Q |
| `POST /groups`<br>`createGroup` | `group create` | #130 | M1 implemented | G | W |
| `GET /groups/{groupId}`<br>`getGroup` | `group show <id>` | #130 | M1 implemented | A | Q |
| `PUT /groups/{groupId}`<br>`replaceGroup` | `group update <id>` | #130 | M1 implemented | G | C |
| `DELETE /groups/{groupId}`<br>`deleteGroup` | `group delete <id>` | #130 | M1 implemented | G | W |
| `GET /groups/{groupId}/browse`<br>`browseGroup` | `group browse <id>` | #130 | M1 implemented | R* | Q |
| `GET /groups/{groupId}/members`<br>`listGroupMembers` | `group members list <id>` | #130 | M1 implemented | A | Q |
| `PUT /groups/{groupId}/members`<br>`replaceGroupMembers` | `group members set <id>` | #130 | M1 implemented | G | C |
| `GET /groups/{groupId}/capacity`<br>`getGroupCapacity` | `capacity show --group <id>` | #138 | M2 | A | Q |
| `GET /groups/{groupId}/resolution`<br>`getGroupResolution` | `group resolution <id>` | #130 | M1 implemented | A | Q |
| `POST /repositories/{repositoryId}/publish-sessions`<br>`createPublishSession` | `publish create --repo <id>` | #132 | M1 implemented | W | U |
| `GET /repositories/{repositoryId}/artifacts`<br>`listArtifacts` | `artifact list --repo <id>` | #131 | M1 implemented | R* | Q |
| `GET /repositories/{repositoryId}/artifact-usage`<br>`listRepositoryArtifactUsage` | `artifact usage --repo <id>` | #131 | M1 implemented | R* | Q |
| `GET /repositories/{repositoryId}/artifact-search`<br>`searchRepositoryArtifacts` | `artifact search --repo <id>` | #131 | M1 implemented | R* | Q |
| `GET /repositories/{repositoryId}/browse`<br>`browseRepository` | `artifact browse --repo <id>` (repo browse alias) | #131 | M1 implemented | R* | Q |
| `GET /repositories/{repositoryId}/artifact-identities`<br>`listRepositoryArtifactIdentities` | `artifact identities --repo <id>` | #131 | M1 implemented | R | Q |
| `GET /repositories/{repositoryId}/artifacts/{artifactId}`<br>`getArtifact` | `artifact show <id> --repo <id>` | #131 | M1 implemented | R* | Q |
| `DELETE /repositories/{repositoryId}/artifacts/{artifactId}`<br>`deleteArtifact` | `artifact delete <id> --repo <id>` | #131 | M1 implemented | W | W |
| `GET /repositories/{repositoryId}/artifact-intelligence`<br>`getArtifactIntelligence` | `artifact intelligence show --repo <id>` | #131 | M1 implemented | R* | Q |
| `PUT /repositories/{repositoryId}/artifact-intelligence`<br>`replaceArtifactIntelligence` | `artifact intelligence set --repo <id>` | #138 | M2 | RI | C |
| `GET /repositories/{repositoryId}/artifact-quarantine`<br>`getArtifactQuarantine` | `artifact quarantine show --repo <id>` | #131 | M1 implemented | R | Q |
| `PUT /repositories/{repositoryId}/artifact-quarantine`<br>`replaceArtifactQuarantine` | `artifact quarantine set --repo <id>` | #138 | M2 | RA | C |
| `GET /repositories/{repositoryId}/pypi/projects/{project}/versions/{version}/yank`<br>`getPyPIYankState` | `pypi yank show --repo <id>` | #311 | M2 planned | R* | Q |
| `PATCH /repositories/{repositoryId}/pypi/projects/{project}/versions/{version}/yank`<br>`updatePyPIYankState` | `pypi yank set --repo <id>` | #311 | M2 planned | W | C |
| `GET /repositories/{repositoryId}/oci/images`<br>`listOCIImages` | `oci images --repo <id>` | #131 | M1 implemented | R* | Q |
| `GET /repositories/{repositoryId}/oci/manifests`<br>`listOCIManifests` | `oci manifests --repo <id>` | #131 | M1 implemented | R* | Q |
| `GET /repositories/{repositoryId}/maven/coordinates`<br>`listMavenCoordinates` | `maven coordinates --repo <id>` | #131 | M1 implemented | R* | Q |
| `POST /repositories/{repositoryId}/apt/publication-sessions`<br>`createAPTPublicationSession` | `apt publication-session create --repo <id>` | #138 | M2 | W | W |
| `GET /repositories/{repositoryId}/apt/publication-sessions/{sessionId}`<br>`getAPTPublicationSession` | `apt publication-session show --repo <id> --id <session>` | #138 | M2 | W | Q |
| `PUT /repositories/{repositoryId}/apt/publication-sessions/{sessionId}/package`<br>`uploadAPTPublicationPackage` | `apt publication-session upload --repo <id> --id <session>` | #138 | M2 | W | W |
| `GET /repositories/{repositoryId}/apt/lifecycle`<br>`getAPTLifecycleState` | `apt lifecycle show` (snapshot list reuses snapshots) | #138 | M2 | RA | Q |
| `POST /repositories/{repositoryId}/apt/lifecycle`<br>`applyAPTLifecycle` | `apt lifecycle apply --repo <id>` | #138 | M2 | RA | W |
| `POST /repositories/{repositoryId}/apt/lifecycle/preview`<br>`previewAPTLifecycle` | `apt lifecycle preview --repo <id>` | #138 | M2 | RA | D |
| `POST /repositories/{repositoryId}/apt/snapshots/prune`<br>`pruneAPTSnapshots` | `apt snapshot prune --repo <id>` | #138 | M2 | RA | W |
| `POST /repositories/{repositoryId}/apt/snapshots`<br>`publishAPTRepositorySnapshot` | `apt snapshot publish --repo <id>` | #138 | M2 | W | W |
| `POST /repositories/{repositoryId}/apt/snapshots/restore`<br>`restoreAPTRepositorySnapshot` | `apt snapshot restore --repo <id>` | #138 | M2 | RA | W |
| `GET /repositories/{repositoryId}/apt/snapshots/{snapshotId}/archive`<br>`exportAPTRepositorySnapshot` | `apt snapshot archive --repo <id> --output <file>` | #138 | M2 | RA | Q |
| `GET /repositories/{repositoryId}/apt/signing-state`<br>`getAPTRepositorySigningState` | `apt signing-state --repo <id>` | #138 | M2 | RA | Q |
| `GET /repositories/{repositoryId}/conan/references`<br>`listConanReferences` | `conan references --repo <id>` | #131 | M2 implemented | R* | Q |
| `GET /repositories/{repositoryId}/conan/recipe-revisions`<br>`listConanRecipeRevisions` | `conan recipe-revisions --repo <id>` | #131 | M2 implemented | R* | Q |
| `GET /repositories/{repositoryId}/conan/package-revisions`<br>`listConanPackageRevisions` | `conan package-revisions --repo <id>` | #131 | M2 implemented | R* | Q |
| `GET /repositories/{repositoryId}/conan/package-ids`<br>`listConanPackageIds` | `conan package-ids --repo <id>` | #131 | M2 implemented | R* | Q |
| `DELETE /repositories/{repositoryId}/conan/package-revisions/{revision}`<br>`deleteConanPackageRevision` | `conan package-revision delete --repo <id>` | #131 | M2 implemented | W | W |
| `GET /repositories/{repositoryId}/cache/entries`<br>`listProxyCacheEntries` | `cache entries --repo <id>` | #137 | M2 | R* | Q |
| `POST /repositories/{repositoryId}/cache/invalidate`<br>`invalidateProxyCache` | `cache invalidate --repo <id>` | #137 | M2 | RA | W |
| `POST /repositories/{repositoryId}/cache/negative:clear`<br>`clearProxyNegativeCache` | `cache clear-negative --repo <id>` | #137 | M2 | RA | W |
| `POST /repositories/{repositoryId}/cache/refresh`<br>`refreshProxyCache` | `cache refresh --repo <id>` | #137 | M2 | RA | W |
| `GET /repositories/{repositoryId}/proxy/health`<br>`getProxyHealth` | `proxy health --repo <id>` | #137 | M2 | R | Q |
| `POST /repositories/{repositoryId}/conan-publish-sessions`<br>`createConanPublishSession` | not exposed: Conan session orchestration is outside the CLI M1 scope; official Conan 2 upload owns native publication | #132 / #312 | N/A | W | N |
| `GET /conan-publish-sessions/{sessionId}`<br>`getConanPublishSession` | not exposed: Conan session orchestration is outside the CLI M1 scope; official Conan 2 upload owns native publication | #132 / #312 | N/A | R | N |
| `PUT /conan-publish-sessions/{sessionId}/objects/{objectName}`<br>`uploadConanPublishObject` | not exposed: Conan session orchestration is outside the CLI M1 scope; official Conan 2 upload owns native publication | #132 / #312 | N/A | W | N |
| `POST /conan-publish-sessions/{sessionId}:commit`<br>`commitConanPublishSession` | not exposed: Conan session orchestration is outside the CLI M1 scope; official Conan 2 upload owns native publication | #132 / #312 | N/A | W | N |
| `GET /publish-sessions/{sessionId}`<br>`getPublishSession` | `publish status <session>` | #132 | M1 implemented | R | U |
| `PUT /publish-sessions/{sessionId}/objects/{objectName}`<br>`uploadPublishObject` | `publish upload <session>` | #132 | M1 implemented | W | U |
| `POST /publish-sessions/{sessionId}:commit`<br>`commitPublishSession` | `publish commit <session>` | #132 | M1 implemented | W | U |

## Additional protocol operations in the native bundle

Management operations repeat in the native bundle and reuse the table above;
they are not implemented twice. The 48 additional operations below are
classified one by one. Native URLs follow each protocol's server override and
real routes and must not be joined to `/api/v2` blindly. Retired Nexus repository aliases are not part of this contract.

| Protocol API / operationId | Planned command / handling | Owning issue | Phase | Permission | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `GET /npm/{repository}/{package}`<br>`getNPMRegistryPackage` | Native npm metadata and governance; no duplicate notarxctl registry client | #309 / #310 | N/A | R | N: official npm owns the protocol operation |
| `PUT /npm/{repository}/{package}`<br>`putNPMRegistryPackage` | Native npm metadata and governance; no duplicate notarxctl registry client | #309 | N/A | W | N: official npm owns the protocol operation |
| `PUT /npm/{repository}/{package}/-rev/{revision}`<br>`unpublishNPMRegistryVersions` | Native npm metadata and governance; no duplicate notarxctl registry client | #310 | N/A | RA | N: official npm owns the protocol operation |
| `DELETE /npm/{repository}/{package}/-rev/{revision}`<br>`unpublishNPMRegistryPackage` | Native npm metadata and governance; no duplicate notarxctl registry client | #310 | N/A | RA | N: official npm owns the protocol operation |
| `DELETE /npm/{repository}/{package}/-/{tarball}/-rev/{revision}`<br>`acknowledgeNPMUnpublishTarball` | Native npm metadata and governance; no duplicate notarxctl registry client | #310 | N/A | RA | N: official npm owns the protocol operation |
| `GET /npm/{repository}/-/package/{package}/dist-tags`<br>`listNPMDistTags` | native npm client manages dist-tags; notarxctl does not orchestrate this protocol action | #308 | N/A | native-format authorization | N: npm dist-tag owns this action; no separate notarxctl command |
| `HEAD /npm/{repository}/-/package/{package}/dist-tags`<br>`headNPMDistTags` | native npm client manages dist-tags; notarxctl does not orchestrate this protocol action | #308 | N/A | native-format authorization | N: npm dist-tag owns this action; no separate notarxctl command |
| `PUT /npm/{repository}/-/package/{package}/dist-tags/{tag}`<br>`setNPMDistTag` | native npm client manages dist-tags; notarxctl does not orchestrate this protocol action | #308 | N/A | native-format authorization | N: npm dist-tag owns this action; no separate notarxctl command |
| `DELETE /npm/{repository}/-/package/{package}/dist-tags/{tag}`<br>`deleteNPMDistTag` | native npm client manages dist-tags; notarxctl does not orchestrate this protocol action | #308 | N/A | native-format authorization | N: npm dist-tag owns this action; no separate notarxctl command |
| `GET /conan/{repository}/v1/ping`<br>`pingConanRemote` | native Conan 2 client; `repo connect` onboarding | #312 | N/A | native-format authorization | N: official Conan client owns the wire workflow |
| `GET /conan/{repository}/v2/users/authenticate`<br>`authenticateConanRemote` | native Conan 2 client; `repo connect` onboarding | #312 | N/A | native-format authorization | N: official Conan client owns the wire workflow |
| `GET /conan/{repository}/v2/users/check_credentials`<br>`checkConanCredentials` | native Conan 2 client; `repo connect` onboarding | #312 | N/A | native-format authorization | N: official Conan client owns the wire workflow |
| `GET /conan/{repository}/v2/conans/{path}`<br>`readConanRepresentation` | native Conan 2 client; `repo connect` onboarding | #312 | N/A | native-format authorization | N: official Conan client owns the wire workflow |
| `HEAD /conan/{repository}/v2/conans/{path}`<br>`headConanRepresentation` | native Conan 2 client; `repo connect` onboarding | #312 | N/A | native-format authorization | N: official Conan client owns the wire workflow |
| `PUT /conan/{repository}/v2/conans/{name}/{version}/{user}/{channel}/revisions/{recipeRevision}/files/{file}`<br>`uploadConanRecipeFile` | native Conan 2 client; `repo connect` onboarding | #312 | N/A | native-format authorization | N: official Conan client owns the wire workflow |
| `PUT /conan/{repository}/v2/conans/{name}/{version}/{user}/{channel}/revisions/{recipeRevision}/packages/{packageId}/revisions/{packageRevision}/files/{file}`<br>`uploadConanPackageFile` | native Conan 2 client; `repo connect` onboarding | #312 | N/A | native-format authorization | N: official Conan client owns the wire workflow |
| `GET /raw/{repository}/{path}`<br>`readRawContent` | `publish raw --resumable` (upload status) | #132 | M2 implemented | R / W | U: verified target, uploadId and offset; native content download remains a native-client operation |
| `HEAD /raw/{repository}/{path}`<br>`headRawContent` | `publish raw` | #132 | M1 implemented | R | U: digest/size read-back without object bytes |
| `PUT /raw/{repository}/{path}`<br>`putRawHostedContent` | `publish raw` | #132 | M1 implemented | W | U: size/digest plus conflict and read-back |
| `POST /raw/{repository}/{path}`<br>`startRawHostedResumableUpload` | `publish raw --resumable` | #132 | M2 implemented | W | U: offset/digest and interrupted replay |
| `PATCH /raw/{repository}/{path}`<br>`appendRawHostedResumableUpload` | `publish raw --resumable` | #132 | M2 implemented | W | U: offset/digest and interrupted replay |
| `DELETE /raw/{repository}/{path}`<br>`deleteRawHostedContent` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `GET /raw/{repository}/`<br>`listRawRootDirectory` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `GET /apt/{repository}/{path}`<br>`getAptAsset` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `HEAD /apt/{repository}/{path}`<br>`headAptAsset` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `GET /hf/{repository}/{org}/{name}/resolve/{revision}/{path}`<br>`resolveHuggingFaceModelFile` | no separate CLI protocol client; native client and onboarding configuration | #165 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `HEAD /hf/{repository}/{org}/{name}/resolve/{revision}/{path}`<br>`headHuggingFaceModelFile` | no separate CLI protocol client; native client and onboarding configuration | #165 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `GET /hf/{repository}/datasets/{org}/{name}/resolve/{revision}/{path}`<br>`resolveHuggingFaceDatasetFile` | no separate CLI protocol client; native client and onboarding configuration | #165 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `HEAD /hf/{repository}/datasets/{org}/{name}/resolve/{revision}/{path}`<br>`headHuggingFaceDatasetFile` | no separate CLI protocol client; native client and onboarding configuration | #165 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `GET /hf/{repository}/api/{repoType}/{org}/{name}/xet-read-token/{revision}`<br>`getHuggingFaceXetReadToken` | no separate CLI protocol client; native client and onboarding configuration | #165 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `GET /v2/_catalog`<br>`listOCICatalog` | Native ORAS/OCI discovery; no separate notarxctl registry client | #315 | N/A | explicit repository-resource discovery authorization | N: official OCI clients own Catalog/Referrers; notarxctl neither registers nor sends this protocol action |
| `HEAD /v2/_catalog`<br>`headOCICatalog` | Native ORAS/OCI discovery; no separate notarxctl registry client | #315 | N/A | explicit repository-resource discovery authorization | N: official OCI clients own Catalog/Referrers; notarxctl neither registers nor sends this protocol action |
| `GET /v2/{name}/referrers/{digest}`<br>`listOCIReferrers` | Native ORAS/OCI discovery; no separate notarxctl registry client | #316 | N/A | explicit repository-resource discovery authorization | N: official OCI clients own Catalog/Referrers; notarxctl neither registers nor sends this protocol action |
| `HEAD /v2/{name}/referrers/{digest}`<br>`headOCIReferrers` | Native ORAS/OCI discovery; no separate notarxctl registry client | #316 | N/A | explicit repository-resource discovery authorization | N: official OCI clients own Catalog/Referrers; notarxctl neither registers nor sends this protocol action |
| `POST /v2/{name}/blobs/uploads/`<br>`startOCIUpload` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `GET /v2/{name}/blobs/uploads/{uuid}`<br>`getOCIUploadStatus` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `PATCH /v2/{name}/blobs/uploads/{uuid}`<br>`appendOCIUpload` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `PUT /v2/{name}/blobs/uploads/{uuid}`<br>`completeOCIUpload` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `DELETE /v2/{name}/blobs/uploads/{uuid}`<br>`cancelOCIUpload` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `GET /v2/{name}/blobs/{digest}`<br>`readOCIBlob` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `HEAD /v2/{name}/blobs/{digest}`<br>`headOCIBlob` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `GET /scim/v2/ServiceProviderConfig`<br>`getSCIMServiceProviderConfig` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `GET /scim/v2/Schemas`<br>`listSCIMSchemas` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `GET /scim/v2/Schemas/{id}`<br>`getSCIMSchema` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `GET /scim/v2/ResourceTypes`<br>`listSCIMResourceTypes` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `GET /scim/v2/ResourceTypes/{id}`<br>`getSCIMResourceType` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `GET /scim/v2/Users`<br>`listSCIMUsers` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `POST /scim/v2/Users`<br>`createSCIMUser` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `GET /scim/v2/Users/{id}`<br>`getSCIMUser` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `PUT /scim/v2/Users/{id}`<br>`replaceSCIMUser` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `PATCH /scim/v2/Users/{id}`<br>`patchSCIMUser` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `DELETE /scim/v2/Users/{id}`<br>`deleteSCIMUser` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `GET /scim/v2/Groups`<br>`listSCIMGroups` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `POST /scim/v2/Groups`<br>`createSCIMGroup` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `GET /scim/v2/Groups/{id}`<br>`getSCIMGroup` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `PUT /scim/v2/Groups/{id}`<br>`replaceSCIMGroup` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `PATCH /scim/v2/Groups/{id}`<br>`patchSCIMGroup` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `DELETE /scim/v2/Groups/{id}`<br>`deleteSCIMGroup` | N: identity-provider-facing SCIM 2.0 protocol; no CLI | #60 | N/A | SCIM bearer token | N: the CLI neither registers nor sends this protocol action |
| `GET /v2/{name}/manifests/{reference}`<br>`readOCIManifest` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `HEAD /v2/{name}/manifests/{reference}`<br>`headOCIManifest` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `PUT /v2/{name}/manifests/{reference}`<br>`putOCIManifest` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `DELETE /v2/{name}/manifests/{reference}`<br>`deleteOCIManifest` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `GET /v2/{name}/tags/list`<br>`listOCITags` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `HEAD /v2/{name}/tags/list`<br>`headOCITags` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `GET /repository/maven/{repository}/{assetPath}`<br>`readMavenAsset` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `PUT /repository/maven/{repository}/{assetPath}`<br>`stageMavenProtocolAsset` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `POST /repository/maven/{repository}/coordinates/{coordinate}:commit`<br>`commitMavenCoordinate` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `GET /go/{repository}/{modulePath}`<br>`readGoModuleRepresentation` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `HEAD /go/{repository}/{modulePath}`<br>`headGoModuleRepresentation` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `PUT /go/{repository}/{modulePath}`<br>`publishGoHostedModule` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `GET /.well-known/openid-configuration`<br>`getOpenIDConfiguration` | no separate CLI protocol client; anonymous OIDC discovery for verifiers (ADR 0020) | #71 | N/A | anonymous | N: the CLI neither registers nor sends this protocol action |
| `GET /.well-known/notarx`<br>`getInstanceDocument` | no separate CLI protocol client; anonymous instance document read by senders through `remote add` and `remote test` (ADR 0021) | #83 | N/A | anonymous | N: the CLI neither registers nor sends this protocol action |
| `GET /.well-known/jwks.json`<br>`getIssuerJWKS` | no separate CLI protocol client; anonymous key set for verifiers (ADR 0020) | #71 | N/A | anonymous | N: the CLI neither registers nor sends this protocol action |
| `GET /cargo/{repository}/{path}`<br>`getCargoRegistryRepresentation` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `HEAD /cargo/{repository}/{path}`<br>`headCargoRegistryRepresentation` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `PUT /cargo/{repository}/{path}`<br>`putCargoHostedRegistryMutation` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |
| `DELETE /cargo/{repository}/{path}`<br>`yankCargoHostedCrateVersion` | no separate CLI protocol client; native client and onboarding configuration | #130 | N/A | native-format authorization | N: the CLI neither registers nor sends this protocol action |

This does not block the Maven/OCI/Conan management views already served under
`/api/v2`, and it does not turn the earlier "as needed" protocol-client helpers
into a first-release commitment. Choosing to add an orchestration action later
starts by updating this map and the phase, reusing the existing resource
implementation.

## Contract-external HTTP, local entry points, and capabilities without an API

Each row states the planned entry point, owning issue, phase, permission, and
acceptance. "Already exists" for a local entry point means the gateway binary
only, never that notarxctl implements it:

| Capability and actual entry point | Planned CLI/documentation and owning issue | Phase | Permission/acceptance |
| --- | --- | --- | --- |
| GET /livez, GET /readyz | info/status, #125/#135 | M0/M1 | existing public health routes; verify 204, unhealthy, and timeout separately; not equivalent to full business success |
| POST /auth/login, POST /auth/change-password, GET /auth/session, POST /auth/logout | auth login/change-password/logout plus the identity description, #129 | M1 | the actor's existing authentication/session state; missing private input fails bounded; the token is never echoed; non-interactive browser OIDC login is not promised |
| client profiles/config, commands introspection, version | #125/#129 | M0/M1 | local client files and input only, never the server .env; precedence/0600/schema and both navigation views are tested |
| notarx version | #125 documents the CLI/remote/server binary version distinction | LOCAL, already exists | local execution identity; the local version is never reported as the remote instance version |
| notarx preflight list/run | #137 points at the existing entry point | LOCAL, already exists | host/server local configuration privileges; explicit checks and redacted results; an API token does not replace host privileges |
| notarx preflight capacity --input | #137, reusing #202 | LOCAL, already exists | explicit offline inventory/capacity plan; unique objects, peak, and logical quota are distinguished, unknown/insufficient blocks; not a live reservation |
| notarx preflight backup --input | #137, reusing #200 | LOCAL, already exists | explicit backup manifest and private file reads; integrity/compatibility verification; readyz cannot substitute |
| notarx backup export/restore --spec --bundle | #137 points at the existing operations; #200/#201 keep the remaining scope | LOCAL, already exists; remote BLOCKED | additional PG/S3/file/Docker privileges and explicit isolated targets; digest/software/schema/semantic read-back; never overwrites an existing target implicitly |
| notarx evidence collect --gateway-url --output | #137 points at the existing collection; #135 reuses it read-only for remote | LOCAL, already exists | explicit target and output directory plus administrator credentials; redacted evidence and version identity; not a durable server-side job |
| generic delete/restore of a version (tombstone and restore, ADR 0028 slice 4) | `generic delete` and `generic restore`, #220 (the commands of #223 end at list, show, seal, properties, push and pull) | BLOCKED | no management API for version tombstone or restore has shipped; no invented endpoint may claim coverage; the commands arrive with the operations and an administrator-only purge |
| gateway snapshot-import verify/dry-run/apply/takeover | #137 documentation, related #257/#259/#202 | LOCAL, already exists; online migration BLOCKED | frozen Maven bundle / private spec / target DB-S3 privileges; identity/bytes/checkpoint/capacity; no appends after takeover, no Nexus enumeration |
| gateway apt-snapshot verify | #138 documentation; remote archive/restore see the table above | LOCAL, already exists | local archive read and digest integrity; the output signatures=not_checked must not pretend trusted signature verification passed |
| gateway theme validate/install/list | #139 distinguishes the local directory from remote managed | LOCAL, already exists | explicit local directory/file privileges; package validation and ownership; a remote admin token does not buy local write access |
| complete system backup/restore, host/deployment configuration, worker/PG/S3/SMTP relay/auth/CA | #126/#137/#139 define the deployment entry points; remote needs a dedicated API | LOCAL/remote BLOCKED | no generic remote management endpoint yet; no implicit SSH/container/database access; no automatic probing from real secrets |
| Manual full and full-rescan incremental byte migration | #381/#382/#206/#425/#442, Console #383; no notarxctl command | API/Console, implemented | durable Maven release, Raw, OCI, Conan 2 and npm batches keep the source read-only and expose capacity/identity/full-byte verification evidence; protocol consumers still need the configured native endpoints |
| Periodically scheduled Nexus synchronization | #443 and #269; API/Console plan management, notarxctl deferred to #137 | API/Console, implemented; CLI deferred | administrator-only plans freeze a successful batch's source version, target mappings and native policy; each occurrence needs a fresh operator capacity observation or waits in waiting_capacity; pause/stop prevent future dispatches; not fully unattended migration |
| physical mount alerts, cluster sender readiness/backlog, and more complete notifications | #255/#256 and #209–#213/#229/#230; a future #336 extends it | BLOCKED | the existing partial capability/heartbeat is not proof that every role shipped; wait for real API/schema/deployment acceptance; do not fabricate fields or commands |

Sources: `cmd/notarx/main.go`; `internal/{backupops,snapshotimport,preflight,evidence,aptpublication,consoletheme}/cli*.go`;
`docs/repository-deletion-contract.md`, `user-governance.md`,
`runtime-log-product-design.md`, `email-notification-delivery.md`,
`maven-snapshot-import.md`. Platform authority on the management API and local
operator file/OS privileges are two different authorization boundaries.

## Drift guard and definition of done

- The "classify every operation" requirement stays. `make cli-contract-check`
  enumerates the operation set of both bundles (method + path + operationId)
  and requires every operation to be classified on this page; an unclassified
  contract operation fails with "new operation needs explicit classification".
  Renames and removals that leave stale rows fail too, and so does an N/A row
  that drops its reason or its `N` acceptance code. Management operations and
  the native protocol extras are both in scope; the sets are derived from the
  bundles, never a hardcoded ignore list.
- Map coverage and implementation coverage are separate checks: every row
  must be classified, but only implemented rows or rows in the currently
  delivered milestone require command registration and real acceptance.
  Planned M2 or blocked rows do not force first-release implementation. A row
  claims implementation by carrying `implemented` in its Phase cell (for
  example `M1 implemented`); the guard reads the installed command registry
  through `notarxctl commands --json` and fails when an implemented row has no
  registered command, or when the command does not declare the row's
  endpoint (`METHOD /api/v2/path`) in its metadata. One command may serve
  several rows, such as `artifact search --global` and `--repo`, only because
  each row's endpoint is checked against what the command declares it sends.
- One resource command/alias uses one registration implementation; the CLI
  tree's CI/platform tags do not duplicate operations, and JSON output schema,
  permission, mutation, pagination, idempotency, dry-run, and wait/cancel
  metadata all come from one metadata source.
- Supplementary acceptance: injecting a new unclassified operationId fails;
  a rename or removal that is still referenced fails; planned M2 rows are not
  misjudged as a missing first release; an implemented row without a
  registered command fails, and so does one whose endpoint the command does
  not declare;
  and #140 runnable recipes are generated only for implemented commands.
- The map records the contract baseline SHA and review date above; the guard
  re-derives the operation counts and the page never pins the old counts as
  fact.
- Bilingual registration: `docs/cli-command-map.md` and
  `docs/cli-command-map.zh-CN.md` link both ways and appear in
  `docs/site-map.json`; `make docs-check` covers the pair.

## NuGet Hosted preview (#287)

These explicit preview APIs have no notarxctl commands. NuGet remains absent from the admitted format catalog and creation selector. Official dotnet clients use the protocol operations. N/A classifies the CLI only and does not claim #288/#289 completion.

| Operation | CLI applicability | Owner | Phase | Permission | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `GET /previews/nuget/repositories/{repositoryId}/capacity`<br>`getNuGetPreviewCapacity` | preview API; no CLI command | #287 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `PUT /previews/nuget/repositories/{repositoryId}/capacity`<br>`replaceNuGetPreviewQuota` | preview API; no CLI command | #287 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `POST /previews/nuget/repositories`<br>`createNuGetPreviewRepository` | preview API; no CLI command | #287 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `GET /previews/nuget/repositories/{repositoryId}`<br>`getNuGetPreviewRepository` | preview API; no CLI command | #287 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `GET /previews/nuget/repositories/{repositoryId}/packages`<br>`listNuGetPreviewPackages` | preview API; no CLI command | #287 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `GET /previews/nuget/repositories/{repositoryId}/packages/{packageId}/{version}`<br>`getNuGetPreviewPackage` | preview API; no CLI command | #287 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `POST /previews/nuget/repositories/{repositoryId}/publication-sessions`<br>`createNuGetPublicationSession` | preview API; no CLI command | #287 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `GET /previews/nuget/repositories/{repositoryId}/publication-sessions/{sessionId}`<br>`getNuGetPublicationSession` | preview API; no CLI command | #287 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `PUT /previews/nuget/repositories/{repositoryId}/publication-sessions/{sessionId}/package`<br>`stageNuGetPublicationSession` | preview API; no CLI command | #287 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `POST /previews/nuget/repositories/{repositoryId}/publication-sessions/{sessionId}/commit`<br>`commitNuGetPublicationSession` | preview API; no CLI command | #287 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `GET /nuget/{repository}/v3/{path}`<br>`readNuGetHostedRepresentation` | preview API; no CLI command | #287 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `HEAD /nuget/{repository}/v3/{path}`<br>`headNuGetHostedRepresentation` | preview API; no CLI command | #287 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `PUT /nuget/{repository}/api/v2/package/`<br>`publishNuGetHostedPackage` | preview API; no CLI command | #287 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |

## NuGet Proxy/Group preview (#288)

These opt-in preview operations have no notarxctl command. They do not admit NuGet into the public format catalog or Console; governance and formal admission remain #289.

| Operation | CLI applicability | Owner | Phase | Permission | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `PUT /previews/nuget/repositories/{repositoryId}`<br>`replaceNuGetPreviewRepository` | preview API; no CLI command | #288 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `POST /previews/nuget/groups`<br>`createNuGetPreviewGroup` | preview API; no CLI command | #288 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `GET /previews/nuget/groups/{groupId}`<br>`getNuGetPreviewGroup` | preview API; no CLI command | #288 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `PUT /previews/nuget/groups/{groupId}`<br>`replaceNuGetPreviewGroup` | preview API; no CLI command | #288 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `GET /previews/nuget/groups/{groupId}/sources/{packageId}/{version}`<br>`getNuGetPreviewGroupSource` | preview API; no CLI command | #288 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `POST /previews/nuget/groups/{groupId}/sources/{packageId}/{version}/revalidate`<br>`revalidateNuGetPreviewGroupSource` | preview API; no CLI command | #288 | N/A | native-format or preview authorization | N: explicit opt-in preview; no CLI command registers or sends this operation |

## Helm Hosted preview (#290)

Default-off. Management operations use the preview API; official Helm owns native reads. No notarxctl Helm command is registered.

| API / operationId | Planned command | Owning issue | Phase | Permission | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `POST /previews/helm/repositories`<br>`createHelmPreviewRepository` | N: Helm preview API or official client; CLI deferred | #290 | N/A | A | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `GET /previews/helm/repositories/{repositoryId}`<br>`getHelmPreviewRepository` | N: Helm preview API or official client; CLI deferred | #290 | N/A | R | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `GET /previews/helm/repositories/{repositoryId}/charts`<br>`listHelmPreviewCharts` | N: Helm preview API or official client; CLI deferred | #290 | N/A | R | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `GET /previews/helm/repositories/{repositoryId}/charts/{chartName}/{version}`<br>`getHelmPreviewChart` | N: Helm preview API or official client; CLI deferred | #290 | N/A | R | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `PUT /previews/helm/repositories/{repositoryId}/charts/{chartName}/{version}`<br>`publishHelmPreviewChart` | N: Helm preview API or official client; CLI deferred | #290 | N/A | W | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `GET /previews/helm/repositories/{repositoryId}/capacity`<br>`getHelmPreviewCapacity` | N: Helm preview API or official client; CLI deferred | #290 | N/A | R | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `PUT /previews/helm/repositories/{repositoryId}/capacity`<br>`replaceHelmPreviewQuota` | N: Helm preview API or official client; CLI deferred | #290 | N/A | RA | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `GET /helm/{repository}/index.yaml`<br>`readHelmHostedIndex` | N: Helm preview API or official client; CLI deferred | #290 | N/A | R | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `HEAD /helm/{repository}/index.yaml`<br>`headHelmHostedIndex` | N: Helm preview API or official client; CLI deferred | #290 | N/A | R | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `GET /helm/{repository}/charts/{chartName}/{version}/{filename}`<br>`readHelmHostedAsset` | N: Helm preview API or official client; CLI deferred | #290 | N/A | R | N: explicit opt-in preview; no CLI command registers or sends this operation |
| `HEAD /helm/{repository}/charts/{chartName}/{version}/{filename}`<br>`headHelmHostedAsset` | N: Helm preview API or official client; CLI deferred | #290 | N/A | R | N: explicit opt-in preview; no CLI command registers or sends this operation |

## Helm Proxy/Group preview (#291)

The management command names below are M2 plans only, not registered or
verified notarxctl commands. Use the opt-in preview API today. Official Helm
clients follow the frozen chart and provenance URLs in the index; notarxctl does
not add another Helm protocol client. These rows do not admit Helm into the
public format catalog.

| API / operationId | Planned command | Owning issue | Phase | Permission | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `PUT /previews/helm/repositories/{repositoryId}`<br>`replaceHelmPreviewRepository` | `helm preview repo update <id>` | #291 | M2 planned | A | C |
| `POST /previews/helm/groups`<br>`createHelmPreviewGroup` | `helm preview group create` | #291 | M2 planned | A | W |
| `GET /previews/helm/groups/{groupId}`<br>`getHelmPreviewGroup` | `helm preview group show <id>` | #291 | M2 planned | R on all current members | Q |
| `PUT /previews/helm/groups/{groupId}`<br>`replaceHelmPreviewGroup` | `helm preview group update <id>` | #291 | M2 planned | A | C |
| `GET /previews/helm/groups/{groupId}/sources/{chartName}/{version}`<br>`getHelmPreviewSource` | `helm preview group source <id> --chart <name> --version <version>` | #291 | M2 planned | R on the chart in all current members | Q |
| `GET /helm/{repository}/snapshots/{snapshotId}/charts/{chartName}/{version}/{filename}`<br>`readHelmSnapshotAsset` | official Helm snapshot asset read; no notarxctl protocol client | #291 | N/A | R* | N: official Helm follows the index snapshot URL; notarxctl neither registers nor sends this protocol action |
| `HEAD /helm/{repository}/snapshots/{snapshotId}/charts/{chartName}/{version}/{filename}`<br>`headHelmSnapshotAsset` | native Helm snapshot asset metadata; no notarxctl protocol client | #291 | N/A | R* | N: the native Helm HTTP endpoint serves asset metadata; notarxctl neither registers nor sends this protocol action |

## NuGet governance preview (#449)

The four management command names are M2 plans only; none is registered or
verified. Use the opt-in preview management API for governance evidence,
digest/version guarded changes, and the independent quarantine read policy.
Native PackagePublish unlist/relist remains separate from tombstoning and
quarantine. The relist API row does not claim an official dotnet CLI relist
command. This mapping does not mark the remaining #289 admission complete.

| API / operationId | Planned command | Owning issue | Phase | Permission | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `GET /previews/nuget/repositories/{repositoryId}/packages/{packageId}/{version}/governance`<br>`getNuGetPreviewGovernance` | `nuget preview governance show --repo <id> --package <id> --version <version>` | #449 | M2 planned | RA | Q |
| `POST /previews/nuget/repositories/{repositoryId}/packages/{packageId}/{version}/governance`<br>`applyNuGetPreviewGovernance` | `nuget preview governance apply --repo <id> --package <id> --version <version>` | #449 | M2 planned | RA | C |
| `GET /previews/nuget/repositories/{repositoryId}/quarantine-read-policy`<br>`getNuGetPreviewQuarantineReadPolicy` | `nuget preview quarantine-policy show --repo <id>` | #449 | M2 planned | RA | Q |
| `PUT /previews/nuget/repositories/{repositoryId}/quarantine-read-policy`<br>`replaceNuGetPreviewQuarantineReadPolicy` | `nuget preview quarantine-policy set --repo <id>` | #449 | M2 planned | RA | C |
| `DELETE /nuget/{repository}/api/v2/package/{packageId}/{version}`<br>`unlistNuGetHostedPackage` | native NuGet PackagePublish unlist; no notarxctl registry client | #449 | N/A | W | N: NuGet clients use the native unlist endpoint; notarxctl neither registers nor sends this protocol action |
| `POST /nuget/{repository}/api/v2/package/{packageId}/{version}`<br>`relistNuGetHostedPackage` | native NuGet PackagePublish relist API; no notarxctl registry client | #449 | N/A | W | N: use the native relist API; no notarxctl or official dotnet CLI relist command is claimed |

## Helm governance, NuGet replication and RubyGems Hosted previews (#457–#459)

The seven command names below remain M2 plans. Their APIs require explicit
preview enablement; no new notarxctl command is registered or verified here. NuGet
retry retains the original durable actor and credential identity and rechecks
current authority. RubyGems uses the preview API and official clients. Formal
format admission remains under #292, #289 and #293.

| API / operationId | Planned command | Owning issue | Phase | Permission | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `GET /previews/helm/repositories/{repositoryId}/charts/{chartName}/{version}/governance`<br>`getHelmPreviewGovernance` | `helm preview governance show --repo <id> --chart <name> --version <version>` | #457 | M2 planned | RA | Q |
| `POST /previews/helm/repositories/{repositoryId}/charts/{chartName}/{version}/governance`<br>`applyHelmPreviewGovernance` | `helm preview governance apply --repo <id> --chart <name> --version <version>` | #457 | M2 planned | RA | C |
| `GET /previews/helm/repositories/{repositoryId}/quarantine-read-policy`<br>`getHelmPreviewQuarantineReadPolicy` | `helm preview quarantine-policy show --repo <id>` | #457 | M2 planned | RA | Q |
| `PUT /previews/helm/repositories/{repositoryId}/quarantine-read-policy`<br>`replaceHelmPreviewQuarantineReadPolicy` | `helm preview quarantine-policy set --repo <id>` | #457 | M2 planned | RA | C |
| `POST /previews/nuget/replication-jobs`<br>`createNuGetPreviewReplicationJob` | `nuget preview replication create --file <plan.json>` | #458 | M2 planned | source R + target W | W |
| `GET /previews/nuget/replication-jobs/{jobId}`<br>`getNuGetPreviewReplicationJob` | `nuget preview replication show <job-id>` | #458 | M2 planned | source R + target R | Q |
| `POST /previews/nuget/replication-jobs/{jobId}/retry`<br>`retryNuGetPreviewReplicationJob` | `nuget preview replication retry <job-id> --file <retry.json>` | #458 | M2 planned | original actor + source R + target W | C |
| `POST /previews/rubygems/repositories`<br>`createRubyGemsPreviewRepository` | explicit RubyGems preview API | #459 | N/A | A | N: this Hosted preview has an explicit management API; no notarxctl command is registered or verified |
| `GET /previews/rubygems/repositories/{repositoryId}`<br>`getRubyGemsPreviewRepository` | explicit RubyGems preview API | #459 | N/A | R | N: this Hosted preview has an explicit management API; no notarxctl command is registered or verified |
| `GET /previews/rubygems/repositories/{repositoryId}/capacity`<br>`getRubyGemsPreviewCapacity` | explicit RubyGems preview API | #459 | N/A | A | N: this Hosted preview has an explicit management API; no notarxctl command is registered or verified |
| `PUT /previews/rubygems/repositories/{repositoryId}/capacity`<br>`setRubyGemsPreviewQuota` | explicit RubyGems preview API | #459 | N/A | A | N: this Hosted preview has an explicit management API; no notarxctl command is registered or verified |
| `POST /rubygems/{repository}/api/v1/gems`<br>`pushRubyGemHostedPreview` | official RubyGems/Bundler client | #459 | N/A | W | N: official RubyGems/Bundler uses the native endpoint; notarxctl neither registers nor sends this operation |
| `GET /rubygems/{repository}/{index}`<br>`readRubyGemsHostedPreviewIndex` | official RubyGems/Bundler client | #459 | N/A | R* | N: official RubyGems/Bundler uses the native endpoint; notarxctl neither registers nor sends this operation |
| `HEAD /rubygems/{repository}/{index}`<br>`headRubyGemsHostedPreviewIndex` | official RubyGems/Bundler client | #459 | N/A | R* | N: official RubyGems/Bundler uses the native endpoint; notarxctl neither registers nor sends this operation |
| `GET /rubygems/{repository}/info/{gemName}`<br>`readRubyGemsHostedPreviewInfo` | official RubyGems/Bundler client | #459 | N/A | R* | N: official RubyGems/Bundler uses the native endpoint; notarxctl neither registers nor sends this operation |
| `GET /rubygems/{repository}/gems/{gemFile}`<br>`downloadRubyGemHostedPreview` | official RubyGems/Bundler client | #459 | N/A | R* | N: official RubyGems/Bundler uses the native endpoint; notarxctl neither registers nor sends this operation |
| `HEAD /rubygems/{repository}/gems/{gemFile}`<br>`headRubyGemHostedPreview` | official RubyGems/Bundler client | #459 | N/A | R* | N: official RubyGems/Bundler uses the native endpoint; notarxctl neither registers nor sends this operation |
| `GET /rubygems/{repository}/quick/Marshal.4.8/{gemspecFile}`<br>`readRubyGemsHostedPreviewGemspec` | official RubyGems/Bundler client | #459 | N/A | R* | N: official RubyGems/Bundler uses the native endpoint; notarxctl neither registers nor sends this operation |
| `HEAD /rubygems/{repository}/info/{gemName}`<br>`headRubyGemsHostedPreviewInfo` | official RubyGems/Bundler client | #459 | N/A | R* | N: official RubyGems/Bundler uses the native endpoint; notarxctl neither registers nor sends this operation |
| `HEAD /rubygems/{repository}/quick/Marshal.4.8/{gemspecFile}`<br>`headRubyGemsHostedPreviewGemspec` | official RubyGems/Bundler client | #459 | N/A | R* | N: official RubyGems/Bundler uses the native endpoint; notarxctl neither registers nor sends this operation |
| `POST /previews/helm/replication-jobs`<br>`createHelmPreviewReplicationJob` | `helm preview replication create --file <plan.json>` | #463 | M2 planned | source R + target W | W |
| `GET /previews/helm/replication-jobs/{jobId}`<br>`getHelmPreviewReplicationJob` | `helm preview replication show <job-id>` | #463 | M2 planned | source R + target R | Q |
| `POST /previews/helm/replication-jobs/{jobId}/retry`<br>`retryHelmPreviewReplicationJob` | `helm preview replication retry <job-id> --file <retry.json>` | #463 | M2 planned | original actor + source R + target W | C |
| `GET /previews/rubygems/repositories/{repositoryId}/packages/{gemName}/{gemVersion}/{platform}/governance`<br>`getRubyGemsPreviewGovernance` | explicit RubyGems preview API | #462 | N/A | RA for the exact package | N: this Hosted preview exposes versioned governance; no notarxctl command is registered or verified |
| `POST /previews/rubygems/repositories/{repositoryId}/packages/{gemName}/{gemVersion}/{platform}/governance`<br>`changeRubyGemsPreviewGovernance` | explicit RubyGems preview API | #462 | N/A | RA for the exact package | N: this Hosted preview exposes versioned governance; no notarxctl command is registered or verified |
| `DELETE /rubygems/{repository}/api/v1/gems/yank`<br>`yankRubyGemHostedPreview` | native `gem yank` | #462 | N/A | W for the exact package | N: the official RubyGems client owns native yank; notarxctl does not implement another protocol client |
| `GET /previews/rubygems/repositories/{repositoryId}/quarantine-read-policy`<br>`getRubyGemsPreviewQuarantineReadPolicy` | explicit RubyGems preview API | #468 | N/A | RA | N: independent versioned policy API; no notarxctl command is registered or verified |
| `PUT /previews/rubygems/repositories/{repositoryId}/quarantine-read-policy`<br>`replaceRubyGemsPreviewQuarantineReadPolicy` | explicit RubyGems preview API | #468 | N/A | RA | N: independent versioned policy API; no notarxctl command is registered or verified |
| `GET /previews/rubygems/repositories/{repositoryId}/packages/{gemName}/{gemVersion}/{platform}`<br>`getRubyGemsPreviewPackageIdentity` | explicit RubyGems preview API | #472 | N/A | source R for the exact package | N: this Hosted replication preview is API-only; no notarxctl command is registered or verified |
| `POST /previews/rubygems/replication-jobs`<br>`createRubyGemsPreviewReplicationJob` | explicit RubyGems preview API | #472 | N/A | source R + target W for every item | N: this Hosted replication preview is API-only; no notarxctl command is registered or verified |
| `GET /previews/rubygems/replication-jobs/{jobId}`<br>`getRubyGemsPreviewReplicationJob` | explicit RubyGems preview API | #472 | N/A | source R + target R for every item | N: this Hosted replication preview is API-only; no notarxctl command is registered or verified |
| `POST /previews/rubygems/replication-jobs/{jobId}/retry`<br>`retryRubyGemsPreviewReplicationJob` | explicit RubyGems preview API | #472 | N/A | original actor + source R + target W | N: this Hosted replication preview is API-only; no notarxctl command is registered or verified |

RubyGems governance POST also accepts independent `quarantine`/`release` (#468); the existing exact-package administrator permission and CAS contract apply. Release does not undo yank, and restore does not clear quarantine.
