import { ESLint } from "eslint";
import { describe, expect, it } from "vitest";

// The jsx-a11y plugin declares peers only up to ESLint 9 (see #324). Keep a
// deliberately failing fixture so a plugin or ESLint bump that silently stops
// the accessibility rules fails here instead of passing lint.
async function lint(code: string) {
  const eslint = new ESLint({ cwd: process.cwd() });
  const [result] = await eslint.lintText(code, {
    filePath: "src/test/a11yFixture.tsx",
  });
  return result.messages.map((message) => message.ruleId);
}

describe("jsx-a11y lint rules", () => {
  it("still reports an image without alternative text", async () => {
    expect(await lint('export const x = <img src="a.png" />;\n')).toContain(
      "jsx-a11y/alt-text",
    );
  });

  it("still reports a click handler on a non-interactive element", async () => {
    expect(
      await lint("export const x = <div onClick={() => {}}>go</div>;\n"),
    ).toContain("jsx-a11y/click-events-have-key-events");
  });

  it("accepts an accessible image", async () => {
    expect(
      await lint('export const x = <img src="a.png" alt="logo" />;\n'),
    ).toEqual([]);
  });
});
