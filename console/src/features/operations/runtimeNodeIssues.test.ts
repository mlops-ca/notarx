import { describe, expect, it } from "vitest";
import { createTranslator } from "../../messages/translate";
import { runtimeIssueText } from "./runtimeNodeIssues";

const codes = [
  "api_unavailable",
  "scheduler_unavailable",
  "worker_unavailable",
  "duplicate_instance_id",
  "stale_nodes",
  "build_identity_unknown",
  "mixed_build",
] as const;

describe("runtimeIssueText", () => {
  it("localizes every stable code from the en-CA table, not from the server text", () => {
    const t = createTranslator("en-CA");
    for (const code of codes) {
      const text = runtimeIssueText({ code, message: "server text" }, t);
      expect(text, code).not.toBe("server text");
      expect(text.length, code).toBeGreaterThan(10);
    }
    expect(runtimeIssueText({ code: "api_unavailable", message: "x" }, t)).toBe(
      "No online API nodes",
    );
  });

  it("falls back to the English message for a code it does not know", () => {
    const t = createTranslator("en-CA");
    const unknown = { code: "future_issue", message: "A future condition" };
    expect(runtimeIssueText(unknown as never, t)).toBe("A future condition");
    expect(
      runtimeIssueText({ code: "constructor", message: "Safe" } as never, t),
    ).toBe("Safe");
  });

  it("falls back to en-CA text for a locale that lags (zh-CN)", () => {
    const t = createTranslator("zh-CN");
    expect(
      runtimeIssueText({ code: "mixed_build", message: "server text" }, t),
    ).toContain("rolling upgrade");
  });
});
