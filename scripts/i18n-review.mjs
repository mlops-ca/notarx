#!/usr/bin/env node
// The automated fr-CA review (ADR 0014, "Translation workflow", slice 13):
//
//   node scripts/i18n-review.mjs [--provider NAME] [--root DIR] [--base-root DIR]
//                                [--doc FILE]... [--write-sidecar] [--report FILE]
//
// The provider comes from --provider or the I18N_REVIEW_PROVIDER variable:
// anthropic, mistral, cohere (the API key is read from ANTHROPIC_API_KEY,
// MISTRAL_API_KEY or COHERE_API_KEY) or stub (deterministic, for tests).
//
// What leaves the machine: the changed fr-CA strings with their en-CA text (or
// the prose of the changed fr-CA documents), the glossary and the writing rules.
// Never repository data, paths or code. --base-root is a checkout of the base
// revision; only keys whose en-CA or fr-CA text differs from it are reviewed
// (without it, every key that is not yet reviewed). The script reads files from
// --root and never executes them, so a workflow can run this trusted copy
// against the head of a pull request.
//
// Findings are printed as GitHub annotations. The exit status is 1 when a
// critical key (a destructive action) has a finding, 2 when the review could
// not run. Keys that pass are recorded "reviewed" with --write-sidecar.
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { RULES } from "./i18n-draft.mjs";
import {
  defaultMessagesDir, dictionaryPath, featureNames, fingerprint, markReviewed,
  placeholders, readDictionary, readReference, readSidecar,
} from "./i18n-lib.mjs";

export const PROVIDER_VARIABLE = "I18N_REVIEW_PROVIDER";
export const KINDS = ["glossary", "france-french", "typography", "meaning", "other"];

/** The glossary the prompt and the local checks use: fr-CA label and avoid list per term. */
export function readGlossary(dir = defaultMessagesDir) {
  const source = readFileSync(join(dir, "glossary.ts"), "utf8");
  const terms = [];
  for (const m of source.matchAll(/^ {4}(?:"([^"]+)"|([A-Za-z]+)): \{ label: "([^"]+)"(?:, avoid: \[([^\]]*)\])?/gm)) {
    const avoid = m[4] ? [...m[4].matchAll(/"([^"]+)"/g)].map((a) => a[1]) : [];
    terms.push({ term: m[1] ?? m[2], label: m[3], avoid });
  }
  return terms;
}

// France-French wording that Quebec usage replaces (the writing rules in the ADR).
const FRANCE_FRENCH = [
  [/\be-?mails?\b/i, "courriel"],
  [/\bm[ée]l\b/i, "courriel"],
  [/\bweek-?ends?\b/i, "fin de semaine"],
  [/\bparking\b/i, "stationnement"],
];

