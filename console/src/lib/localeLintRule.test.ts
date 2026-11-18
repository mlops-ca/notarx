import { ESLint } from "eslint";
import { describe, expect, it } from "vitest";

async function lint(code: string) {
  const eslint = new ESLint({ cwd: process.cwd() });
  const [result] = await eslint.lintText(code, {
    filePath: "src/lib/fixture.ts",
  });
  return result.messages
    .filter((message) => message.ruleId === "no-restricted-syntax")
    .map((message) => message.message);
}

describe("locale-less toLocale calls", () => {
  it.each([
    "new Date(value).toLocaleString();",
    "new Date(value).toLocaleDateString();",
    "new Date(value).toLocaleTimeString();",
    "n.toLocaleString();",
    "n.toLocaleString(undefined, { maximumFractionDigits: 2 });",
    "d.toLocaleDateString(undefined);",
  ])("are rejected: %s", async (code) => {
    const messages = await lint(
      `export const x = (value: string, n: number, d: Date) => { ${code} };`,
    );
    expect(messages).toHaveLength(1);
    expect(messages[0]).toContain("useFormatters");
  });

  it.each([
    "n.toLocaleString(locale);",
    'n.toLocaleString("fr-CA", { maximumFractionDigits: 2 });',
    "d.toLocaleDateString(locale, { dateStyle: 'short' });",
    "formatters.number.format(n);",
  ])("are allowed: %s", async (code) => {
    const messages = await lint(
      `export const x = (n: number, d: Date, locale: string, formatters: Intl.NumberFormat) => { ${code.replace("formatters.number", "formatters")} };`,
    );
    expect(messages).toEqual([]);
  });
});
