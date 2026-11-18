import type { RuntimeNodeHealthIssue } from "../../client";
import type { MessageKey } from "../../messages";
import type { Translate } from "../../lib/preferences";

/**
 * The server returns a stable code and an English message and never localizes
 * (ADR 0014); the Console owns the localized text, keyed by code.
 */
const issueMessages: Record<RuntimeNodeHealthIssue["code"], MessageKey> = {
  api_unavailable: "operations.noOnlineApiNodes",
  scheduler_unavailable: "operations.noOnlineSchedulerNodes",
  worker_unavailable: "operations.noOnlineWorkerNodes",
  duplicate_instance_id: "operations.duplicateInstanceIds",
  stale_nodes: "operations.staleHeartbeatNodes",
  build_identity_unknown: "operations.buildIdentityUnknown",
  mixed_build: "operations.mixedBuild",
};

/** The issue's text in the reader's language; the English message for an unknown code. */
export function runtimeIssueText(
  issue: Pick<RuntimeNodeHealthIssue, "code" | "message">,
  t: Translate,
): string {
  return Object.hasOwn(issueMessages, issue.code)
    ? t(issueMessages[issue.code])
    : issue.message;
}