/** Mechanical findings: avoid-terms, France-French words, typography. Placeholders and code spans are skipped. */
export function localFindings(unit, glossary) {
  // Placeholders, code spans, link targets, URLs and HTML tags are data, not prose.
  const prose = unit.fr
    .replace(/\{[^{}]+\}/g, "")
    .replace(/`[^`]*`/g, "x")
    .replace(/\]\([^)]*\)/g, "]")
    .replace(/https?:\/\/\S+/g, "")
    .replace(/<[^>]*>/g, "");
  const findings = [];
  const add = (kind, message) => findings.push({ id: unit.id, kind, message, source: "local" });
  for (const { term, label, avoid } of glossary) {
    for (const word of avoid) {
      if (prose.toLowerCase().includes(word.toLowerCase())) add("glossary", "uses " + word + " for " + term + "; the glossary says " + label);
    }
  }
  for (const [pattern, better] of FRANCE_FRENCH) {
    if (pattern.test(prose)) add("france-french", "France-French wording; use " + better);
  }
  if (/ [?!;]/.test(prose)) add("typography", "no space before ? ! ;");
  if (/(?<! ) :/.test(prose)) add("typography", "a no-break space (U+00A0) goes before a colon");
  if (/\p{L}'\p{L}/u.test(prose)) add("typography", "use the typographic apostrophe (U+2019) between letters");
  if (/«(?! )|(?<! )»/.test(prose)) add("typography", "guillemets take a no-break space inside");
  if (unit.en !== undefined && placeholders(unit.en).join() !== placeholders(unit.fr).join()) add("meaning", "the placeholders differ from the en-CA text");
  return findings;
}

/** The prompt for one batch: rules, glossary and the strings under review, nothing else. */
export function buildReviewPrompt(units, glossary) {
  const strings = Object.fromEntries(units.map((u) => [u.id, u.en === undefined ? { fr: u.fr } : { en: u.en, fr: u.fr }]));
  return [
    "You review Canadian French (fr-CA, Quebec usage) Console and documentation text against its Canadian English (en-CA) source.",
    "Report only real problems: glossary avoid-terms or wrong glossary labels, France-French wording,",
    "typography that breaks the writing rules, and meaning that drifts from the en-CA text.",
    "Return only a JSON object: {\"findings\": [{\"id\": \"<id>\", \"kind\": \"glossary|france-french|typography|meaning|other\", \"message\": \"<one sentence>\"}]}.",
    "Return {\"findings\": []} when everything is correct.",
    "",
    "Writing rules:",
    ...RULES["fr-CA"].map((rule) => "- " + rule),
    "",
    "Glossary (term = label):",
    ...glossary.map((g) => "- " + g.term + " = " + g.label + (g.avoid.length ? " (avoid: " + g.avoid.join(", ") + ")" : "")),
    "",
    "Strings:",
    JSON.stringify(strings, null, 2),
    "",
  ].join("\n");
}

/** Reads the JSON object a model printed, tolerating prose or a code fence around it. */
export function parseFindings(text, askedIds) {
  const start = text.indexOf("{");
  const end = text.lastIndexOf("}");
  if (start < 0 || end < start) throw new Error("the reviewer printed no JSON object");
  const data = JSON.parse(text.slice(start, end + 1));
  if (!Array.isArray(data.findings)) throw new Error("the reviewer's JSON has no findings array");
  const asked = new Set(askedIds);
  return data.findings
    .filter((f) => f && asked.has(f.id) && typeof f.message === "string" && f.message.trim() !== "")
    .map((f) => ({ id: f.id, kind: KINDS.includes(f.kind) ? f.kind : "other", message: f.message.trim(), source: "llm" }));
}

async function post(fetchImpl, name, url, headers, body) {
  const response = await fetchImpl(url, { method: "POST", headers: { "content-type": "application/json", ...headers }, body: JSON.stringify(body) });
  if (!response.ok) throw new Error(name + " answered HTTP " + response.status);
  return response.json();
}

/**
 * The provider interface: { name, review(prompt) -> Promise<string> } returns the
 * model's text. Each adapter is one HTTPS call; fetchImpl is injectable so tests
 * replay recorded responses without a network.
 */
export const PROVIDERS = {
  anthropic: ({ env, fetchImpl }) => ({
    name: "anthropic",
    async review(prompt) {
      const data = await post(fetchImpl, "anthropic", "https://api.anthropic.com/v1/messages",
        { "x-api-key": env.ANTHROPIC_API_KEY, "anthropic-version": "2023-06-01" },
        { model: env.I18N_REVIEW_MODEL || "claude-sonnet-5-5", max_tokens: 8192, messages: [{ role: "user", content: prompt }] });
      return (data.content ?? []).filter((part) => part.type === "text").map((part) => part.text).join("");
    },
  }),
  mistral: ({ env, fetchImpl }) => ({
    name: "mistral",
    async review(prompt) {
      const data = await post(fetchImpl, "mistral", "https://api.mistral.ai/v1/chat/completions",
        { authorization: "Bearer " + env.MISTRAL_API_KEY },
        { model: env.I18N_REVIEW_MODEL || "mistral-large-latest", messages: [{ role: "user", content: prompt }], response_format: { type: "json_object" } });
      return data.choices?.[0]?.message?.content ?? "";
    },
  }),
  cohere: ({ env, fetchImpl }) => ({
    name: "cohere",
    async review(prompt) {
      const data = await post(fetchImpl, "cohere", "https://api.cohere.com/v2/chat",
        { authorization: "Bearer " + env.COHERE_API_KEY },
        { model: env.I18N_REVIEW_MODEL || "command-a-03-2025", messages: [{ role: "user", content: prompt }], response_format: { type: "json_object" } });
      return (data.message?.content ?? []).filter((part) => part.type === "text").map((part) => part.text).join("");
    },
  }),
  // Deterministic: flags a string whose French is the English text, untranslated.
  stub: () => ({
    name: "stub",
    async review(prompt) {
      const strings = JSON.parse(prompt.slice(prompt.indexOf("Strings:\n") + "Strings:\n".length));
      const findings = Object.entries(strings)
        .filter(([, s]) => s.en !== undefined && s.en === s.fr)
        .map(([id]) => ({ id, kind: "meaning", message: "the French text equals the English text" }));
      return JSON.stringify({ findings });
    },
  }),
};

const KEY_VARIABLES = { anthropic: "ANTHROPIC_API_KEY", mistral: "MISTRAL_API_KEY", cohere: "COHERE_API_KEY" };
const REAL_PROVIDERS = () => Object.keys(PROVIDERS).filter((name) => name !== "stub").join(", ");

/** Selects the provider by name; an unknown or unset name fails with a clear error. */
export function selectProvider(name, { env = process.env, fetchImpl = globalThis.fetch } = {}) {
  if (!name) throw new Error("no review provider: set " + PROVIDER_VARIABLE + " (or pass --provider) to one of " + REAL_PROVIDERS());
  if (!Object.hasOwn(PROVIDERS, name)) throw new Error("unknown review provider " + JSON.stringify(name) + ": expected one of " + REAL_PROVIDERS());
  if (KEY_VARIABLES[name] && !env[KEY_VARIABLES[name]]) throw new Error("the " + name + " provider needs the " + KEY_VARIABLES[name] + " secret");
  return PROVIDERS[name]({ env, fetchImpl });
}

/** Every fr-CA key with its text and the file it lives in. */
function readFrenchWithFiles(dir) {
  const out = {};
  for (const feature of featureNames(dir)) {
    const path = dictionaryPath(dir, feature, "fr-CA");
    for (const [key, text] of Object.entries(readDictionary(path))) out[key] = { text, file: path };
  }
  return out;
}

/** The strings to review: keys whose en-CA or fr-CA text differs from the base (or are not reviewed). */
export function changedUnits({ root, baseRoot }) {
  const reference = readReference(root);
  const french = readFrenchWithFiles(root);
  const sidecar = readSidecar(root);
  const baseReference = baseRoot ? readReference(baseRoot) : undefined;
  const baseFrench = baseRoot ? readFrenchWithFiles(baseRoot) : undefined;
  const units = [];
  for (const [key, fr] of Object.entries(french)) {
    if (!(key in reference)) continue;
    const en = reference[key];
    const changed = baseRoot
      ? baseReference[key] !== en || baseFrench[key]?.text !== fr.text
      : sidecar.keys[key] !== "reviewed" || sidecar.reviewed[key] !== fingerprint(en, fr.text);
    if (changed) units.push({ id: key, en, fr: fr.text, file: fr.file });
  }
  return units.sort((a, b) => (a.id < b.id ? -1 : 1));
}

/** The prose paragraphs of a fr-CA document; fenced code, tables, URL-only and blank lines are skipped. */
export function docUnits(path, text) {
  const units = [];
  let fenced = false;
  let paragraph = [];
  let start = 0;
  const flush = () => {
    const prose = paragraph.join(" ").trim();
    if (prose && !/^https?:\/\/\S+$/.test(prose)) units.push({ id: "doc:" + path + ":" + start, fr: prose, file: path });
    paragraph = [];
  };
  text.split("\n").forEach((line, index) => {
    if (/^\s*(```|~~~)/.test(line)) { flush(); fenced = !fenced; return; }
    if (fenced || /^\s*\|/.test(line) || line.trim() === "") { flush(); return; }
    if (paragraph.length === 0) start = index + 1;
    paragraph.push(line.trim());
  });
  flush();
  return units;
}

