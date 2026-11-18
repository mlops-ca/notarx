import { describe, expect, it } from "vitest";
import { LocalizedError } from "../messages/translate";
import { visibilityConflict } from "./visibilityConflict";

describe("visibilityConflict", () => {
  it("names the offending repositories of a visibility_conflict problem", () => {
    const converted = visibilityConflict({
      code: "visibility_conflict",
      status: 409,
      members: [
        { name: "internal", visibility: "private" },
        { name: "staging", visibility: "authenticated" },
      ],
    });
    expect(converted).toBeInstanceOf(LocalizedError);
    expect((converted as LocalizedError).key).toBe("groups.visibilityConflict");
    expect((converted as LocalizedError).message).toContain(
      "internal (private), staging (authenticated)",
    );
  });

  it("still reads when no member is disclosed", () => {
    const converted = visibilityConflict({ code: "visibility_conflict" });
    expect((converted as LocalizedError).message).toContain("(-)");
  });

  it("leaves every other error alone", () => {
    const other = { code: "version_conflict", message: "stale" };
    expect(visibilityConflict(other)).toBe(other);
    expect(visibilityConflict("boom")).toBe("boom");
    expect(visibilityConflict(null)).toBeNull();
  });
});
