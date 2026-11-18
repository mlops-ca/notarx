import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import ts from "typescript";
import { describe, expect, it } from "vitest";
import { enCA, zhCN } from ".";
import { frCAStyleIssues } from "./frCAStyle";
import { glossary, type GlossaryLocale } from "./glossary";
import { createTranslator, errorText, LocalizedError } from "./translate";

const root = resolve(process.cwd(), "src");
const messagesDir = join(root, "messages");

type Dictionary = Record<string, string>;

function placeholders(message: string): string[] {
  return [...message.matchAll(/\{([^{}]+)\}/g)].map((match) => match[1]).sort();
}

async function load(
  name: string,
  locale: "en-CA" | "fr-CA" | "zh-CN",
): Promise<Dictionary | undefined> {
  if (!existsSync(join(messagesDir, `${name}.${locale}.ts`))) return undefined;
  const module: Record<string, unknown> = await import(
    `./${name}.${locale}.ts`
  );
  return (
    Object.values(module).find(
      (value): value is Dictionary =>
        typeof value === "object" && value !== null,
    ) ?? {}
  );
}

/** Every feature dictionary, found by its en-CA file; fr-CA and zh-CN are optional. */
async function featureDictionaries() {
  const names = readdirSync(messagesDir)
    .filter((name) => name.endsWith(".en-CA.ts"))
    .map((name) => name.slice(0, -".en-CA.ts".length));
  return Promise.all(
    names.map(async (name) => {
      const [en, fr, zh] = await Promise.all([
        load(name, "en-CA"),
        load(name, "fr-CA"),
        load(name, "zh-CN"),
      ]);
      return { name, en: en!, fr, zh };
    }),
  );
}

function sourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      return path === messagesDir || entry.name === "client"
        ? []
        : sourceFiles(path);
    }
    return /\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)
      ? [path]
      : [];
  });
}

// Any script outside Latin: Han, Cyrillic, Greek, Arabic and so on. Common
// punctuation, digits and symbols and inherited marks are not scripts.
const nonLatin = /[^\p{Script=Latin}\p{Script=Common}\p{Script=Inherited}]/u;

// Text that is the same in every locale: a language's own name, and the
// built-in theme packages, whose descriptions mirror the server's packages.
const languageNames = new Set(["中", "中文", "简体中文", "切换到中文"]);
const exemptFiles = new Set(["lib/theme/defaultConsoleThemes.ts"]);

/** Every string, template and JSX text in a file that carries non-Latin script. */
function nonLatinLiterals(path: string): string[] {
  const source = readFileSync(path, "utf8");
  const found: string[] = [];
  const file = ts.createSourceFile(
    path,
    source,
    ts.ScriptTarget.Latest,
    true,
    path.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
  );
  file.forEachChild(function visit(node) {
    if (
      (ts.isStringLiteralLike(node) ||
        ts.isTemplateHead(node) ||
        ts.isTemplateMiddle(node) ||
        ts.isTemplateTail(node) ||
        ts.isJsxText(node)) &&
      nonLatin.test(node.text) &&
      !languageNames.has(node.text.trim())
    ) {
      const { line } = file.getLineAndCharacterOfPosition(node.getStart());
      found.push(`${relative(root, path)}:${line + 1} ${node.text.trim()}`);
    }
    node.forEachChild(visit);
  });
  return found;
}

