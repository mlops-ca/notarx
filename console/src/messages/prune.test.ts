import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { afterEach, describe, expect, it } from "vitest";

const script = resolve(process.cwd(), "scripts/i18n-prune.mjs");

// The overlay values are placeholders; only the keys matter to the script.
const en = `export const sampleEnCA = {
  "sample.kept": "Kept",
  "sample.renamed": "Renamed",
};
`;
const zh = `export const sampleZhCN = {
  "sample.kept": "kept",
  "sample.removed": "removed",
  "sample.alsoRemoved": "also removed",
  "sample.renamed": "renamed",
};
`;

const directories: string[] = [];

function fixture(): string {
  const directory = mkdtempSync(join(tmpdir(), "i18n-prune-"));
  directories.push(directory);
  writeFileSync(join(directory, "sample.en-CA.ts"), en);
  writeFileSync(join(directory, `sample.zh-CN.ts`), zh);
  return directory;
}

function run(...args: string[]) {
  try {
    return {
      code: 0,
      out: execFileSync("node", [script, ...args], { encoding: "utf8" }),
    };
  } catch (error) {
    const failure = error as { status: number; stdout: string };
    return { code: failure.status, out: failure.stdout };
  }
}

afterEach(() => {
  for (const directory of directories.splice(0)) {
    rmSync(directory, { recursive: true, force: true });
  }
});

describe("i18n-prune", () => {
  it("detects stale zh-CN keys without changing files under --check", () => {
    const directory = fixture();
    const result = run("--check", directory);
    expect(result.code).toBe(1);
    expect(result.out).toContain("sample.removed");
    expect(result.out).toContain("sample.alsoRemoved");
    expect(readFileSync(join(directory, `sample.zh-CN.ts`), "utf8")).toBe(zh);
  });

  it("removes the keys en-CA no longer has and keeps the others", () => {
    const directory = fixture();
    const result = run(directory);
    expect(result.code).toBe(0);
    const pruned = readFileSync(join(directory, `sample.zh-CN.ts`), "utf8");
    expect(pruned).toBe(`export const sampleZhCN = {
  "sample.kept": "kept",
  "sample.renamed": "renamed",
};
`);
    expect(run("--check", directory)).toEqual({
      code: 0,
      out: "No stale zh-CN keys.\n",
    });
  });

  it("finds no stale key in the real dictionaries", () => {
    expect(run("--check", resolve(process.cwd(), "src/messages"))).toEqual({
      code: 0,
      out: "No stale zh-CN keys.\n",
    });
  });
});
