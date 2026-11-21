import { describe, expect, it } from "vitest";
import { LocalizedError } from "../messages/translate";
import { remoteInstanceInUse } from "./remoteInstanceInUse";

describe("remoteInstanceInUse", () => {
  it("names the blocking promotion rules of an in_use problem", () => {
    const converted = remoteInstanceInUse(
      {
        code: "in_use",
        status: 409,
        rules: [
          { ruleId: "1", name: "nightly" },
          { ruleId: "2", name: "release" },
        ],
      },
      "staging",
    );
    expect(converted).toBeInstanceOf(LocalizedError);
    expect((converted as LocalizedError).key).toBe("remoteInstances.inUse");
    expect((converted as LocalizedError).message).toContain("staging");
    expect((converted as LocalizedError).message).toContain("nightly, release");
  });

  it("still reads when no rule is listed", () => {
    const converted = remoteInstanceInUse({ code: "in_use" }, "staging");
    expect((converted as LocalizedError).message).toContain("(-)");
  });

  it("leaves every other error alone", () => {
    const other = { code: "not_found", message: "gone" };
    expect(remoteInstanceInUse(other, "staging")).toBe(other);
    expect(remoteInstanceInUse("boom", "staging")).toBe("boom");
    expect(remoteInstanceInUse(null, "staging")).toBeNull();
  });
});
