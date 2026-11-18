#!/usr/bin/env node
// The fr-CA release gate (ADR 0014, "Translation workflow"):
//
//   node scripts/i18n-release-check.mjs [messages-directory]
//
// Fails while any fr-CA key is still a draft, and while any critical key
// (a destructive action: delete, quarantine, revoke...) has not passed the
// automated LLM review, which marks a passing key "reviewed" in
// console/src/messages/.review/fr-CA.json together with a fingerprint of the
// text it covered; a review of other text is stale and fails too. zh-CN has no
// release gate.
import { defaultMessagesDir, fingerprint, readFrench, readReference, readSidecar } from "./i18n-lib.mjs";

/** The problems that stop a release; empty when the gate passes. */
export function releaseProblems(dir = defaultMessagesDir) {
  const reference = readReference(dir);
  const sidecar = readSidecar(dir);
  const french = readFrench(dir);
  const problems = [];
  const critical = new Set(sidecar.critical);
  for (const key of Object.keys(reference)) {
    if (!(key in french)) problems.push(["missing", key, "has no fr-CA text"]);
    const status = sidecar.keys[key];
    if (status === undefined) problems.push(["unrecorded", key, "is not in the review sidecar (run node scripts/i18n-draft.mjs --sync-sidecar)"]);
    else if (status !== "draft" && status !== "reviewed") problems.push(["invalid", key, 'has status "' + status + '" (draft or reviewed)']);
    else if (status === "draft") problems.push([critical.has(key) ? "critical-draft" : "draft", key, "is still a draft"]);
    else if (key in french && sidecar.reviewed[key] !== fingerprint(reference[key], french[key])) {
      problems.push([critical.has(key) ? "critical-stale" : "stale-review", key, "was reviewed for other text (en-CA or fr-CA changed since)"]);
    }
  }
  for (const key of Object.keys(sidecar.keys)) if (!(key in reference)) problems.push(["stale", key, "is in the sidecar but not in en-CA"]);
  for (const key of sidecar.critical) if (!(key in reference)) problems.push(["stale", key, "is a critical key that is not in en-CA"]);
  // Critical keys come first: they need the review, not just a human glance.
  const order = { "critical-draft": 0, "critical-stale": 1, missing: 2, invalid: 3, unrecorded: 4, stale: 5, "stale-review": 6, draft: 7 };
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
  console.error("Critical keys must pass the automated LLM review (ADR 0014 slice 13, #257), which records them as reviewed; no key may stay a draft.");
  return 1;
}

if (import.meta.url === new URL(process.argv[1], "file:").href) process.exit(main());
