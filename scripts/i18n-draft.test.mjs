import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import {
  acceptDrafts, appendEntries, buildPrompt, draftMissing, stubModel,
} from "./i18n-draft.mjs";
import { isCritical, readDictionary, readSidecar, syncSidecar } from "./i18n-lib.mjs";

const EN = `export const fooEnCA = {
  "foo.title": "Repositories",
  "foo.deleteRepo": "Delete repository",
  "foo.count":
    "{count} items in {name}",
};
`;
const FR_PARTIAL = `import type { fooEnCA } from "./foo.en-CA";

export const fooFrCA: Partial<Record<keyof typeof fooEnCA, string>> = {
  "foo.title": "Dépôts",
};
`;

function fixture(fr = FR_PARTIAL) {
  const dir = mkdtempSync(join(tmpdir(), "i18n-draft-"));
  writeFileSync(join(dir, "foo.en-CA.ts"), EN);
  writeFileSync(join(dir, "foo.fr-CA.ts"), fr);
  writeFileSync(join(dir, "glossary.ts"), '  {\n    Repository: { label: "dépôt" },\n    Tombstone: { label: "pierre tombale", avoid: ["épitaphe"] },\n  };\n'.replace(/^/, "const frCATerms = {\n").replace(/;\n$/, "\n"));
  return dir;
}

test("reads dictionaries whose text sits on the next line", () => {
  const dir = fixture();
  assert.deepEqual(readDictionary(join(dir, "foo.en-CA.ts")), {
    "foo.title": "Repositories",
    "foo.deleteRepo": "Delete repository",
    "foo.count": "{count} items in {name}",
  });
});

test("the prompt holds the rules, the glossary and only the asked strings", () => {
  const dir = fixture();
  const prompt = buildPrompt("fr-CA", { "foo.deleteRepo": "Delete repository" }, dir);
  assert.match(prompt, /courriel/);
  assert.match(prompt, /Repository = dépôt/);
  assert.match(prompt, /Tombstone = pierre tombale \(avoid: épitaphe\)/);
  assert.match(prompt, /"foo.deleteRepo": "Delete repository"/);
  assert.doesNotMatch(prompt, /foo\.title/);
});

test("drafting with the stub model is deterministic and records drafts", async () => {
  const run = async () => {
    const dir = fixture();
    const result = await draftMissing({ dir, locale: "fr-CA", model: stubModel });
    return { dir, result };
  };
  const first = await run();
  const second = await run();
  assert.deepEqual(first.result.drafted, ["foo.deleteRepo", "foo.count"]);
  assert.equal(
    readFileSync(join(first.dir, "foo.fr-CA.ts"), "utf8"),
    readFileSync(join(second.dir, "foo.fr-CA.ts"), "utf8"),
  );
  assert.deepEqual(readDictionary(join(first.dir, "foo.fr-CA.ts")), {
    "foo.title": "Dépôts",
    "foo.deleteRepo": "[stub] Delete repository",
    "foo.count": "[stub] {count} items in {name}",
  });
  const sidecar = readSidecar(first.dir);
  assert.deepEqual(sidecar.keys, { "foo.deleteRepo": "draft", "foo.count": "draft" });
  assert.deepEqual(sidecar.critical, ["foo.deleteRepo"]);
});

test("a dry run changes nothing", async () => {
  const dir = fixture();
  const result = await draftMissing({ dir, locale: "fr-CA", model: stubModel, dryRun: true });
  assert.equal(result.drafted.length, 2);
  assert.equal(readFileSync(join(dir, "foo.fr-CA.ts"), "utf8"), FR_PARTIAL);
});

test("an answer that loses a placeholder falls back to a TODO(fr-CA) draft", async () => {
  const dir = fixture();
  const model = { draft: (_p, asked) => Object.fromEntries(Object.keys(asked).map((k) => [k, "sans variables"])) };
  const result = await draftMissing({ dir, locale: "fr-CA", model });
  assert.deepEqual(result.todo, ["foo.count"]);
  const source = readFileSync(join(dir, "foo.fr-CA.ts"), "utf8");
  assert.match(source, /\/\/ TODO\(fr-CA\)\n {2}"foo.count":\n {4}"\{count\} items in \{name\}"/);
  assert.equal(readSidecar(dir).keys["foo.count"], "draft");
});

test("acceptDrafts keeps only complete answers for the asked keys", () => {
  const asked = { a: "Hello {name}", b: "Plain" };
  const { accepted, rejected } = acceptDrafts(asked, { a: "Bonjour {name}", b: "", c: "extra" });
  assert.deepEqual(accepted, { a: "Bonjour {name}" });
  assert.deepEqual(rejected, ["b"]);
});

test("appendEntries escapes quotes and keeps no-break spaces visible", () => {
  const out = appendEntries('export const x = {\n};\n', { k: 'Nom\u00a0: « a »' });
  assert.match(out, /"k":\n {4}"Nom\\u00a0: « a »",/);
});

test("zh-CN drafting needs no sidecar", async () => {
  const dir = fixture();
  writeFileSync(join(dir, "foo.zh-CN.ts"), "export const fooZhCN = {\n};\n");
  const result = await draftMissing({ dir, locale: "zh-CN", model: stubModel, features: ["foo"], limit: 1 });
  assert.equal(result.drafted.length, 1);
  assert.deepEqual(readSidecar(dir), { keys: {}, critical: [] });
});

test("critical keys are the destructive actions", () => {
  assert.ok(isCritical("repository.deleteGroup", "Remove"));
  assert.ok(isCritical("x.y", "Quarantine artifact"));
  assert.ok(isCritical("x.revokeKey", "Key"));
  assert.ok(!isCritical("x.save", "Save"));
});

test("syncSidecar records every key, drops stale ones and keeps reviews", () => {
  const dir = fixture();
  mkdirSync(join(dir, ".review"), { recursive: true });
  writeFileSync(join(dir, ".review", "fr-CA.json"), JSON.stringify({ keys: { "foo.title": "reviewed", "gone.key": "draft" }, critical: ["gone.key"] }));
  const sidecar = syncSidecar(dir);
  assert.deepEqual(sidecar.keys, { "foo.count": "draft", "foo.deleteRepo": "draft", "foo.title": "reviewed" });
  assert.deepEqual(sidecar.critical, ["foo.deleteRepo"]);
});
