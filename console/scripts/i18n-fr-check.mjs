// Checks fr-CA dictionaries against en-CA (ADR 0014): the same keys, the same
// placeholders, no empty text, no CJK and the fr-CA writing rules.
// Usage: cd console && node scripts/i18n-fr-check.mjs <feature-name>...   (e.g. lib formats)
import { readFileSync } from "node:fs";
import ts from "typescript";
import vm from "node:vm";

function load(path) {
  const out = ts.transpileModule(readFileSync(path, "utf8"), {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2022,
    },
  }).outputText;
  const module = { exports: {} };
  vm.runInNewContext("(function(exports, require, module){" + out + "\n})", {})(
    module.exports,
    () => ({}),
    module,
  );
  return Object.values(module.exports)[0];
}
const ph = (m) =>
  [...m.matchAll(/\{([^{}]+)\}/g)]
    .map((x) => x[1])
    .sort()
    .join(",");
// The writing rules are the ones messages.test.ts enforces (src/messages/frCAStyle.ts).
const style = load("src/messages/frCAStyle.ts");
let bad = 0;
for (const name of process.argv.slice(2)) {
  const en = load("src/messages/" + name + ".en-CA.ts");
  const fr = load("src/messages/" + name + ".fr-CA.ts");
  const problems = [];
  for (const key of Object.keys(en)) {
    if (!(key in fr)) {
      problems.push("missing " + key);
      continue;
    }
    if (typeof fr[key] !== "string" || fr[key].trim() === "")
      problems.push("empty " + key);
    else {
      if (ph(fr[key]) !== ph(en[key]))
        problems.push(
          "placeholders differ " +
            key +
            ": " +
            ph(en[key]) +
            " vs " +
            ph(fr[key]),
        );
      for (const i of style(fr[key])) problems.push(key + ": " + i);
      if (/[\u3400-\u9fff]/.test(fr[key])) problems.push("CJK in " + key);
    }
  }
  for (const key of Object.keys(fr))
    if (!(key in en)) problems.push("extra " + key);
  const same = Object.keys(en).filter((k) => fr[k] === en[k]).length;
  console.log(
    name +
      ": " +
      Object.keys(en).length +
      " keys, " +
      problems.length +
      " problems, " +
      same +
      " identical to en-CA",
  );
  for (const p of problems.slice(0, 60)) console.log("  " + p);
  bad += problems.length;
}
process.exit(bad ? 1 : 0);
