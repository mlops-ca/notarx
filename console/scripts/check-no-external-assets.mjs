// Fails when the built console references an external font or image URL, so
// air-gapped installs never reach for a CDN (ADR 0013, Branding).
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

const root = process.argv[2] ?? "dist";
const external = [
  /url\(\s*["']?(?:https?:)?\/\//gi,
  /@import\s+(?:url\()?\s*["']?(?:https?:)?\/\//gi,
  /<(?:link|img|script|source)\b[^>]*\b(?:href|src)=["'](?:https?:)?\/\//gi,
];

function* files(dir) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) yield* files(path);
    else if (/\.(?:html|css)$/.test(name)) yield path;
  }
}

const failures = [];
for (const path of files(root)) {
  const text = readFileSync(path, "utf8");
  for (const pattern of external) {
    for (const match of text.matchAll(pattern)) {
      failures.push(`${path}: ${match[0].slice(0, 80)}`);
    }
  }
}
if (failures.length > 0) {
  console.error(
    `external asset references in the built console:\n${failures.join("\n")}`,
  );
  process.exit(1);
}
console.log("built console has no external font or image URLs");
