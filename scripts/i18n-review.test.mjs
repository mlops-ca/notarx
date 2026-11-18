import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { markReviewed, readSidecar, syncSidecar } from "./i18n-lib.mjs";
import {
  PROVIDERS, buildReviewPrompt, changedUnits, docUnits, localFindings, main, parseFindings,
  readDocument, readGlossary, review, selectProvider,
} from "./i18n-review.mjs";

const EN = 'export const fooEnCA = {\n  "foo.title": "Repositories",\n  "foo.deleteRepo": "Delete repository",\n  "foo.count": "{count} items in {name}",\n};\n';
const FR = 'export const fooFrCA = {\n  "foo.title": "Dépôts",\n  "foo.deleteRepo": "Supprimer le référentiel",\n  "foo.count": "{count} éléments dans {name}",\n};\n';
const GLOSSARY = 'const frCATerms = {\n    Repository: { label: "dépôt", avoid: ["référentiel"] },\n    Tombstone: { label: "pierre tombale", avoid: ["épitaphe"] },\n};\n';

function repo(fr = FR) {
  const root = mkdtempSync(join(tmpdir(), "i18n-review-"));
  const dir = join(root, "console/src/messages");
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, "foo.en-CA.ts"), EN);
  writeFileSync(join(dir, "foo.fr-CA.ts"), fr);
  writeFileSync(join(dir, "glossary.ts"), GLOSSARY);
  syncSidecar(dir);
  return { root, dir };
}

const fixture = (name) => JSON.parse(readFileSync(new URL("./fixtures/i18n-review/" + name, import.meta.url), "utf8"));

/** A fetch that records the request and replays a recorded response. */
function recorded(body, status = 200) {
  const calls = [];
  const fetchImpl = async (url, init) => {
    calls.push({ url, init });
    return { ok: status < 400, status, json: async () => body };
  };
  return { calls, fetchImpl };
}

test("an unknown or missing provider fails with a clear error", () => {
  assert.throws(() => selectProvider("openai", { env: {} }), /unknown review provider "openai": expected one of anthropic, mistral, cohere/);
  assert.throws(() => selectProvider(undefined, { env: {} }), /set I18N_REVIEW_PROVIDER/);
  assert.throws(() => selectProvider("mistral", { env: {} }), /needs the MISTRAL_API_KEY secret/);
});

test("the prompt holds the rules, the glossary and only the strings under review", () => {
  const { dir } = repo();
  const glossary = readGlossary(dir);
  assert.deepEqual(glossary[0], { term: "Repository", label: "dépôt", avoid: ["référentiel"] });
  const prompt = buildReviewPrompt([{ id: "foo.deleteRepo", en: "Delete repository", fr: "Supprimer le référentiel" }], glossary);
  assert.match(prompt, /courriel/);
  assert.match(prompt, /Tombstone = pierre tombale \(avoid: épitaphe\)/);
  assert.match(prompt, /"foo.deleteRepo"/);
  assert.doesNotMatch(prompt, /foo\.title|\/tmp|console\/src/);
});

test("local checks catch avoid-terms, France-French, typography and placeholder drift", () => {
  const glossary = readGlossary(repo().dir);
  const kinds = (fr, en) => localFindings({ id: "k", en, fr }, glossary).map((f) => f.kind);
  assert.deepEqual(kinds("Supprimer le référentiel", "Delete repository"), ["glossary"]);
  assert.deepEqual(kinds("Envoyer un e-mail", "Send an email"), ["france-french"]);
  assert.deepEqual(kinds("Nom : {name}", "Name: {name}"), ["typography"]);
  assert.deepEqual(kinds("Nom : {name}", "Name: {name}"), []);
  assert.deepEqual(kinds("l'artefact", "the artifact"), ["typography"]);
  assert.deepEqual(kinds("Supprimer ?", "Delete?"), ["typography"]);
  assert.deepEqual(kinds("Nom {nom}", "Name {name}"), ["meaning"]);
  assert.deepEqual(kinds("Utiliser le courriel {email}", "Use the email {email}"), []);
});

