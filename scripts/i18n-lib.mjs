// Shared helpers for the Console translation tooling (ADR 0014): reading the
// typed message dictionaries without a TypeScript toolchain, the fr-CA review
// sidecar and the critical-key rule. Plain Node, no dependencies.
import { existsSync, readdirSync, readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { createHash } from "node:crypto";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

export const repoRoot = join(dirname(fileURLToPath(import.meta.url)), "..");
export const defaultMessagesDir = join(repoRoot, "console/src/messages");
export const LOCALES = ["fr-CA", "zh-CN"];

/** One `"key": "text",` entry (any indentation: prettier wraps long type annotations); the text may sit on the next line. */
const ENTRY = /^([ \t]+)"([^"]+)":\s*(?:\n\s*)?(?:"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)'),?[ \t]*$/gm;

/** Decodes a TypeScript double-quoted string body (\uXXXX, \", \n, \\ and \'). */
export function unescape(raw, quote = '"') {
  const body = quote === "'" ? raw.replace(/\\'/g, "'").replace(/(?<!\\)"/g, '\\"') : raw;
  return JSON.parse('"' + body + '"');
}

/** Encodes text as a TypeScript double-quoted string body, keeping no-break spaces visible. */
export function escape(text) {
  return JSON.stringify(text).slice(1, -1).replace(/\u00a0/g, "\\u00a0");
}

/** The key/text pairs of a feature dictionary file; {} when the file is absent. */
export function readDictionary(path) {
  if (!existsSync(path)) return {};
  const source = readFileSync(path, "utf8");
  const out = {};
  const declared = [...source.matchAll(/^[ \t]+"([^"]+)":/gm)].map((match) => match[1]);
  for (const match of source.matchAll(ENTRY)) {
    out[match[2]] = match[3] !== undefined ? unescape(match[3]) : unescape(match[4], "'");
  }
  // A key whose text is a template literal or a concatenation would be skipped
  // silently, and with it its review; refuse the file instead.
  const unread = declared.filter((key) => !(key in out));
  if (unread.length) {
    throw new Error(path + ": unsupported entry formatting for " + unread.slice(0, 5).join(", ") + " (use one plain quoted string per key)");
  }
  return out;
}

/** The feature names, found by their en-CA files. */
export function featureNames(dir = defaultMessagesDir) {
  return readdirSync(dir)
    .filter((name) => name.endsWith(".en-CA.ts"))
    .map((name) => name.slice(0, -".en-CA.ts".length))
    .sort();
}

export function dictionaryPath(dir, feature, locale) {
  return join(dir, feature + "." + locale + ".ts");
}

/** Every en-CA key with its text, across all features. */
export function readReference(dir = defaultMessagesDir) {
  const out = {};
  for (const feature of featureNames(dir)) {
    Object.assign(out, readDictionary(dictionaryPath(dir, feature, "en-CA")));
  }
  return out;
}

export function placeholders(text) {
  return [...text.matchAll(/\{([^{}]+)\}/g)].map((m) => m[1]).sort();
}

/**
 * Keys of destructive actions (delete, remove, quarantine, revoke, purge,
 * tombstone, disable) are critical: a wrong translation there misleads
 * (ADR 0014, "Security considerations"). Decided from the key and the en-CA text.
 */
const DESTRUCTIVE = /\b(delet|remov|quarantin|revok|purg|tombston|disabl)/i;
export function isCritical(key, enText) {
  return DESTRUCTIVE.test(key.replace(/([a-z])([A-Z])/g, "$1 $2")) || DESTRUCTIVE.test(enText);
}

/**
 * What a review covers: the en-CA text and the fr-CA text of a key. A review
 * recorded for other text is stale, so editing either side sends the key back
 * to draft.
 */
export function fingerprint(enText, frText) {
  return createHash("sha256").update(enText + "\0" + frText).digest("hex").slice(0, 16);
}

/** Every fr-CA key with its text, across all features. */
export function readFrench(dir = defaultMessagesDir) {
  const out = {};
  for (const feature of featureNames(dir)) Object.assign(out, readDictionary(dictionaryPath(dir, feature, "fr-CA")));
  return out;
}

export function sidecarPath(dir = defaultMessagesDir) {
  return join(dir, ".review", "fr-CA.json");
}

/**
 * The fr-CA review sidecar: `keys` maps a key to "draft" or "reviewed";
 * `critical` lists the destructive-action keys, a separate flag; `reviewed`
 * maps each reviewed key to the fingerprint of the text the review covered.
 */
export function readSidecar(dir = defaultMessagesDir) {
  const path = sidecarPath(dir);
  if (!existsSync(path)) return { keys: {}, critical: [], reviewed: {} };
  const data = JSON.parse(readFileSync(path, "utf8"));
  return { keys: data.keys ?? {}, critical: data.critical ?? [], reviewed: data.reviewed ?? {} };
}

export function writeSidecar(dir, sidecar) {
  const keys = Object.fromEntries(Object.entries(sidecar.keys).sort(([a], [b]) => (a < b ? -1 : 1)));
  const critical = [...new Set(sidecar.critical)].sort();
  const path = sidecarPath(dir);
  mkdirSync(dirname(path), { recursive: true });
  const reviewed = Object.fromEntries(Object.entries(sidecar.reviewed ?? {}).filter(([key]) => keys[key] === "reviewed").sort(([a], [b]) => (a < b ? -1 : 1)));
  writeFileSync(path, JSON.stringify({ keys, critical, reviewed }, null, 2) + "\n");
}

/**
 * Brings the sidecar in line with the dictionaries: every en-CA key has an
 * entry (new keys start as draft), stale keys go, and the critical list gains
 * the destructive-action keys (it never loses one).
 */
export function syncSidecar(dir = defaultMessagesDir, { newStatus = "draft" } = {}) {
  const reference = readReference(dir);
  const french = readFrench(dir);
  const current = readSidecar(dir);
  const keys = {};
  const reviewed = {};
  for (const key of Object.keys(reference)) {
    const status = current.keys[key] ?? newStatus;
    // A review covers the text it saw: changed text is a draft again.
    const covered = status === "reviewed" && current.reviewed[key] === fingerprint(reference[key], french[key] ?? "");
    keys[key] = status === "reviewed" && !covered ? "draft" : status;
    if (covered) reviewed[key] = current.reviewed[key];
  }
  const critical = current.critical.filter((key) => key in reference);
  for (const [key, text] of Object.entries(reference)) {
    if (isCritical(key, text)) critical.push(key);
  }
  const next = { keys, critical: [...new Set(critical)].sort(), reviewed };
  writeSidecar(dir, next);
  return next;
}

/**
 * Records keys as reviewed (what the LLM review step does for the keys that
 * pass): the status plus the fingerprint of the current en-CA and fr-CA text.
 */
export function markReviewed(dir, keys) {
  const reference = readReference(dir);
  const french = readFrench(dir);
  const sidecar = readSidecar(dir);
  for (const key of keys) {
    if (!(key in reference) || !(key in french)) throw new Error("cannot review " + key + ": it has no en-CA and fr-CA text");
    sidecar.keys[key] = "reviewed";
    sidecar.reviewed[key] = fingerprint(reference[key], french[key]);
  }
  writeSidecar(dir, sidecar);
  return sidecar;
}