/**
 * Runs the review: local checks for every unit, the provider for batches of
 * them. Returns the findings, the keys that passed and the critical failures.
 * A document paragraph is reviewed but never recorded in the key sidecar.
 */
export async function review({ units, provider, glossary, critical = new Set(), batch = 60 }) {
  const findings = [];
  for (const unit of units) findings.push(...localFindings(unit, glossary));
  for (let i = 0; i < units.length; i += batch) {
    const slice = units.slice(i, i + batch);
    const text = await provider.review(buildReviewPrompt(slice, glossary));
    findings.push(...parseFindings(text, slice.map((u) => u.id)));
  }
  const flagged = new Set(findings.map((f) => f.id));
  const passed = units.filter((u) => !flagged.has(u.id) && !u.id.startsWith("doc:")).map((u) => u.id);
  const criticalFailures = [...flagged].filter((id) => critical.has(id)).sort();
  return { findings, passed, criticalFailures };
}

function annotate(finding, unit, root) {
  const file = unit?.file ? unit.file.replace(resolve(root) + "/", "") : "";
  const encode = (s) => s.replace(/%/g, "%25").replace(/\r/g, "%0D").replace(/\n/g, "%0A");
  const label = finding.id.startsWith("doc:") ? "line " + finding.id.split(":").pop() : finding.id;
  return "::" + (finding.severity === "error" ? "error" : "warning") + (file ? " file=" + file : "") + "::" + encode(label + " [" + finding.kind + "] " + finding.message);
}