test("the stub provider is deterministic", async () => {
  const units = [
    { id: "a", en: "Retry", fr: "Retry", file: "x" },
    { id: "b", en: "Retry", fr: "Réessayer", file: "x" },
  ];
  const run = () => review({ units, provider: PROVIDERS.stub({}), glossary: [], critical: new Set(["a"]) });
  const first = await run();
  assert.deepEqual(first, await run());
  assert.deepEqual(first.passed, ["b"]);
  assert.deepEqual(first.criticalFailures, ["a"]);
});

for (const [name, key, check] of [
  ["anthropic", "ANTHROPIC_API_KEY", (call) => {
    assert.equal(call.url, "https://api.anthropic.com/v1/messages");
    assert.equal(call.init.headers["x-api-key"], "k");
    assert.equal(call.init.headers["anthropic-version"], "2023-06-01");
  }],
  ["mistral", "MISTRAL_API_KEY", (call) => {
    assert.equal(call.url, "https://api.mistral.ai/v1/chat/completions");
    assert.equal(call.init.headers.authorization, "Bearer k");
  }],
  ["cohere", "COHERE_API_KEY", (call) => {
    assert.equal(call.url, "https://api.cohere.com/v2/chat");
    assert.equal(call.init.headers.authorization, "Bearer k");
  }],
]) {
  test("the " + name + " adapter sends one request and reads the recorded response", async () => {
    const { calls, fetchImpl } = recorded(fixture(name + "-response.json"));
    const provider = selectProvider(name, { env: { [key]: "k" }, fetchImpl });
    const { dir } = repo();
    const units = changedUnits({ root: dir });
    const result = await review({ units, provider, glossary: readGlossary(dir), critical: new Set(["foo.deleteRepo"]) });
    assert.equal(calls.length, 1);
    check(calls[0]);
    const body = JSON.parse(calls[0].init.body);
    const sent = JSON.stringify(body);
    assert.match(sent, /foo\.deleteRepo/);
    assert.doesNotMatch(sent, /i18n-review-|\/console\/src/);
    assert.ok(result.findings.some((f) => f.id === "foo.deleteRepo" && f.source === "llm" && f.kind === "glossary"));
    assert.deepEqual(result.criticalFailures, ["foo.deleteRepo"]);
    assert.ok(result.passed.includes("foo.title"));
  });
}

test("a provider that answers with an error status fails without echoing the key", async () => {
  const { fetchImpl } = recorded({}, 401);
  const provider = selectProvider("anthropic", { env: { ANTHROPIC_API_KEY: "secret-key" }, fetchImpl });
  await assert.rejects(provider.review("p"), (error) => /HTTP 401/.test(error.message) && !error.message.includes("secret-key"));
});

test("parseFindings keeps only findings for asked keys and tolerates a code fence", () => {
  const text = "Here you go:\n" + "\u0060\u0060\u0060json\n" + JSON.stringify({ findings: [{ id: "a", kind: "bogus", message: " Bad. " }, { id: "zzz", kind: "meaning", message: "x" }, { id: "a", message: "" }] }) + "\n\u0060\u0060\u0060";
  assert.deepEqual(parseFindings(text, ["a"]), [{ id: "a", kind: "other", message: "Bad.", source: "llm" }]);
  assert.throws(() => parseFindings("no json", ["a"]), /no JSON object/);
  assert.throws(() => parseFindings("{}", ["a"]), /no findings array/);
});

test("only keys that differ from the base are reviewed", () => {
  const base = repo();
  const head = repo(FR.replace("Dépôts", "Dépôts d’artefacts"));
  assert.deepEqual(changedUnits({ root: head.dir, baseRoot: base.dir }).map((u) => u.id), ["foo.title"]);
  assert.deepEqual(changedUnits({ root: base.dir, baseRoot: base.dir }), []);
  // Without a base, every key that is not recorded as reviewed.
  assert.equal(changedUnits({ root: base.dir }).length, 3);
  markReviewed(base.dir, ["foo.title"]);
  assert.equal(changedUnits({ root: base.dir }).length, 2);
});

