import { createTranslator } from "../../messages/translate";
import { describe, expect, it } from "vitest";
import {
  auditOutcomeIsDenied,
  auditOutcomeLabel,
  auditOutcomeTone,
} from "./auditOutcomes";

const en = createTranslator("en-CA");

describe("auditOutcomeLabel", () => {
  it("labels every outcome the gateway records, in both languages", () => {
    const samples: [string, string, string][] = [
      ["resolved", "Resolved", "Resolved"],
      ["access_denied", "Access denied", "Access denied"],
      [
        "internal_preferred",
        "Internal copy preferred",
        "Internal copy preferred",
      ],
      ["not_found", "Not found", "Not found"],
      ["group_disabled", "Group disabled", "Group disabled"],
      ["proxy_denied", "Proxy denied", "Proxy denied"],
      ["upstream_error", "Upstream error", "Upstream error"],
      ["storage_error", "Storage error", "Storage error"],
    ];
    for (const [code, chinese, english] of samples) {
      expect(auditOutcomeLabel(code, en)).toBe(chinese);
      expect(auditOutcomeLabel(code, en)).toBe(english);
    }
  });

  it("keeps an unknown outcome readable by showing the code itself", () => {
    expect(auditOutcomeLabel("future_outcome", en)).toBe("future_outcome");
  });
});

describe("auditOutcomeTone", () => {
  it("separates refusals from the rest", () => {
    expect(auditOutcomeTone("resolved")).toBe("success");
    expect(auditOutcomeTone("access_denied")).toBe("danger");
    expect(auditOutcomeTone("storage_error")).toBe("danger");
    expect(auditOutcomeTone("not_found")).toBe("warning");
    expect(auditOutcomeTone(undefined)).toBe("neutral");
  });
});

describe("auditOutcomeIsDenied", () => {
  it("counts every refusal as denied and nothing else", () => {
    expect(auditOutcomeIsDenied("denied")).toBe(true);
    expect(auditOutcomeIsDenied("access_denied")).toBe(true);
    expect(auditOutcomeIsDenied("proxy_denied")).toBe(true);
    expect(auditOutcomeIsDenied("resolved")).toBe(false);
    expect(auditOutcomeIsDenied("storage_error")).toBe(false);
    expect(auditOutcomeIsDenied(undefined)).toBe(false);
  });
});