describe("message dictionaries", () => {
  it("define no empty en-CA message", async () => {
    for (const { name, en } of await featureDictionaries()) {
      expect(Object.keys(en).length, name).toBeGreaterThan(0);
      for (const [key, message] of Object.entries(en)) {
        expect(message.trim(), key).not.toBe("");
      }
    }
  });

  it("keep zh-CN a subset of en-CA with the same placeholders", async () => {
    for (const { name, en, zh } of await featureDictionaries()) {
      if (!zh) continue;
      const stale = Object.keys(zh).filter((key) => !(key in en));
      expect(
        stale,
        `${name}: stale zh-CN keys (run scripts/i18n-prune.mjs)`,
      ).toEqual([]);
      for (const [key, message] of Object.entries(zh)) {
        expect(message.trim(), key).not.toBe("");
        expect(placeholders(message), key).toEqual(placeholders(en[key]));
      }
    }
  });

  it("keep fr-CA the same keys as en-CA, with the same placeholders", async () => {
    for (const { name, en, fr } of await featureDictionaries()) {
      expect(fr, `${name}: fr-CA dictionary is missing`).toBeDefined();
      expect(Object.keys(fr!).sort(), name).toEqual(Object.keys(en).sort());
      for (const [key, message] of Object.entries(fr!)) {
        expect(message.trim(), key).not.toBe("");
        expect(placeholders(message), key).toEqual(placeholders(en[key]));
      }
    }
  });

  it("write fr-CA with courriel, French spacing and typographic apostrophes", async () => {
    const issues: string[] = [];
    for (const { name, fr } of await featureDictionaries()) {
      for (const [key, message] of Object.entries(fr ?? {})) {
        for (const issue of frCAStyleIssues(message)) {
          issues.push(`${name} ${key}: ${issue}`);
        }
      }
    }
    expect(issues).toEqual([]);
  });

  it("flags the fr-CA writing mistakes the rules name", () => {
    const nb = "\u00a0";
    expect(frCAStyleIssues("Envoyer un e-mail")).toHaveLength(1);
    expect(frCAStyleIssues("Envoyer un email")).toHaveLength(1);
    expect(frCAStyleIssues("Envoyer des emails")).toHaveLength(1);
    expect(frCAStyleIssues("Les e-mails partent")).toHaveLength(1);
    expect(frCAStyleIssues("Envoyer un courriel")).toEqual([]);
    expect(frCAStyleIssues("Envoyer un `email` {email}")).toEqual([]);
    expect(frCAStyleIssues("Supprimer ?")).toHaveLength(1);
    expect(frCAStyleIssues("Supprimer le dépôt !")).toHaveLength(1);
    expect(frCAStyleIssues("Nom : {name}")).toHaveLength(1);
    expect(frCAStyleIssues(`Nom${nb}: {name}`)).toEqual([]);
    expect(frCAStyleIssues("Supprimer le dépôt?")).toEqual([]);
    expect(frCAStyleIssues("l'artefact")).toHaveLength(1);
    expect(frCAStyleIssues("l’artefact")).toEqual([]);
    expect(frCAStyleIssues("«Dépôt»")).toHaveLength(1);
    expect(frCAStyleIssues(`«${nb}Dépôt${nb}»`)).toEqual([]);
  });

  it("record every fr-CA key in the review sidecar", () => {
    const sidecar = JSON.parse(
      readFileSync(join(messagesDir, ".review", "fr-CA.json"), "utf8"),
    ) as { keys: Record<string, string>; critical: string[] };
    expect(Object.keys(sidecar.keys).sort()).toEqual(Object.keys(enCA).sort());
    for (const [key, status] of Object.entries(sidecar.keys)) {
      expect(["draft", "reviewed"], key).toContain(status);
    }
    for (const key of sidecar.critical)
      expect(key in sidecar.keys, key).toBe(true);
  });

  it("never define one key in two dictionaries", async () => {
    const owners = new Map<string, string>();
    const duplicates: string[] = [];
    for (const { name, en } of await featureDictionaries()) {
      for (const key of Object.keys(en)) {
        const owner = owners.get(key);
        if (owner) duplicates.push(`${key} (${owner}, ${name})`);
        owners.set(key, name);
      }
    }
    expect(duplicates).toEqual([]);
    expect(owners.size).toBe(Object.keys(enCA).length);
  });

  it("name each domain term the way the glossary does, per locale", async () => {
    const tables: Partial<Record<GlossaryLocale, Dictionary>> = {
      "en-CA": enCA,
      "zh-CN": zhCN as Dictionary,
    };
    const dictionaries = await featureDictionaries();
    if (dictionaries.some(({ fr }) => fr)) {
      tables["fr-CA"] = Object.assign({}, ...dictionaries.map(({ fr }) => fr));
    }
    const misnamed = Object.entries(tables).flatMap(([locale, table]) =>
      Object.entries(table).flatMap(([key, message]) =>
        glossary.flatMap(({ term, labels, avoid }) =>
          (avoid?.[locale as keyof typeof avoid] ?? [])
            .filter((synonym) => message.includes(synonym))
            .map(
              (synonym) =>
                `${locale} ${key}: ${synonym} -> ${labels[locale as keyof typeof labels] ?? term}`,
            ),
        ),
      ),
    );
    expect(misnamed).toEqual([]);
  });

  it("only keep messages some source file uses", () => {
    // locales.ts names the language-picker messages by key.
    const source = [...sourceFiles(root), join(messagesDir, "locales.ts")]
      .map((path) => readFileSync(path, "utf8"))
      .join("\n");
    // Keys built at run time, such as `account.kind.${kind}`.
    const prefixes = [...source.matchAll(/`([a-zA-Z]+\.[A-Za-z.]*)\$\{/g)].map(
      (match) => match[1],
    );
    const unused = Object.keys(enCA).filter(
      (key) =>
        !source.includes(`"${key}"`) &&
        !prefixes.some((prefix) => key.startsWith(prefix)),
    );
    expect(unused).toEqual([]);
  });

  it("keeps display text out of components", () => {
    const literals = sourceFiles(root)
      .filter((path) => !exemptFiles.has(relative(root, path)))
      .flatMap(nonLatinLiterals);
    expect(literals).toEqual([]);
  });

  it("reports the zh-CN coverage without failing on it", () => {
    const total = Object.keys(enCA).length;
    const translated = Object.keys(zhCN).filter((key) => key in enCA).length;
    const percent = ((translated / total) * 100).toFixed(1);
    console.info(`zh-CN coverage: ${translated}/${total} keys (${percent}%)`);
    expect(translated).toBeLessThanOrEqual(total);
  });
});

describe("glossary", () => {
  it("labels every term in en-CA and keeps fr-CA avoid lists next to a fr-CA label", () => {
    const terms = glossary.map(({ term }) => term);
    expect(new Set(terms).size).toBe(terms.length);
    for (const entry of glossary) {
      expect(entry.labels["en-CA"], entry.term).not.toBe("");
      if (entry.avoid?.["fr-CA"])
        expect(entry.labels["fr-CA"], entry.term).toBeDefined();
    }
  });

  it("carries the fr-CA labels agreed in ADR 0014", () => {
    const fr = Object.fromEntries(
      glossary.map(({ term, labels }) => [term, labels["fr-CA"]]),
    );
    expect(fr).toMatchObject({
      Repository: "dépôt",
      Artifact: "artefact",
      Promotion: "promotion",
      Group: "groupe",
      Tombstone: "pierre tombale",
      Quarantine: "quarantaine",
      Upstream: "amont",
      "Proxy repository": "dépôt mandataire",
      "Service account": "compte de service",
      Grant: "autorisation",
      "Audit event": "événement d’audit",
    });
    const avoid = Object.fromEntries(
      glossary.map(({ term, avoid }) => [term, avoid?.["fr-CA"]]),
    );
    expect(avoid.Tombstone).toEqual(["épitaphe"]);
    expect(avoid.Grant).toEqual(["subvention"]);
  });
});

describe("createTranslator", () => {
  it("fills named placeholders once and leaves unknown ones literal", () => {
    const t = createTranslator("en-CA");
    expect(t("ui.retry")).toBe("Retry");
    expect(t("system.disableTarget", { name: "{name} ops" })).toBe(
      "Disable {name} ops",
    );
    expect(t("system.disableTarget", {})).toBe("Disable {name}");
  });

  it("translates a LocalizedError where it is shown", () => {
    const t = createTranslator("en-CA");
    const error = new LocalizedError(
      "publicBrowse.failedToReadOciTagsStatus",
      { status: 503 },
      { status: 503 },
    );
    expect(error.status).toBe(503);
    expect(errorText(error, t, "ui.requestFailed")).toBe(
      "Failed to read OCI tags (503)",
    );
    expect(errorText(new Error("plain"), t, "ui.requestFailed")).toBe("plain");
    expect(errorText(undefined, t, "ui.requestFailed")).toBe("Request failed");
  });
});