test("document paragraphs skip code, tables and bare URLs", () => {
  const text = "# Titre\n\nUn paragraphe\nsur deux lignes.\n\n" + "\u0060\u0060\u0060sh\nnotarx --help\n\u0060\u0060\u0060\n\n| a | b |\n|---|---|\n\nhttps://example.test/x\n\nFin.\n";
  assert.deepEqual(docUnits("README.fr-CA.md", text).map((u) => [u.id, u.fr]), [
    ["doc:README.fr-CA.md:1", "# Titre"],
    ["doc:README.fr-CA.md:3", "Un paragraphe sur deux lignes."],
    ["doc:README.fr-CA.md:15", "Fin."],
  ]);
});

test("a document is read only as a regular file inside the checkout", () => {
  const { root } = repo();
  writeFileSync(join(root, "README.fr-CA.md"), "Bonjour.\n");
  assert.equal(readDocument(root, "README.fr-CA.md"), "Bonjour.\n");
  // A document the pull request deleted is skipped.
  assert.equal(readDocument(root, "gone.fr-CA.md"), undefined);
  // A symlink to another file is refused, whatever it points at.
  const outside = join(mkdtempSync(join(tmpdir(), "i18n-outside-")), "secret.txt");
  writeFileSync(outside, "secret");
  symlinkSync(outside, join(root, "evil.fr-CA.md"));
  assert.throws(() => readDocument(root, "evil.fr-CA.md"), /not a regular file/);
  assert.throws(() => readDocument(root, "../x.fr-CA.md"), /not a repository fr-CA document/);
  assert.throws(() => readDocument(root, "/etc/passwd"), /not a repository fr-CA document/);
  assert.throws(() => readDocument(root, "notes.md"), /not a repository fr-CA document/);
});

test("without a glossary file there is nothing to review and nothing is sent", async () => {
  const root = mkdtempSync(join(tmpdir(), "i18n-review-empty-"));
  const logs = [];
  const log = console.log;
  console.log = (line) => logs.push(line);
  try {
    assert.equal(await main(["--provider", "stub", "--root", root], {}, undefined), 0);
  } finally {
    console.log = log;
  }
  assert.match(logs.join("\n"), /nothing changed, nothing sent/);
});

test("main fails on a critical finding, records passing keys and prints annotations", async () => {
  const base = repo();
  const head = repo();
  const out = join(head.root, "report.json");
  const logs = [];
  const log = console.log;
  console.log = (line) => logs.push(line);
  let code;
  try {
    code = await main(["--provider", "stub", "--root", head.root, "--base-root", base.root, "--write-sidecar", "--report", out], {}, undefined);
  } finally {
    console.log = log;
  }
  assert.equal(code, 0);
  assert.match(logs.join("\n"), /nothing changed, nothing sent/);

  // A critical key whose French is the English text fails the check; the rest pass and are recorded.
  const frozen = repo(FR.replace("Supprimer le référentiel", "Delete repository"));
  console.log = (line) => logs.push(line);
  try {
    code = await main(["--provider", "stub", "--root", frozen.root, "--write-sidecar", "--report", out], {}, undefined);
  } finally {
    console.log = log;
  }
  assert.equal(code, 1);
  assert.ok(logs.some((line) => line.startsWith("::error file=console/src/messages/foo.fr-CA.ts::foo.deleteRepo [meaning]")));
  const sidecar = readSidecar(frozen.dir);
  assert.equal(sidecar.keys["foo.title"], "reviewed");
  assert.equal(sidecar.keys["foo.deleteRepo"], "draft");
  assert.deepEqual(JSON.parse(readFileSync(out, "utf8")).criticalFailures, ["foo.deleteRepo"]);
});
