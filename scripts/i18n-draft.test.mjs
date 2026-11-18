import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import {
  acceptDrafts, appendEntries, buildPrompt, draftMissing, skeleton, stubModel,
} from "./i18n-draft.mjs";
import { isCritical, markReviewed, readDictionary, readSidecar, syncSidecar } from "./i18n-lib.mjs";

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
  assert.deepEqual(readSidecar(dir), { keys: {}, critical: [], reviewed: {} });
});

test("critical keys are the destructive actions", () => {
  assert.ok(isCritical("repository.deleteGroup", "Remove"));
  assert.ok(isCritical("x.y", "Quarantine artifact"));
  assert.ok(isCritical("x.revokeKey", "Key"));
  assert.ok(!isCritical("x.save", "Save"));
});

test("syncSidecar records every key, drops stale ones and keeps reviews of unchanged text", () => {
  const dir = fixture();
  markReviewed(dir, ["foo.title"]);
  const withStale = readSidecar(dir);
  withStale.keys["gone.key"] = "draft";
  withStale.critical.push("gone.key");
  writeFileSync(join(dir, ".review", "fr-CA.json"), JSON.stringify(withStale));
  const sidecar = syncSidecar(dir);
  assert.deepEqual(sidecar.keys, { "foo.count": "draft", "foo.deleteRepo": "draft", "foo.title": "reviewed" });
  assert.deepEqual(sidecar.critical, ["foo.deleteRepo"]);
  assert.deepEqual(Object.keys(sidecar.reviewed), ["foo.title"]);
});

test("a review of other text goes back to draft when the sidecar is synced", () => {
  const dir = fixture();
  markReviewed(dir, ["foo.title"]);
  writeFileSync(join(dir, "foo.fr-CA.ts"), FR_PARTIAL.replace("Dépôts", "Référentiels"));
  const sidecar = syncSidecar(dir);
  assert.equal(sidecar.keys["foo.title"], "draft");
  assert.deepEqual(sidecar.reviewed, {});
});

test("a dictionary entry the reader cannot parse is refused, not skipped", () => {
  const dir = fixture();
  writeFileSync(join(dir, "foo.en-CA.ts"), EN.replace('"Repositories"', "`Repositories`"));
  assert.throws(() => readDictionary(join(dir, "foo.en-CA.ts")), /unsupported entry formatting for foo\.title/);
});

test("drafting creates the dictionary of a feature that has none for the locale", async () => {
  const dir = fixture();
  writeFileSync(join(dir, "bar.en-CA.ts"), 'export const barEnCA = {\n  "bar.name": "Name",\n};\n');
  const result = await draftMissing({ dir, locale: "fr-CA", model: stubModel, features: ["bar"] });
  assert.deepEqual(result.drafted, ["bar.name"]);
  const source = readFileSync(join(dir, "bar.fr-CA.ts"), "utf8");
  assert.match(source, /^import type \{ barEnCA \} from "\.\/bar\.en-CA";/);
  assert.match(source, /export const barFrCA: Record<keyof typeof barEnCA, string> = \{/);
  assert.equal(readSidecar(dir).keys["bar.name"], "draft");
  assert.match(skeleton("nexus-batch", "zh-CN"), /nexusBatchZhCN: Partial<Record<keyof typeof nexusBatchEnCA, string>> = \{/);
});

test("a model that fails mid-run leaves the earlier batches written and recorded", async () => {
  const dir = fixture();
  let calls = 0;
  const flaky = {
    draft(prompt, asked) {
      if (++calls > 1) throw new Error("the model command failed");
      return stubModel.draft(prompt, asked);
    },
  };
  await assert.rejects(draftMissing({ dir, locale: "fr-CA", model: flaky, batch: 1 }), /model command failed/);
  assert.deepEqual(Object.keys(readSidecar(dir).keys), ["foo.deleteRepo"]);
  assert.ok("foo.deleteRepo" in readDictionary(join(dir, "foo.fr-CA.ts")));
});
