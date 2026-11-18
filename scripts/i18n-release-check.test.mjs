import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { defaultMessagesDir, isCritical, readReference, readSidecar } from "./i18n-lib.mjs";
import { releaseProblems } from "./i18n-release-check.mjs";

const script = join(dirname(fileURLToPath(import.meta.url)), "i18n-release-check.mjs");

function fixture({ keys, critical = [], fr = { "a.save": "Enregistrer", "a.deleteRepo": "Supprimer le dépôt" } }) {
  const dir = mkdtempSync(join(tmpdir(), "i18n-release-"));
  writeFileSync(join(dir, "a.en-CA.ts"), 'export const aEnCA = {\n  "a.save": "Save",\n  "a.deleteRepo": "Delete repository",\n};\n');
  writeFileSync(join(dir, "a.fr-CA.ts"), "export const aFrCA = {\n" + Object.entries(fr).map(([k, v]) => '  "' + k + '": "' + v + '",\n').join("") + "};\n");
  mkdirSync(join(dir, ".review"));
  writeFileSync(join(dir, ".review", "fr-CA.json"), JSON.stringify({ keys, critical }));
  return dir;
}

const run = (dir) => spawnSync("node", [script, dir], { encoding: "utf8" });

test("passes when every key is reviewed", () => {
  const dir = fixture({ keys: { "a.save": "reviewed", "a.deleteRepo": "reviewed" }, critical: ["a.deleteRepo"] });
  assert.deepEqual(releaseProblems(dir), []);
  const result = run(dir);
  assert.equal(result.status, 0);
  assert.match(result.stdout, /passed/);
});

test("fails on a draft key", () => {
  const dir = fixture({ keys: { "a.save": "draft", "a.deleteRepo": "reviewed" } });
  assert.deepEqual(releaseProblems(dir).map(([kind, key]) => [kind, key]), [["draft", "a.save"]]);
  const result = run(dir);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /draft: a\.save/);
});

test("reports a critical key that has not passed the review first", () => {
  const dir = fixture({ keys: { "a.save": "draft", "a.deleteRepo": "draft" }, critical: ["a.deleteRepo"] });
  assert.deepEqual(releaseProblems(dir).map(([kind]) => kind), ["critical-draft", "draft"]);
  assert.match(run(dir).stderr, /critical-draft: a\.deleteRepo/);
});

test("fails on missing text, unrecorded and stale keys", () => {
  const dir = fixture({ keys: { "a.save": "reviewed", "old.key": "reviewed" }, critical: [], fr: { "a.save": "Enregistrer" } });
  assert.deepEqual(
    releaseProblems(dir).map(([kind, key]) => kind + ":" + key),
    ["missing:a.deleteRepo", "unrecorded:a.deleteRepo", "stale:old.key"],
  );
});

test("rejects a status other than draft or reviewed", () => {
  const dir = fixture({ keys: { "a.save": "reviewed", "a.deleteRepo": "approved" } });
  assert.deepEqual(releaseProblems(dir).map(([kind]) => kind), ["invalid"]);
});

test("the committed fr-CA sidecar covers every en-CA key and every destructive-action key", () => {
  const reference = readReference(defaultMessagesDir);
  const sidecar = readSidecar(defaultMessagesDir);
  assert.deepEqual(Object.keys(sidecar.keys).sort(), Object.keys(reference).sort());
  const critical = new Set(sidecar.critical);
  const unflagged = Object.entries(reference).filter(([key, text]) => isCritical(key, text) && !critical.has(key));
  assert.deepEqual(unflagged.map(([key]) => key), []);
  assert.ok(sidecar.critical.length > 0);
});
