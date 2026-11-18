#!/usr/bin/env node
// Removes zh-CN message keys that no longer exist in en-CA (ADR 0014).
//
//   node scripts/i18n-prune.mjs [--check] [directory]
//
// en-CA is the typed reference, so a key removed or renamed there must go from
// the zh-CN overlay in the same change. Without --check the stale entries are
// deleted in place; with --check they are only listed and the exit status is 1.
// The directory defaults to src/messages.

import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import ts from "typescript";

/** The property names and source ranges of the first object literal in a file. */
function properties(source, fileName) {
  const file = ts.createSourceFile(
    fileName,
    source,
    ts.ScriptTarget.Latest,
    true,
  );
  const found = [];
  file.forEachChild((statement) => {
    if (!ts.isVariableStatement(statement)) return;
    for (const declaration of statement.declarationList.declarations) {
      const literal = declaration.initializer;
      if (!literal || !ts.isObjectLiteralExpression(literal)) continue;
      for (const property of literal.properties) {
        if (!ts.isPropertyAssignment(property)) continue;
        const name = property.name;
        if (!ts.isStringLiteralLike(name) && !ts.isIdentifier(name)) continue;
        found.push({
          key: name.text,
          start: property.getFullStart(),
          end: property.getEnd(),
        });
      }
    }
  });
  return found;
}

/**
 * The zh-CN source without the entries whose key is not in `enKeys`, and the
 * removed keys. A trailing comma after a removed entry goes with it.
 */
export function pruneSource(source, enKeys, fileName = "dictionary.ts") {
  const stale = properties(source, fileName).filter(
    ({ key }) => !enKeys.has(key),
  );
  let output = source;
  for (const { start, end } of [...stale].reverse()) {
    const comma = output[end] === "," ? end + 1 : end;
    output = output.slice(0, start) + output.slice(comma);
  }
  return { output, removed: stale.map(({ key }) => key) };
}

/** Every key defined by the en-CA dictionaries in a directory. */
export function enCAKeys(directory) {
  const keys = new Set();
  for (const name of readdirSync(directory)) {
    if (!name.endsWith(".en-CA.ts")) continue;
    const path = join(directory, name);
    for (const { key } of properties(readFileSync(path, "utf8"), path)) {
      keys.add(key);
    }
  }
  return keys;
}

/** Prunes (or, with check, only reports) every zh-CN dictionary in a directory. */
export function pruneDirectory(directory, { check = false } = {}) {
  const keys = enCAKeys(directory);
  const report = [];
  for (const name of readdirSync(directory)) {
    if (!name.endsWith(".zh-CN.ts")) continue;
    const path = join(directory, name);
    const { output, removed } = pruneSource(
      readFileSync(path, "utf8"),
      keys,
      path,
    );
    if (removed.length === 0) continue;
    report.push({ file: name, removed });
    if (!check) writeFileSync(path, output);
  }
  return report;
}

const invokedDirectly =
  process.argv[1] &&
  resolve(process.argv[1]) === new URL(import.meta.url).pathname;
if (invokedDirectly) {
  const args = process.argv.slice(2);
  const check = args.includes("--check");
  const directory = resolve(
    args.find((arg) => !arg.startsWith("--")) ?? "src/messages",
  );
  const report = pruneDirectory(directory, { check });
  for (const { file, removed } of report) {
    console.log(
      `${file}: ${check ? "stale" : "removed"} ${removed.join(", ")}`,
    );
  }
  if (report.length === 0) console.log("No stale zh-CN keys.");
  else if (check) process.exitCode = 1;
}