function parseArgs(argv) {
  const args = { docs: [] };
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    if (flag === "--provider") args.provider = argv[++i];
    else if (flag === "--root") args.root = argv[++i];
    else if (flag === "--base-root") args.baseRoot = argv[++i];
    else if (flag === "--doc") args.docs.push(argv[++i]);
    else if (flag === "--write-sidecar") args.write = true;
    else if (flag === "--report") args.report = argv[++i];
    else throw new Error("unknown argument " + flag);
  }
  return args;
}

export async function main(argv = process.argv.slice(2), env = process.env, fetchImpl = globalThis.fetch) {
  const args = parseArgs(argv);
  const repo = resolve(args.root ?? ".");
  const messages = join(repo, "console/src/messages");
  const provider = selectProvider(args.provider ?? env[PROVIDER_VARIABLE], { env, fetchImpl });
  const glossary = readGlossary(messages);
  const units = existsSync(join(messages, "glossary.ts"))
    ? changedUnits({ root: messages, baseRoot: args.baseRoot ? join(resolve(args.baseRoot), "console/src/messages") : undefined })
    : [];
  for (const doc of args.docs) {
    // Only fr-CA documents inside the checkout are read.
    if (!doc.endsWith(".fr-CA.md") || doc.startsWith("/") || doc.split("/").includes("..")) throw new Error("not a repository fr-CA document: " + doc);
    units.push(...docUnits(doc, readFileSync(join(repo, doc), "utf8")));
  }
  if (units.length === 0) {
    console.log("fr-CA review: nothing changed, nothing sent.");
    return 0;
  }
  const sidecar = readSidecar(messages);
  const result = await review({ units, provider, glossary, critical: new Set(sidecar.critical) });
  const byId = new Map(units.map((u) => [u.id, u]));
  for (const finding of result.findings) {
    finding.severity = result.criticalFailures.includes(finding.id) ? "error" : "warning";
    console.log(annotate(finding, byId.get(finding.id), repo));
  }
  if (args.write && result.passed.length) markReviewed(messages, result.passed);
  if (args.report) {
    writeFileSync(args.report, JSON.stringify({ provider: provider.name, reviewed: result.passed, findings: result.findings, criticalFailures: result.criticalFailures }, null, 2) + "\n");
  }
  console.log("fr-CA review (" + provider.name + "): " + units.length + " reviewed, " + result.passed.length + " passed, " + result.findings.length + " findings, " + result.criticalFailures.length + " critical keys failing.");
  return result.criticalFailures.length ? 1 : 0;
}

if (import.meta.url === new URL(process.argv[1], "file:").href) {
  main().then((code) => process.exit(code), (error) => { console.error("fr-CA review could not run: " + error.message); process.exit(2); });
}
