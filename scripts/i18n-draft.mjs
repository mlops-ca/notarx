#!/usr/bin/env node
// Drafts fr-CA and zh-CN Console strings for missing keys with a locally run
// LLM (ADR 0014, "Translation workflow"), and keeps the fr-CA review sidecar.
//
//   node scripts/i18n-draft.mjs --locale fr-CA --model-cmd "ollama run <model>" [--feature NAME] [--limit N] [--dry-run]
//   node scripts/i18n-draft.mjs --locale fr-CA --model stub
//   node scripts/i18n-draft.mjs --sync-sidecar
//
// The model is a command you run on your own machine: it receives the prompt on
// stdin and prints a JSON object {key: text} on stdout. Nothing is sent to a
// network service by this script, and the prompt holds only the UI strings of
// the keys being drafted, the glossary and the writing rules, never repository
// data. New fr-CA drafts are recorded as "draft" in
// console/src/messages/.review/fr-CA.json; the release check fails while any
// stays a draft. zh-CN has no sidecar and no release gate.
import { spawnSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import {
  LOCALES, defaultMessagesDir, dictionaryPath, escape, featureNames, isCritical,
  placeholders, readDictionary, readSidecar, syncSidecar, writeSidecar,
} from "./i18n-lib.mjs";

export const RULES = {
  "fr-CA": [
    "Write Canadian French (Quebec usage), not France French.",
    "Say courriel, never e-mail or email. No space before ? ! ;. A no-break space (U+00A0) before a colon. Guillemets with no-break spaces inside. The typographic apostrophe (U+2019).",
    "Keep working jargon in English: pipeline, serving, edge, token, namespace, webhook, runner, manifest, digest, tag.",
    "Sentence case for headings and buttons; imperative for actions (Supprimer le dépôt).",
    "Keep {placeholders}, code spans, commands, URLs and format names exactly as written.",
  ],
  "zh-CN": [
    "Write Simplified Chinese for the Console UI.",
    "Keep {placeholders}, code spans, commands, URLs and format names exactly as written.",
  ],
};

/** The glossary labels for a locale, from console/src/messages/glossary.ts. */
export function glossaryFor(locale, dir = defaultMessagesDir) {
  const source = readFileSync(dir + "/glossary.ts", "utf8");
  const lines = [];
  if (locale === "fr-CA") {
    for (const m of source.matchAll(/^ {4}(?:"([^"]+)"|([A-Za-z]+)): \{ label: "([^"]+)"(?:, avoid: \[([^\]]*)\])?/gm)) {
      lines.push((m[1] ?? m[2]) + " = " + m[3] + (m[4] ? " (avoid: " + m[4].replace(/"/g, "") + ")" : ""));
    }
  }
  return lines;
}

/** The prompt for one batch: rules, glossary and the strings, nothing else. */
export function buildPrompt(locale, entries, dir = defaultMessagesDir) {
  const glossary = glossaryFor(locale, dir);
  return [
    "Translate the Console UI strings below from Canadian English (en-CA) to " + locale + ".",
    "Return only a JSON object that maps each key to its translation.",
    "",
    "Writing rules:",
    ...RULES[locale].map((rule) => "- " + rule),
    ...(glossary.length ? ["", "Glossary (term = label):", ...glossary.map((line) => "- " + line)] : []),
    "",
    "Strings:",
    JSON.stringify(entries, null, 2),
    "",
  ].join("\n");
}

/** A deterministic model for tests and dry runs: the en-CA text, tagged. */
export const stubModel = {
  name: "stub",
  draft(_prompt, entries) {
    return Object.fromEntries(Object.entries(entries).map(([key, text]) => [key, "[stub] " + text]));
  },
};

/** A model run as a local command: prompt on stdin, JSON on stdout. */
export function commandModel(command) {
  return {
    name: command,
    draft(prompt) {
      const run = spawnSync(command, { shell: true, input: prompt, encoding: "utf8", maxBuffer: 64 << 20 });
      if (run.status !== 0) throw new Error("the model command failed: " + (run.stderr || "exit " + run.status));
      const start = run.stdout.indexOf("{");
      const end = run.stdout.lastIndexOf("}");
      if (start < 0 || end < start) throw new Error("the model printed no JSON object");
      return JSON.parse(run.stdout.slice(start, end + 1));
    },
  };
}

/** Keeps only the answers for the asked keys that preserve the placeholders. */
export function acceptDrafts(asked, answers) {
  const accepted = {};
  const rejected = [];
  for (const [key, text] of Object.entries(asked)) {
    const answer = answers[key];
    if (typeof answer !== "string" || answer.trim() === "" || placeholders(answer).join() !== placeholders(text).join()) {
      rejected.push(key);
    } else {
      accepted[key] = answer;
    }
  }
  return { accepted, rejected };
}

/** Appends entries before the closing brace of the dictionary object. */
export function appendEntries(source, entries, marker) {
  const close = source.lastIndexOf("}");
  if (close < 0) throw new Error("no object literal in the dictionary file");
  const body = source.slice(0, close);
  const indent = (source.match(/^([ \t]+)"[^"]+":/m) ?? [, "  "])[1];
  const lines = Object.entries(entries).map(([key, text]) => {
    const prefix = marker ? indent + "// " + marker + "\n" : "";
    return prefix + indent + '"' + key + '":\n' + indent + '  "' + escape(text) + '",\n';
  });
  const separator = body.endsWith("\n") ? "" : "\n";
  return body + separator + lines.join("") + source.slice(close);
}

const camel = (name) => name.replace(/-([a-z])/g, (_, c) => c.toUpperCase());
const SUFFIX = { "fr-CA": "FrCA", "zh-CN": "ZhCN" };

/** The file a new feature dictionary starts from: fr-CA is a total Record, zh-CN a Partial one. */
export function skeleton(feature, locale) {
  const reference = camel(feature) + "EnCA";
  const type = locale === "fr-CA" ? "Record" : "Partial<Record";
  const close = locale === "fr-CA" ? "" : ">";
  return (
    'import type { ' + reference + ' } from "./' + feature + '.en-CA";\n\n' +
    "export const " + camel(feature) + SUFFIX[locale] + ": " + type + "<keyof typeof " + reference + ", string>" + close + " = {\n};\n"
  );
}

/**
 * Drafts the keys a locale lacks. Each batch is written (and, for fr-CA,
 * recorded as a draft in the sidecar) as soon as it is accepted, so a model
 * that fails later leaves the earlier batches recorded, never unrecorded. A
 * feature with no dictionary file for the locale gets one from `skeleton`. A
 * key the model cannot answer safely (fr-CA only) gets the en-CA text under a
 * TODO(fr-CA) marker, as a draft.
 */
export async function draftMissing({
  dir = defaultMessagesDir, locale, model, features, limit = Infinity, batch = 40, dryRun = false,
}) {
  if (!LOCALES.includes(locale)) throw new Error("locale must be one of " + LOCALES.join(", "));
  const result = { drafted: [], todo: [], files: [] };
  let remaining = limit;
  for (const feature of features ?? featureNames(dir)) {
    const en = readDictionary(dictionaryPath(dir, feature, "en-CA"));
    const path = dictionaryPath(dir, feature, locale);
    const have = readDictionary(path);
    const missing = Object.entries(en).filter(([key]) => !(key in have)).slice(0, remaining);
    if (missing.length === 0) continue;
    let source = existsSync(path) ? readFileSync(path, "utf8") : skeleton(feature, locale);
    for (let i = 0; i < missing.length; i += batch) {
      const asked = Object.fromEntries(missing.slice(i, i + batch));
      const answers = await model.draft(buildPrompt(locale, asked, dir), asked);
      const { accepted, rejected } = acceptDrafts(asked, answers);
      source = appendEntries(source, accepted);
      if (locale === "fr-CA" && rejected.length) {
        source = appendEntries(source, Object.fromEntries(rejected.map((key) => [key, asked[key]])), "TODO(fr-CA)");
      }
      if (!dryRun) {
        writeFileSync(path, source);
        if (locale === "fr-CA") recordDrafts(dir, [...Object.keys(accepted), ...rejected], en);
      }
      result.drafted.push(...Object.keys(accepted));
      if (locale === "fr-CA") result.todo.push(...rejected);
    }
    remaining -= missing.length;
    result.files.push(path);
    if (remaining <= 0) break;
  }
  return result;
}

/** Records keys as drafts in the sidecar; the destructive-action ones also become critical. */
function recordDrafts(dir, keys, reference) {
  const sidecar = readSidecar(dir);
  for (const key of keys) {
    sidecar.keys[key] = "draft";
    delete sidecar.reviewed[key];
    if (isCritical(key, reference[key] ?? "")) sidecar.critical.push(key);
  }
  writeSidecar(dir, sidecar);
}

function parse(argv) {
  const args = { features: undefined };
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    if (flag === "--locale") args.locale = argv[++i];
    else if (flag === "--feature") (args.features ??= []).push(argv[++i]);
    else if (flag === "--model") args.model = argv[++i];
    else if (flag === "--model-cmd") args.modelCmd = argv[++i];
    else if (flag === "--limit") args.limit = Number(argv[++i]);
    else if (flag === "--dry-run") args.dryRun = true;
    else if (flag === "--sync-sidecar") args.sync = true;
    else throw new Error("unknown argument " + flag);
  }
  return args;
}

async function main() {
  const args = parse(process.argv.slice(2));
  if (args.sync) {
    const sidecar = syncSidecar();
    console.log("fr-CA sidecar synced: " + Object.keys(sidecar.keys).length + " keys, " + sidecar.critical.length + " critical.");
    return 0;
  }
  if (!args.locale) throw new Error("--locale fr-CA|zh-CN is required (or --sync-sidecar)");
  if (args.locale === "zh-CN" && !args.features && args.limit === undefined) {
    throw new Error("zh-CN lags by design: pass --feature NAME or --limit N");
  }
  const model = args.model === "stub" ? stubModel : args.modelCmd ? commandModel(args.modelCmd) : undefined;
  if (!model) throw new Error("pass --model stub or --model-cmd \"<local command>\"");
  const result = await draftMissing({ locale: args.locale, model, features: args.features, limit: args.limit, dryRun: args.dryRun });
  console.log(
    (args.dryRun ? "Would draft " : "Drafted ") + result.drafted.length + " " + args.locale + " strings" +
      (result.todo.length ? " (" + result.todo.length + " left as TODO(fr-CA))" : "") + " in " + result.files.length + " files.",
  );
  return 0;
}

if (import.meta.url === new URL(process.argv[1], "file:").href) {
  main().then((code) => process.exit(code), (error) => { console.error(error.message); process.exit(1); });
}
