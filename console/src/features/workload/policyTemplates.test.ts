import { describe, expect, it } from "vitest";
import type { WorkloadIssuerKind } from "../../client";
import {
  parseConditions,
  policyTemplate,
  recommendsSingleUse,
  templateJson,
} from "./policyTemplates";

const kinds: WorkloadIssuerKind[] = [
  "github_actions",
  "gitlab_ci",
  "kubernetes",
  "generic",
];

// The mandatory conditions of ADR 0019 per issuer kind.
const mandatoryClaims: Record<WorkloadIssuerKind, string[]> = {
  github_actions: ["repository_id", "ref"],
  gitlab_ci: ["project_id", "ref_protected"],
  kubernetes: ["sub"],
  generic: ["sub"],
};

describe("policy templates", () => {
  it.each(kinds)("%s starts with its mandatory conditions", (kind) => {
    const claims = policyTemplate(kind).conditions.map((condition) =>
      String(condition.claim),
    );
    for (const mandatory of mandatoryClaims[kind]) {
      expect(claims).toContain(mandatory);
    }
    expect(policyTemplate(kind).kind).toBe(kind);
  });

  it("never offers a bare glob", () => {
    for (const kind of kinds) {
      for (const condition of policyTemplate(kind).conditions) {
        expect(condition.values).not.toContain("*");
        expect(condition.values).not.toContain("**");
      }
    }
  });

  it("round-trips through the editor text", () => {
    for (const kind of kinds) {
      expect(parseConditions(templateJson(kind))).toEqual(
        policyTemplate(kind).conditions,
      );
    }
  });

  it("recommends single use for exchange-only policies only", () => {
    expect(recommendsSingleUse(true)).toBe(true);
    expect(recommendsSingleUse(false)).toBe(false);
  });

  it.each(["", "{}", "[]", "[1]", "not json", "[null]"])(
    "rejects %j as conditions",
    (text) => {
      expect(parseConditions(text)).toBeNull();
    },
  );
});
