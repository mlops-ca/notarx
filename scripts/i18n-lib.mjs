// Shared helpers for the Console translation tooling (ADR 0014): reading the
// typed message dictionaries without a TypeScript toolchain, the fr-CA review
// sidecar and the critical-key rule. Plain Node, no dependencies.
import { existsSync, readdirSync, readFileSync, writeFileSync, mkdirSync } from "node:fs";
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
  for (const match of source.matchAll(ENTRY)) {
    out[match[2]] = match[3] !== undefined ? unescape(match[3]) : unescape(match[4], "'");
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

export function sidecarPath(dir = defaultMessagesDir) {
  return join(dir, ".review", "fr-CA.json");
}

/**
 * The fr-CA review sidecar: `keys` maps a key to "draft" or "reviewed";
 * `critical` lists the destructive-action keys, a separate flag.
 */
export function readSidecar(dir = defaultMessagesDir) {
  const path = sidecarPath(dir);
  if (!existsSync(path)) return { keys: {}, critical: [] };
  const data = JSON.parse(readFileSync(path, "utf8"));
  return { keys: data.keys ?? {}, critical: data.critical ?? [] };
}

export function writeSidecar(dir, sidecar) {
  const keys = Object.fromEntries(Object.entries(sidecar.keys).sort(([a], [b]) => (a < b ? -1 : 1)));
  const critical = [...new Set(sidecar.critical)].sort();
  const path = sidecarPath(dir);
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, JSON.stringify({ keys, critical }, null, 2) + "\n");
}

/**
 * Brings the sidecar in line with the dictionaries: every en-CA key has an
 * entry (new keys start as draft), stale keys go, and the critical list gains
 * the destructive-action keys (it never loses one).
 */
export function syncSidecar(dir = defaultMessagesDir, { newStatus = "draft" } = {}) {
  const reference = readReference(dir);
  const current = readSidecar(dir);
  const keys = {};
  for (const key of Object.keys(reference)) keys[key] = current.keys[key] ?? newStatus;
  const critical = current.critical.filter((key) => key in reference);
  for (const [key, text] of Object.entries(reference)) {
    if (isCritical(key, text)) critical.push(key);
  }
  const next = { keys, critical: [...new Set(critical)].sort() };
  writeSidecar(dir, next);
  return next;
}
