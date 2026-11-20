import type { WorkloadCondition, WorkloadIssuerKind } from "../../client";
import type { MessageKey } from "../../messages";

/**
 * A starting point for the conditions of a trust policy. Every template holds
 * the mandatory immutable-identity conditions of its issuer kind (ADR 0019),
 * with placeholder values the operator must replace; the server rejects a policy
 * without them.
 */
export interface PolicyTemplate {
  kind: WorkloadIssuerKind;
  label: MessageKey;
  conditions: WorkloadCondition[];
}

const templates: Record<WorkloadIssuerKind, PolicyTemplate> = {
  github_actions: {
    kind: "github_actions",
    label: "workload.template.github_actions",
    conditions: [
      {
        claim: "repository_id",
        op: "eq",
        values: ["REPLACE_WITH_REPOSITORY_ID"],
      },
      { claim: "ref", op: "eq", values: ["refs/heads/main"] },
      { claim: "environment", op: "eq", values: ["REPLACE_WITH_ENVIRONMENT"] },
    ],
  },
  gitlab_ci: {
    kind: "gitlab_ci",
    label: "workload.template.gitlab_ci",
    conditions: [
      { claim: "project_id", op: "eq", values: ["REPLACE_WITH_PROJECT_ID"] },
      { claim: "ref_protected", op: "eq", values: ["true"] },
    ],
  },
  kubernetes: {
    kind: "kubernetes",
    label: "workload.template.kubernetes",
    conditions: [
      {
        claim: "sub",
        op: "eq",
        values: ["system:serviceaccount:team-a:builder"],
      },
    ],
  },
  generic: {
    kind: "generic",
    label: "workload.template.generic",
    conditions: [{ claim: "sub", op: "eq", values: ["REPLACE_WITH_SUBJECT"] }],
  },
};

/** The template of an issuer kind. */
export function policyTemplate(kind: WorkloadIssuerKind): PolicyTemplate {
  return templates[kind];
}

/** The template conditions as the pretty-printed JSON the policy editor shows. */
export function templateJson(kind: WorkloadIssuerKind): string {
  return JSON.stringify(templates[kind].conditions, null, 2);
}

/**
 * Whether a policy should default to single use: one used only through the
 * token exchange needs each token once, and a replayed token is then refused
 * (ADR 0019, resolved question 2). Policies that serve many requests with one
 * token (a registry login, a chatty client) must not.
 */
export function recommendsSingleUse(exchangeOnly: boolean): boolean {
  return exchangeOnly;
}

/** Parses the conditions editor; null when it is not a JSON list of objects. */
export function parseConditions(text: string): WorkloadCondition[] | null {
  try {
    const parsed: unknown = JSON.parse(text);
    if (
      !Array.isArray(parsed) ||
      parsed.length === 0 ||
      parsed.some((item) => typeof item !== "object" || item === null)
    ) {
      return null;
    }
    return parsed as WorkloadCondition[];
  } catch {
    return null;
  }
}
