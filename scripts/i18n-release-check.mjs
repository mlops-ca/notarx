#!/usr/bin/env node
// The fr-CA release gate (ADR 0014, "Translation workflow"):
//
//   node scripts/i18n-release-check.mjs [messages-directory]
//
// Fails while any fr-CA key is still a draft, and while any critical key
// (a destructive action: delete, quarantine, revoke...) has not passed the
// automated LLM review, which marks a passing key "reviewed" in
// console/src/messages/.review/fr-CA.json. zh-CN has no release gate.
import { defaultMessagesDir, featureNames, dictionaryPath, readDictionary, readReference, readSidecar } from "./i18n-lib.mjs";

/** The problems that stop a release; empty when the gate passes. */
export function releaseProblems(dir = defaultMessagesDir) {
  const reference = readReference(dir);
  const sidecar = readSidecar(dir);
  const french = {};
  for (const feature of featureNames(dir)) Object.assign(french, readDictionary(dictionaryPath(dir, feature, "fr-CA")));
  const problems = [];
  const critical = new Set(sidecar.critical);
  for (const key of Object.keys(reference)) {
    if (!(key in french)) problems.push(["missing", key, "has no fr-CA text"]);
    const status = sidecar.keys[key];
    if (status === undefined) problems.push(["unrecorded", key, "is not in the review sidecar (run node scripts/i18n-draft.mjs --sync-sidecar)"]);
    else if (status !== "draft" && status !== "reviewed") problems.push(["invalid", key, 'has status "' + status + '" (draft or reviewed)']);
    else if (status === "draft") problems.push([critical.has(key) ? "critical-draft" : "draft", key, "is still a draft"]);
  }
  for (const key of Object.keys(sidecar.keys)) if (!(key in reference)) problems.push(["stale", key, "is in the sidecar but not in en-CA"]);
  for (const key of sidecar.critical) if (!(key in reference)) problems.push(["stale", key, "is a critical key that is not in en-CA"]);
  // Critical keys come first: they need the review, not just a human glance.
  const order = { "critical-draft": 0, missing: 1, invalid: 2, unrecorded: 3, stale: 4, draft: 5 };
  return problems.sort((a, b) => order[a[0]] - order[b[0]] || (a[1] < b[1] ? -1 : 1));
}

function main() {
  const problems = releaseProblems(process.argv[2]);
  if (problems.length === 0) {
    console.log("i18n release check passed: every fr-CA key is reviewed.");
    return 0;
  }
  const counts = {};
  for (const [kind] of problems) counts[kind] = (counts[kind] ?? 0) + 1;
  console.error("i18n release check failed (" + Object.entries(counts).map(([k, n]) => n + " " + k).join(", ") + "):");
  for (const [kind, key, text] of problems.slice(0, 50)) console.error("  " + kind + ": " + key + " " + text);
  if (problems.length > 50) console.error("  ... and " + (problems.length - 50) + " more");
  console.error("Critical keys must pass the LLM review (.github/workflows/i18n-review.yml); no key may stay a draft.");
  return 1;
}

if (import.meta.url === new URL(process.argv[1], "file:").href) process.exit(main());
